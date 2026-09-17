package cloudstore

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var azureAccountPattern = regexp.MustCompile(`^[a-z0-9]{3,24}$`)

var azureEndpointSuffixes = map[string]bool{
	"core.windows.net": true, "core.usgovcloudapi.net": true,
	"core.chinacloudapi.cn": true, "core.cloudapi.de": true,
}

type azureClient struct {
	httpClient   *http.Client
	endpoint     *url.URL
	account      string
	accountKey   []byte
	tenantID     string
	clientID     string
	clientSecret string
	tokenURL     *url.URL
	mu           sync.Mutex
	token        string
	tokenUntil   time.Time
	now          func() time.Time
}

func newAzureClient(options FactoryOptions, values map[string]string) (*azureClient, error) {
	account, encodedKey, suffix := "", "", "core.windows.net"
	if connection := strings.TrimSpace(values["connection_string"]); connection != "" {
		parts := map[string]string{}
		for _, item := range strings.Split(connection, ";") {
			key, value, ok := strings.Cut(item, "=")
			if ok {
				parts[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
			}
		}
		account, encodedKey = parts["accountname"], parts["accountkey"]
		if parts["endpointsuffix"] != "" {
			suffix = strings.ToLower(parts["endpointsuffix"])
		}
		if account == "" || encodedKey == "" {
			return nil, errors.New("Azure connection string requires AccountName and AccountKey")
		}
	} else {
		account = strings.TrimSpace(values["storage_account"])
	}
	if !azureAccountPattern.MatchString(account) {
		return nil, errors.New("Azure storage_account must be 3-24 lowercase letters or digits")
	}
	if !azureEndpointSuffixes[suffix] {
		return nil, errors.New("Azure EndpointSuffix is not a recognized Azure cloud")
	}
	endpoint, err := endpointURL(options.AzureBlobEndpoint, "https://"+account+".blob."+suffix, options.AllowInsecureTestEndpoints)
	if err != nil {
		return nil, err
	}
	client := &azureClient{httpClient: options.HTTPClient, endpoint: endpoint, account: account, now: time.Now}
	if encodedKey != "" {
		client.accountKey, err = base64.StdEncoding.DecodeString(encodedKey)
		if err != nil || len(client.accountKey) == 0 {
			return nil, errors.New("Azure AccountKey is not valid base64")
		}
		return client, nil
	}
	client.tenantID, client.clientID, client.clientSecret = values["tenant_id"], values["client_id"], values["client_secret"]
	if client.tenantID == "" || client.clientID == "" || client.clientSecret == "" {
		return nil, errors.New("Azure credentials need a connection string or tenant_id, client_id, client_secret, and storage_account")
	}
	if strings.ContainsAny(client.tenantID, "/\\\x00\r\n") {
		return nil, errors.New("Azure tenant_id is invalid")
	}
	tokenBase, err := endpointURL(options.AzureTokenEndpoint, "https://login.microsoftonline.com", options.AllowInsecureTestEndpoints)
	if err != nil {
		return nil, err
	}
	tokenBase.Path = strings.TrimSuffix(tokenBase.Path, "/") + "/" + url.PathEscape(client.tenantID) + "/oauth2/v2.0/token"
	client.tokenURL = tokenBase
	return client, nil
}

func (c *azureClient) ListBuckets(ctx context.Context) ([]string, error) {
	result, marker := []string{}, ""
	for {
		query := url.Values{"comp": {"list"}, "maxresults": {"5000"}}
		if marker != "" {
			query.Set("marker", marker)
		}
		response, err := c.do(ctx, http.MethodGet, "", "", query, nil, -1, "")
		if err != nil {
			return nil, err
		}
		var payload struct {
			Containers struct {
				Items []struct {
					Name string `xml:"Name"`
				} `xml:"Container"`
			} `xml:"Containers"`
			NextMarker string `xml:"NextMarker"`
		}
		if err := xml.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(&payload); err != nil {
			response.Body.Close()
			return nil, fmt.Errorf("decode Azure container list: %w", err)
		}
		response.Body.Close()
		for _, item := range payload.Containers.Items {
			if item.Name != "" {
				result = append(result, item.Name)
			}
		}
		if payload.NextMarker == "" {
			return result, nil
		}
		marker = payload.NextMarker
	}
}

func (c *azureClient) ListPage(ctx context.Context, bucket, prefix, delimiter string, limit int, marker string) (Page, error) {
	if err := validateBucket(bucket); err != nil {
		return Page{}, err
	}
	query := url.Values{
		"restype": {"container"}, "comp": {"list"},
		"maxresults": {strconv.Itoa(normalizedLimit(limit, DefaultObjectLimit, MaxPageSize))},
	}
	if prefix != "" {
		query.Set("prefix", prefix)
	}
	if delimiter != "" {
		query.Set("delimiter", delimiter)
	}
	if marker != "" {
		query.Set("marker", marker)
	}
	response, err := c.do(ctx, http.MethodGet, bucket, "", query, nil, -1, "")
	if err != nil {
		return Page{}, err
	}
	defer response.Body.Close()
	var payload struct {
		Blobs struct {
			Items []struct {
				Name       string `xml:"Name"`
				Properties struct {
					Size         int64  `xml:"Content-Length"`
					LastModified string `xml:"Last-Modified"`
				} `xml:"Properties"`
			} `xml:"Blob"`
			Prefixes []struct {
				Name string `xml:"Name"`
			} `xml:"BlobPrefix"`
		} `xml:"Blobs"`
		NextMarker string `xml:"NextMarker"`
	}
	if err := xml.NewDecoder(io.LimitReader(response.Body, 32<<20)).Decode(&payload); err != nil {
		return Page{}, fmt.Errorf("decode Azure blob list: %w", err)
	}
	page := Page{NextToken: payload.NextMarker, Objects: make([]Object, 0, len(payload.Blobs.Items)), Prefixes: make([]string, 0, len(payload.Blobs.Prefixes))}
	for _, item := range payload.Blobs.Items {
		page.Objects = append(page.Objects, Object{Key: item.Name, Size: item.Properties.Size, LastModified: item.Properties.LastModified})
	}
	for _, item := range payload.Blobs.Prefixes {
		if item.Name != "" {
			page.Prefixes = append(page.Prefixes, item.Name)
		}
	}
	return page, nil
}

func (c *azureClient) HeadObject(ctx context.Context, bucket, key string) (ObjectInfo, error) {
	response, err := c.do(ctx, http.MethodHead, bucket, key, nil, nil, -1, "")
	if err != nil {
		return ObjectInfo{}, err
	}
	response.Body.Close()
	return ObjectInfo{Size: response.ContentLength, ContentType: response.Header.Get("Content-Type")}, nil
}

func (c *azureClient) OpenObject(ctx context.Context, bucket, key string) (io.ReadCloser, ObjectInfo, error) {
	response, err := c.do(ctx, http.MethodGet, bucket, key, nil, nil, -1, "")
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	return response.Body, ObjectInfo{Size: response.ContentLength, ContentType: response.Header.Get("Content-Type")}, nil
}

func (c *azureClient) PutObject(ctx context.Context, bucket, key string, content io.Reader, size int64, contentType string) (int64, error) {
	if size < 0 {
		return 0, errors.New("cloud upload size must be known")
	}
	response, err := c.do(ctx, http.MethodPut, bucket, key, nil, content, size, contentType)
	if err != nil {
		return 0, err
	}
	response.Body.Close()
	return size, nil
}

func (c *azureClient) do(ctx context.Context, method, bucket, key string, query url.Values, body io.Reader, size int64, contentType string) (*http.Response, error) {
	if bucket != "" {
		if err := validateBucket(bucket); err != nil {
			return nil, err
		}
	}
	if key != "" {
		if err := validateObjectKey(key); err != nil {
			return nil, err
		}
	}
	target := *c.endpoint
	escapedPath := strings.TrimSuffix(c.endpoint.EscapedPath(), "/")
	if bucket != "" {
		escapedPath += "/" + url.PathEscape(bucket)
	}
	if key != "" {
		escapedPath += "/" + escapeS3Key(key)
	}
	if escapedPath == "" {
		escapedPath = "/"
	}
	setEscapedPath(&target, escapedPath)
	if query != nil {
		target.RawQuery = query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return nil, err
	}
	if size >= 0 {
		request.ContentLength = size
	}
	request.Header.Set("x-ms-date", c.now().UTC().Format(http.TimeFormat))
	request.Header.Set("x-ms-version", "2023-11-03")
	if method == http.MethodPut && key != "" {
		request.Header.Set("x-ms-blob-type", "BlockBlob")
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	if len(c.accountKey) > 0 {
		c.sign(request)
	} else {
		token, err := c.accessToken(ctx)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("Azure Blob request: %w", err)
	}
	if !isSuccess(response.StatusCode) {
		return nil, responseError(response)
	}
	return response, nil
}

func (c *azureClient) sign(request *http.Request) {
	contentLength := ""
	if request.ContentLength > 0 {
		contentLength = strconv.FormatInt(request.ContentLength, 10)
	}
	xmsNames := []string{}
	for name := range request.Header {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "x-ms-") {
			xmsNames = append(xmsNames, lower)
		}
	}
	sort.Strings(xmsNames)
	var canonicalHeaders strings.Builder
	for _, name := range xmsNames {
		canonicalHeaders.WriteString(name)
		canonicalHeaders.WriteByte(':')
		canonicalHeaders.WriteString(strings.Join(strings.Fields(request.Header.Get(name)), " "))
		canonicalHeaders.WriteByte('\n')
	}
	canonicalResource := "/" + c.account + request.URL.EscapedPath()
	query := request.URL.Query()
	queryNames := make([]string, 0, len(query))
	for name := range query {
		queryNames = append(queryNames, strings.ToLower(name))
	}
	sort.Strings(queryNames)
	for _, name := range queryNames {
		values := append([]string(nil), query[name]...)
		sort.Strings(values)
		canonicalResource += "\n" + name + ":" + strings.Join(values, ",")
	}
	stringToSign := strings.Join([]string{
		request.Method, request.Header.Get("Content-Encoding"), request.Header.Get("Content-Language"),
		contentLength, request.Header.Get("Content-MD5"), request.Header.Get("Content-Type"), "",
		request.Header.Get("If-Modified-Since"), request.Header.Get("If-Match"), request.Header.Get("If-None-Match"),
		request.Header.Get("If-Unmodified-Since"), request.Header.Get("Range"), canonicalHeaders.String() + canonicalResource,
	}, "\n")
	hash := hmac.New(sha256.New, c.accountKey)
	_, _ = hash.Write([]byte(stringToSign))
	request.Header.Set("Authorization", "SharedKey "+c.account+":"+base64.StdEncoding.EncodeToString(hash.Sum(nil)))
}

func (c *azureClient) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now().UTC()
	if c.token != "" && now.Add(time.Minute).Before(c.tokenUntil) {
		return c.token, nil
	}
	form := url.Values{
		"client_id": {c.clientID}, "client_secret": {c.clientSecret},
		"grant_type": {"client_credentials"}, "scope": {"https://storage.azure.com/.default"},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("request Azure access token: %w", err)
	}
	if !isSuccess(response.StatusCode) {
		return "", responseError(response)
	}
	defer response.Body.Close()
	var payload struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode Azure access token: %w", err)
	}
	if payload.AccessToken == "" {
		return "", errors.New("Azure token response omitted access_token")
	}
	if payload.ExpiresIn <= 0 {
		payload.ExpiresIn = 3600
	}
	c.token, c.tokenUntil = payload.AccessToken, now.Add(time.Duration(payload.ExpiresIn)*time.Second)
	return c.token, nil
}
