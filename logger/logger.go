// Package logger provides logging functionality for the Forklift middleware.
package logger

import (
	"log"
	"os"
)

// Logger is an interface that represents the logging capabilities required by the Forklift middleware.
type Logger interface {
	Debugf(format string, args ...interface{})
	Infof(format string, args ...interface{})
	Warnf(format string, args ...interface{})
	Errorf(format string, args ...interface{})
}

// NewLogger initializes and returns a simple logger.
func NewLogger(pluginName string) Logger {
	return &simpleLogger{
		logger: log.New(os.Stdout, "plugin:"+pluginName+" ", log.LstdFlags),
	}
}

type simpleLogger struct {
	logger *log.Logger
}

func (s *simpleLogger) Debugf(format string, args ...interface{}) {
	s.logger.Printf("DEBUG: "+format, args...)
}

func (s *simpleLogger) Infof(format string, args ...interface{}) {
	s.logger.Printf("INFO: "+format, args...)
}

func (s *simpleLogger) Warnf(format string, args ...interface{}) {
	s.logger.Printf("WARN: "+format, args...)
}

func (s *simpleLogger) Errorf(format string, args ...interface{}) {
	s.logger.Printf("ERROR: "+format, args...)
}
