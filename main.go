// Package traefik_forklift_middleware is a plugin for Traefik that implements AB testing functionality.
package traefik_forklift_middleware

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/daemonp/traefik-forklift-middleware/abtest"
)

var debug bool

// Config holds the configuration for the AB testing middleware
type Config struct {
	V1Backend string                `json:"v1Backend,omitempty"`
	V2Backend string                `json:"v2Backend,omitempty"`
	Rules     []abtest.RoutingRule  `json:"rules,omitempty"`
}

// CreateConfig creates a new Config
func CreateConfig() *Config {
	return &Config{}
}

// New creates a new AB testing middleware
func New(ctx context.Context, next http.Handler, config *Config, name string) (http.Handler, error) {
	debug = os.Getenv("DEBUG") == "true"
	if debug {
		log.Printf("Debug: Creating new AB testing middleware with config: %+v", config)
	}
	return abtest.NewABTest(next, &abtest.Config{
		V1Backend: config.V1Backend,
		V2Backend: config.V2Backend,
		Rules:     config.Rules,
	}, name), nil
}
