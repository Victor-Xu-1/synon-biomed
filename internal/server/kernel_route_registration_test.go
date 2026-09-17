package server

import (
	"net/http"
	"testing"
)

func TestKernelRouteRegistrationCoexistsWithLegacyFrameAndRefreshPatterns(t *testing.T) {
	server := New(Options{})
	mux := http.NewServeMux()
	mux.HandleFunc("/api/frames/", server.handleFrameCompatibility)
	mux.HandleFunc("/api/system/refresh-kernels", server.handleRefreshKernels)
	server.registerKernelRoutes(mux)

	for _, methodAndPath := range [][2]string{
		{http.MethodGet, "/api/kernels"},
		{http.MethodGet, "/api/frames/root/kernels"},
		{http.MethodPost, "/api/frames/frame/kernels/kernel/stop"},
		{http.MethodPost, "/api/frames/frame/kernel-exec"},
		{http.MethodPost, "/api/frames/frame/structure-electrostatic-map"},
		{http.MethodPost, "/api/frames/frame/kernel-exec/exec/interrupt"},
		{http.MethodPost, "/api/system/refresh-kernels"},
	} {
		request, err := http.NewRequest(methodAndPath[0], methodAndPath[1], nil)
		if err != nil {
			t.Fatal(err)
		}
		if pattern := request.Pattern; pattern != "" {
			t.Fatalf("request unexpectedly matched before ServeHTTP: %q", pattern)
		}
		// Handler resolution is enough here; endpoint behavior is covered by
		// real SQLite and subprocess tests in kernel_api_test.go.
		_, pattern := mux.Handler(request)
		if pattern == "" {
			t.Fatalf("%s %s has no registered handler", methodAndPath[0], methodAndPath[1])
		}
	}
}
