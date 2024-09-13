package abtest

import (
	"your-project/abtest/config"
	"your-project/abtest/engine"
	"your-project/abtest/handler"
)

func New(config *config.Config) *handler.ABTestHandler {
	ruleEngine := engine.New(config)
	return handler.New(config, ruleEngine)
}
