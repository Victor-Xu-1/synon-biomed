//go:build darwin

package kernel

import (
	"errors"
	"os"
	"testing"
)

func TestDarwinKernelLaunchFailsWithoutNativeBoundary(t *testing.T) {
	evidence := (&Manager{}).RetryConfinementEvidence()
	if evidence.Available || evidence.Mode != "unavailable" || evidence.Reason != darwinConfinementUnverifiedReason {
		t.Fatalf("unexpected macOS confinement evidence: %#v", evidence)
	}
	if diagnostic := DiagnoseConfinementEvidence(evidence); diagnostic.Code != "kernel_confinement_unavailable" {
		t.Fatalf("unexpected macOS confinement diagnostic: %#v", diagnostic)
	}
	manager := NewManager(Config{})
	worker, err := manager.startWorker(
		"darwin-boundary", t.TempDir(), "/usr/bin/true", nil, []string{"PATH=/usr/bin"},
		nil, nil, "", "",
	)
	if worker != nil || !errors.Is(err, ErrConfinementUnavailable) {
		t.Fatalf("worker launch must fail before execution: worker=%v err=%v", worker, err)
	}
}

func TestDarwinWorkerCannotBypassBoundaryWithMountsOrAuxiliaryFD(t *testing.T) {
	parent, child, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	defer child.Close()
	command, err := newConfinedWorkerCommandWithAuxiliary(
		t.TempDir(), "/usr/bin/true", nil, []string{"PATH=/usr/bin"},
		[]WorkerMount{{Path: t.TempDir(), Writable: true}},
		[]string{t.TempDir()}, []*os.File{child},
	)
	if command != nil || !errors.Is(err, ErrConfinementUnavailable) {
		t.Fatalf("unverified mount and descriptor policy must fail: command=%v err=%v", command, err)
	}
}

func TestDarwinKernelEgressPolicyRequiresBroker(t *testing.T) {
	for _, fixture := range []struct {
		name     string
		allowed  []string
		upstream string
	}{
		{name: "approved domain", allowed: []string{"example.com"}},
		{name: "upstream proxy", upstream: "https://proxy.example.com"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			proxy, err := startKernelEgressProxy(t.TempDir(), "darwin-egress", fixture.allowed, nil, fixture.upstream)
			if proxy != nil || !errors.Is(err, ErrConfinementUnavailable) {
				t.Fatalf("unbrokered egress must fail: proxy=%v err=%v", proxy, err)
			}
		})
	}
	proxy, err := startKernelEgressProxy(t.TempDir(), "darwin-no-egress", nil, nil)
	if proxy != nil || err != nil {
		t.Fatalf("no requested egress must not fabricate a broker: proxy=%v err=%v", proxy, err)
	}
}
