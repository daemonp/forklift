package abtest

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"hash/fnv"
	"io"
	"io/ioutil"
	"log"
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

const (
	sessionCookieName    = "abtest_session_id"
	sessionCookieMaxAge  = 86400 * 30 // 30 days
	cacheDuration        = 24 * time.Hour
	cacheCleanupInterval = 10 * time.Minute
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
func NewABTest(next http.Handler, config *Config, name string) *ABTest {
	// Sort rules by priority (higher priority first)
	sort.Slice(config.Rules, func(i, j int) bool {
		return config.Rules[i].Priority > config.Rules[j].Priority
	})

	ruleEngine := &RuleEngine{
		config: config,
		cache:  &sync.Map{},
	}

	go ruleEngine.cleanupCache()

	return &ABTest{
		next:       next,
		config:     config,
		name:       name,
		ruleEngine: ruleEngine,
	}
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
		log.Printf("Received request: %s %s", req.Method, req.URL.Path)
		log.Printf("Headers: %v", req.Header)
	}

	// Check for existing session cookie
	var sessionID string
	cookie, err := req.Cookie(sessionCookieName)
	if err == http.ErrNoCookie {
		// Generate new session ID if cookie doesn't exist
		sessionID, err = generateSessionID()
		if err != nil {
			log.Printf("Error generating session ID: %v", err)
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
		log.Printf("Error reading session cookie: %v", err)
		http.Error(rw, "Internal Server Error", http.StatusInternalServerError)
		return
	} else {
		sessionID = cookie.Value
	}

	if debug {
		log.Printf("Session ID: %s", sessionID)
	}

	// Debug: Log POST payload
	var body []byte
	if req.Method == "POST" && req.Body != nil {
		// Read the body
		var err error
		body, err = ioutil.ReadAll(req.Body)
		if err != nil {
			log.Printf("Error reading request body: %v", err)
		} else {
			if debug {
				log.Printf("Request body: %s", string(body))
			}
			// Restore the body for further processing
			req.Body = ioutil.NopCloser(bytes.NewBuffer(body))
		}

		// Parse the form
		if err := req.ParseForm(); err != nil {
			log.Printf("Error parsing form: %v", err)
		} else if debug {
			log.Printf("POST form data: %v", req.PostForm)
		}
	} else if req.Method == "POST" {
		log.Printf("POST request with nil body")
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

	// Add a header to indicate the selected backend
	rw.Header().Set("X-Selected-Backend", backend)

	log.Printf("Routing request to backend: %s", backend)

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
		log.Printf("Error creating proxy request: %v", err)
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
		log.Printf("Final request URL: %s", proxyReq.URL.String())
		log.Printf("Final request headers: %v", proxyReq.Header)
	}

	// Send the request to the selected backend
	client := &http.Client{}
	resp, err := client.Do(proxyReq)
	if err != nil {
		log.Printf("Error sending request to backend: %v", err)
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
		log.Printf("Error copying response body: %v", err)
	}

	if debug {
		log.Printf("Response status code: %d", resp.StatusCode)
		log.Printf("Response headers: %v", resp.Header)
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
	formValue := req.PostFormValue(condition.Parameter)
	if debug {
		log.Printf("Form parameter %s: %s", condition.Parameter, formValue)
	}
	result := compareValues(formValue, condition.Operator, condition.Value)
	if debug {
		log.Printf("Form condition result: %v", result)
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
		log.Printf("Query parameter %s: %s", condition.QueryParam, queryValue)
	}
	result := compareValues(queryValue, condition.Operator, condition.Value)
	if debug {
		log.Printf("Query condition result: %v", result)
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

	log.Printf("Routing decision for session %s: V%d", sessionID, map[bool]int{false: 1, true: 2}[decision])

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
