package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestP5WebSpeechSettingsUseEncryptedUserScopedVaultAndMigrateLegacy(t *testing.T) {
	root := t.TempDir()
	app := New(Options{FileRoot: root})
	apiKey := "openai-sensitive-test-key"
	candidate := map[string]any{
		"enabled":  true,
		"provider": "openai",
		"openai": map[string]any{
			"api_key":  apiKey,
			"base_url": "https://speech.example.test/v1",
			"model":    "gpt-4o-transcribe",
		},
	}
	body, err := json.Marshal(map[string]any{webSpeechSettingKey: candidate})
	if err != nil {
		t.Fatal(err)
	}
	put := httptest.NewRequest(http.MethodPut, "/api/settings/client", bytes.NewReader(body))
	put.RemoteAddr = "127.0.0.1:12345"
	putResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(putResponse, put)
	if putResponse.Code != http.StatusOK {
		t.Fatalf("put status=%d body=%s", putResponse.Code, putResponse.Body.String())
	}

	assertFileDoesNotContain(t, filepath.Join(root, "settings.json"), apiKey)
	assertFileDoesNotContain(t, filepath.Join(root, "secrets", "vault.enc"), apiKey)
	secret, found, err := app.secretStore.ResolveForUser(webSpeechSecretID("local"), "local")
	if err != nil || !found || !strings.Contains(secret.Value, apiKey) {
		t.Fatalf("encrypted secret found=%v err=%v value=%q", found, err, secret.Value)
	}
	if _, found, err := app.secretStore.ResolveForUser(webSpeechSecretID("local"), "other-user"); err != nil || found {
		t.Fatalf("cross-user secret lookup found=%v err=%v", found, err)
	}

	get := httptest.NewRequest(http.MethodGet, "/api/settings/client?keys="+webSpeechSettingKey, nil)
	get.RemoteAddr = "127.0.0.1:12345"
	getResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(getResponse, get)
	if getResponse.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", getResponse.Code, getResponse.Body.String())
	}
	var restored map[string]any
	if err := json.Unmarshal(getResponse.Body.Bytes(), &restored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored[webSpeechSettingKey], candidate) {
		t.Fatalf("restored=%#v want=%#v", restored[webSpeechSettingKey], candidate)
	}

	if _, err := app.secretStore.DeleteForUser(webSpeechSecretID("local"), "local"); err != nil {
		t.Fatal(err)
	}
	legacy := map[string]any{
		"enabled": false,
		"openai":  map[string]any{"api_key": "legacy-sensitive-key", "model": "whisper-1"},
	}
	legacyKey := webClientSettingsPrefix("local") + webSpeechSettingKey
	if _, err := app.settingsStore.Set(legacyKey, legacy); err != nil {
		t.Fatal(err)
	}
	migrated, found, err := app.readWebSpeechConfig("local")
	if err != nil || !found || !reflect.DeepEqual(migrated, legacy) {
		t.Fatalf("migrated found=%v err=%v value=%#v", found, err, migrated)
	}
	if _, found, err := app.settingsStore.Get(legacyKey); err != nil || found {
		t.Fatalf("legacy plaintext remained found=%v err=%v", found, err)
	}
	assertFileDoesNotContain(t, filepath.Join(root, "settings.json"), "legacy-sensitive-key")
}

func TestP5WebSpeechOpenAIUsesRealMultipartProtocol(t *testing.T) {
	audio := testWAVAudio()
	fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/audio/transcriptions" {
			t.Errorf("request=%s %s", r.Method, r.URL.String())
			http.Error(w, "bad route", http.StatusBadRequest)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer openai-test-key" {
			t.Errorf("authorization=%q", got)
		}
		if err := r.ParseMultipartForm(1024 * 1024); err != nil {
			t.Errorf("parse multipart: %v", err)
			http.Error(w, "bad multipart", http.StatusBadRequest)
			return
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			t.Errorf("open file: %v", err)
			http.Error(w, "missing file", http.StatusBadRequest)
			return
		}
		defer file.Close()
		received, _ := io.ReadAll(file)
		if !bytes.Equal(received, audio) {
			t.Errorf("audio=%x want=%x", received, audio)
		}
		if r.FormValue("model") != "gpt-4o-mini-transcribe" || r.FormValue("language") != "zh" || r.FormValue("prompt") != "普通话" {
			t.Errorf("model=%q language=%q prompt=%q", r.FormValue("model"), r.FormValue("language"), r.FormValue("prompt"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"text":"协议已打通","language":"zh"}`)
	}))
	defer fixture.Close()

	app := New(Options{FileRoot: t.TempDir(), HTTPClient: fixture.Client()})
	app.speechClientForURL = fixtureSpeechClientFactory(t, fixture)
	if err := app.writeWebSpeechConfig("local", map[string]any{
		"enabled": true, "provider": "openai",
		"openai": map[string]any{
			"api_key": "openai-test-key", "base_url": fixture.URL + "/v1",
			"model": "gpt-4o-mini-transcribe", "language": "zh-CN", "prompt": "普通话",
		},
	}); err != nil {
		t.Fatal(err)
	}
	response := performWebSpeechRequest(t, app, audio, "sample.wav", "audio/wav", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"provider":"openai"`) || !strings.Contains(response.Body.String(), "协议已打通") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestP5WebSpeechDeepgramUsesRealRawAudioProtocol(t *testing.T) {
	audio := testWAVAudio()
	fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/listen" {
			t.Errorf("request=%s %s", r.Method, r.URL.String())
			http.Error(w, "bad route", http.StatusBadRequest)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Token deepgram-test-key" {
			t.Errorf("authorization=%q", got)
		}
		if r.Header.Get("Content-Type") != "audio/wav" {
			t.Errorf("content-type=%q", r.Header.Get("Content-Type"))
		}
		query := r.URL.Query()
		if query.Get("model") != "nova-3" || query.Get("language") != "zh-CN" || query.Get("punctuate") != "true" || query.Get("smart_format") != "false" {
			t.Errorf("query=%v", query)
		}
		received, _ := io.ReadAll(r.Body)
		if !bytes.Equal(received, audio) {
			t.Errorf("audio=%x want=%x", received, audio)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"results":{"channels":[{"alternatives":[{"transcript":"真实协议返回"}]}]}}`)
	}))
	defer fixture.Close()

	app := New(Options{FileRoot: t.TempDir(), HTTPClient: fixture.Client()})
	app.speechClientForURL = fixtureSpeechClientFactory(t, fixture)
	if err := app.writeWebSpeechConfig("local", map[string]any{
		"enabled": true, "provider": "deepgram",
		"deepgram": map[string]any{
			"api_key": "deepgram-test-key", "base_url": fixture.URL + "/v1", "model": "nova-3",
			"language": "zh-CN", "punctuate": true, "smartFormat": false,
		},
	}); err != nil {
		t.Fatal(err)
	}
	response := performWebSpeechRequest(t, app, audio, "sample.wav", "audio/wav", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"provider":"deepgram"`) || !strings.Contains(response.Body.String(), "真实协议返回") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestP5WebSpeechRejectsUnsafeEndpointsUnsupportedAudioAndOversizedResponses(t *testing.T) {
	app := New(Options{FileRoot: t.TempDir()})
	if err := app.writeWebSpeechConfig("local", map[string]any{
		"enabled": false, "provider": localSpeechProvider,
		"local": map[string]any{"language": "zh-CN", "model": localSpeechModelName},
	}); err != nil {
		t.Fatal(err)
	}
	disabled := performWebSpeechRequest(t, app, testWAVAudio(), "sample.wav", "audio/wav", "")
	assertWebSpeechError(t, disabled, http.StatusBadRequest, "STT_DISABLED")

	if err := app.writeWebSpeechConfig("local", map[string]any{
		"enabled": true, "provider": "openai",
		"openai": map[string]any{"api_key": "", "base_url": "https://127.0.0.1:444/v1", "model": "whisper-1"},
	}); err != nil {
		t.Fatal(err)
	}
	unsafe := performWebSpeechRequest(t, app, testWAVAudio(), "sample.wav", "audio/wav", "")
	assertWebSpeechError(t, unsafe, http.StatusBadRequest, "STT_INVALID_ENDPOINT")

	unsupported := performWebSpeechRequest(t, app, []byte("not-an-audio-file"), "sample.wav", "audio/wav", "")
	assertWebSpeechError(t, unsupported, http.StatusUnsupportedMediaType, "STT_UNSUPPORTED_AUDIO")

	_, oversizedErr := validateWebSpeechFileHeader(&multipart.FileHeader{
		Filename: "large.wav", Size: maxWebSpeechAudioBytes + 1,
	}, "large.wav", "audio/wav")
	if oversizedErr == nil || oversizedErr.Status != http.StatusRequestEntityTooLarge || oversizedErr.Code != "STT_FILE_TOO_LARGE" {
		t.Fatalf("oversized error=%#v", oversizedErr)
	}

	fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(bytes.Repeat([]byte("x"), maxWebSpeechResponseBytes+1))
	}))
	defer fixture.Close()
	bounded := New(Options{FileRoot: t.TempDir(), HTTPClient: fixture.Client()})
	bounded.speechClientForURL = fixtureSpeechClientFactory(t, fixture)
	if err := bounded.writeWebSpeechConfig("local", map[string]any{
		"enabled": true, "provider": "openai",
		"openai": map[string]any{"api_key": "test", "base_url": fixture.URL, "model": "whisper-1"},
	}); err != nil {
		t.Fatal(err)
	}
	tooLarge := performWebSpeechRequest(t, bounded, testWAVAudio(), "sample.wav", "audio/wav", "")
	assertWebSpeechError(t, tooLarge, http.StatusBadGateway, "STT_UPSTREAM_ERROR")
}

func TestParseWebSpeechAudioRemovesMultipartTempFileOnClose(t *testing.T) {
	audio := append(append([]byte(nil), testWAVAudio()...), bytes.Repeat([]byte{0}, 300*1024)...)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="file"; filename="sample.wav"`)
	header.Set("Content-Type", "audio/wav")
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(audio); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("fileName", "sample.wav"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("mimeType", "audio/wav"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/stt", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	input, parseErr := parseWebSpeechAudio(httptest.NewRecorder(), request)
	if parseErr != nil {
		t.Fatalf("parse audio: %#v", parseErr)
	}
	if input == nil {
		t.Fatal("parse audio returned nil input")
	}
	osFile, ok := input.File.(*os.File)
	if !ok {
		_ = input.Close()
		t.Fatalf("multipart file type=%T, want *os.File spill file", input.File)
	}
	spilledPath := osFile.Name()
	if _, err := os.Stat(spilledPath); err != nil {
		t.Fatalf("multipart spill file was not created: %v", err)
	}
	if err := input.Close(); err != nil {
		t.Fatalf("close multipart input: %v", err)
	}
	if _, err := os.Stat(spilledPath); !os.IsNotExist(err) {
		t.Fatalf("multipart spill file remained after close: path=%s err=%v", spilledPath, err)
	}
}

func fixtureSpeechClientFactory(t *testing.T, fixture *httptest.Server) func(context.Context, string, *http.Client) (*http.Client, error) {
	t.Helper()
	return func(_ context.Context, endpoint string, _ *http.Client) (*http.Client, error) {
		if !strings.HasPrefix(endpoint, fixture.URL+"/") {
			return nil, io.ErrUnexpectedEOF
		}
		return fixture.Client(), nil
	}
}

func performWebSpeechRequest(t *testing.T, app *Server, audio []byte, fileName, mimeType, language string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="file"; filename="`+fileName+`"`)
	header.Set("Content-Type", mimeType)
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(audio); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("fileName", fileName); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("mimeType", mimeType); err != nil {
		t.Fatal(err)
	}
	if language != "" {
		if err := writer.WriteField("languageHint", language); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/stt", &body)
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	return response
}

func testWAVAudio() []byte {
	return []byte{
		'R', 'I', 'F', 'F', 36, 0, 0, 0, 'W', 'A', 'V', 'E',
		'f', 'm', 't', ' ', 16, 0, 0, 0, 1, 0, 1, 0,
		0x40, 0x1f, 0, 0, 0x80, 0x3e, 0, 0, 2, 0, 16, 0,
		'd', 'a', 't', 'a', 0, 0, 0, 0,
	}
}

func assertWebSpeechError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status || !strings.Contains(response.Body.String(), `"code":"`+code+`"`) {
		t.Fatalf("status=%d body=%s want status=%d code=%s", response.Code, response.Body.String(), status, code)
	}
}

func assertFileDoesNotContain(t *testing.T, path, secret string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(secret)) {
		t.Fatalf("%s contains plaintext secret", path)
	}
}
