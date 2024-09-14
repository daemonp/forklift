package integration

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

const (
	traefikURL = "http://localhost:80"
)

func TestIntegration(t *testing.T) {
	tests := []struct {
		name         string
		path         string
		method       string
		body         string
		expectedBody string
	}{
		{"Route to V1 or V2", "/", "GET", "", "Hello from V"},
		{"Route to V1 or V2 (second request)", "/", "GET", "", "Hello from V"},
		{"Route to V2 (POST with MID=a)", "/", "POST", "MID=a", "Hello from V"},
		{"Route to V1 (POST without MID)", "/", "POST", "", "Hello from V1"},
	}

	client := &http.Client{}
	var sessionID string

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req *http.Request
			var err error
			if tt.method == "POST" {
				req, err = http.NewRequest(tt.method, traefikURL+tt.path, strings.NewReader(tt.body))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			} else {
				req, err = http.NewRequest(tt.method, traefikURL+tt.path, nil)
			}
			if err != nil {
				t.Fatalf("Failed to create request: %v", err)
			}

			if sessionID != "" {
				req.AddCookie(&http.Cookie{Name: "abtest_session_id", Value: sessionID})
			}

			resp, err := client.Do(req)
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

			if !strings.Contains(string(body), tt.expectedBody) {
				t.Errorf("Expected body to contain %q, got %q", tt.expectedBody, string(body))
			}

			t.Logf("Test: %s", tt.name)
			t.Logf("Request method: %s", tt.method)
			t.Logf("Request body: %s", tt.body)
			t.Logf("Response body: %s", string(body))
			t.Logf("Selected backend: %s", resp.Header.Get("X-Selected-Backend"))

			if tt.method == "POST" && tt.body == "MID=a" {
				if !strings.Contains(string(body), "Hello from V") {
					t.Errorf("Expected response from a backend for POST with MID=a, got: %s", string(body))
				}
			}

			if sessionID == "" {
				for _, cookie := range resp.Cookies() {
					if cookie.Name == "abtest_session_id" {
						sessionID = cookie.Value
						t.Logf("Session ID: %s", sessionID)
						break
					}
				}
			}
		})
	}
}

func TestGradualRolloutIntegration(t *testing.T) {
	v1Count := 0
	v2Count := 0
	totalRequests := 1000

	for i := 0; i < totalRequests; i++ {
		resp, err := http.Get(traefikURL + "/")
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

		if strings.Contains(string(body), "Hello from V2") {
			v2Count++
		} else if strings.Contains(string(body), "Hello from V1") {
			v1Count++
		} else {
			t.Errorf("Unexpected response body: %s", string(body))
		}

		// Add a small delay to avoid overwhelming the server
		time.Sleep(10 * time.Millisecond)
	}

	v2Percentage := float64(v2Count) / float64(totalRequests) * 100
	fmt.Printf("V2 percentage: %.2f%%\n", v2Percentage)
	if v2Percentage < 45 || v2Percentage > 55 {
		t.Errorf("Gradual rollout distribution outside expected range: V2 percentage = %.2f%%", v2Percentage)
	}
}
