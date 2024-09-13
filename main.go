// Package main is a plugin for Traefik that implements AB testing functionality.
package main

import (
	"context"
	"net/http"

	"github.com/daemonp/traefik-forklift-middleware/abtest"
)

// CreateConfig creates a new Config
func CreateConfig() *abtest.Config {
	return abtest.CreateConfig()
}

// New creates a new AB testing middleware
func New(ctx context.Context, next http.Handler, config *abtest.Config, name string) (http.Handler, error) {
	return abtest.New(ctx, next, config, name)
}
