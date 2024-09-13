package testing

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/daemonp/traefik-forklift-middleware/abtest"
)

// NewTestServer creates a new test server with the given handler
func NewTestServer(handler http.HandlerFunc) *httptest.Server {
	return httptest.NewServer(handler)
}

// NewV1TestServer creates a new test server simulating a V1 backend
func NewV1TestServer() *httptest.Server {
	return NewTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("V1 Backend"))
	})
}

// NewV2TestServer creates a new test server simulating a V2 backend
func NewV2TestServer() *httptest.Server {
	return NewTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("V2 Backend"))
	})
}

// NewTestServerWithPathRewrite creates a new test server that handles path prefix rewrites
func NewTestServerWithPathRewrite() *httptest.Server {
	return NewTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fmt.Sprintf("Received path: %s", r.URL.Path)))
	})
}

// TestPathPrefixRewrite runs test cases for path prefix rewrites
func TestPathPrefixRewrite(t *testing.T) {
	testCases := []struct {
		name         string
		pathPrefix   string
		requestPath  string
		expectedPath string
	}{
		{
			name:         "No prefix",
			pathPrefix:   "",
			requestPath:  "/api/v1/users",
			expectedPath: "/api/v1/users",
		},
		{
			name:         "Simple prefix",
			pathPrefix:   "/api",
			requestPath:  "/api/v1/users",
			expectedPath: "/v1/users",
		},
		{
			name:         "Nested prefix",
			pathPrefix:   "/api/v1",
			requestPath:  "/api/v1/users/123",
			expectedPath: "/users/123",
		},
		{
			name:         "Exact match prefix",
			pathPrefix:   "/api/v1/users",
			requestPath:  "/api/v1/users",
			expectedPath: "/",
		},
	}

	server := NewTestServerWithPathRewrite()
	defer server.Close()

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			url := server.URL + tc.requestPath
			resp, err := http.Get(url)
			if err != nil {
				t.Fatalf("Failed to send request: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				t.Errorf("Expected status OK, got %v", resp.Status)
			}

			body := make([]byte, 1024)
			n, _ := resp.Body.Read(body)
			receivedPath := string(body[:n])

			// Simulate path prefix rewrite
			rewrittenPath := strings.TrimPrefix(tc.requestPath, tc.pathPrefix)
			if rewrittenPath == "" {
				rewrittenPath = "/"
			}

			expectedResponse := fmt.Sprintf("Received path: %s", rewrittenPath)
			if receivedPath != expectedResponse {
				t.Errorf("Expected path %s, got %s", expectedResponse, receivedPath)
			}
		})
	}
}

// TestABTestPathPrefixRewrite tests the path prefix rewrite functionality in the ABTest middleware
func TestABTestPathPrefixRewrite(t *testing.T) {
	// Create test servers for V1 and V2 backends
	v1Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fmt.Sprintf("V1 Backend: %s", r.URL.Path)))
	}))
	defer v1Server.Close()

	v2Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fmt.Sprintf("V2 Backend: %s", r.URL.Path)))
	}))
	defer v2Server.Close()

	// Create ABTest configuration
	config := &abtest.Config{
		V1Backend: v1Server.URL,
		V2Backend: v2Server.URL,
		Rules: []abtest.RoutingRule{
			{
				PathPrefix: "/api",
				Backend:    v2Server.URL,
				Percentage: 100,
			},
		},
	}

	// Create ABTest middleware
	abTestHandler := abtest.NewABTest(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), config, "test")

	// Create test server with ABTest middleware
	testServer := httptest.NewServer(abTestHandler)
	defer testServer.Close()

	// Test cases
	testCases := []struct {
		name           string
		path           string
		expectedPrefix string
		expectedPath   string
	}{
		{
			name:           "No prefix rewrite",
			path:           "/users",
			expectedPrefix: "V1 Backend:",
			expectedPath:   "/users",
		},
		{
			name:           "Prefix rewrite",
			path:           "/api/users",
			expectedPrefix: "V2 Backend:",
			expectedPath:   "/users",
		},
		{
			name:           "Nested prefix rewrite",
			path:           "/api/v1/users",
			expectedPrefix: "V2 Backend:",
			expectedPath:   "/v1/users",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Get(testServer.URL + tc.path)
			if err != nil {
				t.Fatalf("Failed to send request: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				t.Errorf("Expected status OK, got %v", resp.Status)
			}

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("Failed to read response body: %v", err)
			}

			expectedResponse := fmt.Sprintf("%s %s", tc.expectedPrefix, tc.expectedPath)
			if string(body) != expectedResponse {
				t.Errorf("Expected response %q, got %q", expectedResponse, string(body))
			}
		})
	}
}
