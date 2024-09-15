// File: tests/middleware_test.go
package tests

import (
	"context"
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
				Path:       "/",
				Method:     "GET",
				Backend:    servers["echo1"].URL,
				Percentage: 50,
				Priority:   1,
			},
			{
				Path:       "/",
				Method:     "GET",
				Backend:    servers["echo2"].URL,
				Percentage: 50,
				Priority:   1,
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
		Debug: true,
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

func runBasicTests(t *testing.T, middleware *forklift.Forklift) {
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

func runPercentageBasedRoutingTest(t *testing.T, middleware *forklift.Forklift) {
	t.Helper()
	t.Run("Percentage-based routing for GET /", func(t *testing.T) {
		totalRequests := 1000
		hitsEcho1 := 0
		hitsEcho2 := 0

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

		t.Logf("Echo1: %.2f%%, Echo2: %.2f%%", percentageEcho1, percentageEcho2)

		if percentageEcho1 < 45 || percentageEcho1 > 55 {
			t.Errorf("Expected Echo1 to receive approximately 50%% of traffic, got %.2f%%", percentageEcho1)
		}
		if percentageEcho2 < 45 || percentageEcho2 > 55 {
			t.Errorf("Expected Echo2 to receive approximately 50%% of traffic, got %.2f%%", percentageEcho2)
		}
	})
}

func runDefaultBackendTest(t *testing.T, middleware *forklift.Forklift) {
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

func runSessionAffinityTest(t *testing.T, middleware *forklift.Forklift) {
	t.Helper()
	t.Run("Session affinity test", func(t *testing.T) {
		req := createTestRequest(t, "GET", "/", nil, nil)
		rr := httptest.NewRecorder()
		middleware.ServeHTTP(rr, req)

		cookies := rr.Result().Cookies()
		if len(cookies) == 0 {
			t.Fatal("Expected a session cookie to be set")
		}

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

		// Make multiple requests with the same session ID
		expectedBody := strings.TrimSpace(rr.Body.String())
		for range 10 {
			req := createTestRequest(t, "GET", "/", nil, nil)
			req.AddCookie(&http.Cookie{
				Name:  sessionCookieName,
				Value: sessionID,
			})
			rr := httptest.NewRecorder()
			middleware.ServeHTTP(rr, req)

			body := strings.TrimSpace(rr.Body.String())
			if body != expectedBody {
				t.Errorf("Expected consistent backend '%v', got '%v'", expectedBody, body)
			}
		}
	})
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
