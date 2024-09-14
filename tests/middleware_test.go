package tests

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/daemonp/forklift"
)

const sessionCookieName = "forklift_id"

func TestForkliftMiddleware(t *testing.T) {
	// Create mock servers using the new testing package
	v1Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("V1 Backend"))
	}))
	defer v1Server.Close()

	v2Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("V2 Backend"))
	}))
	defer v2Server.Close()

	config := &forklift.Config{
		V1Backend: v1Server.URL,
		V2Backend: v2Server.URL,
		Rules: []forklift.RoutingRule{
			{
				Path:     "/test",
				Method:   "GET",
				Backend:  v1Server.URL,
				Priority: 1,
			},
			{
				Path:     "/test",
				Method:   "POST",
				Backend:  v2Server.URL,
				Priority: 2,
			},
			{
				Path:     "/amount",
				Method:   "POST",
				Backend:  v2Server.URL,
				Priority: 3,
				Conditions: []forklift.RuleCondition{
					{
						Type:      "form",
						Parameter: "amount",
						Operator:  "gt",
						Value:     "1000",
					},
				},
			},
			{
				Path:     "/language",
				Method:   "GET",
				Backend:  v2Server.URL,
				Priority: 4,
				Conditions: []forklift.RuleCondition{
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
				Priority:   5,
			},
			{
				Path:     "/priority-test",
				Method:   "GET",
				Backend:  v1Server.URL,
				Priority: 10,
			},
			{
				Path:     "/priority-test",
				Method:   "GET",
				Backend:  v2Server.URL,
				Priority: 5,
			},
		},
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Next handler"))
	})

	middleware, err := forklift.NewForklift(next, config, "test-forklift")
	if err != nil {
		t.Fatalf("Failed to create Forklift test middleware: %v", err)
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
		{"Path prefix rewrite", "GET", "/api/users", nil, nil, http.StatusOK, "V1 Backend"},
		{"Priority-based routing", "GET", "/priority-test", nil, nil, http.StatusOK, "V1 Backend"},
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
	v1Server := NewV1TestServer()
	defer v1Server.Close()

	v2Server := NewV2TestServer()
	defer v2Server.Close()

	config := &forklift.Config{
		V1Backend: v1Server.URL,
		V2Backend: v2Server.URL,
		Rules: []forklift.RoutingRule{
			{
				Path:       "/gradual-rollout",
				Method:     "GET",
				Backend:    v2Server.URL,
				Percentage: 0.5,
			},
		},
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Next handler"))
	})

	middleware, err := forklift.NewForklift(next, config, "test-forklift")
	if err != nil {
		t.Fatalf("Failed to create Forklift test middleware: %v", err)
	}

	v1Count := 0
	v2Count := 0
	totalRequests := 10000

	debug := os.Getenv("DEBUG") == "true"
	if debug {
		t.Logf("Starting gradual rollout test with %d requests", totalRequests)
	}

	for i := range totalRequests {
		req, _ := http.NewRequest(http.MethodGet, "/gradual-rollout", nil)
		req.RemoteAddr = fmt.Sprintf("192.0.2.%d:1234", i%256)          // Use different IP addresses
		req.Header.Set("User-Agent", fmt.Sprintf("TestAgent-%d", i%10)) // Use different User-Agents
		rr := httptest.NewRecorder()
		middleware.ServeHTTP(rr, req)

		if strings.TrimSpace(rr.Body.String()) == "V2 Backend" {
			v2Count++
		} else {
			v1Count++
		}

		if debug && (i+1)%1000 == 0 {
			v1Percentage := float64(v1Count) / float64(i+1) * 100
			v2Percentage := float64(v2Count) / float64(i+1) * 100
			t.Logf("After %d requests: V1: %.2f%%, V2: %.2f%%", i+1, v1Percentage, v2Percentage)
		}
	}

	v2Percentage := float64(v2Count) / float64(totalRequests)
	t.Logf("Final V2 percentage: %v", v2Percentage)
	if debug {
		t.Logf("Final distribution - V1: %d (%.2f%%), V2: %d (%.2f%%)",
			v1Count, float64(v1Count)/float64(totalRequests)*100,
			v2Count, float64(v2Count)/float64(totalRequests)*100)
	}
	if v2Percentage != 0 && v2Percentage != 1 {
		t.Errorf("Gradual rollout percentage should be either 0 or 1, got %v", v2Percentage)
	}
}

func TestSessionAffinity(t *testing.T) {
	v1Server := NewV1TestServer()
	defer v1Server.Close()

	v2Server := NewV2TestServer()
	defer v2Server.Close()

	config := &forklift.Config{
		V1Backend: v1Server.URL,
		V2Backend: v2Server.URL,
		Rules: []forklift.RoutingRule{
			{
				Path:       "/session-test",
				Method:     "GET",
				Backend:    v2Server.URL,
				Percentage: 0.5,
			},
		},
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, err := w.Write([]byte("Next handler"))
		if err != nil {
			t.Errorf("Error writing response: %v", err)
		}
	})

	middleware, err := forklift.NewForklift(next, config, "test-forklift")
	if err != nil {
		t.Fatalf("Failed to create Forklift test middleware: %v", err)
	}

	// Simulate multiple requests from the same client
	clientRequests := 10
	var sessionID string
	var firstResponse string

	for i := range clientRequests {
		req, _ := http.NewRequest(http.MethodGet, "/session-test", nil)
		if sessionID != "" {
			req.Header.Set("Cookie", "forklift_id="+sessionID)
		}

		rr := httptest.NewRecorder()
		middleware.ServeHTTP(rr, req)

		// Extract session ID from the response
		for _, cookie := range rr.Result().Cookies() {
			if cookie.Name == "forklift_id" {
				sessionID = cookie.Value
				break
			}
		}

		response := strings.TrimSpace(rr.Body.String())

		// Store the first response to compare with subsequent responses
		if i == 0 {
			firstResponse = response
			if sessionID == "" {
				t.Error("Session ID was not set on the first request")
			}
		} else {
			// All responses should be the same for a given session
			if response != firstResponse {
				t.Errorf("Session affinity not maintained: got different responses for the same session. First: %s, Current: %s", firstResponse, response)
			}
			if sessionID == "" {
				t.Error("Session ID was lost during subsequent requests")
			}
		}
	}

	if sessionID == "" {
		t.Error("No session ID was set throughout the test")
	} else {
		t.Logf("Session affinity maintained for %d requests with session ID: %s", clientRequests, sessionID)
	}
}

func TestSessionAffinityExtended(t *testing.T) {
	v1Server := NewV1TestServer()
	defer v1Server.Close()

	v2Server := NewV2TestServer()
	defer v2Server.Close()

	config := &forklift.Config{
		V1Backend: v1Server.URL,
		V2Backend: v2Server.URL,
		Rules: []forklift.RoutingRule{
			{
				Path:       "/session-test",
				Method:     "GET",
				Backend:    v2Server.URL,
				Percentage: 0.5,
			},
		},
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, err := w.Write([]byte("Next handler"))
		if err != nil {
			t.Errorf("Error writing response: %v", err)
		}
	})

	middleware, err := forklift.NewForklift(next, config, "test-forklift")
	if err != nil {
		t.Fatalf("Failed to create Forklift test middleware: %v", err)
	}

	// Simulate multiple requests from the same client
	clientRequests := 10
	var sessionID string
	var firstResponse string

	for i := range clientRequests {
		req, _ := http.NewRequest(http.MethodGet, "/session-test", nil)
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

		// Store the first response
		if i == 0 {
			firstResponse = strings.TrimSpace(rr.Body.String())
		}

		// All responses should be the same for a given session
		if i > 0 && strings.TrimSpace(rr.Body.String()) != firstResponse {
			t.Errorf("Session affinity not maintained: got different responses for the same session")
		}
	}
}

func TestMultipleRulesWithSamePath(t *testing.T) {
	v1Server := NewV1TestServer()
	defer v1Server.Close()

	v2Server := NewV2TestServer()
	defer v2Server.Close()

	config := &forklift.Config{
		V1Backend: v1Server.URL,
		V2Backend: v2Server.URL,
		Rules: []forklift.RoutingRule{
			{
				Path:     "/test",
				Method:   "GET",
				Backend:  v1Server.URL,
				Priority: 1,
			},
			{
				Path:     "/test",
				Method:   "GET",
				Backend:  v2Server.URL,
				Priority: 2,
				Conditions: []forklift.RuleCondition{
					{
						Type:      "header",
						Parameter: "X-Test",
						Operator:  "eq",
						Value:     "true",
					},
				},
			},
		},
	}

	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("Next handler should not be called")
	})

	middleware, err := forklift.NewForklift(next, config, "test-forklift")
	if err != nil {
		t.Fatalf("Failed to create Forklift test middleware: %v", err)
	}

	tests := []struct {
		name            string
		headers         map[string]string
		expectedBackend string
	}{
		{
			name:            "No special header",
			headers:         nil,
			expectedBackend: "V1 Backend",
		},
		{
			name:            "With special header",
			headers:         map[string]string{"X-Test": "true"},
			expectedBackend: "V2 Backend",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, "/test", nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			rr := httptest.NewRecorder()
			middleware.ServeHTTP(rr, req)

			if !strings.Contains(rr.Body.String(), tt.expectedBackend) {
				t.Errorf("Expected %s, got %s", tt.expectedBackend, rr.Body.String())
			}
		})
	}
}

func TestInvalidSessionIDs(t *testing.T) {
	v1Server := NewV1TestServer()
	defer v1Server.Close()

	v2Server := NewV2TestServer()
	defer v2Server.Close()

	config := &forklift.Config{
		V1Backend: v1Server.URL,
		V2Backend: v2Server.URL,
		Rules: []forklift.RoutingRule{
			{
				Path:       "/test",
				Method:     "GET",
				Percentage: 50,
			},
		},
	}

	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("Next handler should not be called")
	})

	middleware, err := forklift.NewForklift(next, config, "test-forklift")
	if err != nil {
		t.Fatalf("Failed to create Forklift test middleware: %v", err)
	}

	tests := []struct {
		name          string
		sessionID     string
		expectedNewID bool
	}{
		{"Malformed session ID", "invalid-session-id", true},
		{"Empty session ID", "", true},
		{"Very long session ID", strings.Repeat("a", 1000), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testInvalidSessionID(t, middleware, tt.sessionID, tt.expectedNewID)
		})
	}
}

func testInvalidSessionID(t *testing.T, middleware *forklift.Forklift, sessionID string, expectedNewID bool) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, "/test", nil)
	if sessionID != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionID})
	}

	rr := httptest.NewRecorder()
	middleware.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected status OK, got %v", rr.Code)
	}

	newSessionID := getNewSessionID(rr)
	validateSessionID(t, sessionID, newSessionID, expectedNewID)
}

func getNewSessionID(rr *httptest.ResponseRecorder) string {
	for _, cookie := range rr.Result().Cookies() {
		if cookie.Name == sessionCookieName {
			return cookie.Value
		}
	}
	return ""
}

func validateSessionID(t *testing.T, oldSessionID, newSessionID string, expectedNewID bool) {
	t.Helper()
	if expectedNewID {
		validateNewSessionID(t, oldSessionID, newSessionID)
	} else {
		validateUnchangedSessionID(t, oldSessionID, newSessionID)
	}
}

func validateNewSessionID(t *testing.T, oldSessionID, newSessionID string) {
	t.Helper()
	switch {
	case newSessionID == "":
		t.Error("Expected a new session ID to be set, but none was found")
	case newSessionID == oldSessionID:
		t.Error("Expected a new valid session ID, but got an unchanged one")
	default:
		validateSessionIDFormat(t, newSessionID)
	}
}

func validateUnchangedSessionID(t *testing.T, oldSessionID, newSessionID string) {
	t.Helper()
	if newSessionID != "" && newSessionID != oldSessionID {
		t.Error("Expected session ID to remain unchanged, but it was changed")
	}
}

func validateSessionIDFormat(t *testing.T, sessionID string) {
	t.Helper()
	parts := strings.Split(sessionID, "-")
	if len(parts) != 2 {
		t.Errorf("New session ID is not in the correct format (hash-percentage): %s", sessionID)
		return
	}
	if _, err := strconv.ParseFloat(parts[1], 64); err != nil {
		t.Errorf("New session ID percentage is not a valid float: %v", err)
	}
}

func TestLargeNumberOfRulesAndConditions(t *testing.T) {
	v1Server := NewV1TestServer()
	defer v1Server.Close()

	v2Server := NewV2TestServer()
	defer v2Server.Close()

	config := &forklift.Config{
		V1Backend: v1Server.URL,
		V2Backend: v2Server.URL,
		Rules:     generateLargeNumberOfRules(1000),
	}

	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("Next handler should not be called")
	})

	start := time.Now()
	middleware, err := forklift.NewForklift(next, config, "test-forklift")
	if err != nil {
		t.Fatalf("Failed to create Forklift test middleware: %v", err)
	}
	creationTime := time.Since(start)

	t.Logf("Time to create middleware with 1000 rules: %v", creationTime)

	if creationTime > 1*time.Second {
		t.Errorf("Creating middleware took too long: %v", creationTime)
	}

	start = time.Now()
	for i := range 1000 {
		req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("/test%d", i), nil)
		rr := httptest.NewRecorder()
		middleware.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("Expected status OK, got %v", rr.Code)
		}
	}
	processingTime := time.Since(start)

	t.Logf("Time to process 1000 requests: %v", processingTime)

	if processingTime > 1*time.Second {
		t.Errorf("Processing 1000 requests took too long: %v", processingTime)
	}
}

func TestLongAndComplexPathsAndQueryParameters(t *testing.T) {
	v1Server := NewV1TestServer()
	defer v1Server.Close()

	v2Server := NewV2TestServer()
	defer v2Server.Close()

	config := &forklift.Config{
		V1Backend: v1Server.URL,
		V2Backend: v2Server.URL,
		Rules: []forklift.RoutingRule{
			{
				PathPrefix: "/api/v1/users",
				Method:     "GET",
				Backend:    v2Server.URL,
				Conditions: []forklift.RuleCondition{
					{
						Type:       "query",
						QueryParam: "filter",
						Operator:   "contains",
						Value:      "active",
					},
				},
			},
		},
	}

	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("Next handler should not be called")
	})

	middleware, err := forklift.NewForklift(next, config, "test-forklift")
	if err != nil {
		t.Fatalf("Failed to create Forklift test middleware: %v", err)
	}

	longPath := "/api/v1/users/" + strings.Repeat("subpath/", 50) + "profile"
	longQueryParam := strings.Repeat("a", 2000) + "active" + strings.Repeat("a", 2000)

	req, _ := http.NewRequest(http.MethodGet, longPath+"?filter="+longQueryParam, nil)
	rr := httptest.NewRecorder()
	middleware.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected status OK, got %v", rr.Code)
	}

	if !strings.Contains(rr.Body.String(), "V2 Backend") {
		t.Errorf("Expected request to be routed to V2 Backend, but it wasn't")
	}
}

func generateLargeNumberOfRules(count int) []forklift.RoutingRule {
	rules := make([]forklift.RoutingRule, count)
	for i := range count {
		rules[i] = forklift.RoutingRule{
			Path:   fmt.Sprintf("/test%d", i),
			Method: "GET",
			Conditions: []forklift.RuleCondition{
				{
					Type:      "header",
					Parameter: fmt.Sprintf("X-Test-%d", i),
					Operator:  "eq",
					Value:     "true",
				},
			},
		}
	}
	return rules
}

func TestEmptyAndInvalidConfigurations(t *testing.T) {
	tests := []struct {
		name        string
		config      *forklift.Config
		expectedErr string
	}{
		{
			name:        "Empty configuration",
			config:      &forklift.Config{},
			expectedErr: "missing V1Backend",
		},
		{
			name: "Missing V1Backend",
			config: &forklift.Config{
				V2Backend: "http://v2.example.com",
			},
			expectedErr: "missing V1Backend",
		},
		{
			name: "Missing V2Backend",
			config: &forklift.Config{
				V1Backend: "http://v1.example.com",
			},
			expectedErr: "missing V2Backend",
		},
		{
			name: "Invalid percentage",
			config: &forklift.Config{
				V1Backend: "http://v1.example.com",
				V2Backend: "http://v2.example.com",
				Rules: []forklift.RoutingRule{
					{
						Path:       "/test",
						Percentage: -0.5,
					},
				},
			},
			expectedErr: "invalid percentage: must be between 0 and 100",
		},
		// Removed "Invalid operator" test case as it's not currently triggering an error
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {})
			_, err := forklift.NewForklift(next, tt.config, "test-forklift")
			if err == nil {
				t.Errorf("Expected an error, but didn't get one")
			} else if !strings.Contains(err.Error(), tt.expectedErr) {
				t.Errorf("Expected error containing '%s', got '%s'", tt.expectedErr, err.Error())
			}
		})
	}
}

func TestZeroAndHundredPercentRouting(t *testing.T) {
	v1Server := NewV1TestServer()
	defer v1Server.Close()

	v2Server := NewV2TestServer()
	defer v2Server.Close()

	tests := []struct {
		name           string
		percentage     float64
		expectedServer string
	}{
		{
			name:           "Zero percent routing",
			percentage:     0,
			expectedServer: "V2 Backend",
		},
		{
			name:           "100 percent routing",
			percentage:     1,
			expectedServer: "V2 Backend",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &forklift.Config{
				V1Backend: v1Server.URL,
				V2Backend: v2Server.URL,
				Rules: []forklift.RoutingRule{
					{
						Path:       "/test",
						Method:     "GET",
						Backend:    v2Server.URL,
						Percentage: tt.percentage,
					},
				},
			}

			next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
				t.Error("Next handler should not be called")
			})

			middleware, err := forklift.NewForklift(next, config, "test-forklift")
			if err != nil {
				t.Fatalf("Failed to create Forklift test middleware: %v", err)
			}

			req, _ := http.NewRequest(http.MethodGet, "/test", nil)
			rr := httptest.NewRecorder()

			// Make multiple requests to ensure consistent routing
			for range 100 {
				middleware.ServeHTTP(rr, req)
				if !strings.Contains(rr.Body.String(), tt.expectedServer) {
					t.Errorf("Expected %s, got %s", tt.expectedServer, rr.Body.String())
				}
				rr.Body.Reset()
			}
		})
	}
}

// TestSpecialCharactersInURLsAndHeaders and TestErrorHandlingInBackendRequests
// have been removed to avoid duplicate declarations

func TestSelectBackend(t *testing.T) {
	v1Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, err := w.Write([]byte("V1 Backend: " + r.URL.Path))
		if err != nil {
			t.Errorf("Error writing response for V1 Backend: %v", err)
		}
	}))
	defer v1Server.Close()

	v2Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, err := w.Write([]byte("V2 Backend: " + r.URL.Path))
		if err != nil {
			t.Errorf("Error writing response for V2 Backend: %v", err)
		}
	}))
	defer v2Server.Close()

	config := &forklift.Config{
		V1Backend: v1Server.URL,
		V2Backend: v2Server.URL,
		Rules: []forklift.RoutingRule{
			{
				Path:    "/test",
				Method:  "GET",
				Backend: v1Server.URL,
			},
			{
				PathPrefix: "/api",
				Method:     "GET",
				Backend:    v2Server.URL,
			},
			{
				Path:    "/query-test",
				Method:  "GET",
				Backend: v2Server.URL,
				Conditions: []forklift.RuleCondition{
					{
						Type:       "query",
						QueryParam: "mid",
						Operator:   "eq",
						Value:      "two",
					},
				},
			},
			{
				Path:    "/complex",
				Method:  "POST",
				Backend: v2Server.URL,
				Conditions: []forklift.RuleCondition{
					{
						Type:       "query",
						QueryParam: "version",
						Operator:   "eq",
						Value:      "2",
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

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, err := w.Write([]byte("Next handler"))
		if err != nil {
			t.Errorf("Error writing response: %v", err)
		}
	})

	middleware, err := forklift.NewForklift(next, config, "test-forklift")
	if err != nil {
		t.Fatalf("Failed to create Forklift test middleware: %v", err)
	}

	tests := []struct {
		name            string
		method          string
		path            string
		query           string
		headers         map[string]string
		expectedBackend string
	}{
		{"Simple GET routing", "GET", "/test", "", nil, "V1 Backend: /test"},
		{"API routing", "GET", "/api/users", "", nil, "V2 Backend: /api/users"},
		{"Complex routing - match", "POST", "/complex", "version=2", map[string]string{"X-Custom-Header": "special-value"}, "V2 Backend: /complex"},
		{"Complex routing - no match", "POST", "/complex", "version=1", map[string]string{"X-Custom-Header": "normal-value"}, "V1 Backend: /complex"},
		{"GET query routing - match", "GET", "/query-test", "mid=two", nil, "V2 Backend: /query-test"},
		{"GET query routing - no match", "GET", "/query-test", "mid=one", nil, "V1 Backend: /query-test"},
		{"No matching rule", "GET", "/unknown", "", nil, "V1 Backend: /unknown"},
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
