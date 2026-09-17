package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"synon-go/internal/mcpdirectory"
	secretstore "synon-go/internal/persistence/secrets"
)

const (
	webSpeechSettingKey                = "tools.speechToText"
	webSpeechSecretPrefix              = "web-speech-"
	maxWebSpeechConfigBytes            = 64 * 1024
	maxWebSpeechAudioBytes       int64 = 30 * 1024 * 1024
	maxWebSpeechRequestBytes     int64 = maxWebSpeechAudioBytes + 128*1024
	maxWebSpeechResponseBytes          = 1024 * 1024
	webSpeechRequestTimeout            = 120 * time.Second
	defaultOpenAISpeechBaseURL         = "https://api.openai.com/v1"
	defaultOpenAISpeechModel           = "gpt-4o-transcribe"
	defaultDeepgramSpeechBaseURL       = "https://api.deepgram.com/v1"
	defaultDeepgramSpeechModel         = "nova-3"
)

type webSpeechConfig struct {
	AutoSend *bool                    `json:"autoSend,omitempty"`
	Enabled  bool                     `json:"enabled"`
	Provider string                   `json:"provider,omitempty"`
	OpenAI   *webOpenAISpeechConfig   `json:"openai,omitempty"`
	Deepgram *webDeepgramSpeechConfig `json:"deepgram,omitempty"`
	Local    *webLocalSpeechConfig    `json:"local,omitempty"`
}

type webOpenAISpeechConfig struct {
	APIKey      string   `json:"api_key"`
	BaseURL     string   `json:"base_url,omitempty"`
	Language    string   `json:"language,omitempty"`
	Model       string   `json:"model"`
	Prompt      string   `json:"prompt,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
}

type webDeepgramSpeechConfig struct {
	APIKey         string `json:"api_key"`
	BaseURL        string `json:"base_url,omitempty"`
	DetectLanguage *bool  `json:"detectLanguage,omitempty"`
	Language       string `json:"language,omitempty"`
	Model          string `json:"model"`
	Punctuate      *bool  `json:"punctuate,omitempty"`
	SmartFormat    *bool  `json:"smartFormat,omitempty"`
}

type webLocalSpeechConfig struct {
	Language string `json:"language,omitempty"`
	Model    string `json:"model,omitempty"`
}

func defaultLocalWebSpeechConfig() webSpeechConfig {
	return webSpeechConfig{
		Enabled:  true,
		Provider: localSpeechProvider,
		Local: &webLocalSpeechConfig{
			Language: "zh-CN",
			Model:    localSpeechModelName,
		},
	}
}

// resolveWebSpeechRuntimeConfig keeps local speech usable after the advanced
// provider settings UI is removed. A user who has never saved speech settings
// gets the privacy-preserving local provider; an explicitly saved setting,
// including an explicit disabled choice, remains authoritative.
func (s *Server) resolveWebSpeechRuntimeConfig(userID string) (webSpeechConfig, *webSpeechAPIError) {
	value, found, err := s.readWebSpeechConfig(userID)
	if err != nil {
		return webSpeechConfig{}, newWebSpeechError(
			http.StatusInternalServerError,
			"STT_STORAGE_ERROR",
			"speech-to-text configuration is unavailable",
		)
	}
	if !found {
		return defaultLocalWebSpeechConfig(), nil
	}
	_, config, err := decodeWebSpeechConfigSetting(value)
	if err != nil {
		return webSpeechConfig{}, newWebSpeechError(
			http.StatusBadRequest,
			"STT_INVALID_CONFIG",
			"speech-to-text configuration is invalid",
		)
	}
	if !config.Enabled {
		return webSpeechConfig{}, newWebSpeechError(
			http.StatusBadRequest,
			"STT_DISABLED",
			"speech-to-text is not enabled",
		)
	}
	return config, nil
}

type webSpeechAudioInput struct {
	File         multipart.File
	FileName     string
	MIMEType     string
	LanguageHint string
	Size         int64
	form         *multipart.Form
}

func (input *webSpeechAudioInput) Close() error {
	if input == nil {
		return nil
	}
	var closeErr error
	if input.File != nil {
		closeErr = errors.Join(closeErr, input.File.Close())
		input.File = nil
	}
	if input.form != nil {
		closeErr = errors.Join(closeErr, input.form.RemoveAll())
		input.form = nil
	}
	return closeErr
}

type webSpeechResult struct {
	Language string `json:"language,omitempty"`
	Model    string `json:"model"`
	Provider string `json:"provider"`
	Text     string `json:"text"`
}

type webSpeechAPIError struct {
	Status  int
	Code    string
	Message string
}

func (err *webSpeechAPIError) Error() string {
	if err == nil {
		return ""
	}
	return err.Code
}

func newWebSpeechError(status int, code, message string) *webSpeechAPIError {
	return &webSpeechAPIError{Status: status, Code: code, Message: message}
}

func (s *Server) handleWebSpeechToText(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeWebSpeechError(w, newWebSpeechError(http.StatusMethodNotAllowed, "STT_METHOD_NOT_ALLOWED", "speech-to-text requires POST"))
		return
	}
	userID := strings.TrimSpace(r.Header.Get("X-Synon-User-Id"))
	if userID == "" {
		writeWebSpeechError(w, newWebSpeechError(http.StatusUnauthorized, "STT_AUTH_REQUIRED", "authentication is required"))
		return
	}
	config, configErr := s.resolveWebSpeechRuntimeConfig(userID)
	if configErr != nil {
		writeWebSpeechError(w, configErr)
		return
	}

	input, parseErr := parseWebSpeechAudio(w, r)
	if parseErr != nil {
		writeWebSpeechError(w, parseErr)
		return
	}
	defer func() { _ = input.Close() }()

	ctx, cancel := context.WithTimeout(r.Context(), webSpeechRequestTimeout)
	defer cancel()
	var result webSpeechResult
	switch config.Provider {
	case "openai":
		result, parseErr = s.transcribeOpenAISpeech(ctx, config.OpenAI, input)
	case "deepgram":
		result, parseErr = s.transcribeDeepgramSpeech(ctx, config.Deepgram, input)
	case localSpeechProvider:
		result, parseErr = s.transcribeLocalSpeech(ctx, config.Local, input)
	default:
		parseErr = newWebSpeechError(http.StatusBadRequest, "STT_INVALID_CONFIG", "speech-to-text provider is invalid")
	}
	if cleanupErr := input.Close(); cleanupErr != nil {
		writeWebSpeechError(w, newWebSpeechError(http.StatusInternalServerError, "STT_AUDIO_CLEANUP_FAILED", "temporary speech audio cleanup failed"))
		return
	}
	if parseErr != nil {
		writeWebSpeechError(w, parseErr)
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"success": true, "data": result})
}

func writeWebSpeechError(w http.ResponseWriter, speechErr *webSpeechAPIError) {
	if speechErr == nil {
		speechErr = newWebSpeechError(http.StatusInternalServerError, "STT_INTERNAL_ERROR", "speech-to-text failed")
	}
	writeWorkspaceJSON(w, speechErr.Status, map[string]any{
		"success": false,
		"code":    speechErr.Code,
		"error":   speechErr.Message,
	})
}

func decodeWebSpeechConfigSetting(value any) ([]byte, webSpeechConfig, error) {
	raw, object, err := marshalWebSpeechSettingObject(value)
	if err != nil {
		return nil, webSpeechConfig{}, err
	}
	_ = object
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var config webSpeechConfig
	if err := decoder.Decode(&config); err != nil {
		return nil, webSpeechConfig{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, webSpeechConfig{}, errors.New("speech configuration must contain one JSON object")
	}
	config.Provider = strings.ToLower(strings.TrimSpace(config.Provider))
	if config.Provider == "" {
		config.Provider = "openai"
	}
	if config.Provider != "openai" && config.Provider != "deepgram" && config.Provider != localSpeechProvider {
		return nil, webSpeechConfig{}, errors.New("speech provider must be openai, deepgram, or local")
	}
	if err := validateOpenAISpeechConfig(config.OpenAI); err != nil {
		return nil, webSpeechConfig{}, err
	}
	if err := validateDeepgramSpeechConfig(config.Deepgram); err != nil {
		return nil, webSpeechConfig{}, err
	}
	if err := validateLocalSpeechConfig(config.Local); err != nil {
		return nil, webSpeechConfig{}, err
	}
	return raw, config, nil
}

func marshalWebSpeechSettingObject(value any) ([]byte, map[string]any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, nil, err
	}
	if len(raw) == 0 || len(raw) > maxWebSpeechConfigBytes {
		return nil, nil, errors.New("speech configuration exceeds the size limit")
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, nil, errors.New("speech configuration must be a JSON object")
	}
	return raw, object, nil
}

func validateOpenAISpeechConfig(config *webOpenAISpeechConfig) error {
	if config == nil {
		return nil
	}
	if err := validateSpeechSecret(config.APIKey, 16*1024); err != nil {
		return fmt.Errorf("openai api key: %w", err)
	}
	if err := validateSpeechBaseURL(config.BaseURL); err != nil {
		return fmt.Errorf("openai base URL: %w", err)
	}
	if err := validateSpeechToken(config.Model, 256); err != nil {
		return fmt.Errorf("openai model: %w", err)
	}
	if err := validateSpeechLanguage(config.Language); err != nil {
		return fmt.Errorf("openai language: %w", err)
	}
	if !utf8.ValidString(config.Prompt) || len(config.Prompt) > 4096 || strings.IndexByte(config.Prompt, 0) >= 0 {
		return errors.New("openai prompt is invalid or too long")
	}
	if config.Temperature != nil && (*config.Temperature < 0 || *config.Temperature > 1) {
		return errors.New("openai temperature must be between 0 and 1")
	}
	return nil
}

func validateDeepgramSpeechConfig(config *webDeepgramSpeechConfig) error {
	if config == nil {
		return nil
	}
	if err := validateSpeechSecret(config.APIKey, 16*1024); err != nil {
		return fmt.Errorf("deepgram api key: %w", err)
	}
	if err := validateSpeechBaseURL(config.BaseURL); err != nil {
		return fmt.Errorf("deepgram base URL: %w", err)
	}
	if err := validateSpeechToken(config.Model, 256); err != nil {
		return fmt.Errorf("deepgram model: %w", err)
	}
	if err := validateSpeechLanguage(config.Language); err != nil {
		return fmt.Errorf("deepgram language: %w", err)
	}
	return nil
}

func validateLocalSpeechConfig(config *webLocalSpeechConfig) error {
	if config == nil {
		return nil
	}
	if err := validateSpeechLanguage(config.Language); err != nil {
		return fmt.Errorf("local language: %w", err)
	}
	model := strings.TrimSpace(config.Model)
	if model != "" && model != localSpeechModelName {
		return errors.New("local model is not supported")
	}
	return nil
}

func validateSpeechSecret(value string, limit int) error {
	if !utf8.ValidString(value) || len(value) > limit {
		return errors.New("value is invalid or too long")
	}
	for _, r := range value {
		if unicode.IsControl(r) || r == '\u007f' {
			return errors.New("value contains control characters")
		}
	}
	return nil
}

func validateSpeechToken(value string, limit int) error {
	if !utf8.ValidString(value) || len(value) > limit {
		return errors.New("value is invalid or too long")
	}
	for _, r := range value {
		if unicode.IsControl(r) || r == '\u007f' {
			return errors.New("value contains control characters")
		}
	}
	return nil
}

func validateSpeechLanguage(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if len(value) > 64 || !utf8.ValidString(value) {
		return errors.New("language is invalid or too long")
	}
	for index, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || ((r == '-' || r == '_') && index > 0) {
			continue
		}
		return errors.New("language must be a BCP-47 style tag")
	}
	return nil
}

func validateSpeechBaseURL(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if len(value) > 2048 || !utf8.ValidString(value) {
		return errors.New("URL is invalid or too long")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.Opaque != "" || parsed.User != nil || parsed.Fragment != "" || parsed.RawQuery != "" {
		return errors.New("URL must be an absolute HTTP or HTTPS base URL without credentials, query, or fragment")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("URL scheme must be HTTP or HTTPS")
	}
	return nil
}

func webSpeechSecretID(userID string) string {
	digest := sha256String(strings.TrimSpace(userID))
	return webSpeechSecretPrefix + digest[:32]
}

func sha256String(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func (s *Server) writeWebSpeechConfig(userID string, value any) error {
	legacyKey := webClientSettingsPrefix(userID) + webSpeechSettingKey
	if value == nil {
		var deleteErrors []error
		if s.settingsStore != nil {
			if _, err := s.settingsStore.Delete(legacyKey); err != nil {
				deleteErrors = append(deleteErrors, err)
			}
		}
		if s.secretStore != nil {
			if _, err := s.secretStore.DeleteForUser(webSpeechSecretID(userID), userID); err != nil {
				deleteErrors = append(deleteErrors, err)
			}
		}
		return errors.Join(deleteErrors...)
	}
	raw, _, err := decodeWebSpeechConfigSetting(value)
	if err != nil {
		return err
	}
	if s.secretStore == nil {
		return errors.New("secret vault is not configured")
	}
	if err := s.persistWebSpeechSecret(userID, raw); err != nil {
		return err
	}
	if s.settingsStore != nil {
		if _, err := s.settingsStore.Delete(legacyKey); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) persistWebSpeechSecret(userID string, raw []byte) error {
	if s.secretStore == nil {
		return errors.New("secret vault is not configured")
	}
	id := webSpeechSecretID(userID)
	_, found, err := s.secretStore.ResolveForUser(id, userID)
	if err != nil {
		return err
	}
	update := func(secret *secretstore.Secret) error {
		secret.Provider = "generic"
		secret.Name = "Web speech-to-text configuration"
		secret.Value = string(raw)
		return nil
	}
	if found {
		_, err = s.secretStore.UpdateForUser(id, userID, update)
		return err
	}
	_, err = s.secretStore.Create(secretstore.Secret{
		ID:       id,
		UserID:   userID,
		Provider: "generic",
		Name:     "Web speech-to-text configuration",
		Value:    string(raw),
	})
	if err == nil {
		return nil
	}
	// A concurrent first write may have won between Resolve and Create.
	if _, updateErr := s.secretStore.UpdateForUser(id, userID, update); updateErr == nil {
		return nil
	}
	return err
}

func (s *Server) readWebSpeechConfig(userID string) (map[string]any, bool, error) {
	id := webSpeechSecretID(userID)
	if s.secretStore != nil {
		secret, found, err := s.secretStore.ResolveForUser(id, userID)
		if err != nil {
			return nil, false, err
		}
		if found {
			_, object, err := marshalWebSpeechSettingObject(json.RawMessage(secret.Value))
			return object, err == nil, err
		}
	}
	if s.settingsStore == nil {
		return nil, false, nil
	}
	legacyKey := webClientSettingsPrefix(userID) + webSpeechSettingKey
	legacy, found, err := s.settingsStore.Get(legacyKey)
	if err != nil || !found {
		return nil, false, err
	}
	if s.secretStore == nil {
		return nil, false, errors.New("secret vault is not configured for legacy speech settings migration")
	}
	raw, object, err := marshalWebSpeechSettingObject(legacy.Value)
	if err != nil {
		return nil, false, err
	}
	if err := s.persistWebSpeechSecret(userID, raw); err != nil {
		return nil, false, err
	}
	if _, err := s.settingsStore.Delete(legacyKey); err != nil {
		return nil, false, err
	}
	return object, true, nil
}

func parseWebSpeechAudio(w http.ResponseWriter, r *http.Request) (*webSpeechAudioInput, *webSpeechAPIError) {
	r.Body = http.MaxBytesReader(w, r.Body, maxWebSpeechRequestBytes)
	formOwned := false
	defer func() {
		if !formOwned && r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()
	if err := r.ParseMultipartForm(256 * 1024); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, newWebSpeechError(http.StatusRequestEntityTooLarge, "STT_FILE_TOO_LARGE", "audio exceeds the 30 MiB limit")
		}
		return nil, newWebSpeechError(http.StatusBadRequest, "STT_INVALID_REQUEST", "speech-to-text requires bounded multipart audio")
	}
	form := r.MultipartForm
	cleanup := func() {
		if form != nil {
			_ = form.RemoveAll()
		}
	}
	if form == nil || len(form.File) != 1 || len(form.File["file"]) != 1 {
		cleanup()
		return nil, newWebSpeechError(http.StatusBadRequest, "STT_INVALID_REQUEST", "multipart field file is required exactly once")
	}
	for key := range form.File {
		if key != "file" {
			cleanup()
			return nil, newWebSpeechError(http.StatusBadRequest, "STT_INVALID_REQUEST", "unexpected multipart file field")
		}
	}
	for key := range form.Value {
		if key != "fileName" && key != "mimeType" && key != "languageHint" {
			cleanup()
			return nil, newWebSpeechError(http.StatusBadRequest, "STT_INVALID_REQUEST", "unexpected multipart value field")
		}
	}
	fileName, ok := singleWebSpeechFormValue(form.Value, "fileName", true)
	if !ok {
		cleanup()
		return nil, newWebSpeechError(http.StatusBadRequest, "STT_INVALID_REQUEST", "multipart field fileName is required exactly once")
	}
	mimeType, ok := singleWebSpeechFormValue(form.Value, "mimeType", true)
	if !ok {
		cleanup()
		return nil, newWebSpeechError(http.StatusBadRequest, "STT_INVALID_REQUEST", "multipart field mimeType is required exactly once")
	}
	languageHint, ok := singleWebSpeechFormValue(form.Value, "languageHint", false)
	if !ok {
		cleanup()
		return nil, newWebSpeechError(http.StatusBadRequest, "STT_INVALID_REQUEST", "multipart field languageHint may appear at most once")
	}
	if err := validateSpeechLanguage(languageHint); err != nil {
		cleanup()
		return nil, newWebSpeechError(http.StatusBadRequest, "STT_INVALID_REQUEST", "language hint is invalid")
	}
	header := form.File["file"][0]
	mediaType, validationErr := validateWebSpeechFileHeader(header, fileName, mimeType)
	if validationErr != nil {
		cleanup()
		return nil, validationErr
	}
	file, err := header.Open()
	if err != nil {
		cleanup()
		return nil, newWebSpeechError(http.StatusBadRequest, "STT_INVALID_REQUEST", "audio file cannot be opened")
	}
	prefix := make([]byte, min(int64(512), header.Size))
	readCount, readErr := io.ReadFull(file, prefix)
	if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		_ = file.Close()
		cleanup()
		return nil, newWebSpeechError(http.StatusBadRequest, "STT_INVALID_REQUEST", "audio file cannot be read")
	}
	prefix = prefix[:readCount]
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		cleanup()
		return nil, newWebSpeechError(http.StatusBadRequest, "STT_INVALID_REQUEST", "audio file is not seekable")
	}
	if !webSpeechSignatureMatches(mediaType, prefix) {
		_ = file.Close()
		cleanup()
		return nil, newWebSpeechError(http.StatusUnsupportedMediaType, "STT_UNSUPPORTED_AUDIO", "audio content does not match its declared type")
	}
	formOwned = true
	return &webSpeechAudioInput{
		File:         file,
		FileName:     fileName,
		MIMEType:     mediaType,
		LanguageHint: strings.TrimSpace(languageHint),
		Size:         header.Size,
		form:         form,
	}, nil
}

func singleWebSpeechFormValue(values map[string][]string, key string, required bool) (string, bool) {
	items, found := values[key]
	if !found {
		return "", !required
	}
	if len(items) != 1 {
		return "", false
	}
	value := strings.TrimSpace(items[0])
	if required && value == "" {
		return "", false
	}
	return value, true
}

func validateWebSpeechFileHeader(header *multipart.FileHeader, fileName, rawMIMEType string) (string, *webSpeechAPIError) {
	if header == nil || header.Size <= 0 {
		return "", newWebSpeechError(http.StatusBadRequest, "STT_INVALID_REQUEST", "audio file must not be empty")
	}
	if header.Size > maxWebSpeechAudioBytes {
		return "", newWebSpeechError(http.StatusRequestEntityTooLarge, "STT_FILE_TOO_LARGE", "audio exceeds the 30 MiB limit")
	}
	if len(fileName) > 255 || fileName != filepath.Base(fileName) || strings.ContainsAny(fileName, "/\\") || fileName != header.Filename {
		return "", newWebSpeechError(http.StatusBadRequest, "STT_INVALID_REQUEST", "audio file name is invalid")
	}
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(rawMIMEType))
	if err != nil || !webSpeechAllowedMIMEType(mediaType) {
		return "", newWebSpeechError(http.StatusUnsupportedMediaType, "STT_UNSUPPORTED_AUDIO", "audio type is not supported")
	}
	return strings.ToLower(mediaType), nil
}

func webSpeechAllowedMIMEType(mediaType string) bool {
	switch strings.ToLower(mediaType) {
	case "audio/aac", "audio/flac", "audio/mp4", "audio/mpeg", "audio/ogg", "audio/wav", "audio/wave", "audio/webm", "audio/x-m4a":
		return true
	default:
		return false
	}
}

func webSpeechSignatureMatches(mediaType string, data []byte) bool {
	switch strings.ToLower(mediaType) {
	case "audio/wav", "audio/wave":
		return len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WAVE"))
	case "audio/webm":
		return len(data) >= 4 && bytes.Equal(data[:4], []byte{0x1a, 0x45, 0xdf, 0xa3})
	case "audio/ogg":
		return len(data) >= 4 && bytes.Equal(data[:4], []byte("OggS"))
	case "audio/flac":
		return len(data) >= 4 && bytes.Equal(data[:4], []byte("fLaC"))
	case "audio/mp4", "audio/x-m4a":
		return len(data) >= 12 && bytes.Equal(data[4:8], []byte("ftyp"))
	case "audio/mpeg":
		return (len(data) >= 3 && bytes.Equal(data[:3], []byte("ID3"))) ||
			(len(data) >= 2 && data[0] == 0xff && data[1]&0xe0 == 0xe0)
	case "audio/aac":
		return len(data) >= 2 && data[0] == 0xff && data[1]&0xf6 == 0xf0
	default:
		return false
	}
}

func (s *Server) transcribeOpenAISpeech(ctx context.Context, config *webOpenAISpeechConfig, input *webSpeechAudioInput) (webSpeechResult, *webSpeechAPIError) {
	if config == nil {
		return webSpeechResult{}, newWebSpeechError(http.StatusBadRequest, "STT_OPENAI_NOT_CONFIGURED", "OpenAI speech-to-text is not configured")
	}
	baseURL := strings.TrimSpace(config.BaseURL)
	apiKey := strings.TrimSpace(config.APIKey)
	if baseURL == "" && apiKey == "" {
		return webSpeechResult{}, newWebSpeechError(http.StatusBadRequest, "STT_OPENAI_NOT_CONFIGURED", "OpenAI speech-to-text is not configured")
	}
	endpoint, err := appendWebSpeechEndpoint(baseURL, defaultOpenAISpeechBaseURL, "/audio/transcriptions")
	if err != nil {
		return webSpeechResult{}, newWebSpeechError(http.StatusBadRequest, "STT_INVALID_ENDPOINT", "speech-to-text endpoint is invalid")
	}
	client, speechErr := s.webSpeechHTTPClient(ctx, endpoint)
	if speechErr != nil {
		return webSpeechResult{}, speechErr
	}
	model := strings.TrimSpace(config.Model)
	if model == "" {
		model = defaultOpenAISpeechModel
	}
	language := strings.TrimSpace(config.Language)
	if language == "" {
		language = input.LanguageHint
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, escapeMultipartFileName(input.FileName)))
	header.Set("Content-Type", input.MIMEType)
	part, err := writer.CreatePart(header)
	if err == nil {
		_, err = input.File.Seek(0, io.SeekStart)
	}
	if err == nil {
		var copied int64
		copied, err = io.Copy(part, io.LimitReader(input.File, maxWebSpeechAudioBytes+1))
		if err == nil && copied != input.Size {
			err = errors.New("audio size changed while preparing transcription")
		}
	}
	if err == nil {
		err = writer.WriteField("model", model)
	}
	if err == nil && language != "" {
		err = writer.WriteField("language", openAISpeechLanguage(language))
	}
	if err == nil && strings.TrimSpace(config.Prompt) != "" {
		err = writer.WriteField("prompt", config.Prompt)
	}
	if err == nil && config.Temperature != nil {
		err = writer.WriteField("temperature", strconv.FormatFloat(*config.Temperature, 'f', -1, 64))
	}
	if closeErr := writer.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return webSpeechResult{}, newWebSpeechError(http.StatusInternalServerError, "STT_INTERNAL_ERROR", "audio request could not be prepared")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body.Bytes()))
	if err != nil {
		return webSpeechResult{}, newWebSpeechError(http.StatusBadRequest, "STT_INVALID_ENDPOINT", "speech-to-text endpoint is invalid")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", writer.FormDataContentType())
	if apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}
	response, err := client.Do(request)
	if err != nil {
		return webSpeechResult{}, classifyWebSpeechUpstreamError(ctx, err)
	}
	defer response.Body.Close()
	raw, speechErr := readWebSpeechUpstreamResponse(response)
	if speechErr != nil {
		return webSpeechResult{}, speechErr
	}
	var payload struct {
		Language string `json:"language"`
		Text     string `json:"text"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return webSpeechResult{}, newWebSpeechError(http.StatusBadGateway, "STT_UPSTREAM_ERROR", "speech provider returned an invalid response")
	}
	if strings.TrimSpace(payload.Language) != "" {
		language = strings.TrimSpace(payload.Language)
	}
	return webSpeechResult{Language: language, Model: model, Provider: "openai", Text: payload.Text}, nil
}

func (s *Server) transcribeDeepgramSpeech(ctx context.Context, config *webDeepgramSpeechConfig, input *webSpeechAudioInput) (webSpeechResult, *webSpeechAPIError) {
	if config == nil || strings.TrimSpace(config.APIKey) == "" {
		return webSpeechResult{}, newWebSpeechError(http.StatusBadRequest, "STT_DEEPGRAM_NOT_CONFIGURED", "Deepgram speech-to-text is not configured")
	}
	endpoint, err := appendWebSpeechEndpoint(strings.TrimSpace(config.BaseURL), defaultDeepgramSpeechBaseURL, "/listen")
	if err != nil {
		return webSpeechResult{}, newWebSpeechError(http.StatusBadRequest, "STT_INVALID_ENDPOINT", "speech-to-text endpoint is invalid")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return webSpeechResult{}, newWebSpeechError(http.StatusBadRequest, "STT_INVALID_ENDPOINT", "speech-to-text endpoint is invalid")
	}
	model := strings.TrimSpace(config.Model)
	if model == "" {
		model = defaultDeepgramSpeechModel
	}
	language := strings.TrimSpace(config.Language)
	if language == "" {
		language = input.LanguageHint
	}
	query := parsed.Query()
	query.Set("model", model)
	if language != "" {
		query.Set("language", language)
	} else if webSpeechBool(config.DetectLanguage, true) {
		query.Set("detect_language", "true")
	}
	query.Set("punctuate", strconv.FormatBool(webSpeechBool(config.Punctuate, true)))
	query.Set("smart_format", strconv.FormatBool(webSpeechBool(config.SmartFormat, true)))
	parsed.RawQuery = query.Encode()
	endpoint = parsed.String()
	client, speechErr := s.webSpeechHTTPClient(ctx, endpoint)
	if speechErr != nil {
		return webSpeechResult{}, speechErr
	}
	if _, err := input.File.Seek(0, io.SeekStart); err != nil {
		return webSpeechResult{}, newWebSpeechError(http.StatusInternalServerError, "STT_INTERNAL_ERROR", "audio request could not be prepared")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, io.LimitReader(input.File, input.Size))
	if err != nil {
		return webSpeechResult{}, newWebSpeechError(http.StatusBadRequest, "STT_INVALID_ENDPOINT", "speech-to-text endpoint is invalid")
	}
	request.ContentLength = input.Size
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Token "+strings.TrimSpace(config.APIKey))
	request.Header.Set("Content-Type", input.MIMEType)
	response, err := client.Do(request)
	if err != nil {
		return webSpeechResult{}, classifyWebSpeechUpstreamError(ctx, err)
	}
	defer response.Body.Close()
	raw, speechErr := readWebSpeechUpstreamResponse(response)
	if speechErr != nil {
		return webSpeechResult{}, speechErr
	}
	var payload struct {
		Results struct {
			Channels []struct {
				Alternatives []struct {
					Transcript string `json:"transcript"`
				} `json:"alternatives"`
			} `json:"channels"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || len(payload.Results.Channels) == 0 || len(payload.Results.Channels[0].Alternatives) == 0 {
		return webSpeechResult{}, newWebSpeechError(http.StatusBadGateway, "STT_UPSTREAM_ERROR", "speech provider returned an invalid response")
	}
	return webSpeechResult{
		Language: language,
		Model:    model,
		Provider: "deepgram",
		Text:     payload.Results.Channels[0].Alternatives[0].Transcript,
	}, nil
}

func (s *Server) webSpeechHTTPClient(ctx context.Context, endpoint string) (*http.Client, *webSpeechAPIError) {
	factory := s.speechClientForURL
	if factory == nil {
		factory = mcpdirectory.SecureHTTPClient
	}
	client, err := factory(ctx, endpoint, s.httpClient)
	if err != nil || client == nil {
		return nil, newWebSpeechError(http.StatusBadRequest, "STT_INVALID_ENDPOINT", "speech-to-text endpoint must be a public HTTPS destination")
	}
	return client, nil
}

func appendWebSpeechEndpoint(baseURL, defaultBaseURL, suffix string) (string, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || parsed.Opaque != "" || parsed.User != nil || parsed.Fragment != "" || parsed.RawQuery != "" {
		return "", errors.New("invalid speech endpoint")
	}
	path := strings.TrimRight(parsed.Path, "/")
	if !strings.HasSuffix(path, suffix) {
		path += suffix
	}
	parsed.Path = path
	return parsed.String(), nil
}

func escapeMultipartFileName(value string) string {
	value = strings.ReplaceAll(value, "\\", "_")
	return strings.ReplaceAll(value, `"`, "_")
}

func openAISpeechLanguage(value string) string {
	value = strings.TrimSpace(value)
	if separator := strings.IndexAny(value, "-_"); separator > 0 {
		return strings.ToLower(value[:separator])
	}
	return strings.ToLower(value)
}

func webSpeechBool(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

func readWebSpeechUpstreamResponse(response *http.Response) ([]byte, *webSpeechAPIError) {
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxWebSpeechResponseBytes+1))
	if err != nil || len(raw) > maxWebSpeechResponseBytes {
		return nil, newWebSpeechError(http.StatusBadGateway, "STT_UPSTREAM_ERROR", "speech provider returned an invalid response")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, newWebSpeechError(http.StatusBadGateway, "STT_UPSTREAM_ERROR", "speech provider rejected the transcription request")
	}
	return raw, nil
}

func classifyWebSpeechUpstreamError(ctx context.Context, err error) *webSpeechAPIError {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return newWebSpeechError(http.StatusGatewayTimeout, "STT_UPSTREAM_TIMEOUT", "speech provider timed out")
	}
	return newWebSpeechError(http.StatusBadGateway, "STT_UPSTREAM_ERROR", "speech provider could not be reached")
}
