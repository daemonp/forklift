// Package config provides configuration structures for the Forklift middleware.
package config

// Config holds the configuration for the Forklift middleware.
type Config struct {
	DefaultBackend string        `json:"defaultBackend,omitempty"`
	Rules          []RoutingRule `json:"rules,omitempty"`
	Debug          bool          `json:"debug,omitempty"`
}

// RoutingRule defines the structure for routing rules in the middleware.
type RoutingRule struct {
	Path              string          `json:"path,omitempty"`
	PathPrefix        string          `json:"pathPrefix,omitempty"`
	Method            string          `json:"method,omitempty"`
	Conditions        []RuleCondition `json:"conditions,omitempty"`
	Backend           string          `json:"backend,omitempty"`
	Percentage        float64         `json:"percentage,omitempty"`
	Priority          int             `json:"priority,omitempty"`
	PathPrefixRewrite string          `json:"pathPrefixRewrite,omitempty"`
}

// RuleCondition defines the structure for conditions in routing rules.
type RuleCondition struct {
	Type       string `json:"type,omitempty"`
	Parameter  string `json:"parameter,omitempty"`
	QueryParam string `json:"queryParam,omitempty"`
	Operator   string `json:"operator,omitempty"`
	Value      string `json:"value,omitempty"`
}

// CreateConfig creates and initializes the plugin configuration.
func CreateConfig() *Config {
	return &Config{
		DefaultBackend: "http://localhost:8080",
		Rules:          []RoutingRule{},
		Debug:          false,
	}
}
