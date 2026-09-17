package agentruntime

import (
	"strings"
	"testing"
)

func TestSynonPlatformPolicyRequiresSamePassEvidenceReconciliation(t *testing.T) {
	policy := SynonPlatformPolicy()
	for _, required := range []string{
		"reconcile every aggregate, threshold, ranking, range",
		"average cannot establish per-record",
		"reconstruct the governing equation",
		"method from an authoritative source",
		"sign or direction",
		"limiting or boundary behavior",
		"publish the corrected immutable artifact version",
		"do not add a post-delivery review round",
	} {
		if !strings.Contains(policy, required) {
			t.Fatalf("platform policy is missing same-pass evidence reconciliation rule %q", required)
		}
	}
}

func TestSynonPlatformPolicyRequiresBilingualPublicDiscovery(t *testing.T) {
	policy := strings.ToLower(SynonPlatformPolicy())
	for _, required := range []string{
		"both chinese and english query variants",
		"zero results in one language",
		"source-specific interface",
	} {
		if !strings.Contains(policy, required) {
			t.Fatalf("platform policy is missing bilingual discovery rule %q", required)
		}
	}
}

func TestSynonPlatformPolicyRequiresTruthfulRetrievalCompleteness(t *testing.T) {
	policy := strings.ToLower(SynonPlatformPolicy())
	for _, required := range []string{
		"broad candidate pool",
		"follow advertised cursors or offsets",
		"short first page is never",
		"provider totals, retrieved counts and truncation",
	} {
		if !strings.Contains(policy, required) {
			t.Fatalf("platform policy is missing retrieval completeness rule %q", required)
		}
	}
}

func TestSynonPlatformPolicyPreservesCompatibilityAcrossSolverRecovery(t *testing.T) {
	policy := strings.Join(strings.Fields(strings.ToLower(SynonPlatformPolicy())), " ")
	for _, required := range []string{
		"missing-build result changes the installation plan",
		"already established compatibility target",
		"another verified package authority",
		"instead of lowering to a build known not to support the observed device",
	} {
		if !strings.Contains(policy, required) {
			t.Fatalf("platform policy is missing solver recovery rule %q", required)
		}
	}
}
