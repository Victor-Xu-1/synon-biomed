package buildinfo

import "testing"

func TestReleaseInfoUsesSynonBiomedIdentity(t *testing.T) {
	info := Release()
	if info.Name != "Synon Biomed" {
		t.Fatalf("Name = %q, want Synon Biomed", info.Name)
	}
	if info.Version != "0.1.1" {
		t.Fatalf("Version = %q, want 0.1.1", info.Version)
	}
	if info.MachineSlug != "synon-biomed" {
		t.Fatalf("MachineSlug = %q, want synon-biomed", info.MachineSlug)
	}
	if info.SourcePackage != "synon-biomed-v0.1.1" {
		t.Fatalf("SourcePackage = %q, want synon-biomed-v0.1.1", info.SourcePackage)
	}
}
