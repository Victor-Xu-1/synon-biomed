package rcsbfiles

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"synon-go/internal/tools/securefetch"
)

type resolverFunc func(context.Context, string, string) ([]netip.Addr, error)

func (f resolverFunc) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return f(ctx, network, host)
}

type fetchCall struct {
	URL    string
	Policy securefetch.Policy
}

type fakeFetcher struct {
	calls       []fetchCall
	body        string
	contentType string
	err         error
}

func (f *fakeFetcher) Fetch(_ context.Context, target string, policy securefetch.Policy) (*securefetch.Response, error) {
	f.calls = append(f.calls, fetchCall{URL: target, Policy: policy})
	if f.err != nil {
		return nil, f.err
	}
	return &securefetch.Response{
		Body: io.NopCloser(strings.NewReader(f.body)), StatusCode: 200,
		ContentType: f.contentType, ContentLength: int64(len(f.body)),
	}, nil
}

func TestClientBuildsClosedRCSBEntryAndLigandRequests(t *testing.T) {
	for _, test := range []struct {
		name        string
		input       Input
		body        string
		contentType string
		wantURL     string
		wantName    string
	}{
		{
			name: "entry pdb", input: Input{ResourceKind: EntryCoordinates, EntryID: "4tz4", Format: FormatPDB},
			body: "HEADER    HUMAN CRBN                                          4TZ4\nATOM      1  N   GLY A   1      11.104  13.207   8.000  1.00 20.00           N  \n", contentType: "text/plain",
			wantURL: "https://files.rcsb.org/download/4TZ4.pdb", wantName: "4TZ4.pdb",
		},
		{
			name: "entry cif", input: Input{ResourceKind: EntryCoordinates, EntryID: "4tz4", Format: FormatCIF},
			body: "data_4TZ4\nloop_\n_atom_site.group_PDB\n_atom_site.Cartn_x\n_atom_site.Cartn_y\n_atom_site.Cartn_z\nATOM 11.1 13.2 8.0\n", contentType: "chemical/x-cif",
			wantURL: "https://files.rcsb.org/download/4TZ4.cif", wantName: "4TZ4.cif",
		},
		{
			name: "ligand cif", input: Input{ResourceKind: LigandDefinition, ComponentID: "lvy", Format: FormatCIF},
			body: "data_LVY\n_chem_comp.id LVY\nloop_\n_chem_comp_atom.comp_id\n_chem_comp_atom.atom_id\nLVY C1\n", contentType: "chemical/x-cif",
			wantURL: "https://files.rcsb.org/ligands/download/LVY.cif", wantName: "LVY.cif",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			prepared, err := Prepare(test.input)
			if err != nil || prepared.Filename != test.wantName {
				t.Fatalf("Prepare() = %#v, %v", prepared, err)
			}
			fetcher := &fakeFetcher{body: test.body, contentType: test.contentType}
			client := New(Options{Fetcher: fetcher, MaxBytes: 50 << 20, Timeout: 30 * time.Second})
			result, err := client.Fetch(context.Background(), test.input)
			if err != nil {
				t.Fatalf("Fetch() error = %v", err)
			}
			defer result.Body.Close()
			body, err := io.ReadAll(result.Body)
			if err != nil {
				t.Fatalf("ReadAll() error = %v", err)
			}
			if string(body) != test.body || result.Filename != test.wantName || len(fetcher.calls) != 1 || fetcher.calls[0].URL != test.wantURL {
				t.Fatalf("result=%#v calls=%#v body=%q", result, fetcher.calls, body)
			}
			if fetcher.calls[0].Policy.MaxBytes != 50<<20 || fetcher.calls[0].Policy.Timeout != 30*time.Second {
				t.Fatalf("policy = %#v", fetcher.calls[0].Policy)
			}
		})
	}
}

func TestClientRejectsInvalidIdentityAndFilenameBeforeFetch(t *testing.T) {
	for _, input := range []Input{
		{ResourceKind: EntryCoordinates, EntryID: "../4TZ4", Format: FormatPDB},
		{ResourceKind: EntryCoordinates, EntryID: "４TZ4", Format: FormatPDB},
		{ResourceKind: EntryCoordinates, EntryID: "4TZ4", ComponentID: "LVY", Format: FormatPDB},
		{ResourceKind: LigandDefinition, ComponentID: "LVY", Format: FormatPDB},
		{ResourceKind: LigandDefinition, ComponentID: "LVY", Format: FormatCIF, Filename: "../LVY.cif"},
		{ResourceKind: EntryCoordinates, EntryID: "4TZ4", Format: FormatPDB, Filename: "4TZ4.cif"},
	} {
		fetcher := &fakeFetcher{body: "unused", contentType: "text/plain"}
		client := New(Options{Fetcher: fetcher, MaxBytes: 50 << 20, Timeout: 30 * time.Second})
		if _, err := client.Fetch(context.Background(), input); err == nil || err.Error() != "rcsb_file_invalid_request" {
			t.Fatalf("input %#v error = %v", input, err)
		}
		if len(fetcher.calls) != 0 {
			t.Fatalf("invalid input reached fetcher: %#v", fetcher.calls)
		}
	}
}

func TestClientRejectsHTMLAndMismatchedScientificContent(t *testing.T) {
	for _, test := range []struct {
		name        string
		input       Input
		body        string
		contentType string
	}{
		{name: "html", input: Input{EntryID: "4TZ4", Format: FormatPDB}, body: "<html>login</html>", contentType: "text/plain"},
		{name: "pdb without coordinates", input: Input{EntryID: "4TZ4", Format: FormatPDB}, body: "HEADER only\nEND\n", contentType: "text/plain"},
		{name: "pdb fake atom prefix", input: Input{EntryID: "4TZ4", Format: FormatPDB}, body: "ATOM  not coordinates\n", contentType: "text/plain"},
		{name: "pdb wrong entry", input: Input{EntryID: "4TZ4", Format: FormatPDB}, body: "HEADER    HUMAN CRBN                                          5ABC\nATOM      1  N   GLY A   1      11.104  13.207   8.000  1.00 20.00           N  \n", contentType: "text/plain"},
		{name: "wrong entry cif", input: Input{EntryID: "4TZ4", Format: FormatCIF}, body: "data_5ABC\n_atom_site.id\n", contentType: "chemical/x-cif"},
		{name: "entry cif labels without coordinate row", input: Input{EntryID: "4TZ4", Format: FormatCIF}, body: "data_4TZ4\nloop_\n_atom_site.group_PDB\n_atom_site.Cartn_x\n_atom_site.Cartn_y\n_atom_site.Cartn_z\n", contentType: "chemical/x-cif"},
		{name: "entry cif nonfinite coordinate", input: Input{EntryID: "4TZ4", Format: FormatCIF}, body: "data_4TZ4\nloop_\n_atom_site.group_PDB\n_atom_site.Cartn_x\n_atom_site.Cartn_y\n_atom_site.Cartn_z\nATOM NaN 1 2\n", contentType: "chemical/x-cif"},
		{name: "wrong ligand cif", input: Input{ResourceKind: LigandDefinition, ComponentID: "LVY", Format: FormatCIF}, body: "data_HEM\n_chem_comp.id HEM\n", contentType: "chemical/x-cif"},
		{name: "wrong ligand value", input: Input{ResourceKind: LigandDefinition, ComponentID: "LVY", Format: FormatCIF}, body: "data_LVY\n_chem_comp.id HEM\n", contentType: "chemical/x-cif"},
		{name: "entry field in another block", input: Input{EntryID: "4TZ4", Format: FormatCIF}, body: "data_4TZ4\n_entry.id 4TZ4\ndata_5ABC\n_atom_site.id\n", contentType: "chemical/x-cif"},
		{name: "cif markers only in comments", input: Input{EntryID: "4TZ4", Format: FormatCIF}, body: "# data_4TZ4 _atom_site.id\ndata_5ABC\n", contentType: "chemical/x-cif"},
		{name: "cif markers only in quotes", input: Input{EntryID: "4TZ4", Format: FormatCIF}, body: "'data_4TZ4' '_atom_site.id'\n", contentType: "chemical/x-cif"},
		{name: "cif markers only in multiline text", input: Input{EntryID: "4TZ4", Format: FormatCIF}, body: ";data_4TZ4\n_atom_site.id\n;\n", contentType: "chemical/x-cif"},
		{name: "cif unclosed quote", input: Input{EntryID: "4TZ4", Format: FormatCIF}, body: "data_4TZ4\n'unclosed\n", contentType: "chemical/x-cif"},
		{name: "cif unclosed multiline", input: Input{EntryID: "4TZ4", Format: FormatCIF}, body: "data_4TZ4\n;unclosed\n", contentType: "chemical/x-cif"},
		{name: "cif valid structure then unclosed quote", input: Input{EntryID: "4TZ4", Format: FormatCIF}, body: "data_4TZ4\nloop_\n_atom_site.group_PDB\n_atom_site.Cartn_x\n_atom_site.Cartn_y\n_atom_site.Cartn_z\nATOM 1 2 3\n'unclosed\n", contentType: "chemical/x-cif"},
		{name: "ligand valid then missing scalar value", input: Input{ResourceKind: LigandDefinition, ComponentID: "LVY", Format: FormatCIF}, body: "data_LVY\n_chem_comp.id LVY\nloop_\n_chem_comp_atom.comp_id\n_chem_comp_atom.atom_id\nLVY C1\n_chem_comp.id\n", contentType: "chemical/x-cif"},
		{name: "valid structure then empty loop", input: Input{EntryID: "4TZ4", Format: FormatCIF}, body: "data_4TZ4\nloop_\n_atom_site.group_PDB\n_atom_site.Cartn_x\n_atom_site.Cartn_y\n_atom_site.Cartn_z\nATOM 1 2 3\nloop_\n", contentType: "chemical/x-cif"},
		{name: "duplicate target block", input: Input{EntryID: "4TZ4", Format: FormatCIF}, body: "data_4TZ4\nloop_\n_atom_site.group_PDB\n_atom_site.Cartn_x\n_atom_site.Cartn_y\n_atom_site.Cartn_z\nATOM 1 2 3\ndata_4TZ4\n", contentType: "chemical/x-cif"},
		{name: "duplicate chem comp id", input: Input{ResourceKind: LigandDefinition, ComponentID: "LVY", Format: FormatCIF}, body: "data_LVY\n_chem_comp.id HEM\n_chem_comp.id LVY\nloop_\n_chem_comp_atom.comp_id\n_chem_comp_atom.atom_id\nLVY C1\n", contentType: "chemical/x-cif"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fetcher := &fakeFetcher{body: test.body, contentType: test.contentType}
			client := New(Options{Fetcher: fetcher, MaxBytes: 50 << 20, Timeout: 30 * time.Second})
			result, err := client.Fetch(context.Background(), test.input)
			if err == nil {
				defer result.Body.Close()
				_, err = io.ReadAll(result.Body)
			}
			if err == nil || err.Error() != "rcsb_file_content_invalid" {
				t.Fatalf("content error = %v", err)
			}
		})
	}
}

func TestClientValidatesCIFTokensAcrossReadBoundaries(t *testing.T) {
	body := "data_4TZ4\nloop_\n_atom_site.group_PDB\n_atom_site.Cartn_x\n_atom_site.Cartn_y\n_atom_site.Cartn_z\nATOM 11.1 13.2 8.0\n_entry.id 4TZ4\n"
	fetcher := &fakeFetcher{body: body, contentType: "chemical/x-cif"}
	client := New(Options{Fetcher: fetcher, MaxBytes: 50 << 20, Timeout: 30 * time.Second})
	result, err := client.Fetch(context.Background(), Input{EntryID: "4TZ4", Format: FormatCIF})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	defer result.Body.Close()
	buffer := make([]byte, 2)
	var received strings.Builder
	for {
		n, readErr := result.Body.Read(buffer)
		received.Write(buffer[:n])
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			t.Fatalf("Read() error = %v", readErr)
		}
	}
	if received.String() != body {
		t.Fatalf("body = %q", received.String())
	}
}

func TestClientUsesSecureFetcherForRealTLSStreaming(t *testing.T) {
	body := "data_4TZ4\nloop_\n_atom_site.group_PDB\n_atom_site.Cartn_x\n_atom_site.Cartn_y\n_atom_site.Cartn_z\nATOM 11.1 13.2 8.0\n"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/download/4TZ4.cif" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		w.Header().Set("Content-Type", "chemical/x-cif")
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatal(err)
	}
	transport := server.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	transport.TLSClientConfig.InsecureSkipVerify = true // Test-only TLS server.
	dialAddress := server.Listener.Addr().String()
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, dialAddress)
	}
	secureClient := securefetch.New(securefetch.Options{
		HTTPClient:                   &http.Client{Transport: transport},
		TestOnlyAllowCustomTransport: true,
		Resolver: resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
		}),
	})
	client := New(Options{
		Fetcher: secureClient, BaseURL: "https://files.rcsb.org:" + port,
		TestOnlyAllowCustomOrigin: true, MaxBytes: 1 << 20, Timeout: 5 * time.Second,
	})
	result, err := client.Fetch(context.Background(), Input{EntryID: "4TZ4", Format: FormatCIF})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	defer result.Body.Close()
	received, err := io.ReadAll(result.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(received) != body {
		t.Fatalf("body = %q", received)
	}
}

func TestClientRejectsUntrustedCustomOriginBeforeFetch(t *testing.T) {
	fetcher := &fakeFetcher{body: "unused", contentType: "text/plain"}
	client := New(Options{
		Fetcher: fetcher, BaseURL: "https://attacker.test", MaxBytes: 1 << 20, Timeout: time.Second,
	})
	_, err := client.Fetch(context.Background(), Input{EntryID: "4TZ4", Format: FormatCIF})
	if err == nil || err.Error() != "rcsb_file_invalid_configuration" || len(fetcher.calls) != 0 {
		t.Fatalf("configuration error=%v calls=%#v", err, fetcher.calls)
	}
}

func TestClientMapsUpstreamStatusWithoutExposingResponseBody(t *testing.T) {
	for _, test := range []struct {
		status int
		want   string
	}{
		{status: 404, want: "rcsb_file_not_found"},
		{status: 429, want: "rcsb_file_rate_limited"},
		{status: 503, want: "rcsb_file_upstream_unavailable"},
		{status: 403, want: "rcsb_file_download_failed"},
	} {
		fetcher := &fakeFetcher{err: statusFetchError(test.status)}
		client := New(Options{Fetcher: fetcher, MaxBytes: 1 << 20, Timeout: time.Second})
		_, err := client.Fetch(context.Background(), Input{EntryID: "4TZ4", Format: FormatPDB})
		if err == nil || err.Error() != test.want {
			t.Fatalf("status %d error = %v", test.status, err)
		}
	}
}

func TestClientFetchesOfficialRCSBFiles(t *testing.T) {
	if os.Getenv("SYNON_TEST_LIVE_RCSB") != "1" {
		t.Skip("set SYNON_TEST_LIVE_RCSB=1 for the official RCSB protocol check")
	}
	client := New(Options{MaxBytes: 50 << 20, Timeout: 2 * time.Minute})
	for _, input := range []Input{
		{ResourceKind: EntryCoordinates, EntryID: "4TZ4", Format: FormatPDB},
		{ResourceKind: EntryCoordinates, EntryID: "4TZ4", Format: FormatCIF},
		{ResourceKind: LigandDefinition, ComponentID: "LVY", Format: FormatCIF},
	} {
		result, err := client.Fetch(context.Background(), input)
		if err != nil {
			t.Fatalf("Fetch(%#v) error = %v", input, err)
		}
		body, readErr := io.ReadAll(result.Body)
		closeErr := result.Body.Close()
		if readErr != nil || closeErr != nil || len(body) < 128 {
			t.Fatalf("Fetch(%#v) bytes=%d readErr=%v closeErr=%v", input, len(body), readErr, closeErr)
		}
	}
}

func statusFetchError(status int) error {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))
	defer server.Close()
	parsed, _ := url.Parse(server.URL)
	_, port, _ := net.SplitHostPort(parsed.Host)
	transport := server.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	transport.TLSClientConfig.InsecureSkipVerify = true
	dialAddress := server.Listener.Addr().String()
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, dialAddress)
	}
	client := securefetch.New(securefetch.Options{
		HTTPClient: &http.Client{Transport: transport}, TestOnlyAllowCustomTransport: true,
		Resolver: resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
		}),
	})
	_, err := client.Fetch(context.Background(), "https://files.rcsb.org:"+port+"/download/4TZ4.pdb", securefetch.Policy{
		AllowedHosts: []string{"files.rcsb.org"}, AllowedPorts: []string{port},
		AcceptedMediaTypes: []string{"chemical/x-pdb"}, MaxBytes: 1 << 20, Timeout: time.Second,
	})
	return err
}
