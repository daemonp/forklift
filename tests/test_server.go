package tests

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/daemonp/forklift"
)

const defaultBufferSize = 1024

// mockServer is a struct that holds a mock server and its associated response.
type mockServer struct {
	server   *httptest.Server
	response string
}

// newMockServer creates a new mock server with the given response.
func newMockServer(response string) *mockServer {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(response))
	}))
	return &mockServer{
		server:   server,
		response: response,
	}
}

// close closes the mock server.
func (ms *mockServer) close() {
	ms.server.Close()
}

// URL returns the URL of the mock server.
func (ms *mockServer) URL() string {
	return ms.server.URL
}

// TestPathPrefixRewrite runs test cases for path prefix rewrites.
func TestPathPrefixRewrite(t *testing.T) {
	server := newMockServer("Received path: /")
	defer server.close()

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

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			url := server.URL() + tc.requestPath
			client := &http.Client{}
			req, err := http.NewRequest(http.MethodGet, url, nil)
			if err != nil {
				t.Fatalf("Failed to create request: %v", err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("Failed to send request: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != http.StatusOK {
				t.Errorf("Expected status OK, got %v", resp.Status)
			}

			body := make([]byte, defaultBufferSize)
			n, _ := resp.Body.Read(body)
			receivedPath := string(bytes.TrimRight(body[:n], "\x00"))

			// Simulate path prefix rewrite
			rewrittenPath := strings.TrimPrefix(tc.requestPath, tc.pathPrefix)
			if rewrittenPath == "" {
				rewrittenPath = "/"
			}

			expectedResponse := "Received path: " + rewrittenPath
			if receivedPath != expectedResponse {
				t.Errorf("Expected path %s, got %s", expectedResponse, receivedPath)
			}
		})
	}
}

// TestForkliftPathPrefixRewrite tests the path prefix rewrite functionality in the Forklift middleware.
func TestForkliftPathPrefixRewrite(t *testing.T) {
	// Create test servers for V1 and V2 backends
	v1Server := newMockServer("V1 Backend: /")
	defer v1Server.close()

	v2Server := newMockServer("V2 Backend: /")
	defer v2Server.close()

	// Create Forklift configuration
	config := &forklift.Config{
		DefaultBackend: v1Server.URL(),
		Rules: []forklift.RoutingRule{
			{
				PathPrefix: "/api",
				Backend:    v2Server.URL(),
			},
		},
	}

	// Create Forklift middleware
	forkliftHandler, err := forklift.NewForklift(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}), config, "test")
	if err != nil {
		t.Fatalf("Failed to create Forklift middleware: %v", err)
	}

	// Create test server with Forklift middleware
	testServer := httptest.NewServer(forkliftHandler)
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
			defer func() {
				if closeErr := resp.Body.Close(); closeErr != nil {
					t.Errorf("Failed to close response body: %v", closeErr)
				}
			}()

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
