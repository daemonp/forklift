// Package logger provides logging functionality for the Forklift middleware.
package logger

import (
	"github.com/traefik/traefik/v3/pkg/log"
)

// NewLogger initializes and returns a Traefik v3 compatible logger.
func NewLogger(pluginName string) log.Logger {
	return log.With(log.Default(), "plugin", pluginName)
}
