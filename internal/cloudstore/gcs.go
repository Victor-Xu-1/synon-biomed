package cloudstore

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type gcsServiceAccount struct {
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	ProjectID   string `json:"project_id"`
	TokenURI    string `json:"token_uri"`
}

type gcsClient struct {
	httpClient  *http.Client
	api         *url.URL
	tokenURL    *url.URL
	account     gcsServiceAccount
	privateKey  *rsa.PrivateKey
	projectID   string
	staticToken string
	mu          sync.Mutex
	token       string
	tokenUntil  time.Time
	now         func() time.Time
}

func newGCSClient(options FactoryOptions, values map[string]string) (*gcsClient, error) {
	rawAccount := strings.TrimSpace(values["service_account_json"])
	if rawAccount == "" {
		if path := strings.TrimSpace(os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")); path != "" {
			raw, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("read GOOGLE_APPLICATION_CREDENTIALS: %w", err)
			}
			if len(raw) > 1<<20 {
				return nil, errors.New("Google application credentials exceed 1 MiB")
			}
			rawAccount = string(raw)
		}
	}
	var account gcsServiceAccount
	var key *rsa.PrivateKey
	if rawAccount != "" {
		if err := json.Unmarshal([]byte(rawAccount), &account); err != nil {
			return nil, fmt.Errorf("decode GCS service_account_json: %w", err)
		}
		if account.ClientEmail == "" || account.PrivateKey == "" {
			return nil, errors.New("GCS service account requires client_email and private_key")
		}
		var err error
		key, err = parseRSAPrivateKey(account.PrivateKey)
		if err != nil {
			return nil, err
		}
	}
	staticToken := strings.TrimSpace(values["access_token"])
	if key == nil && staticToken == "" {
		return nil, errors.New("GCS credentials require service_account_json, access_token, or GOOGLE_APPLICATION_CREDENTIALS")
	}
	projectID := strings.TrimSpace(values["project_id"])
	if projectID == "" {
		projectID = strings.TrimSpace(account.ProjectID)
	}
	if projectID == "" {
		return nil, errors.New("GCS credentials require project_id")
	}
	api, err := endpointURL(options.GCSAPIEndpoint, "https://storage.googleapis.com", options.AllowInsecureTestEndpoints)
	if err != nil {
		return nil, err
	}
	tokenRaw := options.GCSTokenEndpoint
	if tokenRaw == "" {
		tokenRaw = account.TokenURI
	}
	tokenURL, err := endpointURL(tokenRaw, "https://oauth2.googleapis.com/token", options.AllowInsecureTestEndpoints)
	if err != nil {
		return nil, err
	}
	if !options.AllowInsecureTestEndpoints {
		host := strings.ToLower(tokenURL.Hostname())
		if host != "oauth2.googleapis.com" && host != "accounts.google.com" {
			return nil, errors.New("GCS token endpoint must be a Google OAuth host")
		}
	}
	return &gcsClient{
		httpClient: options.HTTPClient, api: api, tokenURL: tokenURL, account: account,
		privateKey: key, projectID: projectID, staticToken: staticToken, now: time.Now,
	}, nil
}

func (c *gcsClient) ListBuckets(ctx context.Context) ([]string, error) {
	result := []string{}
	token := ""
	for {
		query := url.Values{"project": {c.projectID}, "maxResults": {"1000"}}
		if token != "" {
			query.Set("pageToken", token)
		}
		var payload struct {
			Items []struct {
				Name string `json:"name"`
			} `json:"items"`
			Next string `json:"nextPageToken"`
		}
		if err := c.jsonRequest(ctx, http.MethodGet, "/storage/v1/b", query, nil, -1, "", &payload); err != nil {
			return nil, err
		}
		for _, item := range payload.Items {
			if item.Name != "" {
				result = append(result, item.Name)
			}
		}
		if payload.Next == "" {
			return result, nil
		}
		token = payload.Next
	}
}

func (c *gcsClient) ListPage(ctx context.Context, bucket, prefix, delimiter string, limit int, token string) (Page, error) {
	if err := validateBucket(bucket); err != nil {
		return Page{}, err
	}
	query := url.Values{"maxResults": {strconv.Itoa(normalizedLimit(limit, DefaultObjectLimit, MaxPageSize))}}
	if prefix != "" {
		query.Set("prefix", prefix)
	}
	if delimiter != "" {
		query.Set("delimiter", delimiter)
	}
	if token != "" {
		query.Set("pageToken", token)
	}
	var payload struct {
		Items []struct {
			Name    string `json:"name"`
			Size    string `json:"size"`
			Updated string `json:"updated"`
		} `json:"items"`
		Prefixes []string `json:"prefixes"`
		Next     string   `json:"nextPageToken"`
	}
	path := "/storage/v1/b/" + url.PathEscape(bucket) + "/o"
	if err := c.jsonRequest(ctx, http.MethodGet, path, query, nil, -1, "", &payload); err != nil {
		return Page{}, err
	}
	page := Page{Prefixes: payload.Prefixes, NextToken: payload.Next, Objects: make([]Object, 0, len(payload.Items))}
	for _, item := range payload.Items {
		size, _ := strconv.ParseInt(item.Size, 10, 64)
		page.Objects = append(page.Objects, Object{Key: item.Name, Size: size, LastModified: item.Updated})
	}
	return page, nil
}

func (c *gcsClient) HeadObject(ctx context.Context, bucket, key string) (ObjectInfo, error) {
	if err := validateBucket(bucket); err != nil {
		return ObjectInfo{}, err
	}
	if err := validateObjectKey(key); err != nil {
		return ObjectInfo{}, err
	}
	var payload struct {
		Size        string `json:"size"`
		ContentType string `json:"contentType"`
	}
	path := "/storage/v1/b/" + url.PathEscape(bucket) + "/o/" + url.PathEscape(key)
	if err := c.jsonRequest(ctx, http.MethodGet, path, nil, nil, -1, "", &payload); err != nil {
		return ObjectInfo{}, err
	}
	size, _ := strconv.ParseInt(payload.Size, 10, 64)
	return ObjectInfo{Size: size, ContentType: payload.ContentType}, nil
}

func (c *gcsClient) OpenObject(ctx context.Context, bucket, key string) (io.ReadCloser, ObjectInfo, error) {
	if err := validateBucket(bucket); err != nil {
		return nil, ObjectInfo{}, err
	}
	if err := validateObjectKey(key); err != nil {
		return nil, ObjectInfo{}, err
	}
	token, err := c.accessToken(ctx)
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	target := *c.api
	escapedPath := strings.TrimSuffix(c.api.EscapedPath(), "/") + "/download/storage/v1/b/" + url.PathEscape(bucket) + "/o/" + url.PathEscape(key)
	setEscapedPath(&target, escapedPath)
	target.RawQuery = "alt=media"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, ObjectInfo{}, fmt.Errorf("download GCS object: %w", err)
	}
	if !isSuccess(response.StatusCode) {
		return nil, ObjectInfo{}, responseError(response)
	}
	return response.Body, ObjectInfo{Size: response.ContentLength, ContentType: response.Header.Get("Content-Type")}, nil
}

func (c *gcsClient) PutObject(ctx context.Context, bucket, key string, content io.Reader, size int64, contentType string) (int64, error) {
	if err := validateBucket(bucket); err != nil {
		return 0, err
	}
	if err := validateObjectKey(key); err != nil {
		return 0, err
	}
	if size < 0 {
		return 0, errors.New("cloud upload size must be known")
	}
	token, err := c.accessToken(ctx)
	if err != nil {
		return 0, err
	}
	target := *c.api
	target.Path = strings.TrimSuffix(c.api.Path, "/") + "/upload/storage/v1/b/" + url.PathEscape(bucket) + "/o"
	target.RawQuery = (url.Values{"uploadType": {"media"}, "name": {key}}).Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), content)
	if err != nil {
		return 0, err
	}
	request.ContentLength = size
	request.Header.Set("Authorization", "Bearer "+token)
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return 0, fmt.Errorf("upload GCS object: %w", err)
	}
	if !isSuccess(response.StatusCode) {
		return 0, responseError(response)
	}
	response.Body.Close()
	return size, nil
}

func (c *gcsClient) jsonRequest(ctx context.Context, method, path string, query url.Values, body io.Reader, size int64, contentType string, output any) error {
	token, err := c.accessToken(ctx)
	if err != nil {
		return err
	}
	target := *c.api
	escapedPath := strings.TrimSuffix(c.api.EscapedPath(), "/") + path
	setEscapedPath(&target, escapedPath)
	if query != nil {
		target.RawQuery = query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if size >= 0 {
		request.ContentLength = size
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("GCS request: %w", err)
	}
	if !isSuccess(response.StatusCode) {
		return responseError(response)
	}
	defer response.Body.Close()
	if output == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 32<<20)).Decode(output); err != nil {
		return fmt.Errorf("decode GCS response: %w", err)
	}
	return nil
}

func (c *gcsClient) accessToken(ctx context.Context) (string, error) {
	if c.staticToken != "" {
		return c.staticToken, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now().UTC()
	if c.token != "" && now.Add(time.Minute).Before(c.tokenUntil) {
		return c.token, nil
	}
	assertion, err := c.jwtAssertion(now)
	if err != nil {
		return "", err
	}
	form := url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {assertion},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("request GCS access token: %w", err)
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
		return "", fmt.Errorf("decode GCS access token: %w", err)
	}
	if payload.AccessToken == "" {
		return "", errors.New("GCS token response omitted access_token")
	}
	if payload.ExpiresIn <= 0 {
		payload.ExpiresIn = 3600
	}
	c.token, c.tokenUntil = payload.AccessToken, now.Add(time.Duration(payload.ExpiresIn)*time.Second)
	return c.token, nil
}

func (c *gcsClient) jwtAssertion(now time.Time) (string, error) {
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{
		"iss":   c.account.ClientEmail,
		"scope": "https://www.googleapis.com/auth/devstorage.read_write",
		"aud":   c.tokenURL.String(), "iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	})
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, c.privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign GCS service-account JWT: %w", err)
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func parseRSAPrivateKey(value string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(value))
	if block == nil {
		return nil, errors.New("GCS private_key is not PEM")
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if rsaKey, ok := key.(*rsa.PrivateKey); ok {
			return rsaKey, nil
		}
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	return nil, errors.New("GCS private_key is not an RSA private key")
}
