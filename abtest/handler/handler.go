package handler

import (
	"net/http"

	"github.com/daemonp/traefik-forklift-middleware/abtest/config"
	"github.com/daemonp/traefik-forklift-middleware/abtest/engine"
)

type ABTestHandler struct {
	config     *config.Config
	ruleEngine *engine.RuleEngine
}

func New(config *config.Config, ruleEngine *engine.RuleEngine) *ABTestHandler {
	return &ABTestHandler{
		config:     config,
		ruleEngine: ruleEngine,
	}
}

func (h *ABTestHandler) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	backend, _ := h.selectBackend(req)
	// Use the selected backend to route the request
	// Implementation details omitted for brevity
}

func (h *ABTestHandler) selectBackend(req *http.Request) (string, bool) {
	rule, found := h.ruleEngine.MatchRule(req)
	if !found {
		return h.config.V1Backend, true
	}

	if rule.Backend != "" {
		return rule.Backend, true
	}

	if h.ruleEngine.ShouldRouteToV2(req, rule.Percentage) {
		return h.config.V2Backend, true
	}

	return h.config.V1Backend, true
}
