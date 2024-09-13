package abtest

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"hash/fnv"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/patrickmn/go-cache"
	"github.com/sirupsen/logrus"
)

const (
	sessionCookieName = "abtest_session_id"
	sessionCookieMaxAge = 86400 * 30 // 30 days
)

// Config holds the configuration for the AB testing middleware
type Config struct {
	V1Backend string        `json:"v1Backend,omitempty"`
	V2Backend string        `json:"v2Backend,omitempty"`
	Rules     []RoutingRule `json:"rules,omitempty"`
}

// RoutingRule defines a rule for AB testing
type RoutingRule struct {
	Path       string          `json:"path,omitempty"`
	PathPrefix string          `json:"pathPrefix,omitempty"`
	Method     string          `json:"method,omitempty"`
	Conditions []RuleCondition `json:"conditions,omitempty"`
	Backend    string          `json:"backend,omitempty"`
	Percentage float64         `json:"percentage,omitempty"`
	Priority   int             `json:"priority,omitempty"`
}

// RuleCondition defines a condition for a routing rule
type RuleCondition struct {
	Type      string `json:"type,omitempty"`
	Parameter string `json:"parameter,omitempty"`
	Operator  string `json:"operator,omitempty"`
	Value     string `json:"value,omitempty"`
}

// ABTest is the main struct for the AB testing middleware
type ABTest struct {
	next       http.Handler
	config     *Config
	name       string
	ruleEngine *RuleEngine
	logger     *logrus.Logger
}

// generateSessionID creates a new random session ID
func (a *ABTest) generateSessionID() (string, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	if err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

// RuleEngine handles rule matching and caching
type RuleEngine struct {
	config *Config
	cache  *cache.Cache
}

// CreateConfig creates a new Config
func CreateConfig() *Config {
	return &Config{}
}

// New creates a new AB testing middleware
func New(ctx context.Context, next http.Handler, config *Config, name string) (http.Handler, error) {
	logger := logrus.New()
	logger.SetLevel(logrus.InfoLevel)

	// Sort rules by priority (higher priority first)
	sort.Slice(config.Rules, func(i, j int) bool {
		return config.Rules[i].Priority > config.Rules[j].Priority
	})

	ruleEngine := &RuleEngine{
		config: config,
		cache:  cache.New(24*time.Hour, 10*time.Minute),
	}

	return &ABTest{
		next:       next,
		config:     config,
		name:       name,
		ruleEngine: ruleEngine,
		logger:     logger,
	}, nil
}

// ServeHTTP implements the http.Handler interface
func (a *ABTest) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	// Check for existing session cookie
	var sessionID string
	cookie, err := req.Cookie(sessionCookieName)
	if err == http.ErrNoCookie {
		// Generate new session ID if cookie doesn't exist
		sessionID, err = a.generateSessionID()
		if err != nil {
			a.logger.Errorf("Error generating session ID: %v", err)
			http.Error(rw, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		cookie = &http.Cookie{
			Name:     sessionCookieName,
			Value:    sessionID,
			Path:     "/",
			MaxAge:   sessionCookieMaxAge,
			HttpOnly: true,
			Secure:   req.TLS != nil, // Set Secure flag if the request is over HTTPS
			SameSite: http.SameSiteStrictMode,
		}
		http.SetCookie(rw, cookie)
	} else if err != nil {
		a.logger.Errorf("Error reading session cookie: %v", err)
		http.Error(rw, "Internal Server Error", http.StatusInternalServerError)
		return
	} else {
		sessionID = cookie.Value
	}

	backend := a.config.V1Backend
	for _, rule := range a.config.Rules {
		if a.ruleEngine.ruleMatches(req, rule) {
			if rule.Backend != "" {
				backend = rule.Backend
			} else if a.ruleEngine.shouldRouteToV2(sessionID, rule.Percentage) {
				backend = a.config.V2Backend
			}
			break
		}
	}

	// Log the decision for debugging
	a.logger.WithFields(logrus.Fields{
		"sessionID": sessionID,
		"backend":   backend,
	}).Debug("Routing decision made")

	// Add a header to indicate the selected backend
	rw.Header().Set("X-Selected-Backend", backend)

	a.logger.Infof("Routing request to backend: %s", backend)

	// Create a new request to the selected backend
	// Handle path prefix rewrite
	var pathPrefix string
	for _, rule := range a.config.Rules {
		if a.ruleEngine.ruleMatches(req, rule) {
			pathPrefix = rule.PathPrefix
			break
		}
	}

	// Create a new request to the selected backend
	backendPath := req.URL.Path
	var backendURL string
	if pathPrefix != "" && strings.HasPrefix(backendPath, pathPrefix) {
		// For API routing, we want to keep the original path
		trimmedPath := strings.TrimPrefix(backendPath, pathPrefix)
		if trimmedPath == "" {
			trimmedPath = "/"
		}
		backendURL = backend + pathPrefix + trimmedPath
	} else {
		backendURL = backend + backendPath
	}
	proxyReq, err := http.NewRequest(req.Method, backendURL, req.Body)
	if err != nil {
		a.logger.Errorf("Error creating proxy request: %v", err)
		http.Error(rw, "Error creating proxy request", http.StatusInternalServerError)
		return
	}

	// Copy headers from the original request
	proxyReq.Header = make(http.Header)
	for key, values := range req.Header {
		proxyReq.Header[key] = values
	}

	// Update the Host header to match the backend
	proxyReq.Host = proxyReq.URL.Host

	// Log the path rewrite for debugging
	a.logger.WithFields(logrus.Fields{
		"originalPath": req.URL.Path,
		"rewrittenPath": backendPath,
		"backend": backend,
	}).Debug("Path rewrite applied")

	// Send the request to the selected backend
	client := &http.Client{}
	resp, err := client.Do(proxyReq)
	if err != nil {
		a.logger.Errorf("Error sending request to backend: %v", err)
		http.Error(rw, "Error sending request to backend", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

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
	}
}

// SelectBackend chooses the appropriate backend based on the request, rules, and session ID
func (re *RuleEngine) SelectBackend(req *http.Request, sessionID string) string {
	for _, rule := range re.config.Rules {
		if re.ruleMatches(req, rule) {
			if rule.Backend != "" {
				return rule.Backend
			}
			if re.shouldRouteToV2(sessionID, rule.Percentage) {
				return re.config.V2Backend
			}
			return re.config.V1Backend
		}
	}
	return re.config.V1Backend
}

// ruleMatches checks if a request matches a given rule
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
	return re.checkConditions(req, rule.Conditions)
}

// checkConditions verifies if all conditions in a rule are met
func (re *RuleEngine) checkConditions(req *http.Request, conditions []RuleCondition) bool {
	for _, condition := range conditions {
		if !re.checkCondition(req, condition) {
			return false
		}
	}
	return true
}

// checkCondition checks a single condition
func (re *RuleEngine) checkCondition(req *http.Request, condition RuleCondition) bool {
	switch strings.ToLower(condition.Type) {
	case "header":
		return checkHeader(req, condition)
	case "query":
		return checkQuery(req, condition)
	case "cookie":
		return checkCookie(req, condition)
	case "form":
		return checkForm(req, condition)
	default:
		return false
	}
}

func checkForm(req *http.Request, condition RuleCondition) bool {
	if err := req.ParseForm(); err != nil {
		return false
	}
	formValue := req.Form.Get(condition.Parameter)
	return compareValues(formValue, condition.Operator, condition.Value)
}

func checkHeader(req *http.Request, condition RuleCondition) bool {
	headerValue := req.Header.Get(condition.Parameter)
	return compareValues(headerValue, condition.Operator, condition.Value)
}

func checkQuery(req *http.Request, condition RuleCondition) bool {
	queryValue := req.URL.Query().Get(condition.Parameter)
	return compareValues(queryValue, condition.Operator, condition.Value)
}

func checkCookie(req *http.Request, condition RuleCondition) bool {
	cookie, err := req.Cookie(condition.Parameter)
	if err != nil {
		return false
	}
	return compareValues(cookie.Value, condition.Operator, condition.Value)
}

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
		actualFloat, err1 := strconv.ParseFloat(actual, 64)
		expectedFloat, err2 := strconv.ParseFloat(expected, 64)
		if err1 == nil && err2 == nil {
			return actualFloat > expectedFloat
		}
		return false
	default:
		return false
	}
}

// shouldRouteToV2 determines if the request should be routed to V2 based on the percentage and session ID
func (re *RuleEngine) shouldRouteToV2(sessionID string, percentage float64) bool {
	// Use the session ID as the key for consistent routing
	key := sessionID

	// Check if we have a cached decision for this key
	if cachedDecision, found := re.cache.Get(key); found {
		return cachedDecision.(bool)
	}

	// Generate a consistent hash for the key
	h := fnv.New32a()
	h.Write([]byte(key))
	hashValue := h.Sum32()

	// Use the hash to make a consistent decision
	decision := float64(hashValue)/float64(^uint32(0)) < (percentage / 100.0)

	// Cache the decision
	re.cache.Set(key, decision, cache.DefaultExpiration)

	return decision
}
