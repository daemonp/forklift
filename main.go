package main

import (
	"context"
	"net/http"

	"github.com/daemonp/traefik-forklift-middleware/abtest"
)

func main() {
	config := abtest.CreateConfig()
	// Set up your config here
	config.V1Backend = "http://v1.example.com"
	config.V2Backend = "http://v2.example.com"
	config.Rules = []abtest.RoutingRule{
		{
			Path:       "/test",
			Percentage: 50,
		},
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Hello, this is the next handler!"))
	})

	middleware, err := abtest.New(context.Background(), next, config, "abtest")
	if err != nil {
		panic(err)
	}

	http.Handle("/", middleware)
	http.ListenAndServe(":8080", nil)
}
