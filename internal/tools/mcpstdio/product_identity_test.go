package mcpstdio

import "testing"

func TestProductClientInfoUsesSynonBiomedIdentity(t *testing.T) {
	info := productClientInfo()
	if info["name"] != "synon-biomed" || info["version"] != "0.1.1" {
		t.Fatalf("clientInfo = %#v, want synon-biomed 0.1.1", info)
	}
}
