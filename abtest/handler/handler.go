package handler

import (
	"io"
	"net/http"
	"strings"

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
	backend, rule := h.selectBackend(req)
	if rule != nil && rule.PathPrefix != "" {
		req.URL.Path = strings.TrimPrefix(req.URL.Path, rule.PathPrefix)
	}
	// Use the selected backend to route the request
	// Implementation details omitted for brevity
	proxyReq, _ := http.NewRequest(req.Method, backend+req.URL.Path, req.Body)
	proxyReq.Header = req.Header
	resp, _ := http.DefaultClient.Do(proxyReq)
	for k, v := range resp.Header {
		rw.Header()[k] = v
	}
	rw.WriteHeader(resp.StatusCode)
	io.Copy(rw, resp.Body)
}

func (h *ABTestHandler) selectBackend(req *http.Request) (string, *config.Rule) {
	rule, found := h.ruleEngine.MatchRule(req)
	if !found {
		return h.config.V1Backend, nil
	}

	if rule.Backend != "" {
		return rule.Backend, rule
	}

	if h.ruleEngine.ShouldRouteToV2(req, rule.Percentage) {
		return h.config.V2Backend, rule
	}

	return h.config.V1Backend, rule
}
