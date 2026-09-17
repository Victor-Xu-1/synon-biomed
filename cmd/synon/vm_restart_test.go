package main

import "testing"

func TestCollectVMRestartEnvironmentUsesExplicitAllowlist(t *testing.T) {
	environment := collectVMRestartEnvironment([]string{
		"PATH=/usr/bin", "USER=victor_1", "WSL_DISTRO_NAME=Ubuntu",
		"SYNON_CONFIG=/tmp/config.json",
		"WECHAT_BOT_TOKEN=token", "FEISHU_APP_SECRET=secret",
		"HTTPS_PROXY=http://proxy", "AWS_SECRET_ACCESS_KEY=must-not-copy",
		"RANDOM_UNRELATED=value", "malformed",
	})
	for _, key := range []string{"PATH", "USER", "WSL_DISTRO_NAME", "SYNON_CONFIG", "WECHAT_BOT_TOKEN", "FEISHU_APP_SECRET", "HTTPS_PROXY"} {
		if _, ok := environment[key]; !ok {
			t.Fatalf("allowlisted %s was omitted: %#v", key, environment)
		}
	}
	for _, key := range []string{"AWS_SECRET_ACCESS_KEY", "RANDOM_UNRELATED"} {
		if _, ok := environment[key]; ok {
			t.Fatalf("unrelated %s was persisted: %#v", key, environment)
		}
	}
}
