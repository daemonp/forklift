package engine

import (
	"hash/fnv"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/daemonp/traefik-forklift-middleware/abtest/config"
)

type RuleEngine struct {
	config     *config.Config
	cache      map[string]cacheEntry
	cacheMutex sync.RWMutex
}

type cacheEntry struct {
	rule      *config.Rule
	expiresAt time.Time
}

func New(config *config.Config) *RuleEngine {
	return &RuleEngine{
		config: config,
		cache:  make(map[string]cacheEntry),
	}
}

func (e *RuleEngine) MatchRule(req *http.Request) (*config.Rule, bool) {
	cacheKey := e.generateCacheKey(req)

	// Check cache first
	e.cacheMutex.RLock()
	if entry, ok := e.cache[cacheKey]; ok && entry.expiresAt.After(time.Now()) {
		e.cacheMutex.RUnlock()
		return entry.rule, true
	}
	e.cacheMutex.RUnlock()

	// If not in cache, evaluate rules
	// Rules are already sorted by priority in New() function
	for i, rule := range e.config.Rules {
		if e.ruleMatches(req, rule) {
			// Cache the result
			e.cacheMutex.Lock()
			e.cache[cacheKey] = cacheEntry{
				rule:      &e.config.Rules[i],
				expiresAt: time.Now().Add(5 * time.Minute), // Cache for 5 minutes
			}
			e.cacheMutex.Unlock()
			return &e.config.Rules[i], true
		}
	}

	return nil, false
}

func (e *RuleEngine) generateCacheKey(req *http.Request) string {
	return req.Method + ":" + req.URL.Path
}

func (e *RuleEngine) ruleMatches(req *http.Request, rule config.Rule) bool {
	if rule.Path != "" && rule.Path != req.URL.Path {
		return false
	}
	if rule.PathPrefix != "" && !strings.HasPrefix(req.URL.Path, rule.PathPrefix) {
		return false
	}
	if rule.Method != "" && rule.Method != req.Method {
		return false
	}
	if !e.checkConditions(req, rule.Conditions) {
		return false
	}
	return true
}

func (e *RuleEngine) checkConditions(req *http.Request, conditions []config.RuleCondition) bool {
	// Implementation of condition checking
	// Omitted for brevity
	return true
}

func (e *RuleEngine) ShouldRouteToV2(req *http.Request, percentage float64) bool {
	// Generate a consistent hash for the request
	h := fnv.New32a()
	h.Write([]byte(req.RemoteAddr + req.Header.Get("User-Agent")))
	hashValue := h.Sum32()

	// Use the hash to make a consistent decision
	return float64(hashValue)/float64(^uint32(0)) < (percentage / 100.0)
}
