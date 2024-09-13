package traefik_forklift_middleware

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/daemonp/traefik-forklift-middleware/abtest"
)

var debug bool

// Config holds the configuration for the AB testing middleware
type Config struct {
	V1Backend string               `json:"v1Backend,omitempty"`
	V2Backend string               `json:"v2Backend,omitempty"`
	Rules     []abtest.RoutingRule `json:"rules,omitempty"`
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
	abTest, err := abtest.NewABTest(next, &abtest.Config{
		V1Backend: config.V1Backend,
		V2Backend: config.V2Backend,
		Rules:     config.Rules,
	}, name)
	if err != nil {
		return nil, fmt.Errorf("failed to create AB test middleware: %w", err)
	}
	return abTest, nil
}
