package abtest

import (
	"context"
	"hash/fnv"
	"math"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/patrickmn/go-cache"
	"github.com/traefik/traefik/v2/pkg/log"
	"github.com/traefik/traefik/v2/pkg/middlewares"
)

// Config holds the middleware configuration.
type Config struct {
	V1Backend string        `json:"v1Backend,omitempty"`
	V2Backend string        `json:"v2Backend,omitempty"`
	Rules     []RoutingRule `json:"rules,omitempty"`
}

// RoutingRule defines a single routing rule.
type RoutingRule struct {
	Path       string          `json:"path,omitempty"`
	Method     string          `json:"method,omitempty"`
	Conditions []RuleCondition `json:"conditions,omitempty"`
	Backend    string          `json:"backend,omitempty"`
	Percentage float64         `json:"percentage,omitempty"`
}

// RuleCondition defines a condition for a routing rule.
type RuleCondition struct {
	Type      string `json:"type,omitempty"`      // "query", "form", "header"
	Parameter string `json:"parameter,omitempty"` // The name of the parameter to check
	Operator  string `json:"operator,omitempty"`  // "eq", "ne", "gt", "lt", "contains", "regex"
	Value     string `json:"value,omitempty"`     // The value to compare against
}

// CreateConfig creates the default plugin configuration.
func CreateConfig() *Config {
	return &Config{
		V1Backend: "",
		V2Backend: "",
		Rules:     []RoutingRule{},
	}
}

// ABTest is a middleware for A/B testing.
type ABTest struct {
	next   http.Handler
	config *Config
	name   string
	cache  *cache.Cache
}

// New creates a new ABTest middleware.
func New(ctx context.Context, next http.Handler, config *Config, name string) (http.Handler, error) {
	log.FromContext(middlewares.GetLoggerCtx(ctx, name, "abtest")).Debug("Creating middleware")

	return &ABTest{
		next:   next,
		config: config,
		name:   name,
		cache:  cache.New(24*time.Hour, 10*time.Minute),
	}, nil
}

// ServeHTTP implements the http.Handler interface.
func (a *ABTest) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	logger := log.FromContext(req.Context())

	// Ensure X-Session-ID is set
	sessionID := req.Header.Get("X-Session-ID")
	if sessionID == "" {
		sessionID = uuid.New().String()
		req.Header.Set("X-Session-ID", sessionID)
	}

	backend, matched := a.selectBackend(req)

	if !matched {
		logger.Debug("No matching rule found, routing to V1 backend")
		backend = a.config.V1Backend
	}

	logger.Debugf("Routing to backend: %s", backend)
	backendURL, err := url.Parse(backend)
	if err != nil {
		logger.Errorf("Failed to parse backend URL: %v", err)
		http.Error(rw, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	director := func(req *http.Request) {
		req.URL.Scheme = backendURL.Scheme
		req.URL.Host = backendURL.Host
		req.Host = backendURL.Host
	}
	proxy := &httputil.ReverseProxy{Director: director}

	// Set session cookie for affinity
	http.SetCookie(rw, &http.Cookie{
		Name:  "session_id",
		Value: sessionID,
		Path:  "/",
	})

	// Create a custom response writer to capture the status code
	crw := &customResponseWriter{ResponseWriter: rw}

	proxy.ServeHTTP(crw, req)

	// Log the response status code
	logger.Debugf("Response status code: %d", crw.statusCode)
}

// customResponseWriter is a wrapper for http.ResponseWriter that captures the status code
type customResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (crw *customResponseWriter) WriteHeader(code int) {
	crw.statusCode = code
	crw.ResponseWriter.WriteHeader(code)
}

func (a *ABTest) selectBackend(req *http.Request) (string, bool) {
	for _, rule := range a.config.Rules {
		if rule.Path != "" && rule.Path != req.URL.Path {
			continue
		}
		if rule.Method != "" && rule.Method != req.Method {
			continue
		}
		if !a.checkConditions(req, rule.Conditions) {
			continue
		}
		if rule.Percentage > 0 {
			if a.shouldRouteToV2(req, rule.Percentage) {
				return a.config.V2Backend, true
			}
			return a.config.V1Backend, true
		}
		if rule.Backend != "" {
			return rule.Backend, true
		}
		// If no specific backend is set, use V2Backend
		return a.config.V2Backend, true
	}
	// If no rule matches, return empty string and false
	return "", false
}

func (a *ABTest) checkConditions(req *http.Request, conditions []RuleCondition) bool {
	for _, condition := range conditions {
		var value string
		switch condition.Type {
		case "query":
			value = req.URL.Query().Get(condition.Parameter)
		case "form":
			if err := req.ParseForm(); err != nil {
				return false
			}
			value = req.Form.Get(condition.Parameter)
		case "header":
			value = req.Header.Get(condition.Parameter)
		default:
			return false
		}

		switch condition.Operator {
		case "eq":
			if value != condition.Value {
				return false
			}
		case "ne":
			if value == condition.Value {
				return false
			}
		case "contains":
			if !strings.Contains(value, condition.Value) {
				return false
			}
		case "gt":
			v1, err1 := strconv.ParseFloat(value, 64)
			v2, err2 := strconv.ParseFloat(condition.Value, 64)
			if err1 != nil || err2 != nil || v1 <= v2 {
				return false
			}
		case "lt":
			v1, err1 := strconv.ParseFloat(value, 64)
			v2, err2 := strconv.ParseFloat(condition.Value, 64)
			if err1 != nil || err2 != nil || v1 >= v2 {
				return false
			}
		case "regex":
			matched, err := regexp.MatchString(condition.Value, value)
			if err != nil || !matched {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func (a *ABTest) shouldRouteToV2(req *http.Request, percentage float64) bool {
	sessionID := req.Header.Get("X-Session-ID")

	cacheKey := "abtest_" + sessionID
	if cachedDecision, found := a.cache.Get(cacheKey); found {
		return cachedDecision.(bool)
	}

	hash := fnv.New32a()
	hash.Write([]byte(sessionID))
	randomFloat := float64(hash.Sum32()) / float64(math.MaxUint32)

	decision := randomFloat < percentage
	a.cache.Set(cacheKey, decision, cache.DefaultExpiration)

	return decision
}
func parseBackendURL(backend string) (string, error) {
	u, err := url.Parse(backend)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" {
		u.Scheme = "http"
	}
	return u.String(), nil
}
