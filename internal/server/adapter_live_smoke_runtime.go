package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	adapterwechat "synon-go/internal/adapters/wechat"
	"time"
)

func (s *Server) adapterDoctorPlatforms(smokeRequested bool) []map[string]any {
	platforms := s.adapterDiagnostics.Platforms
	if len(platforms) == 0 {
		platforms = []AdapterPlatformDiagnostics{
			{Name: "feishu", CredentialFields: map[string]bool{"app_id": false, "app_secret": false, "verification_token": false}},
			{Name: "wechat", CredentialFields: map[string]bool{"account_id": false, "bot_token": false, "user_id": false}},
		}
	}
	out := make([]map[string]any, 0, len(platforms))
	for _, platform := range platforms {
		fields := map[string]bool{}
		for key, value := range platform.CredentialFields {
			fields[key] = value
		}
		name := strings.TrimSpace(platform.Name)
		endpoint := strings.TrimSpace(platform.Endpoint)
		smokeResult := freshAdapterLiveDeliverySmokeAudit(s.latestAdapterLiveSmokeAudit(name), time.Now().UTC())
		if len(smokeResult) == 0 {
			smokeResult = s.adapterLiveSmokeReadiness(platform, smokeRequested)
		}
		out = append(out, map[string]any{
			"name":               name,
			"enabled":            platform.Enabled,
			"configured":         platform.Configured,
			"inboundConfigured":  platform.InboundConfigured,
			"outboundConfigured": platform.OutboundConfigured,
			"endpoint":           endpoint,
			"credential_fields":  fields,
			"smoke":              smokeResult,
		})
	}
	return out
}

func adapterLiveSmokeHasPass(platforms []map[string]any) bool {
	for _, platform := range platforms {
		if adapterLiveSmokePlatformPass(platform) {
			return true
		}
	}
	return false
}

func adapterCredentialSmokeHasPass(platforms []map[string]any) bool {
	for _, platform := range platforms {
		smoke := objectMapValue(platform["smoke"])
		if boolValue(platform["enabled"], false) && boolValue(platform["configured"], false) &&
			stringValue(smoke["status"]) == "pass" && boolValue(smoke["liveChecked"], false) {
			return true
		}
	}
	return false
}

func adapterLiveSmokeAllRequiredPass(platforms []map[string]any, required []string) bool {
	if len(required) == 0 {
		return false
	}
	passed := map[string]bool{}
	for _, platform := range platforms {
		name := strings.ToLower(strings.TrimSpace(stringValue(platform["name"])))
		if name == "" {
			continue
		}
		if adapterLiveSmokePlatformPass(platform) {
			passed[name] = true
		}
	}
	for _, name := range required {
		if !passed[strings.ToLower(strings.TrimSpace(name))] {
			return false
		}
	}
	return true
}

func adapterLiveSmokePlatformPass(platform map[string]any) bool {
	if !boolValue(platform["enabled"], false) || !boolValue(platform["configured"], false) {
		return false
	}
	smoke := objectMapValue(platform["smoke"])
	return stringValue(smoke["status"]) == "pass" &&
		boolValue(smoke["liveChecked"], false) &&
		boolValue(smoke["deliveryChecked"], false)
}

func freshAdapterLiveDeliverySmokeAudit(audit map[string]any, now time.Time) map[string]any {
	if len(audit) == 0 || !boolValue(audit["secretsRedacted"], false) {
		return nil
	}
	recordedAt, err := time.Parse(time.RFC3339Nano, stringValue(audit["recordedAt"]))
	if err != nil {
		return nil
	}
	age := now.Sub(recordedAt)
	if age < -5*time.Minute || age > adapterLiveSmokeAuditMaxAge {
		return nil
	}
	if age < 0 {
		age = 0
	}
	smoke := sanitizeAdapterSmokeMap(objectMapValue(audit["smoke"]))
	status := stringValue(smoke["status"])
	if (status != "pass" && status != "fail") ||
		!boolValue(smoke["liveChecked"], false) ||
		!boolValue(smoke["deliveryChecked"], false) ||
		stringValue(smoke["method"]) != "outbound_delivery" {
		return nil
	}
	smoke["auditRecordedAt"] = recordedAt.UTC().Format(time.RFC3339Nano)
	smoke["auditAgeSeconds"] = int64(age / time.Second)
	smoke["auditFresh"] = true
	smoke["auditMaxAgeSeconds"] = int64(adapterLiveSmokeAuditMaxAge / time.Second)
	return smoke
}

func (s *Server) adapterLiveSmokeReadiness(platform AdapterPlatformDiagnostics, requested bool) map[string]any {
	platformName := strings.TrimSpace(platform.Name)
	configured := platform.Configured
	endpointConfigured := strings.TrimSpace(platform.Endpoint) != ""
	status := "not_requested"
	message := "Live credential smoke was not requested."
	if requested {
		switch {
		case !configured:
			status = "skipped_missing_credentials"
			message = "Live credential smoke skipped because required platform credentials are incomplete."
		case !endpointConfigured && adapterSmokeRequiresEndpoint(platformName):
			status = "skipped_missing_endpoint"
			message = "Live credential smoke skipped because the platform endpoint is not configured."
		default:
			status = "requires_live_run"
			message = "Credentials and endpoint shape are ready; execute the platform live check in the deployment environment before declaring live delivery pass."
			if strings.EqualFold(platformName, "feishu") {
				if result := s.runFeishuAdapterSmoke(platform); len(result) > 0 {
					return result
				}
			}
			if strings.EqualFold(platformName, "wechat") {
				if result := s.runWeChatAdapterSmoke(platform); len(result) > 0 {
					return result
				}
			}
		}
	}
	return map[string]any{
		"status":             status,
		"message":            message,
		"requested":          requested,
		"method":             adapterSmokeMethod(platformName),
		"liveChecked":        false,
		"credentialComplete": configured,
		"endpointConfigured": endpointConfigured,
		"requiresNetwork":    configured && requested,
	}
}

func (s *Server) runWeChatAdapterSmoke(platform AdapterPlatformDiagnostics) map[string]any {
	token := strings.TrimSpace(platform.CredentialValues["bot_token"])
	endpoint := strings.TrimRight(strings.TrimSpace(platform.Endpoint), "/")
	if token == "" || endpoint == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := s.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	response, err := adapterwechat.GetUpdates(ctx, client, endpoint, token, adapterwechat.GetUpdatesOptions{
		GetUpdatesBuf: "",
		Timeout:       5 * time.Second,
	})
	if err != nil {
		return adapterLiveSmokeFailure("wechat", "WeChat getupdates live smoke request failed", 0, sanitizeAdapterSmokeError(err))
	}
	return map[string]any{
		"status":                "pass",
		"message":               "WeChat getupdates live smoke succeeded.",
		"requested":             true,
		"method":                adapterSmokeMethod("wechat"),
		"liveChecked":           true,
		"credentialComplete":    true,
		"endpointConfigured":    true,
		"requiresNetwork":       true,
		"updatesReachable":      true,
		"messageCount":          len(response.Messages),
		"getUpdatesBufReturned": strings.TrimSpace(response.GetUpdatesBuf) != "",
		"longPollingTimeoutMS":  response.LongPollingTimeoutMS,
	}
}

func (s *Server) runFeishuAdapterSmoke(platform AdapterPlatformDiagnostics) map[string]any {
	appID := strings.TrimSpace(platform.CredentialValues["app_id"])
	appSecret := strings.TrimSpace(platform.CredentialValues["app_secret"])
	endpoint := strings.TrimRight(strings.TrimSpace(platform.Endpoint), "/")
	if appID == "" || appSecret == "" || endpoint == "" {
		return nil
	}
	payload, err := json.Marshal(map[string]string{
		"app_id":     appID,
		"app_secret": appSecret,
	})
	if err != nil {
		return adapterLiveSmokeFailure("feishu", "failed to build Feishu tenant token request", 0, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	requestURL := endpoint + "/open-apis/auth/v3/tenant_access_token/internal"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(payload))
	if err != nil {
		return adapterLiveSmokeFailure("feishu", "failed to build Feishu tenant token request", 0, err)
	}
	request.Header.Set("Content-Type", "application/json")
	client := s.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return adapterLiveSmokeFailure("feishu", "Feishu tenant token request failed", 0, err)
	}
	defer response.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&body); err != nil {
		return adapterLiveSmokeFailure("feishu", "Feishu tenant token response was not valid JSON", response.StatusCode, err)
	}
	code := numberValue(body["code"])
	tokenIssued := strings.TrimSpace(stringValue(body["tenant_access_token"])) != ""
	if response.StatusCode < 200 || response.StatusCode >= 300 || code != 0 || !tokenIssued {
		return map[string]any{
			"status":             "fail",
			"message":            "Feishu tenant token live smoke returned an unsuccessful response.",
			"requested":          true,
			"method":             adapterSmokeMethod("feishu"),
			"liveChecked":        true,
			"credentialComplete": true,
			"endpointConfigured": true,
			"requiresNetwork":    true,
			"httpStatus":         response.StatusCode,
			"code":               code,
			"tokenIssued":        tokenIssued,
		}
	}
	return map[string]any{
		"status":             "pass",
		"message":            "Feishu tenant token live smoke succeeded.",
		"requested":          true,
		"method":             adapterSmokeMethod("feishu"),
		"liveChecked":        true,
		"credentialComplete": true,
		"endpointConfigured": true,
		"requiresNetwork":    true,
		"httpStatus":         response.StatusCode,
		"code":               code,
		"tokenIssued":        true,
		"expireSeconds":      numberValue(body["expire"]),
	}
}

func (s *Server) recordAdapterLiveSmokeAudit(platforms []map[string]any, requested bool) int {
	if s == nil || s.runtimeStore == nil || !requested {
		return 0
	}
	written := 0
	for _, platform := range platforms {
		name := strings.ToLower(strings.TrimSpace(stringValue(platform["name"])))
		if name == "" {
			continue
		}
		smoke := sanitizeAdapterSmokeMap(objectMapValue(platform["smoke"]))
		if len(smoke) == 0 {
			continue
		}
		record := map[string]any{
			"platform":         name,
			"recordedAt":       time.Now().UTC().Format(time.RFC3339Nano),
			"enabled":          boolValue(platform["enabled"], false),
			"configured":       boolValue(platform["configured"], false),
			"endpoint":         stringValue(platform["endpoint"]),
			"credentialFields": boolMapValue(platform["credential_fields"]),
			"smoke":            smoke,
			"secretsRedacted":  true,
		}
		if _, err := s.runtimeStore.Set(adapterLiveSmokeRuntimeNamespace, "platform:"+name, record); err == nil {
			written++
		}
	}
	return written
}

func (s *Server) latestAdapterLiveSmokeAudit(platform string) map[string]any {
	if s == nil || s.runtimeStore == nil {
		return nil
	}
	name := strings.ToLower(strings.TrimSpace(platform))
	if name == "" {
		return nil
	}
	entry, ok, err := s.runtimeStore.Get(adapterLiveSmokeRuntimeNamespace, "platform:"+name)
	if err != nil || !ok {
		return nil
	}
	value := objectMapValue(entry.Value)
	if len(value) == 0 {
		return nil
	}
	return value
}

func (s *Server) latestAdapterLiveSmokeAudits(platforms []map[string]any) []map[string]any {
	items := []map[string]any{}
	for _, platform := range platforms {
		if audit := s.latestAdapterLiveSmokeAudit(stringValue(platform["name"])); len(audit) > 0 {
			items = append(items, audit)
		}
	}
	return items
}

func sanitizeAdapterSmokeMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		if key == "error" {
			out[key] = sanitizeAdapterSmokeText(stringValue(value))
			continue
		}
		out[key] = value
	}
	return out
}

func sanitizeAdapterSmokeError(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(sanitizeAdapterSmokeText(err.Error()))
}

func sanitizeAdapterSmokeText(text string) string {
	if text == "" {
		return text
	}
	wechatTokenPath := regexp.MustCompile(`/cgi-bin/[^/\s]+/`)
	text = wechatTokenPath.ReplaceAllString(text, "/cgi-bin/<redacted>/")
	return text
}

func adapterLiveSmokeFailure(platform string, message string, statusCode int, err error) map[string]any {
	result := map[string]any{
		"status":             "fail",
		"message":            message,
		"requested":          true,
		"method":             adapterSmokeMethod(platform),
		"liveChecked":        true,
		"credentialComplete": true,
		"endpointConfigured": true,
		"requiresNetwork":    true,
	}
	if statusCode > 0 {
		result["httpStatus"] = statusCode
	}
	if err != nil {
		result["error"] = err.Error()
	}
	return result
}

func adapterSmokeMethod(platform string) string {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "feishu":
		return "feishu app credentials token/bootstrap check"
	case "wechat":
		return "wechat account token/bootstrap check"
	default:
		return "adapter credential bootstrap check"
	}
}

func adapterSmokeRequiresEndpoint(platform string) bool {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "wechat", "feishu":
		return true
	default:
		return false
	}
}
