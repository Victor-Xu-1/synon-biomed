package software

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

var (
	ErrNoProvider        = errors.New("no software provider satisfies the request")
	ErrAmbiguousProvider = errors.New("software provider selection is ambiguous")
)

type ProviderDescriptor struct {
	ID              string
	Priority        int
	Local           bool
	Languages       []string
	PackageManagers []PackageManager
	Capabilities    []string
}

type Provider interface {
	Descriptor() ProviderDescriptor
}

// RequestAdmitter lets a provider express combinations that cannot be
// represented by the generic descriptor alone (for example, one package
// source being valid only for one language). The core never interprets a
// provider-specific package specification.
type RequestAdmitter interface {
	AdmitSoftwareRequest(Request) error
}

type Plan struct {
	ProviderID      string  `json:"provider_id"`
	Request         Request `json:"request"`
	RequestDigest   string  `json:"request_digest"`
	Environment     string  `json:"environment"`
	CompatibleReuse bool    `json:"compatible_reuse,omitempty"`
}

type Resolver struct {
	descriptors map[string]ProviderDescriptor
	admitters   map[string]RequestAdmitter
}

func NewResolver(providers ...Provider) (*Resolver, error) {
	resolver := &Resolver{
		descriptors: make(map[string]ProviderDescriptor, len(providers)),
		admitters:   make(map[string]RequestAdmitter, len(providers)),
	}
	for index, provider := range providers {
		if provider == nil {
			return nil, fmt.Errorf("software provider %d is nil", index)
		}
		descriptor, err := normalizeProviderDescriptor(provider.Descriptor())
		if err != nil {
			return nil, fmt.Errorf("software provider %d: %w", index, err)
		}
		if _, duplicate := resolver.descriptors[descriptor.ID]; duplicate {
			return nil, fmt.Errorf("software provider %q is registered more than once", descriptor.ID)
		}
		resolver.descriptors[descriptor.ID] = descriptor
		if admitter, ok := provider.(RequestAdmitter); ok {
			resolver.admitters[descriptor.ID] = admitter
		}
	}
	return resolver, nil
}

func (r *Resolver) Resolve(input Request) (Plan, error) {
	request, err := NormalizeRequest(input)
	if err != nil {
		return Plan{}, err
	}
	if r == nil || len(r.descriptors) == 0 {
		return Plan{}, ErrNoProvider
	}
	if request.Provider != "" {
		descriptor, found := r.descriptors[request.Provider]
		if !found || !r.supports(descriptor, request) {
			return Plan{}, fmt.Errorf("%w: requested provider %q", ErrNoProvider, request.Provider)
		}
		return buildPlan(descriptor.ID, request)
	}
	candidates := make([]ProviderDescriptor, 0, len(r.descriptors))
	for _, descriptor := range r.descriptors {
		if r.supports(descriptor, request) {
			candidates = append(candidates, descriptor)
		}
	}
	if len(candidates) == 0 {
		return Plan{}, ErrNoProvider
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Priority == candidates[j].Priority {
			return candidates[i].ID < candidates[j].ID
		}
		return candidates[i].Priority > candidates[j].Priority
	})
	if len(candidates) > 1 && candidates[0].Priority == candidates[1].Priority {
		return Plan{}, fmt.Errorf("%w: %s and %s have equal priority", ErrAmbiguousProvider, candidates[0].ID, candidates[1].ID)
	}
	return buildPlan(candidates[0].ID, request)
}

func buildPlan(providerID string, request Request) (Plan, error) {
	digest, err := RequestDigest(request)
	if err != nil {
		return Plan{}, err
	}
	environment, err := EnvironmentName(providerID, request)
	if err != nil {
		return Plan{}, err
	}
	return Plan{ProviderID: providerID, Request: request, RequestDigest: digest, Environment: environment}, nil
}

func normalizeProviderDescriptor(input ProviderDescriptor) (ProviderDescriptor, error) {
	result := input
	result.ID = strings.ToLower(strings.TrimSpace(result.ID))
	if !requestIdentifier.MatchString(result.ID) {
		return ProviderDescriptor{}, errors.New("provider id must be a bounded lowercase identifier")
	}
	var err error
	result.Languages, err = normalizeUniqueStrings(result.Languages, 16, func(value string) bool {
		switch strings.ToLower(value) {
		case "python", "r", "native":
			return true
		default:
			return false
		}
	}, "provider language")
	if err != nil || len(result.Languages) == 0 {
		if err == nil {
			err = errors.New("provider must advertise at least one language")
		}
		return ProviderDescriptor{}, err
	}
	result.PackageManagers = append([]PackageManager(nil), result.PackageManagers...)
	seenManagers := map[PackageManager]struct{}{}
	for index, manager := range result.PackageManagers {
		manager = PackageManager(strings.ToLower(strings.TrimSpace(string(manager))))
		if !requestIdentifier.MatchString(string(manager)) {
			return ProviderDescriptor{}, fmt.Errorf("provider package manager %d has an invalid identifier", index)
		}
		if _, duplicate := seenManagers[manager]; duplicate {
			return ProviderDescriptor{}, fmt.Errorf("provider package manager %d is duplicated", index)
		}
		seenManagers[manager] = struct{}{}
		result.PackageManagers[index] = manager
	}
	result.Capabilities, err = normalizeUniqueStrings(result.Capabilities, 256, func(value string) bool {
		return value == "*" || requestIdentifier.MatchString(strings.ToLower(value))
	}, "provider capability")
	if err != nil || len(result.Capabilities) == 0 {
		if err == nil {
			err = errors.New("provider must advertise at least one capability")
		}
		return ProviderDescriptor{}, err
	}
	return result, nil
}

func (r *Resolver) supports(descriptor ProviderDescriptor, request Request) bool {
	if !descriptorSupports(descriptor, request) {
		return false
	}
	admitter := r.admitters[descriptor.ID]
	return admitter == nil || admitter.AdmitSoftwareRequest(request) == nil
}

func descriptorSupports(descriptor ProviderDescriptor, request Request) bool {
	if !containsFold(descriptor.Languages, request.Language) || !containsCapability(descriptor.Capabilities, request.Capability) {
		return false
	}
	managers := make(map[PackageManager]struct{}, len(descriptor.PackageManagers))
	for _, manager := range descriptor.PackageManagers {
		managers[manager] = struct{}{}
	}
	for _, requirement := range request.Packages {
		if _, found := managers[requirement.Manager]; !found {
			return false
		}
	}
	return true
}

func containsFold(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(value, want) {
			return true
		}
	}
	return false
}

func containsCapability(values []string, want string) bool {
	for _, value := range values {
		if value == "*" || strings.EqualFold(value, want) {
			return true
		}
	}
	return false
}
