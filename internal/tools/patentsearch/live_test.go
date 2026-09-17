package patentsearch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"synon-go/internal/tools/securefetch"
)

// TestLivePublicElectricLampPatentSearchAndPDF is an opt-in, non-biomedical
// acceptance test. It exercises real public search, resolves the PDF exposed
// by the Google Patents record for Edison's electric-lamp publication, then
// downloads it through the same bounded public-network policy used by the
// product. Run with:
//
//	SYNON_PATENT_LIVE_TEST=1 go test ./internal/tools/patentsearch -run TestLivePublicElectricLampPatentSearchAndPDF -count=1 -timeout=180s -v
func TestLivePublicElectricLampPatentSearchAndPDF(t *testing.T) {
	if strings.TrimSpace(os.Getenv("SYNON_PATENT_LIVE_TEST")) != "1" {
		t.Skip("live public-patent verification is opt-in")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	client := NewClient(Options{})

	search, err := client.Run(ctx, Input{
		Operation:  OperationSearch,
		Query:      "Thomas Edison electric lamp filament patent",
		Sources:    []string{"google"},
		MaxResults: 6,
	})
	if err != nil {
		t.Fatalf("live patent search: %v", err)
	}
	if len(search.Records) == 0 {
		t.Fatalf("live patent search returned no Google Patents records: warnings=%v statuses=%#v", search.Warnings, search.SourceStatuses)
	}
	for _, record := range search.Records {
		parsed, parseErr := url.Parse(record.URL)
		if parseErr != nil || !strings.EqualFold(parsed.Hostname(), "patents.google.com") {
			t.Fatalf("live patent search returned an out-of-scope record: %#v", record)
		}
	}

	resolved, err := client.Run(ctx, Input{
		Operation:         OperationResolveDownload,
		PublicationNumber: "US 223898 A",
		Sources:           []string{"google"},
	})
	if err != nil {
		t.Fatalf("resolve public electric-lamp patent PDF: %v", err)
	}
	if len(resolved.Downloads) != 1 || resolved.Downloads[0].State != DownloadResolved {
		t.Fatalf("public electric-lamp patent PDF was not resolved: downloads=%#v warnings=%v", resolved.Downloads, resolved.Warnings)
	}

	downloadURL := resolved.Downloads[0].DownloadURL
	parsed, err := url.Parse(downloadURL)
	if err != nil {
		t.Fatalf("parse resolved PDF URL: %v", err)
	}
	fetcher := securefetch.New(securefetch.Options{})
	response, err := fetcher.Fetch(ctx, downloadURL, securefetch.Policy{
		AllowedHosts:       []string{parsed.Hostname()},
		AllowedPorts:       []string{"443"},
		AcceptedMediaTypes: []string{"application/pdf", "application/octet-stream"},
		MaxRedirects:       3,
		MaxBytes:           32 << 20,
		Timeout:            90 * time.Second,
		UserAgent:          "synon-biomed-patent-live-test/1.0",
		IdentityEncoding:   true,
	})
	if err != nil {
		t.Fatalf("download resolved public patent PDF: %v", err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read resolved public patent PDF: %v", err)
	}
	if !bytes.HasPrefix(payload, []byte("%PDF-")) {
		t.Fatalf("resolved public patent did not have a PDF signature: content_type=%q bytes=%d", response.ContentType, len(payload))
	}
	digest := sha256.Sum256(payload)
	t.Logf(
		"search_records=%d publication=%s pdf_url=%s bytes=%d sha256=%s",
		len(search.Records), resolved.PublicationNumber, downloadURL, len(payload), fmt.Sprintf("%x", digest),
	)
}
