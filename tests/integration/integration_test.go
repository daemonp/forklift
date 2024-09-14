package integration

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"gonum.org/v1/gonum/stat/distuv"
)

var chi2 = distuv.ChiSquared{K: 1}

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
		{"Route to Default Backend", "/", "GET", "", "Hello from Default"},
		{"Route to V2 Backend", "/", "POST", "MID=a", "Hello from V2"},
		{"Route to V3 Backend", "/v3", "GET", "", "Hello from V3"},
	}

	client := &http.Client{}
	var sessionID string

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runTest(t, client, &sessionID, tt)
		})
	}
}

func runTest(t *testing.T, client *http.Client, sessionID *string, tt struct {
	name         string
	path         string
	method       string
	body         string
	expectedBody string
},
) {
	t.Helper()
	req, err := createRequest(tt.method, traefikURL+tt.path, tt.body, *sessionID)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Failed to send request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	checkResponse(t, resp, tt)
	logTestDetails(t, tt, resp)
	updateSessionID(t, resp, sessionID)
}

func createRequest(method, url, body, sessionID string) (*http.Request, error) {
	var req *http.Request
	var err error
	if method == "POST" {
		req, err = http.NewRequest(method, url, strings.NewReader(body))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	} else {
		req, err = http.NewRequest(method, url, nil)
	}
	if err != nil {
		return nil, err
	}
	if sessionID != "" {
		req.AddCookie(&http.Cookie{Name: "forklift_id", Value: sessionID})
	}
	return req, nil
}

func checkResponse(t *testing.T, resp *http.Response, tt struct {
	name         string
	path         string
	method       string
	body         string
	expectedBody string
},
) {
	t.Helper()
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

	if tt.method == "POST" && tt.body == "MID=a" {
		if !strings.Contains(string(body), "Hello from V") {
			t.Errorf("Expected response from a backend for POST with MID=a, got: %s", string(body))
		}
	}
}

func logTestDetails(t *testing.T, tt struct {
	name         string
	path         string
	method       string
	body         string
	expectedBody string
}, resp *http.Response,
) {
	t.Helper()
	t.Logf("Test: %s", tt.name)
	t.Logf("Request method: %s", tt.method)
	t.Logf("Request body: %s", tt.body)
	body, _ := io.ReadAll(resp.Body)
	t.Logf("Response body: %s", string(body))
	t.Logf("Selected backend: %s", resp.Header.Get("X-Selected-Backend"))
}

func updateSessionID(t *testing.T, resp *http.Response, sessionID *string) {
	t.Helper()
	if *sessionID == "" {
		for _, cookie := range resp.Cookies() {
			if cookie.Name == "forklift_id" {
				*sessionID = cookie.Value
				t.Logf("Session ID: %s", *sessionID)
				break
			}
		}
	}
}

func TestGradualRolloutIntegration(t *testing.T) {
	backendCounts := make(map[string]int)
	totalRequests := 10000

	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	for i := 0; i < totalRequests; i++ {
		req, err := http.NewRequest("GET", traefikURL+"/gradual", nil)
		if err != nil {
			t.Fatalf("Failed to create request: %v", err)
		}

		// Set a unique session ID for each request
		sessionID := fmt.Sprintf("session-%d", i)
		req.AddCookie(&http.Cookie{Name: "forklift_id", Value: sessionID})

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("Failed to send request: %v", err)
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("Failed to read response body: %v", err)
		}
		resp.Body.Close()

		backendCounts[string(body)]++

		// Add a small delay to avoid overwhelming the server
		time.Sleep(1 * time.Millisecond)
	}

	for i := 1; i <= 7; i++ {
		backendName := fmt.Sprintf("Hello from V%d", i)
		count := backendCounts[backendName]
		percentage := float64(count) / float64(totalRequests) * 100
		fmt.Printf("%s count: %d (%.2f%%)\n", backendName, count, percentage)
	}

	// Chi-square test for equal distribution
	expected := float64(totalRequests) / 7
	chiSquare := 0.0
	for i := 1; i <= 7; i++ {
		backendName := fmt.Sprintf("Hello from V%d", i)
		count := float64(backendCounts[backendName])
		chiSquare += math.Pow(count-expected, 2) / expected
	}

	pValue := 1 - chi2.CDF(chiSquare)

	fmt.Printf("Chi-square statistic: %.4f, p-value: %.4f\n", chiSquare, pValue)

	// Use a significance level of 0.01 (99% confidence)
	if pValue < 0.01 {
		t.Errorf("Distribution is not equal (p-value = %.4f)", pValue)
	} else {
		fmt.Println("Distribution is considered equal (failed to reject null hypothesis)")
	}

	// Check if the distribution is within a reasonable range (12-18%)
	for i := 1; i <= 7; i++ {
		backendName := fmt.Sprintf("Hello from V%d", i)
		count := backendCounts[backendName]
		percentage := float64(count) / float64(totalRequests) * 100
		if percentage < 12 || percentage > 18 {
			t.Errorf("%s distribution is outside the expected range (12-18%%): %.2f%%", backendName, percentage)
		}
	}
}

func TestThreeBackendDistribution(t *testing.T) {
	v1Count := 0
	v2Count := 0
	v3Count := 0
	totalRequests := 1000

	for i := 0; i < totalRequests; i++ {
		var resp *http.Response
		var err error

		if i%3 == 2 {
			// Every third request goes to V3
			resp, err = http.Get(traefikURL + "/v3")
		} else {
			// Other requests go to the default route (V1 or V2)
			resp, err = http.Get(traefikURL + "/")
		}

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

		switch {
		case strings.Contains(string(body), "Hello from V1"):
			v1Count++
		case strings.Contains(string(body), "Hello from V2"):
			v2Count++
		case strings.Contains(string(body), "Hello from V3"):
			v3Count++
		default:
			t.Errorf("Unexpected response body: %s", string(body))
		}

		// Add a small delay to avoid overwhelming the server
		time.Sleep(10 * time.Millisecond)
	}

	v1Percentage := float64(v1Count) / float64(totalRequests) * 100
	v2Percentage := float64(v2Count) / float64(totalRequests) * 100
	v3Percentage := float64(v3Count) / float64(totalRequests) * 100

	fmt.Printf("V1 percentage: %.2f%%\n", v1Percentage)
	fmt.Printf("V2 percentage: %.2f%%\n", v2Percentage)
	fmt.Printf("V3 percentage: %.2f%%\n", v3Percentage)

	if v3Percentage < 30 || v3Percentage > 36 {
		t.Errorf("V3 distribution outside expected range: %.2f%%", v3Percentage)
	}

	if v1Percentage+v2Percentage < 64 || v1Percentage+v2Percentage > 70 {
		t.Errorf("V1+V2 distribution outside expected range: %.2f%%", v1Percentage+v2Percentage)
	}
}
