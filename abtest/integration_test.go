package abtest_test

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestIntegration(t *testing.T) {
	// Assuming Traefik ingress is accessible at the following URL
	ingressURL := "https://test.home.petta.io"

	tests := []struct {
		name         string
		path         string
		expectedBody string
	}{
		{"Route to V1", "/api/v1", "V1 Backend"},
		{"Route to V2", "/api/v2", "V2 Backend"},
		// Add more test cases for different rules and scenarios
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Construct the full URL based on the test case
			fullURL := fmt.Sprintf("%s%s", ingressURL, tt.path)
			resp, err := http.Get(fullURL)
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

			if strings.TrimSpace(string(body)) != tt.expectedBody {
				t.Errorf("Expected body %q, got %q", tt.expectedBody, string(body))
			}
		})
	}
}

func TestGradualRolloutIntegration(t *testing.T) {
	// Assuming Traefik ingress is accessible at the following URL
	ingressURL := "https://test.home.petta.io"

	v1Count := 0
	v2Count := 0
	totalRequests := 100

	for i := 0; i < totalRequests; i++ {
		resp, err := http.Get(ingressURL + "/api/gradual")
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

		if strings.TrimSpace(string(body)) == "V2 Backend" {
			v2Count++
		} else {
			v1Count++
		}
	}

	// Check if the distribution is roughly within the expected range
	v2Percentage := float64(v2Count) / float64(totalRequests) * 100
	if v2Percentage < 40 || v2Percentage > 60 {
		t.Errorf("Gradual rollout distribution outside expected range: V2 percentage = %.2f%%", v2Percentage)
	}
}
