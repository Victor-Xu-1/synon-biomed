package workspace

import (
	"strings"
	"testing"
)

func TestSchemaMigrationIdentityV2GoldenAndBoundaries(t *testing.T) {
	migration := versionedSchemaMigration{
		version:    21,
		name:       "schema-rescue-v21",
		statements: []string{"SELECT 1", "SELECT 2"},
		identityV2: &schemaMigrationIdentityV2{
			CallbackID:        "provider-v21-normalize",
			RuleSpec:          `{"shape":"canonical"}`,
			PreflightIdentity: "provider-shape-v1",
		},
	}
	want := "2b22a3f05cdcb3d3191c74737de82f7d39298f85b0a846d640074ce78ae42d18"
	got, err := migration.validatedChecksum()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("identity v2 checksum=%s", got)
	}

	mutations := []struct {
		name   string
		mutate func(*versionedSchemaMigration)
	}{
		{"version", func(value *versionedSchemaMigration) { value.version++ }},
		{"name", func(value *versionedSchemaMigration) { value.name += " " }},
		{"statement order", func(value *versionedSchemaMigration) {
			value.statements[0], value.statements[1] = value.statements[1], value.statements[0]
		}},
		{"callback", func(value *versionedSchemaMigration) { value.identityV2.CallbackID += "-changed" }},
		{"rule", func(value *versionedSchemaMigration) { value.identityV2.RuleSpec += " " }},
		{"preflight", func(value *versionedSchemaMigration) { value.identityV2.PreflightIdentity += "-changed" }},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			candidate := migration
			candidate.statements = append([]string(nil), migration.statements...)
			identity := *migration.identityV2
			candidate.identityV2 = &identity
			test.mutate(&candidate)
			checksum, err := candidate.validatedChecksum()
			if err != nil {
				t.Fatal(err)
			}
			if checksum == got {
				t.Fatalf("%s did not change identity", test.name)
			}
		})
	}
}

func TestSchemaMigrationIdentityV2RejectsIncompleteOrLegacyMetadata(t *testing.T) {
	valid := versionedSchemaMigration{
		version: 21, name: "migration", statements: []string{"SELECT 1"},
		identityV2: &schemaMigrationIdentityV2{CallbackID: "callback", RuleSpec: "rules", PreflightIdentity: "preflight"},
	}
	tests := []struct {
		name   string
		mutate func(*versionedSchemaMigration)
		want   string
	}{
		{"missing metadata", func(value *versionedSchemaMigration) { value.identityV2 = nil }, "metadata is required"},
		{"empty callback", func(value *versionedSchemaMigration) { value.identityV2.CallbackID = "" }, "non-empty"},
		{"invalid utf8", func(value *versionedSchemaMigration) { value.identityV2.RuleSpec = string([]byte{0xff}) }, "valid UTF-8"},
		{"legacy v2", func(value *versionedSchemaMigration) { value.version = 20 }, "legacy migration"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			identity := *valid.identityV2
			candidate.identityV2 = &identity
			test.mutate(&candidate)
			if _, err := candidate.validatedChecksum(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	if err := validateSchemaMigrationIdentityRegistry([]versionedSchemaMigration{{version: 2, name: "gap"}}); err == nil || !strings.Contains(err.Error(), "not contiguous") {
		t.Fatalf("registry gap error=%v", err)
	}
}
