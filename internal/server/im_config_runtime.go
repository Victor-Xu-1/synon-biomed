package server

import (
	"fmt"
	"sort"
	"strings"
)

func (s *Server) executeIMConfigTool(input map[string]any) (any, error) {
	if err := s.validateRegisteredTool("im_config", input); err != nil {
		return nil, err
	}
	requestedPlatform := strings.ToLower(strings.TrimSpace(stringValue(input["platform"])))
	smoke := boolValue(input["smoke"], false)
	platforms := s.adapterDoctorPlatforms(smoke)
	items := make([]map[string]any, 0, len(platforms))
	configured := 0
	enabled := 0
	livePassed := 0
	missingCredentialPlatforms := []string{}
	found := requestedPlatform == ""
	for _, platform := range platforms {
		name := strings.ToLower(strings.TrimSpace(stringValue(platform["name"])))
		if requestedPlatform != "" && name != requestedPlatform {
			continue
		}
		found = true
		fields := boolMapValue(platform["credential_fields"])
		missingFields := []string{}
		for _, field := range adapterCredentialFieldOrder(name, fields) {
			if !fields[field] {
				missingFields = append(missingFields, field)
			}
		}
		isConfigured := boolValue(platform["configured"], false)
		if isConfigured {
			configured++
		} else {
			missingCredentialPlatforms = append(missingCredentialPlatforms, name)
		}
		if boolValue(platform["enabled"], false) {
			enabled++
		}
		smokeResult := objectMapValue(platform["smoke"])
		if adapterLiveSmokePlatformPass(platform) {
			livePassed++
		}
		status := "missing_credentials"
		switch {
		case adapterLiveSmokePlatformPass(platform):
			status = "live_smoke_pass"
		case isConfigured && smoke:
			status = "live_smoke_pending"
		case isConfigured:
			status = "ready_for_live_smoke"
		}
		lastSmoke := s.latestAdapterLiveSmokeAudit(name)
		items = append(items, map[string]any{
			"name":                     name,
			"status":                   status,
			"enabled":                  boolValue(platform["enabled"], false),
			"configured":               isConfigured,
			"endpoint":                 stringValue(platform["endpoint"]),
			"credentialFields":         fields,
			"missingCredentialFields":  missingFields,
			"env":                      adapterCredentialEnvKeys(name),
			"config":                   adapterCredentialConfigKeys(name),
			"enableValue":              name,
			"liveSmokeCommand":         "synon-go-live-im-smoke --require-all --json --runtime-url http://127.0.0.1:8765",
			"liveSmokeMethod":          adapterSmokeMethod(name),
			"smoke":                    smokeResult,
			"pairingStoreConfigured":   s != nil && s.pairingStore != nil,
			"sessionJournalConfigured": s != nil && s.sessionStore != nil && s.eventJournal != nil,
			"dedupConfigured":          s.adapterDedupConfigured(name),
			"lastLiveSmoke":            lastSmoke,
		})
	}
	if !found {
		return nil, fmt.Errorf("im_config.platform is unsupported: %s", requestedPlatform)
	}
	status := "missing_credentials"
	if len(items) > 0 && configured == len(items) {
		status = "ready_for_live_smoke"
		if smoke && livePassed == len(items) {
			status = "live_smoke_pass"
		} else if smoke {
			status = "live_smoke_pending"
		}
	}
	return map[string]any{
		"ok":             status == "live_smoke_pass" || status == "ready_for_live_smoke",
		"status":         status,
		"smokeRequested": smoke,
		"platforms":      items,
		"summary": map[string]any{
			"platforms":                  len(items),
			"enabled":                    enabled,
			"configured":                 configured,
			"liveSmokePassed":            livePassed,
			"missingCredentialPlatforms": missingCredentialPlatforms,
		},
		"secretsRedacted":         true,
		"credentialStoragePolicy": "Use SYNON_CONFIG or platform environment variables; im_config never returns secret values.",
		"auditNamespace":          adapterLiveSmokeRuntimeNamespace,
	}, nil
}

func boolMapValue(value any) map[string]bool {
	out := map[string]bool{}
	switch typed := value.(type) {
	case map[string]bool:
		for key, fieldValue := range typed {
			out[key] = fieldValue
		}
	case map[string]any:
		for key, fieldValue := range typed {
			out[key] = boolValue(fieldValue, false)
		}
	}
	return out
}

func adapterCredentialFieldOrder(platform string, fields map[string]bool) []string {
	ordered := []string{}
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "feishu":
		ordered = []string{"app_id", "app_secret", "verification_token", "encrypt_key"}
	case "wechat":
		ordered = []string{"account_id", "bot_token", "user_id"}
	}
	seen := map[string]bool{}
	result := []string{}
	for _, key := range ordered {
		if _, ok := fields[key]; ok {
			result = append(result, key)
			seen[key] = true
		}
	}
	extra := make([]string, 0, len(fields))
	for key := range fields {
		if !seen[key] {
			extra = append(extra, key)
		}
	}
	sort.Strings(extra)
	return append(result, extra...)
}

func adapterCredentialEnvKeys(platform string) map[string]string {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "feishu":
		return map[string]string{"app_id": "FEISHU_APP_ID", "app_secret": "FEISHU_APP_SECRET", "verification_token": "FEISHU_VERIFICATION_TOKEN", "encrypt_key": "FEISHU_ENCRYPT_KEY", "endpoint": "FEISHU_DOMAIN", "enabled": "SYNON_ENABLED_ADAPTERS=feishu"}
	case "wechat":
		return map[string]string{"account_id": "WECHAT_ACCOUNT_ID", "bot_token": "WECHAT_BOT_TOKEN", "user_id": "WECHAT_USER_ID", "endpoint": "WECHAT_BASE_URL", "enabled": "SYNON_ENABLED_ADAPTERS=wechat"}
	default:
		return map[string]string{}
	}
}

func adapterCredentialConfigKeys(platform string) map[string]string {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "feishu":
		return map[string]string{"app_id": "feishu.app_id", "app_secret": "feishu.app_secret", "verification_token": "feishu.verification_token", "encrypt_key": "feishu.encrypt_key", "endpoint": "feishu.domain", "enabled": "enabled_adapters[]"}
	case "wechat":
		return map[string]string{"account_id": "wechat.account_id", "bot_token": "wechat.bot_token", "user_id": "wechat.user_id", "endpoint": "wechat.base_url", "enabled": "enabled_adapters[]"}
	default:
		return map[string]string{}
	}
}

func (s *Server) adapterDedupConfigured(platform string) bool {
	if s == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "feishu":
		return s.feishuDedup != nil
	case "wechat":
		return s.wechatDedup != nil
	default:
		return false
	}
}
