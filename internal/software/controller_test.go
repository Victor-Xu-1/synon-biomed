package software

import (
	"context"
	"errors"
	"testing"
)

type fakeProvisioner struct {
	descriptor ProviderDescriptor
	calls      int
	err        error
}

func (p *fakeProvisioner) Descriptor() ProviderDescriptor { return p.descriptor }

func (p *fakeProvisioner) Ensure(_ context.Context, plan Plan, _ string) (ProvisionReceipt, error) {
	p.calls++
	if p.err != nil {
		return ProvisionReceipt{}, p.err
	}
	return ProvisionReceipt{
		ProviderID: plan.ProviderID, Environment: plan.Environment, Generation: "generation-1",
		Executable: plan.Request.Executable, Local: p.descriptor.Local, Verified: true,
		Preflight: true, Disposition: ProvisionDispositionInstalled,
	}, nil
}

func TestControllerAcceptsARegisteredRemoteAdapterWithoutChangingTheToolContract(t *testing.T) {
	provider := &fakeProvisioner{descriptor: ProviderDescriptor{
		ID: "remote-batch", Priority: 100, Local: false, Languages: []string{"native", "python", "r"},
		PackageManagers: []PackageManager{PackageManagerConda, PackageManagerPip}, Capabilities: []string{"*"},
	}}
	controller, err := NewController(provider)
	if err != nil {
		t.Fatal(err)
	}
	request := validRequest()
	request.Provider = "remote-batch"
	plan, err := controller.Resolve(request)
	if err != nil || plan.ProviderID != "remote-batch" {
		t.Fatalf("remote plan=%#v err=%v", plan, err)
	}
	receipt, err := controller.Ensure(context.Background(), plan, "remote-operation")
	if err != nil || receipt.Local || receipt.ProviderID != "remote-batch" || provider.calls != 1 {
		t.Fatalf("remote receipt=%#v err=%v calls=%d", receipt, err, provider.calls)
	}
}

func TestControllerNeverFallsBackAfterSelectedProviderFailure(t *testing.T) {
	selected := &fakeProvisioner{descriptor: ProviderDescriptor{
		ID: "selected", Priority: 100, Local: true, Languages: []string{"native"},
		PackageManagers: []PackageManager{PackageManagerConda}, Capabilities: []string{"*"},
	}, err: errors.New("solver failed")}
	other := &fakeProvisioner{descriptor: ProviderDescriptor{
		ID: "other", Priority: 10, Local: true, Languages: []string{"native"},
		PackageManagers: []PackageManager{PackageManagerConda}, Capabilities: []string{"*"},
	}}
	controller, err := NewController(selected, other)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := controller.Resolve(validRequest())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Ensure(context.Background(), plan, "call-1"); err == nil {
		t.Fatal("selected provider failure was hidden")
	}
	if selected.calls != 1 || other.calls != 0 {
		t.Fatalf("provider fallback occurred: selected=%d other=%d", selected.calls, other.calls)
	}
}

func TestControllerRejectsMutatedPlan(t *testing.T) {
	provider := &fakeProvisioner{descriptor: ProviderDescriptor{
		ID: "local-conda", Priority: 100, Local: true, Languages: []string{"native"},
		PackageManagers: []PackageManager{PackageManagerConda}, Capabilities: []string{"*"},
	}}
	controller, err := NewController(provider)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := controller.Resolve(validRequest())
	if err != nil {
		t.Fatal(err)
	}
	plan.Environment = "different"
	if _, err := controller.Ensure(context.Background(), plan, "call-2"); err == nil || provider.calls != 0 {
		t.Fatalf("mutated plan reached provider: calls=%d err=%v", provider.calls, err)
	}
}
