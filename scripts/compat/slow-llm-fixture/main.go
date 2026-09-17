package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"
)

func main() {
	address := flag.String("address", "127.0.0.1:38994", "loopback listen address")
	delay := flag.Duration("delay", 30*time.Second, "model response delay")
	flag.Parse()
	server := &http.Server{
		Addr:              *address,
		ReadHeaderTimeout: 2 * time.Second,
		Handler:           slowHandler(*delay),
	}
	log.Printf("slow LLM fixture listening on %s", *address)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(fmt.Errorf("serve slow LLM fixture: %w", err))
	}
}

func slowHandler(delay time.Duration) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/health" {
			response.Header().Set("Content-Type", "application/json")
			_, _ = response.Write([]byte(`{"status":"ok"}`))
			return
		}
		time.Sleep(delay)
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusServiceUnavailable)
		_, _ = response.Write([]byte(`{"error":"intentional slow model fixture"}`))
	})
}
