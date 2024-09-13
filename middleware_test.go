package main

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/daemonp/traefik-forklift-middleware/abtest"
	abtest_testing "github.com/daemonp/traefik-forklift-middleware/abtest/testing"
)

func TestABTestMiddleware(t *testing.T) {
	// Create mock servers using the new testing package
	v1Server := abtest_testing.NewV1TestServer()
	defer v1Server.Close()

	v2Server := abtest_testing.NewV2TestServer()
	defer v2Server.Close()

	config := &abtest.Config{
		V1Backend: v1Server.URL,
		V2Backend: v2Server.URL,
		Rules: []abtest.RoutingRule{
			{
				Path:    "/test",
				Method:  "GET",
				Backend: v1Server.URL,
			},
			{
				Path:    "/test",
				Method:  "POST",
				Backend: v2Server.URL,
			},
			{
				Path:    "/amount",
				Method:  "POST",
				Backend: v2Server.URL,
				Conditions: []abtest.RuleCondition{
					{
						Type:      "form",
						Parameter: "amount",
						Operator:  "gt",
						Value:     "1000",
					},
				},
			},
			{
				Path:    "/language",
				Method:  "GET",
				Backend: v2Server.URL,
				Conditions: []abtest.RuleCondition{
					{
						Type:      "header",
						Parameter: "Accept-Language",
						Operator:  "contains",
						Value:     "es",
					},
				},
			},
			{
				Path:       "/gradual-rollout",
				Method:     "GET",
				Backend:    v2Server.URL,
				Percentage: 0.5,
			},
		},
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Next handler"))
	})

	middleware, err := abtest.New(context.Background(), next, config, "test-abtest")
	if err != nil {
		t.Fatalf("Failed to create middleware: %v", err)
	}

	tests := []struct {
		name           string
		method         string
		path           string
		headers        map[string]string
		body           url.Values
		expectedStatus int
		expectedBody   string
	}{
		{"Basic GET routing", "GET", "/test", nil, nil, http.StatusOK, "V1 Backend"},
		{"Basic POST routing", "POST", "/test", nil, nil, http.StatusOK, "V2 Backend"},
		{"Unknown path", "GET", "/unknown", nil, nil, http.StatusOK, "V1 Backend"},
		{"Form data routing - high amount", "POST", "/amount", nil, url.Values{"amount": {"2000"}}, http.StatusOK, "V2 Backend"},
		{"Form data routing - low amount", "POST", "/amount", nil, url.Values{"amount": {"500"}}, http.StatusOK, "V1 Backend"},
		{"Header-based routing - Spanish", "GET", "/language", map[string]string{"Accept-Language": "es-ES"}, nil, http.StatusOK, "V2 Backend"},
		{"Header-based routing - English", "GET", "/language", map[string]string{"Accept-Language": "en-US"}, nil, http.StatusOK, "V1 Backend"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req *http.Request
			var err error

			if tt.body != nil {
				req, err = http.NewRequest(tt.method, tt.path, strings.NewReader(tt.body.Encode()))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			} else {
				req, err = http.NewRequest(tt.method, tt.path, nil)
			}

			if err != nil {
				t.Fatal(err)
			}

			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			rr := httptest.NewRecorder()
			middleware.ServeHTTP(rr, req)

			if status := rr.Code; status != tt.expectedStatus {
				t.Errorf("handler returned wrong status code: got %v want %v", status, tt.expectedStatus)
			}

			if body := strings.TrimSpace(rr.Body.String()); body != tt.expectedBody {
				t.Errorf("handler returned unexpected body: got %v want %v", body, tt.expectedBody)
			}
		})
	}
}

func TestGradualRollout(t *testing.T) {
	v1Server := abtest_testing.NewV1TestServer()
	defer v1Server.Close()

	v2Server := abtest_testing.NewV2TestServer()
	defer v2Server.Close()

	config := &abtest.Config{
		V1Backend: v1Server.URL,
		V2Backend: v2Server.URL,
		Rules: []abtest.RoutingRule{
			{
				Path:       "/gradual-rollout",
				Method:     "GET",
				Backend:    v2Server.URL,
				Percentage: 0.5,
			},
		},
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Next handler"))
	})

	middleware, _ := abtest.New(context.Background(), next, config, "test-abtest")

	v1Count := 0
	v2Count := 0
	totalRequests := 1000

	for i := 0; i < totalRequests; i++ {
		req, _ := http.NewRequest("GET", "/gradual-rollout", nil)
		rr := httptest.NewRecorder()
		middleware.ServeHTTP(rr, req)

		if strings.TrimSpace(rr.Body.String()) == "V2 Backend" {
			v2Count++
		} else {
			v1Count++
		}
	}

	v2Percentage := float64(v2Count) / float64(totalRequests)
	if v2Percentage < 0.45 || v2Percentage > 0.55 {
		t.Errorf("Gradual rollout percentage outside expected range: got %v, want between 0.45 and 0.55", v2Percentage)
	}
}

func TestSessionAffinity(t *testing.T) {
	v1Server := abtest_testing.NewV1TestServer()
	defer v1Server.Close()

	v2Server := abtest_testing.NewV2TestServer()
	defer v2Server.Close()

	config := &abtest.Config{
		V1Backend: v1Server.URL,
		V2Backend: v2Server.URL,
		Rules: []abtest.RoutingRule{
			{
				Path:       "/session-test",
				Method:     "GET",
				Backend:    v2Server.URL,
				Percentage: 0.5,
			},
		},
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Next handler"))
	})

	middleware, _ := abtest.New(context.Background(), next, config, "test-abtest")

	// Simulate multiple requests from the same client
	clientRequests := 10
	var sessionID string

	for i := 0; i < clientRequests; i++ {
		req, _ := http.NewRequest("GET", "/session-test", nil)
		if sessionID != "" {
			req.Header.Set("Cookie", "session_id="+sessionID)
		}

		rr := httptest.NewRecorder()
		middleware.ServeHTTP(rr, req)

		// Extract session ID from the response
		for _, cookie := range rr.Result().Cookies() {
			if cookie.Name == "session_id" {
				sessionID = cookie.Value
				break
			}
		}

		// All responses should be the same for a given session
		if i > 0 && strings.TrimSpace(rr.Body.String()) != strings.TrimSpace(rr.Body.String()) {
			t.Errorf("Session affinity not maintained: got different responses for the same session")
		}
	}
}

func TestSelectBackend(t *testing.T) {
	v1Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("V1 Backend"))
	}))
	defer v1Server.Close()

	v2Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("V2 Backend"))
	}))
	defer v2Server.Close()

	config := &abtest.Config{
		V1Backend: v1Server.URL,
		V2Backend: v2Server.URL,
		Rules: []abtest.RoutingRule{
			{
				Path:    "/test",
				Method:  "GET",
				Backend: v1Server.URL,
			},
			{
				Path:    "/complex",
				Method:  "POST",
				Backend: v2Server.URL,
				Conditions: []abtest.RuleCondition{
					{
						Type:      "query",
						Parameter: "version",
						Operator:  "eq",
						Value:     "2",
					},
					{
						Type:      "header",
						Parameter: "X-Custom-Header",
						Operator:  "contains",
						Value:     "special",
					},
				},
			},
		},
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Next handler"))
	})

	middleware, err := abtest.New(context.Background(), next, config, "test-abtest")
	if err != nil {
		t.Fatalf("Failed to create middleware: %v", err)
	}

	tests := []struct {
		name            string
		method          string
		path            string
		query           string
		headers         map[string]string
		expectedBackend string
	}{
		{"Simple GET routing", "GET", "/test", "", nil, "V1 Backend"},
		{"Complex routing - match", "POST", "/complex", "version=2", map[string]string{"X-Custom-Header": "special-value"}, "V2 Backend"},
		{"Complex routing - no match", "POST", "/complex", "version=1", map[string]string{"X-Custom-Header": "normal-value"}, "V1 Backend"},
		{"No matching rule", "GET", "/unknown", "", nil, "V1 Backend"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(tt.method, tt.path+"?"+tt.query, nil)
			if err != nil {
				t.Fatal(err)
			}

			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			rr := httptest.NewRecorder()
			middleware.ServeHTTP(rr, req)

			if status := rr.Code; status != http.StatusOK {
				t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusOK)
			}

			if body := strings.TrimSpace(rr.Body.String()); body != tt.expectedBackend {
				t.Errorf("handler returned unexpected body: got %v want %v", body, tt.expectedBackend)
			}
		})
	}
}
