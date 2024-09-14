// Package forklift provides a middleware for AB testing in Traefik.
package forklift

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	errEmptyConfig           = errors.New("empty configuration")
	errMissingV1Backend      = errors.New("missing V1Backend")
	errMissingV2Backend      = errors.New("missing V2Backend")
	errInvalidPercentage     = errors.New("invalid percentage: must be between 0 and 100")
	errMissingDefaultBackend = errors.New("missing DefaultBackend")
)

// Config holds the configuration for the AB testing middleware.
type Config struct {
	DefaultBackend string        `json:"defaultBackend,omitempty"`
	Rules          []RoutingRule `json:"rules,omitempty"`
	Debug          bool
	Logger         Logger
}

const (
	sessionCookieName    = "forklift_id"
	sessionCookieMaxAge  = 86400 * 30 // 30 days
	cacheDuration        = 24 * time.Hour
	cacheCleanupInterval = 10 * time.Minute
	maxSessionIDLength   = 128
	defaultTimeout       = 10 * time.Second
	v2Backend            = 2
)

// RoutingRule defines the structure for routing rules in the middleware.
type RoutingRule struct {
	Path              string          `json:"path,omitempty"`
	PathPrefix        string          `json:"pathPrefix,omitempty"`
	Method            string          `json:"method,omitempty"`
	Conditions        []RuleCondition `json:"conditions,omitempty"`
	Backend           string          `json:"backend,omitempty"`
	Priority          int             `json:"priority,omitempty"`
	PathPrefixRewrite string          `json:"pathPrefixRewrite,omitempty"`
}

// RuleCondition defines the structure for conditions in routing rules.
type RuleCondition struct {
	Type       string `json:"type,omitempty"`
	Parameter  string `json:"parameter,omitempty"`
	Operator   string `json:"operator,omitempty"`
	Value      string `json:"value,omitempty"`
	QueryParam string `json:"queryParam,omitempty"`
}

// Forklift is the main struct for the AB testing middleware.
type Forklift struct {
	next       http.Handler
	config     *Config
	name       string
	ruleEngine *RuleEngine
	debug      bool
	logger     Logger
}

// RuleEngine handles rule matching and caching.
type RuleEngine struct {
	config *Config
	cache  *sync.Map
}

// CreateConfig creates a new Config.
func CreateConfig() *Config {
	return &Config{
		Debug:          os.Getenv("DEBUG") == "true",
		Logger:         NewDefaultLogger(),
		DefaultBackend: "http://localhost:8080", // Default backend
		Rules:          []RoutingRule{},         // Initialize empty rules slice
	}
}

// New creates a new AB testing middleware.
func New(_ context.Context, next http.Handler, config *Config, name string) (http.Handler, error) {
	if config.Debug {
		config.Logger.Printf("Debug: Creating new AB testing middleware with config: %+v", config)
	}
	forklift, err := NewForklift(next, config, name)
	if err != nil {
		return nil, fmt.Errorf("failed to create AB test middleware: %w", err)
	}
	return forklift, nil
}

// NewForklift creates a new AB testing middleware.
func NewForklift(next http.Handler, config *Config, name string) (*Forklift, error) {
	if config == nil {
		return nil, errEmptyConfig
	}
	if config.DefaultBackend == "" {
		return nil, errMissingDefaultBackend
	}

	// Sort rules by priority (higher priority first)
	sort.Slice(config.Rules, func(i, j int) bool {
		return config.Rules[i].Priority > config.Rules[j].Priority
	})

	ruleEngine := &RuleEngine{
		config: config,
		cache:  &sync.Map{},
	}

	go ruleEngine.cleanupCache()

	// Ensure logger is initialized
	if config.Logger == nil {
		config.Logger = NewDefaultLogger()
	}

	forklift := &Forklift{
		next:       next,
		config:     config,
		name:       name,
		ruleEngine: ruleEngine,
		debug:      config.Debug,
		logger:     config.Logger,
	}

	forklift.logger.Infof("Starting forklift middleware: %s", name)

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

	backend := a.selectBackend(req)

	if a.debug {
		rw.Header().Set("X-Selected-Backend", backend)
		a.logger.Infof("Routing request to backend: %s", backend)
	}

	proxyReq, err := a.createProxyRequest(req, backend)
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

func (a *Forklift) selectBackend(req *http.Request) string {
	sessionID, err := req.Cookie(sessionCookieName)
	if err != nil {
		a.logger.Printf("Error getting session ID: %v", err)
		return a.config.DefaultBackend
	}

	for _, rule := range a.config.Rules {
		if a.ruleEngine.ruleMatches(req, rule) {
			if rule.Backend != "" {
				var totalThreshold float64
				for _, condition := range rule.Conditions {
					if condition.Type == "SessionID" {
						threshold, err := strconv.ParseFloat(condition.Value, 64)
						if err != nil {
							a.logger.Printf("Error parsing threshold: %v", err)
							continue
						}
						totalThreshold += threshold
						if a.ruleEngine.shouldRouteToV2(sessionID.Value, totalThreshold) {
							return rule.Backend
						}
					}
				}
			}
			// If no SessionID condition matched, return the rule's backend
			return rule.Backend
		}
	}
	return a.config.DefaultBackend
}

func (a *Forklift) createProxyRequest(req *http.Request, backend string) (*http.Request, error) {
	backendURL := a.constructBackendURL(req, backend)
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

func (a *Forklift) constructBackendURL(req *http.Request, backend string) string {
	var pathPrefix string
	for _, rule := range a.config.Rules {
		if a.ruleEngine.ruleMatches(req, rule) {
			pathPrefix = rule.PathPrefix
			break
		}
	}

	backendPath := req.URL.Path
	if pathPrefix != "" && strings.HasPrefix(backendPath, pathPrefix) {
		trimmedPath := strings.TrimPrefix(backendPath, pathPrefix)
		if trimmedPath == "" {
			trimmedPath = "/"
		}
		return backend + pathPrefix + trimmedPath
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
	if rule.PathPrefix != "" && !strings.HasPrefix(req.URL.Path, rule.PathPrefix) {
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

// shouldRouteToV2 determines if the request should be routed to V2 based on the percentage and session ID.
func (re *RuleEngine) shouldRouteToV2(sessionID string, percentage float64) bool {
	// Use the session ID as the key for consistent routing
	key := sessionID

	// Generate a consistent hash for the key
	h := fnv.New32a()
	_, err := h.Write([]byte(key))
	if err != nil {
		re.config.Logger.Printf("Error writing to hash: %v", err)
		return false
	}
	hashValue := h.Sum32()

	// Use the hash to make a consistent decision
	decision := float64(hashValue)/float64(^uint32(0)) < (percentage / 100)

	if re.config.Debug {
		re.config.Logger.Infof("Routing decision for session %s: V%d (percentage: %.2f)", sessionID, map[bool]int{false: 1, true: v2Backend}[decision], percentage)
	}

	return decision
}

// cleanupCache periodically removes old entries from the cache.
func (re *RuleEngine) cleanupCache() {
	ticker := time.NewTicker(cacheCleanupInterval)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()
		re.cache.Range(func(key, value interface{}) bool {
			if cacheEntry, ok := value.(time.Time); ok {
				if now.Sub(cacheEntry) > cacheDuration {
					re.cache.Delete(key)
				}
			}
			return true
		})
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
