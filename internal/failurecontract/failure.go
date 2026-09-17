package failurecontract

import "strings"

// Kind is the closed failure algebra shared by provider, execution, and
// orchestration boundaries. Detail codes remain diagnostic only; control flow
// must branch on Kind so adding package-specific stderr text cannot create a
// second retry policy.
type Kind string

const (
	NotFound          Kind = "not_found"
	Unauthorized      Kind = "unauthorized"
	RateLimited       Kind = "rate_limited"
	QuotaExhausted    Kind = "quota_exhausted"
	InvalidRequest    Kind = "invalid_request"
	Transient         Kind = "transient"
	ImageBuildFailed  Kind = "image_build_failed"
	NetworkDenied     Kind = "network_denied"
	NetworkBridgeDown Kind = "network_bridge_down"
	OwnershipMismatch Kind = "ownership_mismatch"
	ProviderDegraded  Kind = "provider_degraded"
	ResultRejected    Kind = "result_rejected"
)

var validKinds = map[Kind]struct{}{
	NotFound: {}, Unauthorized: {}, RateLimited: {}, QuotaExhausted: {}, InvalidRequest: {},
	Transient: {}, ImageBuildFailed: {}, NetworkDenied: {}, NetworkBridgeDown: {},
	OwnershipMismatch: {}, ProviderDegraded: {}, ResultRejected: {},
}

func Valid(kind Kind) bool {
	_, ok := validKinds[kind]
	return ok
}

// KindForDetailCode is the only compatibility classifier for historical and
// provider-specific detail codes. It never inspects stderr or model prose.
func KindForDetailCode(code string) Kind {
	code = strings.ToLower(strings.TrimSpace(code))
	switch {
	case code == "not_found", strings.Contains(code, "not_found"), strings.Contains(code, "missing"),
		strings.Contains(code, "dependency_unavailable"), strings.Contains(code, "no_provider"):
		return NotFound
	case code == "unauthorized", strings.Contains(code, "unauthorized"), strings.Contains(code, "authentication"),
		strings.Contains(code, "permission_denied"), strings.Contains(code, "approval_unavailable"):
		return Unauthorized
	case code == "rate_limited", strings.Contains(code, "rate_limit"):
		return RateLimited
	case code == "quota_exhausted", strings.Contains(code, "quota"):
		return QuotaExhausted
	case code == "network_denied", strings.Contains(code, "network_denied"), strings.HasPrefix(code, "secure_fetch_"):
		return NetworkDenied
	case code == "network_bridge_down", strings.Contains(code, "network_bridge"), strings.Contains(code, "bridge_down"):
		return NetworkBridgeDown
	case code == "ownership_mismatch", strings.Contains(code, "ownership"), strings.Contains(code, "owner_mismatch"):
		return OwnershipMismatch
	case code == "image_build_failed", strings.Contains(code, "install_failed"), strings.Contains(code, "repair_failed"),
		strings.Contains(code, "build_failed"), strings.Contains(code, "provision_failed"):
		return ImageBuildFailed
	case code == "transient", strings.Contains(code, "timeout"), strings.Contains(code, "temporar"),
		strings.Contains(code, "draining"):
		return Transient
	case code == "provider_degraded", strings.Contains(code, "provider_degraded"), strings.Contains(code, "connector_error"),
		strings.Contains(code, "storage_error"), code == "unavailable", strings.HasSuffix(code, "_unavailable"):
		return ProviderDegraded
	case code == "invalid_request", strings.Contains(code, "invalid_argument"), strings.Contains(code, "arguments_invalid"),
		strings.Contains(code, "request_unsupported"), strings.Contains(code, "input_contract"),
		strings.Contains(code, "api_contract"), strings.Contains(code, "code_preflight"):
		return InvalidRequest
	case code == "result_rejected", strings.Contains(code, "nonzero_exit"), strings.Contains(code, "validation_failed"),
		strings.Contains(code, "output_validation"), strings.Contains(code, "quality_contract"),
		strings.Contains(code, "result_rejected"), strings.Contains(code, "execution_failed"):
		return ResultRejected
	default:
		return ResultRejected
	}
}

// NextAction is closed orchestration metadata. A failed registered job is
// terminal for its current execution unit; a later unit may start only after
// the named external/user/inspection boundary is satisfied.
func NextAction(kind Kind) string {
	switch kind {
	case Unauthorized:
		return "wait_for_user_authority"
	case RateLimited, QuotaExhausted, Transient, NetworkBridgeDown, ProviderDegraded:
		return "wait_for_external_state_then_start_new_execution"
	case NotFound, InvalidRequest, ImageBuildFailed, NetworkDenied, OwnershipMismatch, ResultRejected:
		return "inspect_contract_then_start_new_execution"
	default:
		return "inspect_contract_then_start_new_execution"
	}
}

func ApplyTerminalJobFailure(value map[string]any, detailCode string) Kind {
	kind := KindForDetailCode(detailCode)
	value["failure_kind"] = string(kind)
	value["terminal"] = true
	value["retryable"] = false
	value["next_action"] = NextAction(kind)
	return kind
}
