package kernel

import (
	"errors"
	"sync"
	"time"
)

var ErrConfinementUnavailable = errors.New("kernel process confinement is unavailable")

const darwinConfinementUnverifiedReason = "macOS kernel process confinement is not verified"

// ConfinementEvidence describes the process boundary used for every managed
// kernel worker. It is deliberately separate from language/runtime readiness.
type ConfinementEvidence struct {
	Available    bool   `json:"available"`
	Mode         string `json:"mode"`
	PolicySHA256 string `json:"policy_sha256,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

type ConfinementDiagnostic struct {
	Code    string
	Message string
}

func DiagnoseConfinementEvidence(evidence ConfinementEvidence) ConfinementDiagnostic {
	if evidence.Available {
		return ConfinementDiagnostic{}
	}
	switch evidence.Reason {
	case "bubblewrap is unavailable":
		return ConfinementDiagnostic{Code: "kernel_confinement_unavailable", Message: "kernel process confinement is unavailable"}
	case darwinConfinementUnverifiedReason:
		return ConfinementDiagnostic{Code: "kernel_confinement_unavailable", Message: "kernel process confinement is unavailable"}
	case "Synon kernel confinement is unavailable on this platform":
		return ConfinementDiagnostic{Code: "kernel_confinement_unsupported", Message: "kernel process confinement is unsupported on this platform"}
	default:
		return ConfinementDiagnostic{Code: "kernel_confinement_probe_failed", Message: "kernel process confinement verification failed"}
	}
}

var (
	confinementProbeMu      sync.Mutex
	confinementProbe        ConfinementEvidence
	confinementProbeRetryAt time.Time
)

func (m *Manager) ConfinementEvidence() ConfinementEvidence {
	confinementProbeMu.Lock()
	defer confinementProbeMu.Unlock()
	now := time.Now()
	if confinementProbe.Available || now.Before(confinementProbeRetryAt) {
		return confinementProbe
	}
	confinementProbe = probePlatformConfinement()
	if !confinementProbe.Available {
		confinementProbeRetryAt = now.Add(30 * time.Second)
	}
	return confinementProbe
}

func (m *Manager) RetryConfinementEvidence() ConfinementEvidence {
	confinementProbeMu.Lock()
	defer confinementProbeMu.Unlock()
	now := time.Now()
	confinementProbe = probePlatformConfinement()
	confinementProbeRetryAt = time.Time{}
	if !confinementProbe.Available {
		confinementProbeRetryAt = now.Add(30 * time.Second)
	}
	return confinementProbe
}
