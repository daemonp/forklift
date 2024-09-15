// Package forklift provides a middleware plugin for A/B testing
package forklift

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"hash"
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

// RoutingRule is an alias for config.RoutingRule.
type RoutingRule = config.RoutingRule

// RuleCondition is an alias for config.RuleCondition.
type RuleCondition = config.RuleCondition

var (
	errEmptyConfig           = errors.New("empty configuration")
	errMissingDefaultBackend = errors.New("missing DefaultBackend")
	errInvalidPercentage     = errors.New("invalid percentage: must be between 0 and 100")
)

const (
	sessionCookieName    = "forklift_id"
	sessionCookieMaxAge  = 86400 * 30
	cacheDuration        = 24 * time.Hour
	cacheCleanupInterval = 10 * time.Minute
	maxSessionIDLength   = 128
	defaultTimeout       = 10 * time.Second
	hashModulo           = 10000
	sessionIDByteLength  = 32
	maxPercentage        = 100.0
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

// NewRuleEngine creates a new RuleEngine instance.
func NewRuleEngine(cfg *config.Config, logger logger.Logger) *RuleEngine {
	return &RuleEngine{
		config: cfg,
		cache:  &sync.Map{},
		logger: logger,
	}
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
		return nil, errors.New("invalid configuration type")
	}

	if parsedConfig.DefaultBackend == "" {
		return nil, errors.New("DefaultBackend must be set")
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
func NewForklift(_ context.Context, next http.Handler, cfg *config.Config, name string) (*Forklift, error) {
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

	// Turn off debugging
	cfg.Debug = false

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
	b := make([]byte, sessionIDByteLength)
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

// SelectedBackend represents the selected backend and associated rule.
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
	backends := a.sortBackends(backendPercentages)
	hashValue := a.calculateHash(sessionID, matchingRules)
	selectedBackend := a.selectBackendFromRanges(backends, backendPercentages, hashValue)

	if a.config.Debug {
		a.logger.Debugf("Selected backend: %s (hash value: %f)", selectedBackend, hashValue)
		a.logger.Debugf("Backend percentages: %v", backendPercentages)
	}

	return selectedBackend
}

func (a *Forklift) sortBackends(backendPercentages map[string]float64) []string {
	backends := make([]string, 0, len(backendPercentages))
	for backend := range backendPercentages {
		backends = append(backends, backend)
	}
	sort.Strings(backends)
	return backends
}

func (a *Forklift) selectBackendFromRanges(backends []string, backendPercentages map[string]float64, hashValue float64) string {
	cumulativeRanges := a.createCumulativeRanges(backends, backendPercentages)

	totalPercentage := 0.0
	for _, percentage := range backendPercentages {
		totalPercentage += percentage
	}

	scaledHashValue := hashValue * (totalPercentage / maxPercentage)

	var selectedBackend string
	for _, backend := range backends {
		if scaledHashValue <= cumulativeRanges[backend] {
			selectedBackend = backend
			break
		}
	}

	if selectedBackend == "" {
		selectedBackend = backends[len(backends)-1] // Default to the last backend if no match.
	}

	return selectedBackend
}

func (a *Forklift) createCumulativeRanges(backends []string, backendPercentages map[string]float64) map[string]float64 {
	ranges := make(map[string]float64)
	totalPercentage := 0.0
	for _, backend := range backends {
		percentage := backendPercentages[backend]
		totalPercentage += percentage
		ranges[backend] = totalPercentage
	}

	if totalPercentage > maxPercentage {
		factor := maxPercentage / totalPercentage
		for backend := range ranges {
			ranges[backend] *= factor
		}
	}

	return ranges
}

func (a *Forklift) calculateHash(sessionID string, matchingRules []RoutingRule) float64 {
	h := fnv.New64a()
	a.writeToHash(h, []byte(sessionID))

	for _, rule := range matchingRules {
		if rule.AffinityToken != "" {
			a.writeToHash(h, []byte(rule.AffinityToken))
		} else {
			a.writeToHash(h, []byte(rule.Path), []byte(rule.Method), []byte(rule.Backend))
		}
	}

	hashValue := float64(h.Sum64()) / float64(^uint64(0)) * maxPercentage
	if a.config.Debug {
		a.logger.Debugf("Calculated hash value: %f", hashValue)
	}
	return hashValue
}

func (a *Forklift) writeToHash(h hash.Hash64, data ...[]byte) {
	var err error
	for _, d := range data {
		_, err = h.Write(d)
		if err != nil {
			a.logger.Errorf("Error hashing data: %v", err)
		}
	}
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
	if !re.matchPath(req, rule) {
		return false
	}
	if !re.matchMethod(req, rule) {
		return false
	}
	return re.matchConditions(req, rule)
}

func (re *RuleEngine) matchPath(req *http.Request, rule RoutingRule) bool {
	if rule.Path != "" && rule.Path != req.URL.Path {
		re.logDebugf("Path mismatch: %s != %s", rule.Path, req.URL.Path)
		return false
	}
	if rule.PathPrefix != "" && !strings.HasPrefix(req.URL.Path, rule.PathPrefix) {
		re.logDebugf("Path prefix mismatch: %s for path: %s", rule.PathPrefix, req.URL.Path)
		return false
	}
	if rule.PathPrefix != "" {
		re.logDebugf("Path prefix match: %s for path: %s", rule.PathPrefix, req.URL.Path)
	}
	return true
}

func (re *RuleEngine) matchMethod(req *http.Request, rule RoutingRule) bool {
	if rule.Method != "" && rule.Method != req.Method {
		re.logDebugf("Method mismatch: %s != %s", rule.Method, req.Method)
		return false
	}
	return true
}

func (re *RuleEngine) matchConditions(req *http.Request, rule RoutingRule) bool {
	re.logDebugf("Checking conditions for path: %s", req.URL.Path)
	if len(rule.Conditions) == 0 {
		return true
	}
	return re.checkConditions(req, rule.Conditions)
}

func (re *RuleEngine) logDebugf(format string, args ...interface{}) {
	if re.config.Debug {
		re.logger.Debugf(format, args...)
	}
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
