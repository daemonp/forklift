package main

import (
	"context"
	"math/rand"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

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

func (a *ABTest) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	logger := log.FromContext(ctx)

	backend, err := a.selectBackend(req)
	if err != nil {
		logger.Errorf("Error selecting backend: %v", err)
		http.Error(rw, "Internal server error", http.StatusInternalServerError)
		return
	}

	if backend == "" {
		a.next.ServeHTTP(rw, req)
		return
	}

	a.forwardRequest(backend, rw, req)
}

func (a *ABTest) selectBackend(req *http.Request) (string, error) {
	for _, rule := range a.config.Rules {
		if a.matchesRule(req, rule) {
			if rule.Percentage > 0 && rand.Float64() > rule.Percentage {
				continue
			}
			return rule.Backend, nil
		}
	}
	return "", nil
}

func (a *ABTest) matchesRule(req *http.Request, rule RoutingRule) bool {
	if rule.Path != "" {
		pathRegex, err := regexp.Compile("^" + rule.Path + "$")
		if err != nil || !pathRegex.MatchString(req.URL.Path) {
			return false
		}
	}

	if rule.Method != "" && req.Method != rule.Method {
		return false
	}

	for _, condition := range rule.Conditions {
		if !a.matchesCondition(req, condition) {
			return false
		}
	}

	return true
}

func (a *ABTest) matchesCondition(req *http.Request, condition RuleCondition) bool {
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
		return value == condition.Value
	case "ne":
		return value != condition.Value
	case "gt":
		v1, err1 := strconv.ParseFloat(value, 64)
		v2, err2 := strconv.ParseFloat(condition.Value, 64)
		if err1 != nil || err2 != nil {
			return false
		}
		return v1 > v2
	case "lt":
		v1, err1 := strconv.ParseFloat(value, 64)
		v2, err2 := strconv.ParseFloat(condition.Value, 64)
		if err1 != nil || err2 != nil {
			return false
		}
		return v1 < v2
	case "contains":
		return strings.Contains(value, condition.Value)
	case "regex":
		matched, err := regexp.MatchString(condition.Value, value)
		return err == nil && matched
	default:
		return false
	}
}

func (a *ABTest) forwardRequest(backend string, rw http.ResponseWriter, req *http.Request) {
	// Create a new request to the backend
	backendReq, err := http.NewRequestWithContext(req.Context(), req.Method, backend+req.URL.Path, req.Body)
	if err != nil {
		http.Error(rw, "Error creating backend request", http.StatusInternalServerError)
		return
	}

	// Copy headers
	for name, values := range req.Header {
		for _, value := range values {
			backendReq.Header.Add(name, value)
		}
	}

	// Perform the request
	client := &http.Client{}
	resp, err := client.Do(backendReq)
	if err != nil {
		http.Error(rw, "Error forwarding request to backend", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	// Copy the response headers
	for name, values := range resp.Header {
		for _, value := range values {
			rw.Header().Add(name, value)
		}
	}

	// Set the status code
	rw.WriteHeader(resp.StatusCode)

	// Copy the response body
	_, err = rw.Write(resp.Body)
	if err != nil {
		log.FromContext(req.Context()).Errorf("Error writing response: %v", err)
	}
}
