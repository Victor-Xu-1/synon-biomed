package server

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestWebLogoAssetServesEmbeddedAssetsWithCacheAndIsolationHeaders(t *testing.T) {
	handler := New(Options{}).Handler()
	request := httptest.NewRequest(http.MethodGet, "/api/assets/logos/ai-major/openai.svg", nil)
	request.Header.Set("X-Synon-User-Id", "local")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Type") != "image/svg+xml" ||
		response.Header().Get("X-Content-Type-Options") != "nosniff" ||
		response.Header().Get("Cross-Origin-Resource-Policy") != "same-origin" {
		t.Fatalf("asset headers = %#v", response.Header())
	}
	if !strings.Contains(response.Header().Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatalf("SVG CSP = %q", response.Header().Get("Content-Security-Policy"))
	}
	if !strings.Contains(response.Body.String(), "<svg") {
		t.Fatalf("asset body is not SVG: %q", response.Body.String())
	}
	etag := response.Header().Get("ETag")
	if etag == "" || response.Header().Get("Content-Length") != strconv.Itoa(response.Body.Len()) {
		t.Fatalf("etag=%q content-length=%q body=%d",
			etag, response.Header().Get("Content-Length"), response.Body.Len())
	}

	cached := httptest.NewRequest(http.MethodGet, "/api/assets/logos/ai-major/openai.svg", nil)
	cached.Header.Set("X-Synon-User-Id", "local")
	cached.Header.Set("If-None-Match", "W/"+etag)
	cachedResponse := httptest.NewRecorder()
	handler.ServeHTTP(cachedResponse, cached)
	if cachedResponse.Code != http.StatusNotModified || cachedResponse.Body.Len() != 0 {
		t.Fatalf("cached status=%d body=%q", cachedResponse.Code, cachedResponse.Body.String())
	}

	head := httptest.NewRequest(http.MethodHead, "/api/assets/logos/ai-china/minimax.png", nil)
	head.Header.Set("X-Synon-User-Id", "local")
	headResponse := httptest.NewRecorder()
	handler.ServeHTTP(headResponse, head)
	if headResponse.Code != http.StatusOK || headResponse.Body.Len() != 0 ||
		headResponse.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("HEAD status=%d type=%q body=%d",
			headResponse.Code, headResponse.Header().Get("Content-Type"), headResponse.Body.Len())
	}
}

func TestWebLogoAssetRejectsUnknownInvalidAndUnsupportedRequests(t *testing.T) {
	srv := New(Options{})
	handler := srv.Handler()
	missing := httptest.NewRequest(http.MethodGet, "/api/assets/logos/ai-major/missing.svg", nil)
	missing.Header.Set("X-Synon-User-Id", "local")
	missingResponse := httptest.NewRecorder()
	handler.ServeHTTP(missingResponse, missing)
	if missingResponse.Code != http.StatusNotFound {
		t.Fatalf("missing status=%d body=%s", missingResponse.Code, missingResponse.Body.String())
	}

	method := httptest.NewRequest(http.MethodPost, "/api/assets/logos/ai-major/openai.svg", nil)
	method.Header.Set("X-Synon-User-Id", "local")
	methodResponse := httptest.NewRecorder()
	handler.ServeHTTP(methodResponse, method)
	if methodResponse.Code != http.StatusMethodNotAllowed ||
		methodResponse.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("method status=%d allow=%q", methodResponse.Code, methodResponse.Header().Get("Allow"))
	}

	invalid := httptest.NewRequest(http.MethodGet, "/api/assets/logos/invalid.txt", nil)
	invalid.Header.Set("X-Synon-User-Id", "local")
	invalidResponse := httptest.NewRecorder()
	srv.handleWebLogoAsset(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusBadRequest {
		t.Fatalf("invalid status=%d body=%s", invalidResponse.Code, invalidResponse.Body.String())
	}
}
