// Package logger provides logging functionality for the Forklift middleware.
package logger

// Logger is an interface that represents the logging capabilities required by the Forklift middleware.
type Logger interface {
	Debugf(format string, args ...interface{})
	Infof(format string, args ...interface{})
	Errorf(format string, args ...interface{})
}

// NewLogger initializes and returns a Traefik v3 compatible logger.
func NewLogger(pluginName string) Logger {
	return &traefikLogger{
		logger: log.WithoutContext().With().Str("plugin", pluginName).Logger(),
	}
}

type traefikLogger struct {
	logger log.Logger
}

func (t *traefikLogger) Debugf(format string, args ...interface{}) {
	t.logger.Debug().Msgf(format, args...)
}

func (t *traefikLogger) Infof(format string, args ...interface{}) {
	t.logger.Info().Msgf(format, args...)
}

func (t *traefikLogger) Errorf(format string, args ...interface{}) {
	t.logger.Error().Msgf(format, args...)
}
