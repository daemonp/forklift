package testing

import (
	"net/http"
	"net/http/httptest"
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
