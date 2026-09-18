package mcpstdio

import (
	"testing"

	"synon-go/internal/buildinfo"
)

func TestProductClientInfoUsesSynonBiomedIdentity(t *testing.T) {
	info := productClientInfo()
	if info["name"] != "synon-biomed" || info["version"] != buildinfo.Release().Version {
		t.Fatalf("clientInfo = %#v, want root-authority identity", info)
	}
}
