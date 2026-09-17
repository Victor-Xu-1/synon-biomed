package cloudstore

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type s3Client struct {
	httpClient   *http.Client
	endpoint     *url.URL
	accessKey    string
	secretKey    string
	sessionToken string
	region       string
	pathStyle    bool
	gcs          bool
	projectID    string
	now          func() time.Time
}

func newS3Client(options FactoryOptions, values map[string]string, gcs bool) (*s3Client, error) {
	accessKey := strings.TrimSpace(values["access_key_id"])
	secretKey := strings.TrimSpace(values["secret_access_key"])
	if accessKey == "" || secretKey == "" {
		return nil, errors.New("cloud credentials require access_key_id and secret_access_key")
	}
	region := strings.TrimSpace(values["region"])
	if region == "" {
		region = "us-east-1"
	}
	fallback := "https://s3.amazonaws.com"
	if region != "us-east-1" {
		fallback = "https://s3." + region + ".amazonaws.com"
	}
	rawEndpoint := strings.TrimSpace(values["endpoint"])
	if strings.TrimSpace(options.S3Endpoint) != "" {
		rawEndpoint = options.S3Endpoint
	}
	if gcs {
		fallback = "https://storage.googleapis.com"
		region = "auto"
		rawEndpoint = options.S3Endpoint
	}
	endpoint, err := endpointURL(rawEndpoint, fallback, options.AllowInsecureTestEndpoints)
	if err != nil {
		return nil, err
	}
	pathStyle := boolValue(values["force_path_style"], rawEndpoint != "" || gcs)
	return &s3Client{
		httpClient: options.HTTPClient, endpoint: endpoint, accessKey: accessKey,
		secretKey: secretKey, sessionToken: values["session_token"], region: region,
		pathStyle: pathStyle, gcs: gcs, projectID: values["project_id"], now: time.Now,
	}, nil
}

func (c *s3Client) ListBuckets(ctx context.Context) ([]string, error) {
	request, err := c.request(ctx, http.MethodGet, "", "", nil, -1, "", nil)
	if err != nil {
		return nil, err
	}
	if c.gcs && c.projectID != "" {
		request.Header.Set("x-goog-project-id", c.projectID)
		c.sign(request)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("list cloud buckets: %w", err)
	}
	if !isSuccess(response.StatusCode) {
		return nil, responseError(response)
	}
	defer response.Body.Close()
	var payload struct {
		Buckets struct {
			Items []struct {
				Name string `xml:"Name"`
			} `xml:"Bucket"`
		} `xml:"Buckets"`
	}
	if err := xml.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode cloud bucket list: %w", err)
	}
	result := make([]string, 0, len(payload.Buckets.Items))
	for _, item := range payload.Buckets.Items {
		if item.Name != "" {
			result = append(result, item.Name)
		}
	}
	return result, nil
}

func (c *s3Client) ListPage(ctx context.Context, bucket, prefix, delimiter string, limit int, token string) (Page, error) {
	if err := validateBucket(bucket); err != nil {
		return Page{}, err
	}
	limit = normalizedLimit(limit, DefaultObjectLimit, MaxPageSize)
	query := url.Values{"list-type": {"2"}, "max-keys": {strconv.Itoa(limit)}}
	if prefix != "" {
		query.Set("prefix", prefix)
	}
	if delimiter != "" {
		query.Set("delimiter", delimiter)
	}
	if token != "" {
		query.Set("continuation-token", token)
	}
	request, err := c.request(ctx, http.MethodGet, bucket, "", nil, -1, "", query)
	if err != nil {
		return Page{}, err
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return Page{}, fmt.Errorf("list cloud objects: %w", err)
	}
	if !isSuccess(response.StatusCode) {
		return Page{}, responseError(response)
	}
	defer response.Body.Close()
	var payload struct {
		Contents []struct {
			Key          string `xml:"Key"`
			Size         int64  `xml:"Size"`
			LastModified string `xml:"LastModified"`
		} `xml:"Contents"`
		CommonPrefixes []struct {
			Prefix string `xml:"Prefix"`
		} `xml:"CommonPrefixes"`
		IsTruncated bool   `xml:"IsTruncated"`
		NextToken   string `xml:"NextContinuationToken"`
	}
	if err := xml.NewDecoder(io.LimitReader(response.Body, 32<<20)).Decode(&payload); err != nil {
		return Page{}, fmt.Errorf("decode cloud object list: %w", err)
	}
	page := Page{Objects: make([]Object, 0, len(payload.Contents)), Prefixes: make([]string, 0, len(payload.CommonPrefixes))}
	for _, item := range payload.Contents {
		page.Objects = append(page.Objects, Object{Key: item.Key, Size: item.Size, LastModified: item.LastModified})
	}
	for _, item := range payload.CommonPrefixes {
		if item.Prefix != "" {
			page.Prefixes = append(page.Prefixes, item.Prefix)
		}
	}
	if payload.IsTruncated {
		page.NextToken = payload.NextToken
	}
	return page, nil
}

func (c *s3Client) HeadObject(ctx context.Context, bucket, key string) (ObjectInfo, error) {
	request, err := c.request(ctx, http.MethodHead, bucket, key, nil, -1, "", nil)
	if err != nil {
		return ObjectInfo{}, err
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("inspect cloud object: %w", err)
	}
	if !isSuccess(response.StatusCode) {
		return ObjectInfo{}, responseError(response)
	}
	response.Body.Close()
	return ObjectInfo{Size: response.ContentLength, ContentType: response.Header.Get("Content-Type")}, nil
}

func (c *s3Client) OpenObject(ctx context.Context, bucket, key string) (io.ReadCloser, ObjectInfo, error) {
	request, err := c.request(ctx, http.MethodGet, bucket, key, nil, -1, "", nil)
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, ObjectInfo{}, fmt.Errorf("download cloud object: %w", err)
	}
	if !isSuccess(response.StatusCode) {
		return nil, ObjectInfo{}, responseError(response)
	}
	return response.Body, ObjectInfo{Size: response.ContentLength, ContentType: response.Header.Get("Content-Type")}, nil
}

func (c *s3Client) PutObject(ctx context.Context, bucket, key string, content io.Reader, size int64, contentType string) (int64, error) {
	if size < 0 {
		return 0, errors.New("cloud upload size must be known")
	}
	request, err := c.request(ctx, http.MethodPut, bucket, key, content, size, contentType, nil)
	if err != nil {
		return 0, err
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return 0, fmt.Errorf("upload cloud object: %w", err)
	}
	if !isSuccess(response.StatusCode) {
		return 0, responseError(response)
	}
	response.Body.Close()
	return size, nil
}

func (c *s3Client) request(ctx context.Context, method, bucket, key string, body io.Reader, size int64, contentType string, query url.Values) (*http.Request, error) {
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
	pathStyle := c.pathStyle || strings.Contains(bucket, ".")
	if pathStyle {
		if bucket != "" {
			escapedPath += "/" + awsEscape(bucket)
		}
	} else if bucket != "" {
		target.Host = bucket + "." + target.Host
	}
	if key != "" {
		escapedPath += "/" + escapeS3Key(key)
	} else if escapedPath == "" {
		escapedPath = "/"
	}
	if escapedPath == "" {
		escapedPath = "/"
	}
	decodedPath, err := url.PathUnescape(escapedPath)
	if err != nil {
		return nil, err
	}
	target.Path, target.RawPath = decodedPath, escapedPath
	target.RawQuery = canonicalQuery(query)
	request, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return nil, err
	}
	if size >= 0 {
		request.ContentLength = size
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	c.sign(request)
	return request, nil
}

func (c *s3Client) sign(request *http.Request) {
	now := c.now().UTC()
	amzDate, day := now.Format("20060102T150405Z"), now.Format("20060102")
	payloadHash := "UNSIGNED-PAYLOAD"
	request.Header.Set("x-amz-date", amzDate)
	request.Header.Set("x-amz-content-sha256", payloadHash)
	if c.sessionToken != "" {
		request.Header.Set("x-amz-security-token", c.sessionToken)
	}
	headerNames := []string{"host", "x-amz-content-sha256", "x-amz-date"}
	if c.sessionToken != "" {
		headerNames = append(headerNames, "x-amz-security-token")
	}
	sort.Strings(headerNames)
	var canonicalHeaders strings.Builder
	for _, name := range headerNames {
		value := request.Header.Get(name)
		if name == "host" {
			value = request.URL.Host
		}
		canonicalHeaders.WriteString(name)
		canonicalHeaders.WriteByte(':')
		canonicalHeaders.WriteString(strings.Join(strings.Fields(value), " "))
		canonicalHeaders.WriteByte('\n')
	}
	signedHeaders := strings.Join(headerNames, ";")
	canonicalRequest := strings.Join([]string{
		request.Method, request.URL.EscapedPath(), request.URL.RawQuery,
		canonicalHeaders.String(), signedHeaders, payloadHash,
	}, "\n")
	scope := day + "/" + c.region + "/s3/aws4_request"
	requestHash := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(requestHash[:])
	dateKey := hmacSHA256([]byte("AWS4"+c.secretKey), day)
	regionKey := hmacSHA256(dateKey, c.region)
	serviceKey := hmacSHA256(regionKey, "s3")
	signingKey := hmacSHA256(serviceKey, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))
	request.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+c.accessKey+"/"+scope+", SignedHeaders="+signedHeaders+", Signature="+signature)
}

func hmacSHA256(key []byte, value string) []byte {
	hash := hmac.New(sha256.New, key)
	_, _ = hash.Write([]byte(value))
	return hash.Sum(nil)
}

func canonicalQuery(values url.Values) string {
	if len(values) == 0 {
		return ""
	}
	type encodedPair struct{ key, value string }
	pairs := []encodedPair{}
	for key, rawItems := range values {
		items := append([]string(nil), rawItems...)
		if len(items) == 0 {
			items = []string{""}
		}
		for _, value := range items {
			pairs = append(pairs, encodedPair{key: awsEscape(key), value: awsEscape(value)})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].key == pairs[j].key {
			return pairs[i].value < pairs[j].value
		}
		return pairs[i].key < pairs[j].key
	})
	parts := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		parts = append(parts, pair.key+"="+pair.value)
	}
	return strings.Join(parts, "&")
}

func escapeS3Key(key string) string {
	parts := strings.Split(key, "/")
	for index := range parts {
		parts[index] = awsEscape(parts[index])
	}
	return strings.Join(parts, "/")
}

func awsEscape(value string) string {
	const hexDigits = "0123456789ABCDEF"
	var encoded strings.Builder
	encoded.Grow(len(value))
	for index := 0; index < len(value); index++ {
		valueByte := value[index]
		if valueByte >= 'A' && valueByte <= 'Z' || valueByte >= 'a' && valueByte <= 'z' ||
			valueByte >= '0' && valueByte <= '9' || valueByte == '-' || valueByte == '_' || valueByte == '.' || valueByte == '~' {
			encoded.WriteByte(valueByte)
			continue
		}
		encoded.WriteByte('%')
		encoded.WriteByte(hexDigits[valueByte>>4])
		encoded.WriteByte(hexDigits[valueByte&0x0f])
	}
	return encoded.String()
}

func validateBucket(bucket string) error {
	if bucket == "" || len(bucket) > 255 || strings.ContainsAny(bucket, "/\\\x00\r\n") {
		return errors.New("cloud bucket is invalid")
	}
	for index := 0; index < len(bucket); index++ {
		value := bucket[index]
		if value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' ||
			value == '.' || value == '_' || value == '-' {
			continue
		}
		return errors.New("cloud bucket contains an invalid character")
	}
	return nil
}

func validateObjectKey(key string) error {
	if key == "" || len(key) > 4096 || strings.ContainsAny(key, "\x00\r\n") {
		return errors.New("cloud object key is invalid")
	}
	return nil
}
