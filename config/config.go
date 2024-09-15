// Package config provides configuration structures for the Forklift middleware.
package config

import (
	"gopkg.in/yaml.v3"
)

// Config holds the configuration for the Forklift middleware.
type Config struct {
	DefaultBackend string        `yaml:"defaultBackend,omitempty"`
	Rules          []RoutingRule `yaml:"rules,omitempty"`
	Debug          bool          `yaml:"debug,omitempty"`
}

// RoutingRule defines the structure for routing rules in the middleware.
type RoutingRule struct {
	Path              string          `yaml:"path,omitempty"`
	PathPrefix        string          `yaml:"pathPrefix,omitempty"`
	Method            string          `yaml:"method,omitempty"`
	Conditions        []RuleCondition `yaml:"conditions,omitempty"`
	Backend           string          `yaml:"backend,omitempty"`
	Percentage        float64         `yaml:"percentage,omitempty"`
	Priority          int             `yaml:"priority,omitempty"`
	PathPrefixRewrite string          `yaml:"pathPrefixRewrite,omitempty"`
}

// RuleCondition defines the structure for conditions in routing rules.
type RuleCondition struct {
	Type       string `yaml:"type,omitempty"`
	Parameter  string `yaml:"parameter,omitempty"`
	QueryParam string `yaml:"queryParam,omitempty"`
	Operator   string `yaml:"operator,omitempty"`
	Value      string `yaml:"value,omitempty"`
}

// CreateConfig creates and initializes the plugin configuration.
func CreateConfig() *Config {
	return &Config{
		DefaultBackend: "http://localhost:8080",
		Rules:          []RoutingRule{},
		Debug:          false,
	}
}

// LoadConfig loads the configuration from a YAML string.
func LoadConfig(yamlConfig string) (*Config, error) {
	config := &Config{}
	err := yaml.Unmarshal([]byte(yamlConfig), config)
	if err != nil {
		return nil, err
	}
	return config, nil
}
