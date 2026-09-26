package securefetch

import (
	"context"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientPreservesReportedMIMEParametersWithoutChangingTypePolicy(t *testing.T) {
	for _, header := range []string{"Text/HTML; Charset=UTF-16LE", "text/html; charset=utf-8; profile=source", "text/html; charset=unknown-encoding", "text/html; charset=\"unfinished"} {
		t.Run(header, func(t *testing.T) {
			const payload = "<\x00h\x00t\x00m\x00l\x00>\x00"
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", header)
				_, _ = io.WriteString(w, payload)
			}))
			defer server.Close()
			client, target, policy := testClient(t, server)
			policy.AcceptedMediaTypes = []string{"text/html"}
			response, err := client.Fetch(context.Background(), target+"/page", policy)
			media, parameters, parseErr := mime.ParseMediaType(header)
			if parseErr != nil {
				if !IsCode(err, CodeContentType) {
					t.Fatalf("invalid MIME admitted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil || string(body) != payload || response.ContentType != "text/html" || response.ReportedContentType != mime.FormatMediaType(media, parameters) {
				t.Fatalf("MIME/body not preserved: type=%q reported=%q bytes=%q error=%v", response.ContentType, response.ReportedContentType, body, err)
			}
			// The transport preserves syntactically valid labels. The format
			// consumer, not a network-policy bypass, validates actual encoding.
		})
	}
}
