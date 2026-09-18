package buildinfo

import (
	"testing"

	productidentity "synon-go"
)

func TestReleaseInfoUsesSynonBiomedIdentity(t *testing.T) {
	info := Release()
	if info.Name != "Synon Biomed" {
		t.Fatalf("Name = %q, want Synon Biomed", info.Name)
	}
	if info.Version != productidentity.Current().Version {
		t.Fatalf("Version = %q, want root-authority version", info.Version)
	}
	if info.MachineSlug != "synon-biomed" {
		t.Fatalf("MachineSlug = %q, want synon-biomed", info.MachineSlug)
	}
	if info.SourcePackage != "synon-biomed-v"+productidentity.Current().Version {
		t.Fatalf("SourcePackage = %q, want root-authority package", info.SourcePackage)
	}
}
