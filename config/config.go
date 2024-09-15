// Package config provides configuration structures for the Forklift middleware.
package config

import (
	"fmt"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

// Config holds the configuration for the Forklift middleware.
type Config struct {
	DefaultBackend     string        `yaml:"defaultBackend,omitempty"`
	Rules              []RoutingRule `yaml:"rules,omitempty"`
	Debug              bool          `yaml:"debug,omitempty"`
	ConfigFile         string        `yaml:"configFile,omitempty"`
	DefaultBackendEnv  string        `yaml:"defaultBackendEnv,omitempty"`
	DebugEnv           string        `yaml:"debugEnv,omitempty"`
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
		DefaultBackend: "",
		Rules:          []RoutingRule{},
		Debug:          false,
	}
}

// LoadConfig loads the configuration from a YAML string and applies environment variables.
func LoadConfig(yamlConfig string) (*Config, error) {
	config := &Config{}
	err := yaml.Unmarshal([]byte(yamlConfig), config)
	if err != nil {
		return nil, err
	}

	// Load configuration from file if specified
	if config.ConfigFile != "" {
		err = config.loadFromFile()
		if err != nil {
			return nil, fmt.Errorf("error loading config from file: %w", err)
		}
	}

	// Apply environment variables
	config.applyEnvironmentVariables()

	return config, nil
}

// loadFromFile loads configuration from the specified file
func (c *Config) loadFromFile() error {
	data, err := os.ReadFile(c.ConfigFile)
	if err != nil {
		return err
	}

	return yaml.Unmarshal(data, c)
}

// applyEnvironmentVariables overrides configuration with environment variables
func (c *Config) applyEnvironmentVariables() {
	if c.DefaultBackendEnv != "" {
		if value, exists := os.LookupEnv(c.DefaultBackendEnv); exists {
			c.DefaultBackend = value
		}
	}

	if c.DebugEnv != "" {
		if value, exists := os.LookupEnv(c.DebugEnv); exists {
			c.Debug, _ = strconv.ParseBool(value)
		}
	}
}
