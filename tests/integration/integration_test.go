package integration

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/daemonp/forklift/config"
)

const (
	traefikURL = "http://localhost:80"
)

func closeBody(t *testing.T, body io.Closer) {
	t.Helper()
	if err := body.Close(); err != nil {
		t.Errorf("Error closing response body: %v", err)
	}
}

func TestIntegration(t *testing.T) {
	cfg := &config.Config{
		DefaultBackend: "http://default-backend.example.com",
		Rules: []config.RoutingRule{
			{
				Path:       "/",
				Method:     "GET",
				Backend:    "http://echo1.example.com",
				Percentage: 50,
			},
			{
				Path:       "/",
				Method:     "GET",
				Backend:    "http://echo2.example.com",
				Percentage: 50,
			},
			{
				Path:    "/v3",
				Method:  "GET",
				Backend: "http://echo3.example.com",
			},
			{
				Path:    "/",
				Method:  "POST",
				Backend: "http://echo2.example.com",
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
				Path:    "/query-test",
				Method:  "GET",
				Backend: "http://echo2.example.com",
				Conditions: []config.RuleCondition{
					{
						Type:       "query",
						QueryParam: "mid",
						Operator:   "eq",
						Value:      "two",
					},
				},
			},
		},
		Debug: true,
	}

	tests := []struct {
		name           string
		path           string
		method         string
		body           string
		headers        map[string]string
		expectedBodies []string
	}{
		{
			name:           "GET / should route to echo1 or echo2",
			path:           "/",
			method:         "GET",
			expectedBodies: []string{"Hello from V1", "Hello from V2"},
		},
		{
			name:           "GET /v3 should route to echo3",
			path:           "/v3",
			method:         "GET",
			expectedBodies: []string{"Hello from V3"},
		},
		{
			name:   "POST / with MID=a should route to echo2",
			path:   "/",
			method: "POST",
			body:   "MID=a",
			headers: map[string]string{
				"Content-Type": "application/x-www-form-urlencoded",
			},
			expectedBodies: []string{"Hello from V2"},
		},
		{
			name:           "GET /query-test?mid=two should route to echo2",
			path:           "/query-test?mid=two",
			method:         "GET",
			expectedBodies: []string{"Hello from V2"},
		},
		{
			name:           "GET /unknown should route to default",
			path:           "/unknown",
			method:         "GET",
			expectedBodies: []string{"Default Backend"},
		},
	}

	client := &http.Client{}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runTest(t, client, tt, cfg)
		})
	}

	// Additional test for percentage-based routing
	t.Run("Percentage-based routing for GET /", func(t *testing.T) {
		totalRequests := 1000
		hitsEcho1 := 0
		hitsEcho2 := 0

		for range totalRequests {
			req, err := http.NewRequest(http.MethodGet, traefikURL+"/", nil)
			if err != nil {
				t.Fatalf("Failed to create request: %v", err)
			}

			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("Failed to send request: %v", err)
			}
			bodyBytes, err := io.ReadAll(resp.Body)
			if err != nil {
				closeBody(t, resp.Body)
				t.Fatalf("Failed to read response body: %v", err)
			}
			bodyStr := string(bodyBytes)
			closeBody(t, resp.Body)
			switch {
			case strings.Contains(bodyStr, "Hello from V1"):
				hitsEcho1++
			case strings.Contains(bodyStr, "Hello from V2"):
				hitsEcho2++
			default:
				t.Errorf("Unexpected response body: %v", bodyStr)
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

func runTest(t *testing.T, client *http.Client, tt struct {
	name           string
	path           string
	method         string
	body           string
	headers        map[string]string
	expectedBodies []string
}, cfg *config.Config) {
	t.Helper()
	req, err := createRequest(tt.method, traefikURL+tt.path, tt.body)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	// Add headers if any
	for k, v := range tt.headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Failed to send request: %v", err)
	}
	defer closeBody(t, resp.Body)

	checkResponse(t, resp, tt, cfg)
}

func createRequest(method, urlStr, body string) (*http.Request, error) {
	var req *http.Request
	var err error
	if method == "POST" && body != "" {
		req, err = http.NewRequest(method, urlStr, strings.NewReader(body))
	} else {
		req, err = http.NewRequest(method, urlStr, nil)
	}
	if err != nil {
		return nil, err
	}
	return req, nil
}

func checkResponse(t *testing.T, resp *http.Response, tt struct {
	name           string
	path           string
	method         string
	body           string
	headers        map[string]string
	expectedBodies []string
},
) {
	t.Helper()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status OK, got %v", resp.Status)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Failed to read response body: %v", err)
	}
	bodyStr := string(bodyBytes)

	found := false
	for _, expected := range tt.expectedBodies {
		if strings.Contains(bodyStr, expected) {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("Expected body to contain one of %v, got %q", tt.expectedBodies, bodyStr)
	}
}
