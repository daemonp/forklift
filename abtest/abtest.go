package abtest

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/daemonp/traefik-forklift-middleware/abtest/config"
)

var debug = os.Getenv("DEBUG") == "true"

type DefaultLogger struct{}

var logger Logger = DefaultLogger{}

type Logger interface {
	Printf(format string, v ...interface{})
	Infof(format string, v ...interface{})
}

func (l DefaultLogger) Printf(format string, v ...interface{}) {
	fmt.Fprintf(os.Stdout, format+"\n", v...)
}

func (l DefaultLogger) Infof(format string, v ...interface{}) {
	fmt.Fprintf(os.Stdout, "level=info msg=\""+format+"\"\n", v...)
}

func SetLogger(l Logger) {
	logger = l
}

const (
	sessionCookieName    = "abtest_session_id"
	sessionCookieMaxAge  = 86400 * 30 // 30 days
	cacheDuration        = 24 * time.Hour
	cacheCleanupInterval = 10 * time.Minute
	maxSessionIDLength   = 128
)

// Use the Config type from the config package
type Config = config.Config
type RoutingRule = config.Rule
type RuleCondition = config.RuleCondition

// ABTest is the main struct for the AB testing middleware
type ABTest struct {
	next       http.Handler
	config     *Config
	name       string
	ruleEngine *RuleEngine
}

// RuleEngine handles rule matching and caching
type RuleEngine struct {
	config *Config
	cache  *sync.Map
}

// NewABTest creates a new AB testing middleware
func NewABTest(next http.Handler, config *Config, name string) (*ABTest, error) {
	if config == nil {
		return nil, fmt.Errorf("empty configuration")
	}
	if config.V1Backend == "" {
		return nil, fmt.Errorf("missing V1Backend")
	}
	if config.V2Backend == "" {
		return nil, fmt.Errorf("missing V2Backend")
	}
	for _, rule := range config.Rules {
		if rule.Percentage < 0 || rule.Percentage > 100 {
			return nil, fmt.Errorf("invalid percentage: must be between 0 and 100")
		}
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

	abtest := &ABTest{
		next:       next,
		config:     config,
		name:       name,
		ruleEngine: ruleEngine,
	}

	logger.Infof("Starting forklift middleware: %s", name)

	return abtest, nil
}

// generateSessionID creates a new random session ID
func generateSessionID() (string, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	if err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

// ServeHTTP implements the http.Handler interface
func (a *ABTest) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	if debug {
		logger.Infof("Received request: %s %s", req.Method, req.URL.Path)
		logger.Infof("Headers: %v", req.Header)
	}

	// Check for existing session cookie
	sessionID := getOrCreateSessionID(rw, req)
	if sessionID == "" {
		logger.Infof("Error handling session ID")
		http.Error(rw, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	if req == nil {
		logger.Printf("Error: Request is nil")
		http.Error(rw, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	if debug {
		logger.Infof("Session ID: %s", sessionID)
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

	if debug {
		rw.Header().Set("X-Selected-Backend", backend)
		logger.Infof("Routing request to backend: %s", backend)
	}

	// Create a new request to the selected backend
	var pathPrefix string
	for _, rule := range a.config.Rules {
		if a.ruleEngine.ruleMatches(req, rule) {
			pathPrefix = rule.PathPrefix
			break
		}
	}

	backendPath := req.URL.Path
	var backendURL string
	if pathPrefix != "" && strings.HasPrefix(backendPath, pathPrefix) {
		trimmedPath := strings.TrimPrefix(backendPath, pathPrefix)
		if trimmedPath == "" {
			trimmedPath = "/"
		}
		backendURL = backend + pathPrefix + trimmedPath
	} else {
		backendURL = backend + backendPath
	}
	var proxyBody io.Reader
	if req.Body != nil {
		proxyBody = req.Body
	}
	proxyReq, err := http.NewRequest(req.Method, backendURL, proxyBody)
	if err != nil {
		logger.Printf("Error creating proxy request: %v", err)
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

	if debug {
		logger.Infof("Final request URL: %s", proxyReq.URL.String())
		logger.Infof("Final request headers: %v", proxyReq.Header)
	}

	// Send the request to the selected backend
	client := &http.Client{
		Timeout: 10 * time.Second,
	}
	resp, err := client.Do(proxyReq)
	if err != nil {
		logger.Printf("Error sending request to backend: %v", err)
		http.Error(rw, "Error sending request to backend", http.StatusBadGateway)
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
		logger.Printf("Error copying response body: %v", err)
		// If we've already started writing the response, we can't change the status code
		// So we'll just log the error and return
		return
	}

	if debug {
		logger.Infof("Response status code: %d", resp.StatusCode)
		logger.Infof("Response headers: %v", resp.Header)
	}
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
	if debug {
		logger.Infof("Checking conditions for path: %s", req.URL.Path)
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
	result := false
	switch strings.ToLower(condition.Type) {
	case "header":
		result = checkHeader(req, condition)
	case "query":
		result = checkQuery(req, condition)
	case "cookie":
		result = checkCookie(req, condition)
	case "form":
		result = checkForm(req, condition)
	}
	if debug {
		logger.Infof("Condition check result for %s %s: %v", condition.Type, condition.Parameter, result)
	}
	return result
}

func checkForm(req *http.Request, condition RuleCondition) bool {
	formValue := req.PostFormValue(condition.Parameter)
	if debug {
		logger.Infof("Form parameter %s: %s", condition.Parameter, formValue)
	}
	result := compareValues(formValue, condition.Operator, condition.Value)
	if debug {
		logger.Infof("Form condition result: %v", result)
	}
	return result
}

func checkHeader(req *http.Request, condition RuleCondition) bool {
	headerValue := req.Header.Get(condition.Parameter)
	return compareValues(headerValue, condition.Operator, condition.Value)
}

func checkQuery(req *http.Request, condition RuleCondition) bool {
	queryValue := req.URL.Query().Get(condition.QueryParam)
	if debug {
		logger.Infof("Query parameter %s: %s", condition.QueryParam, queryValue)
		logger.Infof("Comparing query value: %s %s %s", queryValue, condition.Operator, condition.Value)
	}
	result := compareValues(queryValue, condition.Operator, condition.Value)
	if debug {
		logger.Infof("Query condition result: %v", result)
	}
	return result
}

func checkCookie(req *http.Request, condition RuleCondition) bool {
	cookie, err := req.Cookie(condition.Parameter)
	if err != nil {
		return false
	}
	return compareValues(cookie.Value, condition.Operator, condition.Value)
}

// compareValues compares two string values based on the given operator
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

// parseFloats attempts to parse two strings as float64 values
func parseFloats(s1, s2 string) (float64, float64) {
	f1, _ := strconv.ParseFloat(s1, 64)
	f2, _ := strconv.ParseFloat(s2, 64)
	return f1, f2
}

// shouldRouteToV2 determines if the request should be routed to V2 based on the percentage and session ID
func (re *RuleEngine) shouldRouteToV2(sessionID string, percentage float64) bool {
	// Always route to V1 if percentage is 0
	if percentage == 0 {
		return false
	}

	// Always route to V2 if percentage is 100
	if percentage == 100 {
		return true
	}

	// Use the session ID as the key for consistent routing
	key := sessionID

	// Check if we have a cached decision for this key
	if cachedDecision, found := re.cache.Load(key); found {
		return cachedDecision.(bool)
	}

	// Generate a consistent hash for the key
	h := fnv.New32a()
	h.Write([]byte(key))
	hashValue := h.Sum32()

	// Use the hash to make a consistent decision
	decision := float64(hashValue)/float64(^uint32(0)) < (percentage / 100.0)

	// Cache the decision
	re.cache.Store(key, decision)

	if debug {
		logger.Infof("Routing decision for session %s: V%d", sessionID, map[bool]int{false: 1, true: 2}[decision])
	}

	return decision
}

// cleanupCache periodically removes old entries from the cache
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

// isValidSessionID checks if the given session ID is valid
func isValidSessionID(sessionID string) bool {
	if len(sessionID) == 0 || len(sessionID) > maxSessionIDLength {
		return false
	}
	_, err := base64.URLEncoding.DecodeString(sessionID)
	return err == nil
}

// getOrCreateSessionID retrieves the existing session ID or creates a new one
func getOrCreateSessionID(rw http.ResponseWriter, req *http.Request) string {
	cookie, err := req.Cookie(sessionCookieName)
	if err == nil && cookie.Value != "" && isValidSessionID(cookie.Value) {
		return cookie.Value
	}

	sessionID, err := generateSessionID()
	if err != nil {
		logger.Printf("Error generating session ID: %v", err)
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
