package software

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

type ProvisionReceipt struct {
	ProviderID  string   `json:"provider_id"`
	Environment string   `json:"environment"`
	Generation  string   `json:"generation"`
	Packages    []string `json:"packages,omitempty"`
	Executable  string   `json:"executable"`
	Local       bool     `json:"local"`
	Verified    bool     `json:"verified"`
	Preflight   bool     `json:"preflight"`
	Disposition string   `json:"disposition"`
}

const (
	ProvisionDispositionReused    = "reused"
	ProvisionDispositionInstalled = "installed"
	ProvisionDispositionRepaired  = "repaired"
)

type Provisioner interface {
	Provider
	Ensure(context.Context, Plan, string) (ProvisionReceipt, error)
}

// Controller is the sole capability-to-provider authority. Resolve happens
// once; Ensure invokes only the selected provider and deliberately never tries
// another provider after an error.
type Controller struct {
	resolver    *Resolver
	providers   map[string]Provisioner
	descriptors map[string]ProviderDescriptor
}

func NewController(providers ...Provisioner) (*Controller, error) {
	base := make([]Provider, 0, len(providers))
	for _, provider := range providers {
		base = append(base, provider)
	}
	resolver, err := NewResolver(base...)
	if err != nil {
		return nil, err
	}
	controller := &Controller{
		resolver: resolver, providers: make(map[string]Provisioner, len(providers)),
		descriptors: make(map[string]ProviderDescriptor, len(providers)),
	}
	for _, provider := range providers {
		descriptor, err := normalizeProviderDescriptor(provider.Descriptor())
		if err != nil {
			return nil, err
		}
		controller.providers[descriptor.ID] = provider
		controller.descriptors[descriptor.ID] = descriptor
	}
	return controller, nil
}

func (c *Controller) Resolve(input Request) (Plan, error) {
	if c == nil || c.resolver == nil {
		return Plan{}, errors.New("software controller is unavailable")
	}
	return c.resolver.Resolve(input)
}

func (c *Controller) Ensure(ctx context.Context, plan Plan, operationID string) (ProvisionReceipt, error) {
	if c == nil || c.resolver == nil {
		return ProvisionReceipt{}, errors.New("software controller is unavailable")
	}
	operationID = strings.TrimSpace(operationID)
	if operationID == "" || len(operationID) > 512 || strings.ContainsAny(operationID, "\x00\r\n") {
		return ProvisionReceipt{}, errors.New("software operation identity is invalid")
	}
	resolved, err := c.resolver.Resolve(plan.Request)
	if err != nil {
		return ProvisionReceipt{}, err
	}
	if resolved.ProviderID != plan.ProviderID || resolved.RequestDigest != plan.RequestDigest ||
		(resolved.Environment != plan.Environment && !plan.CompatibleReuse) {
		return ProvisionReceipt{}, errors.New("software plan conflicts with the registered provider authority")
	}
	provider := c.providers[plan.ProviderID]
	descriptor, descriptorFound := c.descriptors[plan.ProviderID]
	if provider == nil || !descriptorFound {
		return ProvisionReceipt{}, ErrNoProvider
	}
	receipt, err := provider.Ensure(ctx, plan, operationID)
	if err != nil {
		return ProvisionReceipt{}, fmt.Errorf("software provider %s provisioning failed: %w", plan.ProviderID, err)
	}
	if receipt.ProviderID != plan.ProviderID || receipt.Environment != plan.Environment || receipt.Generation == "" ||
		receipt.Executable != plan.Request.Executable || receipt.Local != descriptor.Local || !receipt.Verified ||
		!receipt.Preflight || !validProvisionDisposition(receipt.Disposition) {
		return ProvisionReceipt{}, errors.New("software provider returned an invalid provisioning receipt")
	}
	receipt.Packages = append([]string(nil), receipt.Packages...)
	return receipt, nil
}

func validProvisionDisposition(value string) bool {
	switch value {
	case ProvisionDispositionReused, ProvisionDispositionInstalled, ProvisionDispositionRepaired:
		return true
	default:
		return false
	}
}
