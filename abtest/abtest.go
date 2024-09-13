package abtest

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/patrickmn/go-cache"
	"github.com/sirupsen/logrus"
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
	Method     string          `json:"method,omitempty"`
	Conditions []RuleCondition `json:"conditions,omitempty"`
	Backend    string          `json:"backend,omitempty"`
	Percentage float64         `json:"percentage,omitempty"`
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

// RuleEngine handles rule matching and caching
type RuleEngine struct {
	config *Config
	cache  *cache.Cache
}

// New creates a new AB testing middleware
func New(ctx context.Context, next http.Handler, config *Config, name string) (http.Handler, error) {
	logger := logrus.New()
	logger.SetLevel(logrus.InfoLevel)

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
	backend, err := a.ruleEngine.SelectBackend(req)
	if err != nil {
		a.logger.Errorf("Error selecting backend: %v", err)
		http.Error(rw, "Error selecting backend", http.StatusInternalServerError)
		return
	}

	// Add a header to indicate the selected backend
	rw.Header().Set("X-Selected-Backend", backend)

	// Modify the request to route to the selected backend
	req.URL.Host = backend
	req.URL.Scheme = "http" // or "https" depending on your setup

	a.logger.Infof("Routing request to backend: %s", backend)

	// Call the next handler with the modified request
	a.next.ServeHTTP(rw, req)
}

// SelectBackend chooses the appropriate backend based on the request and rules
func (re *RuleEngine) SelectBackend(req *http.Request) (string, error) {
	for _, rule := range re.config.Rules {
		if re.ruleMatches(req, rule) {
			if rule.Backend != "" {
				return rule.Backend, nil
			}
			if re.shouldRouteToV2(req, rule.Percentage) {
				return re.config.V2Backend, nil
			}
			return re.config.V1Backend, nil
		}
	}
	return re.config.V1Backend, nil
}

// ruleMatches checks if a request matches a given rule
func (re *RuleEngine) ruleMatches(req *http.Request, rule RoutingRule) bool {
	if rule.Path != "" && rule.Path != req.URL.Path {
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
	switch condition.Type {
	case "Header":
		return checkHeader(req, condition)
	case "Query":
		return checkQuery(req, condition)
	case "Cookie":
		return checkCookie(req, condition)
	default:
		return false
	}
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
	switch operator {
	case "Equals":
		return actual == expected
	case "Contains":
		return strings.Contains(actual, expected)
	case "Prefix":
		return strings.HasPrefix(actual, expected)
	case "Suffix":
		return strings.HasSuffix(actual, expected)
	default:
		return false
	}
}

// shouldRouteToV2 determines if the request should be routed to V2 based on the percentage
func (re *RuleEngine) shouldRouteToV2(req *http.Request, percentage float64) bool {
	// Use the request's IP address as a key for consistency
	key := req.RemoteAddr

	// Check if we have a cached decision for this IP
	if cachedDecision, found := re.cache.Get(key); found {
		return cachedDecision.(bool)
	}

	// Make a new decision based on the percentage
	decision := (float64(time.Now().UnixNano()%100) / 100.0) < percentage

	// Cache the decision for future requests from the same IP
	re.cache.Set(key, decision, cache.DefaultExpiration)

	return decision
}

// CreateConfig creates a new Config
func CreateConfig() *Config {
	return &Config{}
}
