// Package forklift provides a middleware for flexible routing in Traefik v3.
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
	"github.com/traefik/traefik/v3/pkg/log"
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
	logger     log.Logger
}

// RuleEngine handles rule matching and caching.
type RuleEngine struct {
	config *config.Config
	cache  *sync.Map
	logger log.Logger
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
}

// CreateConfig creates a new Config.
func CreateConfig() *config.Config {
	return config.NewConfig()
}

// New creates a new middleware.
func New(ctx context.Context, next http.Handler, cfg *config.Config, name string) (http.Handler, error) {
	logger := logger.NewLogger("forklift")
	if cfg.Debug {
		logger.Debug().Msgf("Creating new Forklift middleware with config: %+v", cfg)
	}
	forklift, err := NewForklift(ctx, next, cfg, name)
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

	logger := logger.NewLogger("forklift").With().Str("middleware", name).Logger()

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

	forklift.logger.Info().Msgf("Starting Forklift middleware: %s", name)

	return forklift, nil
}

// generateSessionID creates a new random session ID.
func generateSessionID() (string, error) {
	const sessionIDBytes = 32
	b := make([]byte, sessionIDBytes)
	_, err := rand.Read(b)
	if err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

// ServeHTTP implements the http.Handler interface.
func (a *Forklift) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	if a.debug {
		a.logger.Infof("Received request: %s %s", req.Method, req.URL.Path)
		a.logger.Infof("Headers: %v", req.Header)
	}

	sessionID := a.handleSessionID(rw, req)
	if sessionID == "" {
		return
	}

	selected := a.selectBackend(req, sessionID)
	backend := selected.Backend
	selectedRule := selected.Rule

	if a.debug {
		rw.Header().Set("X-Selected-Backend", backend)
		a.logger.Infof("Routing request to backend: %s", backend)
	}

	proxyReq, err := a.createProxyRequest(req, backend, selectedRule)
	if err != nil {
		a.logger.Printf("Error creating proxy request: %v", err)
		http.Error(rw, "Error creating proxy request", http.StatusInternalServerError)
		return
	}

	a.sendProxyRequest(rw, proxyReq)
}

func (a *Forklift) handleSessionID(rw http.ResponseWriter, req *http.Request) string {
	sessionID := getOrCreateSessionID(rw, req)
	if sessionID == "" {
		a.logger.Infof("Error handling session ID")
		http.Error(rw, "Internal Server Error", http.StatusInternalServerError)
		return ""
	}

	if req == nil {
		a.logger.Printf("Error: Request is nil")
		http.Error(rw, "Internal Server Error", http.StatusInternalServerError)
		return ""
	}

	if a.debug {
		a.logger.Infof("Session ID: %s", sessionID)
	}

	return sessionID
}

type selectedBackend struct {
	Backend string
	Rule    *RoutingRule
}

func (a *Forklift) selectBackend(req *http.Request, sessionID string) selectedBackend {
	matchingRules := a.getMatchingRules(req)

	if len(matchingRules) == 0 {
		return selectedBackend{Backend: a.config.DefaultBackend, Rule: nil}
	}

	backendPercentages := a.mapBackendsToPercentages(matchingRules)
	totalPercentage := a.sumTotalPercentages(backendPercentages)

	a.adjustPercentages(backendPercentages, &totalPercentage)

	backends := a.buildCumulativePercentages(backendPercentages)

	return a.selectBackendFromPercentages(backends, sessionID)
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

func (a *Forklift) selectBackendFromPercentages(backends []backendEntry, sessionID string) selectedBackend {
	h := fnv.New32a()
	_, err := h.Write([]byte(sessionID))
	if err != nil {
		a.logger.Printf("Error hashing session ID: %v", err)
		return selectedBackend{Backend: a.config.DefaultBackend, Rule: nil}
	}
	hashValue := h.Sum32()
	hashPercentage := float64(hashValue%hashModulo) / hashDivisor

	for _, be := range backends {
		if hashPercentage >= be.LowerBound && hashPercentage < be.UpperBound {
			selectedRule := be.Info.Rules[0]
			return selectedBackend{
				Backend: be.Backend,
				Rule:    selectedRule,
			}
		}
	}

	return selectedBackend{Backend: a.config.DefaultBackend, Rule: nil}
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

	if a.debug {
		a.logger.Infof("Final request URL: %s", proxyReq.URL.String())
		a.logger.Infof("Final request headers: %v", proxyReq.Header)
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
		a.logger.Printf("Error sending request to backend: %v", err)
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
		a.logger.Printf("Error copying response body: %v", err)
		// If we've already started writing the response, we can't change the status code
		// So we'll just log the error and return
		return
	}

	if a.debug {
		a.logger.Infof("Response status code: %d", resp.StatusCode)
		a.logger.Infof("Response headers: %v", resp.Header)
	}
}

// ruleMatches checks if a request matches a given rule.
func (re *RuleEngine) ruleMatches(req *http.Request, rule RoutingRule) bool {
	if rule.Path != "" && rule.Path != req.URL.Path {
		return false
	}
	if rule.PathPrefix != "" && !matchPathPrefix(req.URL.Path, rule.PathPrefix) {
		return false
	}
	if rule.Method != "" && rule.Method != req.Method {
		return false
	}
	if re.config.Debug {
		re.config.Logger.Infof("Checking conditions for path: %s", req.URL.Path)
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
	}
	if re.config.Debug {
		re.config.Logger.Infof("Condition check result for %s %s: %v", condition.Type, condition.Parameter, result)
	}
	return result
}

func (re *RuleEngine) checkForm(req *http.Request, condition RuleCondition) bool {
	if err := req.ParseForm(); err != nil {
		re.config.Logger.Printf("Error parsing form data: %v", err)
		return false
	}
	formValue := req.PostFormValue(condition.Parameter)
	if re.config.Debug {
		re.config.Logger.Infof("Form parameter %s: %s", condition.Parameter, formValue)
	}
	result := compareValues(formValue, condition.Operator, condition.Value)
	if re.config.Debug {
		re.config.Logger.Infof("Form condition result: %v", result)
	}
	return result
}

func (re *RuleEngine) checkHeader(req *http.Request, condition RuleCondition) bool {
	headerValue := req.Header.Get(condition.Parameter)
	return compareValues(headerValue, condition.Operator, condition.Value)
}

func (re *RuleEngine) checkQuery(req *http.Request, condition RuleCondition) bool {
	queryValue := req.URL.Query().Get(condition.QueryParam)
	if re.config.Debug {
		re.config.Logger.Infof("Query parameter %s: %s", condition.QueryParam, queryValue)
		re.config.Logger.Infof("Comparing query value: %s %s %s", queryValue, condition.Operator, condition.Value)
	}
	result := compareValues(queryValue, condition.Operator, condition.Value)
	if re.config.Debug {
		re.config.Logger.Infof("Query condition result: %v", result)
	}
	return result
}

func (re *RuleEngine) checkCookie(req *http.Request, condition RuleCondition) bool {
	cookie, err := req.Cookie(condition.Parameter)
	if err != nil {
		return false
	}
	return compareValues(cookie.Value, condition.Operator, condition.Value)
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
		log.Printf("Error generating session ID: %v", err)
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

// Logger is the interface for logging in the middleware.
// Traefik only accepts stdin/stdout for logging.
type Logger interface {
	Printf(format string, v ...interface{})
	Infof(format string, v ...interface{})
}

// DefaultLogger is the default implementation of the Logger interface.
type DefaultLogger struct{}

// Printf logs a formatted message.
func (l DefaultLogger) Printf(format string, v ...interface{}) {
	_, _ = fmt.Fprintf(os.Stdout, format+"\n", v...)
}

// Infof logs a formatted info message.
func (l DefaultLogger) Infof(format string, v ...interface{}) {
	_, _ = fmt.Fprintf(os.Stdout, "level=info msg=\""+format+"\"\n", v...)
}

// SetLogger sets the logger for the middleware.
func (c *Config) SetLogger(l Logger) {
	c.Logger = l
}

// GetLogger returns the current logger instance.
func (c *Config) GetLogger() *DefaultLogger {
	if dl, ok := c.Logger.(*DefaultLogger); ok {
		return dl
	}
	return NewDefaultLogger()
}

// NewDefaultLogger returns a new instance of DefaultLogger.
func NewDefaultLogger() *DefaultLogger {
	return &DefaultLogger{}
}