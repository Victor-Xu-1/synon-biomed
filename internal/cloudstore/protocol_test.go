package cloudstore

import (
	"bytes"
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestS3ProtocolRoundTrip(t *testing.T) {
	var mu sync.Mutex
	objects := map[string][]byte{"folder/source.txt": []byte("source")}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := verifyS3Signature(r, "AKID", "SECRET"); err != nil {
			http.Error(w, "invalid SigV4: "+err.Error(), http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/" {
			io.WriteString(w, `<ListAllMyBucketsResult><Buckets><Bucket><Name>bucket-a</Name></Bucket></Buckets></ListAllMyBucketsResult>`)
			return
		}
		if r.URL.Path == "/bucket-a" && r.URL.Query().Get("list-type") == "2" {
			io.WriteString(w, `<ListBucketResult><IsTruncated>false</IsTruncated><Contents><Key>folder/source.txt</Key><Size>6</Size><LastModified>2026-07-10T00:00:00Z</LastModified></Contents><CommonPrefixes><Prefix>folder/nested/</Prefix></CommonPrefixes></ListBucketResult>`)
			return
		}
		key := strings.TrimPrefix(r.URL.Path, "/bucket-a/")
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodHead:
			payload, ok := objects[key]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
			w.Header().Set("Content-Type", "text/plain")
		case http.MethodGet:
			payload, ok := objects[key]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/plain")
			w.Write(payload)
		case http.MethodPut:
			payload, _ := io.ReadAll(r.Body)
			objects[key] = payload
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "method", http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	factory := NewFactoryWithOptions(FactoryOptions{HTTPClient: server.Client(), S3Endpoint: server.URL, AllowInsecureTestEndpoints: true})
	client, err := factory.New("s3", Credentials{Values: map[string]string{
		"access_key_id": "AKID", "secret_access_key": "SECRET", "force_path_style": "true",
	}})
	if err != nil {
		t.Fatal(err)
	}
	exerciseProtocolClient(t, client)
}

func TestGCSJSONProtocolRoundTrip(t *testing.T) {
	var mu sync.Mutex
	objects := map[string][]byte{"folder/source.txt": []byte("source")}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			http.Error(w, "missing bearer", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/storage/v1/b":
			io.WriteString(w, `{"items":[{"name":"bucket-a"}]}`)
		case r.URL.Path == "/storage/v1/b/bucket-a/o" && r.Method == http.MethodGet:
			io.WriteString(w, `{"items":[{"name":"folder/source.txt","size":"6","updated":"2026-07-10T00:00:00Z"}],"prefixes":["folder/nested/"]}`)
		case r.URL.Path == "/storage/v1/b/bucket-a/o/folder/source.txt":
			io.WriteString(w, `{"size":"6","contentType":"text/plain"}`)
		case r.URL.Path == "/download/storage/v1/b/bucket-a/o/folder/source.txt":
			w.Header().Set("Content-Type", "text/plain")
			w.Write(objects["folder/source.txt"])
		case r.URL.Path == "/upload/storage/v1/b/bucket-a/o" && r.Method == http.MethodPost:
			mu.Lock()
			objects[r.URL.Query().Get("name")], _ = io.ReadAll(r.Body)
			mu.Unlock()
			io.WriteString(w, `{}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	factory := NewFactoryWithOptions(FactoryOptions{
		HTTPClient: server.Client(), GCSAPIEndpoint: server.URL, AllowInsecureTestEndpoints: true,
	})
	client, err := factory.New("gcs", Credentials{Values: map[string]string{"access_token": "token", "project_id": "project"}})
	if err != nil {
		t.Fatal(err)
	}
	exerciseProtocolClient(t, client)
}

func TestGCSServiceAccountJWTExchange(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: mustPKCS8(t, privateKey)})
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			if err := r.ParseForm(); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			assertion := r.Form.Get("assertion")
			parts := strings.Split(assertion, ".")
			if len(parts) != 3 || r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
				http.Error(w, "invalid assertion", http.StatusBadRequest)
				return
			}
			claimsRaw, _ := base64.RawURLEncoding.DecodeString(parts[1])
			var claims map[string]any
			_ = json.Unmarshal(claimsRaw, &claims)
			if claims["aud"] != server.URL+"/token" || !strings.Contains(fmt.Sprint(claims["scope"]), "devstorage.read_write") {
				http.Error(w, "invalid claims", http.StatusBadRequest)
				return
			}
			signature, _ := base64.RawURLEncoding.DecodeString(parts[2])
			digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
			if err := rsa.VerifyPKCS1v15(&privateKey.PublicKey, crypto.SHA256, digest[:], signature); err != nil {
				http.Error(w, "invalid signature", http.StatusBadRequest)
				return
			}
			io.WriteString(w, `{"access_token":"jwt-token","expires_in":3600}`)
			return
		}
		if r.URL.Path == "/storage/v1/b" && r.Header.Get("Authorization") == "Bearer jwt-token" {
			io.WriteString(w, `{"items":[{"name":"bucket-a"}]}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	account, _ := json.Marshal(map[string]string{
		"client_email": "service@example.iam.gserviceaccount.com", "private_key": string(privatePEM), "project_id": "project",
	})
	factory := NewFactoryWithOptions(FactoryOptions{
		HTTPClient: server.Client(), GCSAPIEndpoint: server.URL, GCSTokenEndpoint: server.URL + "/token", AllowInsecureTestEndpoints: true,
	})
	client, err := factory.New("gcs", Credentials{Values: map[string]string{"service_account_json": string(account)}})
	if err != nil {
		t.Fatal(err)
	}
	buckets, err := client.ListBuckets(context.Background())
	if err != nil || len(buckets) != 1 || buckets[0] != "bucket-a" {
		t.Fatalf("service account buckets=%v err=%v", buckets, err)
	}
}

func TestAzureBlobSharedKeyProtocolRoundTrip(t *testing.T) {
	var mu sync.Mutex
	objects := map[string][]byte{"folder/source.txt": []byte("source")}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := verifyAzureSignature(r, "account", []byte("secret")); err != nil {
			http.Error(w, "invalid SharedKey: "+err.Error(), http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/" && r.URL.Query().Get("comp") == "list" {
			io.WriteString(w, `<EnumerationResults><Containers><Container><Name>bucket-a</Name></Container></Containers><NextMarker></NextMarker></EnumerationResults>`)
			return
		}
		if r.URL.Path == "/bucket-a" && r.URL.Query().Get("comp") == "list" {
			io.WriteString(w, `<EnumerationResults><Blobs><Blob><Name>folder/source.txt</Name><Properties><Content-Length>6</Content-Length><Last-Modified>Fri, 10 Jul 2026 00:00:00 GMT</Last-Modified></Properties></Blob><BlobPrefix><Name>folder/nested/</Name></BlobPrefix></Blobs><NextMarker></NextMarker></EnumerationResults>`)
			return
		}
		key := strings.TrimPrefix(r.URL.Path, "/bucket-a/")
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodHead:
			payload, ok := objects[key]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
			w.Header().Set("Content-Type", "text/plain")
		case http.MethodGet:
			w.Header().Set("Content-Type", "text/plain")
			w.Write(objects[key])
		case http.MethodPut:
			if r.Header.Get("x-ms-blob-type") != "BlockBlob" {
				http.Error(w, "blob type", http.StatusBadRequest)
				return
			}
			objects[key], _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusCreated)
		}
	}))
	defer server.Close()

	connection := "AccountName=account;AccountKey=" + base64.StdEncoding.EncodeToString([]byte("secret"))
	factory := NewFactoryWithOptions(FactoryOptions{
		HTTPClient: server.Client(), AzureBlobEndpoint: server.URL, AllowInsecureTestEndpoints: true,
	})
	client, err := factory.New("azure", Credentials{Values: map[string]string{"connection_string": connection}})
	if err != nil {
		t.Fatal(err)
	}
	exerciseProtocolClient(t, client)
}

func TestAzureServicePrincipalTokenExchange(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/tenant/oauth2/v2.0/token" {
			if err := r.ParseForm(); err != nil || r.Form.Get("client_id") != "client" || r.Form.Get("client_secret") != "secret" || r.Form.Get("scope") != "https://storage.azure.com/.default" {
				http.Error(w, "invalid token request", http.StatusBadRequest)
				return
			}
			io.WriteString(w, `{"access_token":"azure-token","expires_in":3600}`)
			return
		}
		if r.URL.Path == "/" && r.URL.Query().Get("comp") == "list" && r.Header.Get("Authorization") == "Bearer azure-token" {
			io.WriteString(w, `<EnumerationResults><Containers><Container><Name>bucket-a</Name></Container></Containers></EnumerationResults>`)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	factory := NewFactoryWithOptions(FactoryOptions{
		HTTPClient: server.Client(), AzureBlobEndpoint: server.URL, AzureTokenEndpoint: server.URL, AllowInsecureTestEndpoints: true,
	})
	client, err := factory.New("azure", Credentials{Values: map[string]string{
		"tenant_id": "tenant", "client_id": "client", "client_secret": "secret", "storage_account": "account",
	}})
	if err != nil {
		t.Fatal(err)
	}
	buckets, err := client.ListBuckets(context.Background())
	if err != nil || len(buckets) != 1 || buckets[0] != "bucket-a" {
		t.Fatalf("service principal buckets=%v err=%v", buckets, err)
	}
}

func exerciseProtocolClient(t *testing.T, client Client) {
	t.Helper()
	ctx := context.Background()
	buckets, err := client.ListBuckets(ctx)
	if err != nil || len(buckets) != 1 || buckets[0] != "bucket-a" {
		t.Fatalf("buckets=%v err=%v", buckets, err)
	}
	page, err := client.ListPage(ctx, "bucket-a", "folder/", "/", 100, "")
	if err != nil || len(page.Objects) != 1 || page.Objects[0].Key != "folder/source.txt" || len(page.Prefixes) != 1 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	info, err := client.HeadObject(ctx, "bucket-a", "folder/source.txt")
	if err != nil || info.Size != 6 || info.ContentType != "text/plain" {
		t.Fatalf("head=%+v err=%v", info, err)
	}
	reader, downloaded, err := client.OpenObject(ctx, "bucket-a", "folder/source.txt")
	if err != nil {
		t.Fatal(err)
	}
	payload, readErr := io.ReadAll(reader)
	reader.Close()
	if readErr != nil || string(payload) != "source" || downloaded.ContentType != "text/plain" {
		t.Fatalf("download=%q info=%+v err=%v", payload, downloaded, readErr)
	}
	if uploaded, err := client.PutObject(ctx, "bucket-a", "folder/output+file ?.txt", bytes.NewReader([]byte("output")), 6, "text/plain"); err != nil || uploaded != 6 {
		t.Fatalf("upload=%d err=%v", uploaded, err)
	}
}

func TestLiveCloudProvider(t *testing.T) {
	provider := strings.TrimSpace(os.Getenv("SYNON_CLOUD_LIVE_PROVIDER"))
	rawCredentials := strings.TrimSpace(os.Getenv("SYNON_CLOUD_LIVE_CREDENTIALS_JSON"))
	if provider == "" || rawCredentials == "" {
		t.Skip("set SYNON_CLOUD_LIVE_PROVIDER and SYNON_CLOUD_LIVE_CREDENTIALS_JSON")
	}
	values := map[string]string{}
	if err := json.Unmarshal([]byte(rawCredentials), &values); err != nil {
		t.Fatal(err)
	}
	client, err := NewFactory(http.DefaultClient).New(provider, Credentials{Values: values, Region: values["region"]})
	if err != nil {
		t.Fatal(err)
	}
	buckets, err := client.ListBuckets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if bucket := strings.TrimSpace(os.Getenv("SYNON_CLOUD_LIVE_BUCKET")); bucket != "" {
		if _, err := client.ListPage(context.Background(), bucket, os.Getenv("SYNON_CLOUD_LIVE_PREFIX"), "", 1, ""); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("live provider %s returned %d bucket(s)", provider, len(buckets))
}

func TestMinIOIntegration(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("SYNON_MINIO_ENDPOINT"))
	bucket := strings.TrimSpace(os.Getenv("SYNON_MINIO_BUCKET"))
	if endpoint == "" || bucket == "" {
		t.Skip("set SYNON_MINIO_ENDPOINT and SYNON_MINIO_BUCKET")
	}
	accessKey := os.Getenv("SYNON_MINIO_ACCESS_KEY")
	secretKey := os.Getenv("SYNON_MINIO_SECRET_KEY")
	factory := NewFactoryWithOptions(FactoryOptions{
		HTTPClient: http.DefaultClient, S3Endpoint: endpoint, AllowInsecureTestEndpoints: true,
	})
	client, err := factory.New("s3", Credentials{Values: map[string]string{
		"access_key_id": accessKey, "secret_access_key": secretKey,
		"region": "us-east-1", "force_path_style": "true",
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	buckets, err := client.ListBuckets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, value := range buckets {
		found = found || value == bucket
	}
	if !found {
		t.Fatalf("MinIO bucket %q is not listed: %v", bucket, buckets)
	}
	key := "synon-live/space + unicode-测试.txt"
	payload := []byte("real MinIO round trip")
	if _, err := client.PutObject(ctx, bucket, key, bytes.NewReader(payload), int64(len(payload)), "text/plain"); err != nil {
		t.Fatal(err)
	}
	info, err := client.HeadObject(ctx, bucket, key)
	if err != nil || info.Size != int64(len(payload)) {
		t.Fatalf("MinIO head=%+v err=%v", info, err)
	}
	reader, _, err := client.OpenObject(ctx, bucket, key)
	if err != nil {
		t.Fatal(err)
	}
	downloaded, err := io.ReadAll(reader)
	reader.Close()
	if err != nil || !bytes.Equal(downloaded, payload) {
		t.Fatalf("MinIO download=%q err=%v", downloaded, err)
	}
	page, err := client.ListPage(ctx, bucket, "synon-live/", "", 10, "")
	if err != nil || len(page.Objects) == 0 {
		t.Fatalf("MinIO list=%+v err=%v", page, err)
	}
}

func TestAzuriteIntegration(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("SYNON_AZURITE_ENDPOINT"))
	if endpoint == "" {
		t.Skip("set SYNON_AZURITE_ENDPOINT")
	}
	const account = "devstoreaccount1"
	const key = "Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw=="
	factory := NewFactoryWithOptions(FactoryOptions{
		HTTPClient: http.DefaultClient, AzureBlobEndpoint: endpoint, AllowInsecureTestEndpoints: true,
	})
	client, err := factory.New("azure", Credentials{Values: map[string]string{
		"connection_string": "AccountName=" + account + ";AccountKey=" + key,
	}})
	if err != nil {
		t.Fatal(err)
	}
	azure := client.(*azureClient)
	ctx := context.Background()
	response, err := azure.do(ctx, http.MethodPut, "synon-integration", "", url.Values{"restype": {"container"}}, nil, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	payload := []byte("real Azurite round trip")
	objectKey := "folder/space + unicode-测试.txt"
	if _, err := client.PutObject(ctx, "synon-integration", objectKey, bytes.NewReader(payload), int64(len(payload)), "text/plain"); err != nil {
		t.Fatal(err)
	}
	info, err := client.HeadObject(ctx, "synon-integration", objectKey)
	if err != nil || info.Size != int64(len(payload)) {
		t.Fatalf("Azurite head=%+v err=%v", info, err)
	}
	reader, _, err := client.OpenObject(ctx, "synon-integration", objectKey)
	if err != nil {
		t.Fatal(err)
	}
	downloaded, err := io.ReadAll(reader)
	reader.Close()
	if err != nil || !bytes.Equal(downloaded, payload) {
		t.Fatalf("Azurite download=%q err=%v", downloaded, err)
	}
	page, err := client.ListPage(ctx, "synon-integration", "folder/", "", 10, "")
	if err != nil || len(page.Objects) != 1 || page.Objects[0].Key != objectKey {
		t.Fatalf("Azurite list=%+v err=%v", page, err)
	}
}

func TestFakeGCSIntegration(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("SYNON_FAKE_GCS_ENDPOINT"))
	if endpoint == "" {
		t.Skip("set SYNON_FAKE_GCS_ENDPOINT")
	}
	createBody := strings.NewReader(`{"name":"synon-integration"}`)
	request, err := http.NewRequest(http.MethodPost, endpoint+"/storage/v1/b?project=synon-test", createBody)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusConflict {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("create fake GCS bucket status=%d body=%s", response.StatusCode, body)
	}
	response.Body.Close()

	factory := NewFactoryWithOptions(FactoryOptions{
		HTTPClient: http.DefaultClient, GCSAPIEndpoint: endpoint, AllowInsecureTestEndpoints: true,
	})
	client, err := factory.New("gcs", Credentials{Values: map[string]string{"access_token": "emulator-token", "project_id": "synon-test"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	payload := []byte("real fake-gcs-server round trip")
	objectKey := "folder/space + unicode-测试.txt"
	if _, err := client.PutObject(ctx, "synon-integration", objectKey, bytes.NewReader(payload), int64(len(payload)), "text/plain"); err != nil {
		t.Fatal(err)
	}
	info, err := client.HeadObject(ctx, "synon-integration", objectKey)
	if err != nil || info.Size != int64(len(payload)) {
		t.Fatalf("fake GCS head=%+v err=%v", info, err)
	}
	reader, _, err := client.OpenObject(ctx, "synon-integration", objectKey)
	if err != nil {
		t.Fatal(err)
	}
	downloaded, err := io.ReadAll(reader)
	reader.Close()
	if err != nil || !bytes.Equal(downloaded, payload) {
		t.Fatalf("fake GCS download=%q err=%v", downloaded, err)
	}
	page, err := client.ListPage(ctx, "synon-integration", "folder/", "", 10, "")
	if err != nil || len(page.Objects) != 1 || page.Objects[0].Key != objectKey {
		t.Fatalf("fake GCS list=%+v err=%v", page, err)
	}
}

func TestEndpointRejectsCredentialExfiltrationTargets(t *testing.T) {
	for _, endpoint := range []string{"http://example.com", "https://127.0.0.1", "https://localhost", "https://metadata.internal"} {
		_, err := endpointURL(endpoint, "", false)
		if err == nil {
			t.Fatalf("endpoint %q was accepted", endpoint)
		}
	}
	parsed, err := endpointURL("https://storage.example.com", "", false)
	if err != nil || parsed.Host != "storage.example.com" {
		t.Fatalf("public endpoint rejected: %v %v", parsed, err)
	}
}

func TestCanonicalQueryUsesRFC3986Encoding(t *testing.T) {
	query := canonicalQuery(url.Values{"prefix": {"folder/a+b c"}, "x": {"~"}})
	if query != "prefix=folder%2Fa%2Bb%20c&x=~" {
		t.Fatalf("canonical query=%q", query)
	}
	if escaped := escapeS3Key("folder/a+b?#.txt"); escaped != "folder/a%2Bb%3F%23.txt" {
		t.Fatalf("escaped S3 key=%q", escaped)
	}
}

func mustPKCS8(t *testing.T, key *rsa.PrivateKey) []byte {
	t.Helper()
	encoded, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func verifyS3Signature(request *http.Request, accessKey, secretKey string) error {
	authorization := request.Header.Get("Authorization")
	const prefix = "AWS4-HMAC-SHA256 "
	if !strings.HasPrefix(authorization, prefix) {
		return errors.New("authorization scheme is missing")
	}
	fields := map[string]string{}
	for _, field := range strings.Split(strings.TrimPrefix(authorization, prefix), ", ") {
		key, value, ok := strings.Cut(field, "=")
		if ok {
			fields[key] = value
		}
	}
	credential := strings.Split(fields["Credential"], "/")
	if len(credential) != 5 || credential[0] != accessKey || credential[3] != "s3" || credential[4] != "aws4_request" {
		return errors.New("credential scope is invalid")
	}
	signedNames := strings.Split(fields["SignedHeaders"], ";")
	var canonicalHeaders strings.Builder
	for _, name := range signedNames {
		value := request.Header.Get(name)
		if name == "host" {
			value = request.Host
		}
		canonicalHeaders.WriteString(name + ":" + strings.Join(strings.Fields(value), " ") + "\n")
	}
	payloadHash := request.Header.Get("x-amz-content-sha256")
	canonical := strings.Join([]string{
		request.Method, request.URL.EscapedPath(), request.URL.RawQuery,
		canonicalHeaders.String(), fields["SignedHeaders"], payloadHash,
	}, "\n")
	canonicalDigest := sha256.Sum256([]byte(canonical))
	scope := strings.Join(credential[1:], "/")
	stringToSign := "AWS4-HMAC-SHA256\n" + request.Header.Get("x-amz-date") + "\n" + scope + "\n" + hex.EncodeToString(canonicalDigest[:])
	dateKey := testHMAC([]byte("AWS4"+secretKey), credential[1])
	regionKey := testHMAC(dateKey, credential[2])
	serviceKey := testHMAC(regionKey, credential[3])
	signingKey := testHMAC(serviceKey, credential[4])
	expected := hex.EncodeToString(testHMAC(signingKey, stringToSign))
	if !hmac.Equal([]byte(expected), []byte(fields["Signature"])) {
		return errors.New("signature mismatch")
	}
	return nil
}

func verifyAzureSignature(request *http.Request, account string, key []byte) error {
	if request.Header.Get("x-ms-version") == "" {
		return errors.New("x-ms-version is missing")
	}
	contentLength := ""
	if request.ContentLength > 0 {
		contentLength = strconv.FormatInt(request.ContentLength, 10)
	}
	names := []string{}
	for name := range request.Header {
		name = strings.ToLower(name)
		if strings.HasPrefix(name, "x-ms-") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	var canonicalHeaders strings.Builder
	for _, name := range names {
		canonicalHeaders.WriteString(name + ":" + strings.Join(strings.Fields(request.Header.Get(name)), " ") + "\n")
	}
	resource := "/" + account + request.URL.EscapedPath()
	queryNames := []string{}
	for name := range request.URL.Query() {
		queryNames = append(queryNames, strings.ToLower(name))
	}
	sort.Strings(queryNames)
	for _, name := range queryNames {
		values := append([]string(nil), request.URL.Query()[name]...)
		sort.Strings(values)
		resource += "\n" + name + ":" + strings.Join(values, ",")
	}
	stringToSign := strings.Join([]string{
		request.Method, request.Header.Get("Content-Encoding"), request.Header.Get("Content-Language"), contentLength,
		request.Header.Get("Content-MD5"), request.Header.Get("Content-Type"), "", request.Header.Get("If-Modified-Since"),
		request.Header.Get("If-Match"), request.Header.Get("If-None-Match"), request.Header.Get("If-Unmodified-Since"),
		request.Header.Get("Range"), canonicalHeaders.String() + resource,
	}, "\n")
	expected := base64.StdEncoding.EncodeToString(testHMAC(key, stringToSign))
	if request.Header.Get("Authorization") != "SharedKey "+account+":"+expected {
		return errors.New("signature mismatch")
	}
	return nil
}

func testHMAC(key []byte, value string) []byte {
	hash := hmac.New(sha256.New, key)
	_, _ = hash.Write([]byte(value))
	return hash.Sum(nil)
}
