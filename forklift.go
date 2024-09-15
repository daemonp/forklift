// Forklift middleware plugin for A/B testing
package forklift

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/daemonp/forklift/config"
	"github.com/daemonp/forklift/logger"
)

// Alias config types for convenience
type (
	RoutingRule   = config.RoutingRule
	RuleCondition = config.RuleCondition
)

var (
	errEmptyConfig           = errors.New("empty configuration")
	errMissingDefaultBackend = errors.New("missing DefaultBackend")
	errInvalidPercentage     = errors.New("invalid percentage: must be between 0 and 100")
)

// matchPathPrefix checks if the request path matches the rule's path prefix.
func matchPathPrefix(reqPath, rulePrefix string) bool {
	return strings.HasPrefix(reqPath, rulePrefix)
}

const (
	sessionCookieName    = "forklift_id"
	sessionCookieMaxAge  = 86400 * 30 // 30 days
	cacheDuration        = 24 * time.Hour
	cacheCleanupInterval = 10 * time.Minute
	maxSessionIDLength   = 128
	defaultTimeout       = 10 * time.Second
	fullPercentage       = 100.0
	hashModulo           = 10000
	hashDivisor          = 100.0
)

// Forklift is the main struct for the middleware.
type Forklift struct {
	next       http.Handler
	config     *config.Config
	name       string
	ruleEngine *RuleEngine
	logger     logger.Logger
}

// RuleEngine handles rule matching and caching.
type RuleEngine struct {
	config *config.Config
	cache  *sync.Map
	logger logger.Logger
}

func NewRuleEngine(cfg *config.Config, logger logger.Logger) *RuleEngine {
	return &RuleEngine{
		config: cfg,
		cache:  &sync.Map{},
		logger: logger,
	}
}

// backendInfo stores information about a backend.
type backendInfo struct {
	Percentage float64
	Rules      []*config.RoutingRule
}

// backendEntry represents a backend with its selection bounds.
type backendEntry struct {
	Backend    string
	Info       *backendInfo
	LowerBound float64
	UpperBound float64
	RuleHash   uint32
}

// CreateConfig creates a new Config.
func CreateConfig() *config.Config {
	return &config.Config{}
}

// New creates a new middleware.
func New(ctx context.Context, next http.Handler, cfg interface{}, name string) (http.Handler, error) {
	logger := logger.NewLogger("forklift")

	parsedConfig, ok := cfg.(*config.Config)
	if !ok {
		return nil, fmt.Errorf("invalid configuration type")
	}

	if parsedConfig.DefaultBackend == "" {
		return nil, fmt.Errorf("DefaultBackend must be set")
	}

	if parsedConfig.Debug {
		logger.Debugf("Creating new Forklift middleware with config: %+v", parsedConfig)
	}

	forklift, err := NewForklift(ctx, next, parsedConfig, name)
	if err != nil {
		return nil, fmt.Errorf("failed to create Forklift middleware: %w", err)
	}
	return forklift, nil
}

// NewForklift creates a new middleware.
func NewForklift(ctx context.Context, next http.Handler, cfg *config.Config, name string) (*Forklift, error) {
	if cfg == nil {
		return nil, errEmptyConfig
	}
	if cfg.DefaultBackend == "" {
		return nil, errMissingDefaultBackend
	}
	for _, rule := range cfg.Rules {
		if rule.Percentage < 0 || rule.Percentage > 100 {
			return nil, errInvalidPercentage
		}
	}

	// Sort rules by priority (higher priority first)
	sort.Slice(cfg.Rules, func(i, j int) bool {
		return cfg.Rules[i].Priority > cfg.Rules[j].Priority
	})

	logger := logger.NewLogger("forklift")

	ruleEngine := &RuleEngine{
		config: cfg,
		cache:  &sync.Map{},
		logger: logger,
	}

	go ruleEngine.cleanupCache()

	forklift := &Forklift{
		next:       next,
		config:     cfg,
		name:       name,
		ruleEngine: ruleEngine,
		logger:     logger,
	}

	forklift.logger.Infof("Starting Forklift middleware: %s", name)

	return forklift, nil
}

// generateSessionID creates a new random session ID.
func generateSessionID() (string, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	if err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

// ServeHTTP implements the http.Handler interface.
func (a *Forklift) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	if a.config.Debug {
		a.logger.Debugf("Received request: %s %s", req.Method, req.URL.Path)
		a.logger.Debugf("Headers: %v", req.Header)
	}

	sessionID := a.handleSessionID(rw, req)
	if sessionID == "" {
		return
	}

	selected := a.selectBackend(req, sessionID)
	backend := selected.Backend
	selectedRule := selected.Rule

	if a.config.Debug {
		rw.Header().Set("X-Selected-Backend", backend)
		a.logger.Debugf("Routing request to backend: %s", backend)
		if selectedRule != nil {
			a.logger.Debugf("Selected rule: Path: %s, Method: %s, Backend: %s, Percentage: %f",
				selectedRule.Path, selectedRule.Method, selectedRule.Backend, selectedRule.Percentage)
		}
	}

	proxyReq, err := a.createProxyRequest(req, backend, selectedRule)
	if err != nil {
		a.logger.Errorf("Error creating proxy request: %v", err)
		http.Error(rw, "Error creating proxy request", http.StatusInternalServerError)
		return
	}

	a.sendProxyRequest(rw, proxyReq)
}

func (a *Forklift) handleSessionID(rw http.ResponseWriter, req *http.Request) string {
	sessionID := getOrCreateSessionID(rw, req)
	if sessionID == "" {
		a.logger.Errorf("Error handling session ID")
		http.Error(rw, "Internal Server Error", http.StatusInternalServerError)
		return ""
	}

	if req == nil {
		a.logger.Errorf("Error: Request is nil")
		http.Error(rw, "Internal Server Error", http.StatusInternalServerError)
		return ""
	}

	if a.config.Debug {
		a.logger.Debugf("Session ID: %s", sessionID)
	}

	return sessionID
}

type SelectedBackend struct {
	Backend string
	Rule    *RoutingRule
}

func (a *Forklift) selectBackend(req *http.Request, sessionID string) SelectedBackend {
	matchingRules := a.getMatchingRules(req)

	if len(matchingRules) == 0 {
		return SelectedBackend{Backend: a.config.DefaultBackend, Rule: nil}
	}

	// Sort matching rules by priority (higher priority first)
	sort.Slice(matchingRules, func(i, j int) bool {
		return matchingRules[i].Priority > matchingRules[j].Priority
	})

	// Group rules by backend
	backendRules := make(map[string][]RoutingRule)
	for _, rule := range matchingRules {
		backendRules[rule.Backend] = append(backendRules[rule.Backend], rule)
	}

	// Calculate total percentage for each backend
	backendPercentages := make(map[string]float64)
	for backend, rules := range backendRules {
		totalPercentage := 0.0
		for _, rule := range rules {
			if rule.Percentage > 0 {
				totalPercentage += rule.Percentage
			} else {
				totalPercentage = 100.0
				break
			}
		}
		backendPercentages[backend] = totalPercentage
	}

	// Select backend based on percentages and rule hash
	selectedBackend := a.selectBackendByPercentageAndRuleHash(sessionID, backendPercentages, matchingRules)
	if selectedBackend != "" && len(backendRules[selectedBackend]) > 0 {
		return SelectedBackend{Backend: selectedBackend, Rule: &backendRules[selectedBackend][0]}
	}

	// If no backend was selected, use the default backend
	return SelectedBackend{Backend: a.config.DefaultBackend, Rule: nil}
}

func (a *Forklift) selectBackendByPercentageAndRuleHash(sessionID string, backendPercentages map[string]float64, matchingRules []RoutingRule) string {
	// Sort backends to ensure consistent ordering
	backends := make([]string, 0, len(backendPercentages))
	for backend := range backendPercentages {
		backends = append(backends, backend)
	}
	sort.Strings(backends)

	// Create cumulative percentage ranges
	ranges := make(map[string]float64)
	totalPercentage := 0.0
	for _, backend := range backends {
		percentage := backendPercentages[backend]
		totalPercentage += percentage
		ranges[backend] = totalPercentage
	}

	// Normalize percentages if total exceeds 100
	if totalPercentage > 100 {
		factor := 100 / totalPercentage
		for backend := range ranges {
			ranges[backend] *= factor
		}
		totalPercentage = 100
	}

	// Use a consistent hash function
	h := fnv.New64a()
	if _, err := h.Write([]byte(sessionID)); err != nil {
		a.logger.Errorf("Error hashing session ID: %v", err)
		return a.config.DefaultBackend
	}
	for _, rule := range matchingRules {
		if rule.AffinityToken != "" {
			if _, err := h.Write([]byte(rule.AffinityToken)); err != nil {
				a.logger.Errorf("Error hashing affinity token: %v", err)
				return a.config.DefaultBackend
			}
		} else {
			if _, err := h.Write([]byte(rule.Path)); err != nil {
				a.logger.Errorf("Error hashing rule path: %v", err)
				return a.config.DefaultBackend
			}
			if _, err := h.Write([]byte(rule.Method)); err != nil {
				a.logger.Errorf("Error hashing rule method: %v", err)
				return a.config.DefaultBackend
			}
			if _, err := h.Write([]byte(rule.Backend)); err != nil {
				a.logger.Errorf("Error hashing rule backend: %v", err)
				return a.config.DefaultBackend
			}
		}
	}
	hashValue := float64(h.Sum64()) / float64(^uint64(0))

	// Select backend based on where the hash falls in the cumulative ranges
	cumulativePercentage := 0.0
	for _, backend := range backends {
		cumulativePercentage += backendPercentages[backend]
		if hashValue <= cumulativePercentage/100.0 {
			return backend
		}
	}

	// Fallback to default backend (should rarely happen)
	return a.config.DefaultBackend
}

// hashRoutingRule generates a hash for a RoutingRule
func hashRoutingRule(rule RoutingRule) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(rule.Path))
	_, _ = h.Write([]byte(rule.PathPrefix))
	_, _ = h.Write([]byte(rule.Method))
	_, _ = h.Write([]byte(rule.Backend))
	for _, condition := range rule.Conditions {
		_, _ = h.Write([]byte(condition.Type))
		_, _ = h.Write([]byte(condition.Parameter))
		_, _ = h.Write([]byte(condition.QueryParam))
		_, _ = h.Write([]byte(condition.Operator))
		_, _ = h.Write([]byte(condition.Value))
	}
	return h.Sum32()
}

func (a *Forklift) shouldApplyPercentage(sessionID string, percentage float64) bool {
	h := fnv.New32a()
	_, err := h.Write([]byte(sessionID))
	if err != nil {
		a.logger.Errorf("Error hashing session ID: %v", err)
		return false
	}
	hashValue := h.Sum32()
	normalizedHash := float64(hashValue%10000) / 10000.0
	result := normalizedHash < percentage/100.0
	if a.config.Debug {
		a.logger.Debugf("Session ID: %s, Percentage: %.2f, Normalized Hash: %.4f, Result: %v", sessionID, percentage, normalizedHash, result)
	}
	return result
}

func (a *Forklift) getMatchingRules(req *http.Request) []RoutingRule {
	matchingRules := []RoutingRule{}
	for _, rule := range a.config.Rules {
		if a.ruleEngine.ruleMatches(req, rule) {
			matchingRules = append(matchingRules, rule)
		}
	}
	return matchingRules
}

func (a *Forklift) mapBackendsToPercentages(matchingRules []RoutingRule) map[string]*backendInfo {
	backendPercentages := make(map[string]*backendInfo)
	for _, rule := range matchingRules {
		backend := rule.Backend
		percentage := rule.Percentage
		if percentage <= 0 || percentage > fullPercentage {
			percentage = fullPercentage
		}
		if _, exists := backendPercentages[backend]; !exists {
			backendPercentages[backend] = &backendInfo{
				Percentage: 0,
				Rules:      []*RoutingRule{},
			}
		}
		backendPercentages[backend].Percentage += percentage
		backendPercentages[backend].Rules = append(backendPercentages[backend].Rules, &rule)
	}
	return backendPercentages
}

func (a *Forklift) sumTotalPercentages(backendPercentages map[string]*backendInfo) float64 {
	totalPercentage := 0.0
	for _, info := range backendPercentages {
		totalPercentage += info.Percentage
	}
	return totalPercentage
}

func (a *Forklift) adjustPercentages(backendPercentages map[string]*backendInfo, totalPercentage *float64) {
	if *totalPercentage < fullPercentage {
		remaining := fullPercentage - *totalPercentage
		if _, exists := backendPercentages[a.config.DefaultBackend]; !exists {
			backendPercentages[a.config.DefaultBackend] = &backendInfo{
				Percentage: 0,
				Rules:      []*RoutingRule{},
			}
		}
		backendPercentages[a.config.DefaultBackend].Percentage += remaining
		*totalPercentage += remaining
	}

	if *totalPercentage > fullPercentage {
		for _, info := range backendPercentages {
			info.Percentage = info.Percentage * fullPercentage / *totalPercentage
		}
	}
}

func (a *Forklift) buildCumulativePercentages(backendPercentages map[string]*backendInfo) []backendEntry {
	backends := make([]backendEntry, 0, len(backendPercentages))
	for backend, info := range backendPercentages {
		backends = append(backends, backendEntry{
			Backend: backend,
			Info:    info,
		})
	}
	sort.Slice(backends, func(i, j int) bool {
		return backends[i].Backend < backends[j].Backend
	})

	currentLower := 0.0
	for i := range backends {
		be := &backends[i]
		be.LowerBound = currentLower
		be.UpperBound = currentLower + be.Info.Percentage
		currentLower = be.UpperBound
	}

	return backends
}

func (a *Forklift) selectBackendFromPercentages(backends []backendEntry, sessionID string) SelectedBackend {
	h := fnv.New64a()
	_, err := h.Write([]byte(sessionID))
	if err != nil {
		a.logger.Errorf("Error hashing session ID: %v", err)
		return SelectedBackend{Backend: a.config.DefaultBackend, Rule: nil}
	}
	hashValue := h.Sum64()

	totalWeight := uint64(0)
	for _, be := range backends {
		weight := uint64((be.UpperBound - be.LowerBound) * 1000000) // Multiply by 1,000,000 to preserve precision
		totalWeight += weight
	}

	if totalWeight == 0 {
		return SelectedBackend{Backend: a.config.DefaultBackend, Rule: nil}
	}

	selection := hashValue % totalWeight

	cumulativeWeight := uint64(0)
	for _, be := range backends {
		weight := uint64((be.UpperBound - be.LowerBound) * 1000000)
		cumulativeWeight += weight
		if selection < cumulativeWeight {
			selectedRule := be.Info.Rules[0]
			return SelectedBackend{
				Backend: be.Backend,
				Rule:    selectedRule,
			}
		}
	}

	// If no backend was selected (which shouldn't happen), use the default backend
	return SelectedBackend{Backend: a.config.DefaultBackend, Rule: nil}
}

func (a *Forklift) createProxyRequest(req *http.Request, backend string, selectedRule *RoutingRule) (*http.Request, error) {
	backendURL := a.constructBackendURL(req, backend, selectedRule)
	var proxyBody io.Reader
	if req.Body != nil {
		proxyBody = req.Body
	}
	proxyReq, err := http.NewRequest(req.Method, backendURL, proxyBody)
	if err != nil {
		return nil, err
	}

	// Copy headers from the original request
	proxyReq.Header = make(http.Header)
	for key, values := range req.Header {
		proxyReq.Header[key] = values
	}

	// Update the Host header to match the backend
	proxyReq.Host = proxyReq.URL.Host

	if a.config.Debug {
		a.logger.Debugf("Final request URL: %s", proxyReq.URL.String())
		a.logger.Debugf("Final request headers: %v", proxyReq.Header)
	}

	return proxyReq, nil
}

func (a *Forklift) constructBackendURL(req *http.Request, backend string, selectedRule *RoutingRule) string {
	backendPath := req.URL.Path
	if selectedRule != nil && selectedRule.PathPrefixRewrite != "" {
		// Perform path prefix rewrite
		if selectedRule.PathPrefix != "" && strings.HasPrefix(backendPath, selectedRule.PathPrefix) {
			backendPath = strings.Replace(backendPath, selectedRule.PathPrefix, selectedRule.PathPrefixRewrite, 1)
		}
	}
	return backend + backendPath
}

func (a *Forklift) sendProxyRequest(rw http.ResponseWriter, proxyReq *http.Request) {
	client := &http.Client{
		Timeout: defaultTimeout, // Default timeout for client requests
	}
	resp, err := client.Do(proxyReq)
	if err != nil {
		a.logger.Errorf("Error sending request to backend: %v", err)
		http.Error(rw, "Error sending request to backend", http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	// Copy the response from the backend to the original response writer
	for key, values := range resp.Header {
		for _, value := range values {
			rw.Header().Add(key, value)
		}
	}
	rw.WriteHeader(resp.StatusCode)
	_, err = io.Copy(rw, resp.Body)
	if err != nil {
		a.logger.Errorf("Error copying response body: %v", err)
		// If we've already started writing the response, we can't change the status code
		// So we'll just log the error and return
		return
	}

	if a.config.Debug {
		a.logger.Debugf("Response status code: %d", resp.StatusCode)
		a.logger.Debugf("Response headers: %v", resp.Header)
	}
}

// ruleMatches checks if a request matches a given rule.
func (re *RuleEngine) ruleMatches(req *http.Request, rule RoutingRule) bool {
	// Check exact path match
	if rule.Path != "" {
		if rule.Path != req.URL.Path {
			if re.config.Debug {
				re.logger.Debugf("Path mismatch: %s != %s", rule.Path, req.URL.Path)
			}
			return false
		}
	}
	// Check path prefix match
	if rule.PathPrefix != "" {
		if !strings.HasPrefix(req.URL.Path, rule.PathPrefix) {
			if re.config.Debug {
				re.logger.Debugf("Path prefix mismatch: %s for path: %s", rule.PathPrefix, req.URL.Path)
			}
			return false
		}
		if re.config.Debug {
			re.logger.Debugf("Path prefix match: %s for path: %s", rule.PathPrefix, req.URL.Path)
		}
	}
	// Check method match
	if rule.Method != "" && rule.Method != req.Method {
		if re.config.Debug {
			re.logger.Debugf("Method mismatch: %s != %s", rule.Method, req.Method)
		}
		return false
	}
	if re.config.Debug {
		re.logger.Debugf("Checking conditions for path: %s", req.URL.Path)
	}
	// If there are no conditions, return true if we've made it this far
	if len(rule.Conditions) == 0 {
		return true
	}
	return re.checkConditions(req, rule.Conditions)
}

// checkConditions verifies if all conditions in a rule are met.
func (re *RuleEngine) checkConditions(req *http.Request, conditions []RuleCondition) bool {
	for _, condition := range conditions {
		if !re.checkCondition(req, condition) {
			return false
		}
	}
	return true
}

// checkCondition checks a single condition.
func (re *RuleEngine) checkCondition(req *http.Request, condition RuleCondition) bool {
	result := false
	switch strings.ToLower(condition.Type) {
	case "header":
		result = re.checkHeader(req, condition)
	case "query":
		result = re.checkQuery(req, condition)
	case "cookie":
		result = re.checkCookie(req, condition)
	case "form":
		result = re.checkForm(req, condition)
	default:
		re.logger.Warnf("Unknown condition type: %s", condition.Type)
	}
	if re.config.Debug {
		re.logger.Debugf("Condition check result for %s %s: %v", condition.Type, condition.Parameter, result)
	}
	return result
}

func (re *RuleEngine) checkForm(req *http.Request, condition RuleCondition) bool {
	if err := req.ParseForm(); err != nil {
		re.logger.Errorf("Error parsing form data: %v", err)
		return false
	}
	formValue := req.PostFormValue(condition.Parameter)
	if re.config.Debug {
		re.logger.Debugf("Form parameter %s: %s", condition.Parameter, formValue)
	}
	result := compareValues(formValue, condition.Operator, condition.Value)
	if re.config.Debug {
		re.logger.Debugf("Form condition result: %v", result)
	}
	return result
}

func (re *RuleEngine) checkHeader(req *http.Request, condition RuleCondition) bool {
	headerValues := req.Header.Values(condition.Parameter)
	if re.config.Debug {
		re.logger.Debugf("Header %s values: %v", condition.Parameter, headerValues)
	}
	for _, headerValue := range headerValues {
		result := compareValues(strings.TrimSpace(strings.ToLower(headerValue)), condition.Operator, strings.TrimSpace(strings.ToLower(condition.Value)))
		if result {
			if re.config.Debug {
				re.logger.Debugf("Header condition result: true")
				re.logger.Debugf("Header condition details: Parameter=%s, Operator=%s, Value=%s", condition.Parameter, condition.Operator, condition.Value)
			}
			return true
		}
	}
	if re.config.Debug {
		re.logger.Debugf("Header condition result: false")
		re.logger.Debugf("Header condition details: Parameter=%s, Operator=%s, Value=%s", condition.Parameter, condition.Operator, condition.Value)
	}
	return false
}

func (re *RuleEngine) checkQuery(req *http.Request, condition RuleCondition) bool {
	queryValue := req.URL.Query().Get(condition.QueryParam)
	if re.config.Debug {
		re.logger.Debugf("Query parameter %s: %s", condition.QueryParam, queryValue)
		re.logger.Debugf("Comparing query value: %s %s %s", queryValue, condition.Operator, condition.Value)
	}
	result := compareValues(queryValue, condition.Operator, condition.Value)
	if re.config.Debug {
		re.logger.Debugf("Query condition result: %v", result)
	}
	return result
}

func (re *RuleEngine) checkCookie(req *http.Request, condition RuleCondition) bool {
	cookies := req.Cookies()
	for _, cookie := range cookies {
		if cookie.Name == condition.Parameter {
			result := compareValues(cookie.Value, condition.Operator, condition.Value)
			if re.config.Debug {
				re.logger.Debugf("Cookie %s value: %s", condition.Parameter, cookie.Value)
				re.logger.Debugf("Cookie condition result: %v", result)
				re.logger.Debugf("Cookie condition details: Parameter=%s, Operator=%s, Value=%s", condition.Parameter, condition.Operator, condition.Value)
			}
			return result
		}
	}
	if re.config.Debug {
		re.logger.Debugf("Cookie %s not found", condition.Parameter)
	}
	return false
}

// compareValues compares two string values based on the given operator.
func compareValues(actual, operator, expected string) bool {
	switch strings.ToLower(operator) {
	case "eq", "equals":
		return actual == expected
	case "contains":
		return strings.Contains(actual, expected)
	case "prefix":
		return strings.HasPrefix(actual, expected)
	case "suffix":
		return strings.HasSuffix(actual, expected)
	case "gt":
		actualFloat, expectedFloat := parseFloats(actual, expected)
		return actualFloat > expectedFloat
	default:
		return false
	}
}

// parseFloats attempts to parse two strings as float64 values.
func parseFloats(s1, s2 string) (float64, float64) {
	f1, _ := strconv.ParseFloat(s1, 64)
	f2, _ := strconv.ParseFloat(s2, 64)
	return f1, f2
}

// cleanupCache periodically removes old entries from the cache.
func (re *RuleEngine) cleanupCache() {
	ticker := time.NewTicker(cacheCleanupInterval)
	defer ticker.Stop()

	for range ticker.C {
		// Clear the entire cache periodically
		re.cache = &sync.Map{}
	}
}

// isValidSessionID checks if the given session ID is valid.
func isValidSessionID(sessionID string) bool {
	if len(sessionID) == 0 || len(sessionID) > maxSessionIDLength {
		return false
	}
	_, err := base64.URLEncoding.DecodeString(sessionID)
	return err == nil
}

// getOrCreateSessionID retrieves the existing session ID or creates a new one.
func getOrCreateSessionID(rw http.ResponseWriter, req *http.Request) string {
	cookie, err := req.Cookie(sessionCookieName)
	if err == nil && cookie.Value != "" && isValidSessionID(cookie.Value) {
		return cookie.Value
	}

	sessionID, err := generateSessionID()
	if err != nil {
		// Use the logger directly
		logger.NewLogger("forklift").Errorf("Error generating session ID: %v", err)
		return ""
	}

	http.SetCookie(rw, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sessionID,
		Path:     "/",
		MaxAge:   sessionCookieMaxAge,
		HttpOnly: true,
		Secure:   req.TLS != nil,
		SameSite: http.SameSiteStrictMode,
	})

	return sessionID
}
