// File: tests/middleware_test.go
package tests

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/daemonp/forklift"
	"github.com/daemonp/forklift/config"
)

const sessionCookieName = "forklift_id"

func TestForkliftMiddleware(t *testing.T) {
	servers := setupMockServers(t)
	defer closeMockServers(servers)

	config := createTestConfig(servers)
	middleware := createMiddleware(t, config)

	runBasicTests(t, middleware)
	runPercentageBasedRoutingTest(t, middleware)
	runDefaultBackendTest(t, middleware)
	runSessionAffinityTest(t, middleware)
}

func setupMockServers(t *testing.T) map[string]*httptest.Server {
	t.Helper()
	servers := make(map[string]*httptest.Server)
	servers["default"] = createMockServer("Default Backend")
	servers["echo1"] = createMockServer("Hello from V1")
	servers["echo2"] = createMockServer("Hello from V2")
	servers["echo3"] = createMockServer("Hello from V3")
	return servers
}

func createMockServer(response string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(response))
	}))
}

func closeMockServers(servers map[string]*httptest.Server) {
	for _, server := range servers {
		server.Close()
	}
}

func createTestConfig(servers map[string]*httptest.Server) *config.Config {
	return &config.Config{
		DefaultBackend: servers["default"].URL,
		Rules: []config.RoutingRule{
			{
				Path:          "/",
				Method:        "GET",
				Backend:       servers["echo1"].URL,
				Percentage:    50,
				Priority:      1,
				AffinityToken: "group1",
			},
			{
				Path:          "/",
				Method:        "GET",
				Backend:       servers["echo2"].URL,
				Percentage:    50,
				Priority:      1,
				AffinityToken: "group2",
			},
			{
				Path:     "/v3",
				Method:   "GET",
				Backend:  servers["echo3"].URL,
				Priority: 1,
			},
			{
				Path:     "/",
				Method:   "POST",
				Backend:  servers["echo2"].URL,
				Priority: 1,
				Conditions: []config.RuleCondition{
					{
						Type:      "form",
						Parameter: "MID",
						Operator:  "eq",
						Value:     "a",
					},
				},
			},
			{
				Path:     "/query-test",
				Method:   "GET",
				Backend:  servers["echo2"].URL,
				Priority: 1,
				Conditions: []config.RuleCondition{
					{
						Type:       "query",
						QueryParam: "mid",
						Operator:   "eq",
						Value:      "two",
					},
				},
			},
			{
				PathPrefix: "/api",
				Method:     "GET",
				Backend:    servers["echo2"].URL,
				Priority:   1,
			},
			{
				Path:       "/",
				Method:     "POST",
				Backend:    servers["echo3"].URL,
				Percentage: 10,
				Priority:   1,
				Conditions: []config.RuleCondition{
					{
						Type:      "form",
						Parameter: "MID",
						Operator:  "eq",
						Value:     "d",
					},
				},
			},
		},
		Debug: false,
	}
}

func createMiddleware(t *testing.T, cfg *config.Config) http.Handler {
	t.Helper()
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Next handler"))
	})

	middleware, err := forklift.New(context.Background(), next, cfg, "test-forklift")
	if err != nil {
		t.Fatalf("Failed to create Forklift middleware: %v", err)
	}
	return middleware
}

func runBasicTests(t *testing.T, middleware http.Handler) {
	t.Helper()
	tests := []struct {
		name             string
		method           string
		path             string
		headers          map[string]string
		body             url.Values
		cookies          []*http.Cookie
		expectedStatuses []int
		expectedBodies   []string
	}{
		{
			name:             "GET / should route to echo1 or echo2",
			method:           "GET",
			path:             "/",
			expectedStatuses: []int{http.StatusOK},
			expectedBodies:   []string{"Hello from V1", "Hello from V2"},
		},
		{
			name:             "GET /v3 should route to echo3",
			method:           "GET",
			path:             "/v3",
			expectedStatuses: []int{http.StatusOK},
			expectedBodies:   []string{"Hello from V3"},
		},
		{
			name:   "POST / with MID=a should route to echo2",
			method: "POST",
			path:   "/",
			body:   url.Values{"MID": {"a"}},
			headers: map[string]string{
				"Content-Type": "application/x-www-form-urlencoded",
			},
			expectedStatuses: []int{http.StatusOK},
			expectedBodies:   []string{"Hello from V2"},
		},
		{
			name:             "GET /query-test?mid=two should route to echo2",
			method:           "GET",
			path:             "/query-test?mid=two",
			expectedStatuses: []int{http.StatusOK},
			expectedBodies:   []string{"Hello from V2"},
		},
		{
			name:             "GET /unknown should route to default",
			method:           "GET",
			path:             "/unknown",
			expectedStatuses: []int{http.StatusOK},
			expectedBodies:   []string{"Default Backend"},
		},
		{
			name:   "Cookie condition matching",
			method: "GET",
			path:   "/",
			cookies: []*http.Cookie{
				{Name: "user_segment", Value: "premium"},
			},
			expectedStatuses: []int{http.StatusOK},
			expectedBodies:   []string{"Hello from V1", "Hello from V2"}, // Default behavior, as no specific rule for cookies
		},
		{
			name:   "Header condition matching",
			method: "GET",
			path:   "/",
			headers: map[string]string{
				"X-User-Type": "beta",
			},
			expectedStatuses: []int{http.StatusOK},
			expectedBodies:   []string{"Hello from V1", "Hello from V2"}, // Default behavior, as no specific rule for headers
		},
		{
			name:             "Path prefix condition matching",
			method:           "GET",
			path:             "/api/users",
			expectedStatuses: []int{http.StatusOK},
			expectedBodies:   []string{"Hello from V2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := createTestRequest(t, tt.method, tt.path, tt.headers, tt.body)

			// Add Cookies to the request
			for _, cookie := range tt.cookies {
				req.AddCookie(cookie)
			}
			rr := httptest.NewRecorder()
			middleware.ServeHTTP(rr, req)

			if !containsInt(tt.expectedStatuses, rr.Code) {
				t.Errorf("Expected status in %v, got %v", tt.expectedStatuses, rr.Code)
			}

			body := strings.TrimSpace(rr.Body.String())
			if !containsString(tt.expectedBodies, body) {
				t.Errorf("Expected body to be one of %v, got %v", tt.expectedBodies, body)
			}
		})
	}
}

func runPercentageBasedRoutingTest(t *testing.T, middleware http.Handler) {
	t.Helper()
	t.Run("Percentage-based routing for GET /", func(t *testing.T) {
		const totalRequests = 10000
		const warmupRequests = 1000
		const testRuns = 2
		const epsilon = 0.5 // Allow 0.5% deviation

		runTest := func() (float64, float64) {
			hitsEcho1, hitsEcho2 := 0, 0

			// Warm-up phase
			for range warmupRequests {
				req := createTestRequest(t, "GET", "/", nil, nil)
				rr := httptest.NewRecorder()
				middleware.ServeHTTP(rr, req)
			}

			// Actual test
			for range totalRequests {
				req := createTestRequest(t, "GET", "/", nil, nil)
				rr := httptest.NewRecorder()
				middleware.ServeHTTP(rr, req)

				body := strings.TrimSpace(rr.Body.String())
				switch body {
				case "Hello from V1":
					hitsEcho1++
				case "Hello from V2":
					hitsEcho2++
				default:
					t.Errorf("Unexpected response body: %v", body)
				}
			}

			percentageEcho1 := float64(hitsEcho1) / float64(totalRequests) * 100
			percentageEcho2 := float64(hitsEcho2) / float64(totalRequests) * 100

			return percentageEcho1, percentageEcho2
		}

		var totalPercentageEcho1, totalPercentageEcho2 float64
		for i := range testRuns {
			percentageEcho1, percentageEcho2 := runTest()
			totalPercentageEcho1 += percentageEcho1
			totalPercentageEcho2 += percentageEcho2
			t.Logf("Run %d - Echo1: %.2f%%, Echo2: %.2f%%", i+1, percentageEcho1, percentageEcho2)
		}

		avgPercentageEcho1 := totalPercentageEcho1 / float64(testRuns)
		avgPercentageEcho2 := totalPercentageEcho2 / float64(testRuns)

		t.Logf("Average over %d runs - Echo1: %.2f%%, Echo2: %.2f%%", testRuns, avgPercentageEcho1, avgPercentageEcho2)

		if math.Abs(avgPercentageEcho1-50) > epsilon || math.Abs(avgPercentageEcho2-50) > epsilon {
			t.Errorf("Expected both backends to receive 50%% ± %.2f%% of traffic, got Echo1: %.2f%%, Echo2: %.2f%%", epsilon, avgPercentageEcho1, avgPercentageEcho2)
		}
	})
}

func runDefaultBackendTest(t *testing.T, middleware http.Handler) {
	t.Helper()
	t.Run("Routing to default backend when no rules match", func(t *testing.T) {
		req := createTestRequest(t, "GET", "/non-existent", nil, nil)
		rr := httptest.NewRecorder()
		middleware.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %v", rr.Code)
		}

		body := strings.TrimSpace(rr.Body.String())
		if body != "Default Backend" {
			t.Errorf("Expected body 'Default Backend', got '%v'", body)
		}
	})
}

func runSessionAffinityTest(t *testing.T, middleware http.Handler) {
	t.Helper()
	t.Run("Session affinity test", func(t *testing.T) {
		const totalSessions = 10000
		const warmupSessions = 100
		const requestsPerSession = 10
		const epsilon = 0.90 // Allow 0.90% deviation (90 basis points)

		sessionBackends := make(map[string]string)
		backendCounts := make(map[string]int)

		// Warm-up phase
		for range warmupSessions {
			req := createTestRequest(t, "GET", "/", nil, nil)
			rr := httptest.NewRecorder()
			middleware.ServeHTTP(rr, req)
		}

		for i := range totalSessions {
			req := createTestRequest(t, "GET", "/", nil, nil)
			rr := httptest.NewRecorder()
			middleware.ServeHTTP(rr, req)

			cookies := rr.Result().Cookies()
			sessionID := ""
			for _, cookie := range cookies {
				if cookie.Name == sessionCookieName {
					sessionID = cookie.Value
					break
				}
			}

			if sessionID == "" {
				t.Fatal("Session ID not found in cookies")
			}

			body := strings.TrimSpace(rr.Body.String())
			sessionBackends[sessionID] = body
			backendCounts[body]++

			// Make additional requests with the same session ID
			for range requestsPerSession {
				req := createTestRequest(t, "GET", "/", nil, nil)
				req.AddCookie(&http.Cookie{
					Name:  sessionCookieName,
					Value: sessionID,
				})
				rr := httptest.NewRecorder()
				middleware.ServeHTTP(rr, req)

				newBody := strings.TrimSpace(rr.Body.String())
				if newBody != body {
					t.Errorf("Session affinity broken. Session ID %s: Expected backend '%v', got '%v'", sessionID, body, newBody)
				}
			}

			if (i+1)%2000 == 0 {
				t.Logf("Processed %d sessions", i+1)
				logBackendDistribution(t, backendCounts, i+1, epsilon)
			}
		}

		logBackendDistribution(t, backendCounts, totalSessions, epsilon)
	})
}

func logBackendDistribution(t *testing.T, backendCounts map[string]int, totalSessions int, epsilon float64) {
	t.Helper()
	for backend, count := range backendCounts {
		percentage := float64(count) / float64(totalSessions) * 100
		t.Logf("Backend %s: %.2f%% (%d/%d)", backend, percentage, count, totalSessions)
		if math.Abs(percentage-50) > epsilon {
			t.Errorf("Backend distribution for %s is outside the expected range: %.2f%%", backend, percentage)
		}
	}
}

func createTestRequest(t *testing.T, method, path string, headers map[string]string, body url.Values) *http.Request {
	t.Helper()
	var req *http.Request
	var err error

	if body != nil {
		req, err = http.NewRequest(method, path, strings.NewReader(body.Encode()))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req, err = http.NewRequest(method, path, nil)
		if err != nil {
			t.Fatal(err)
		}
	}

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	return req
}

func containsString(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func containsInt(slice []int, item int) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
