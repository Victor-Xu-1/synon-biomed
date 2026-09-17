package providers

import (
	"context"
	"errors"
	"fmt"
	"strings"

	secretstore "synon-go/internal/persistence/secrets"
	workspace "synon-go/internal/persistence/workspace"
)

// ResolveUserProviderProfile resolves one explicit enabled provider owned by
// the user. It is used when a UI selection must disambiguate providers that
// expose the same model name.
func ResolveUserProviderProfile(
	workspaceStore *workspace.Store,
	secretStore *secretstore.Store,
	userID string,
	providerID string,
	input ResolutionInput,
) (ModelProfile, error) {
	userID = strings.TrimSpace(userID)
	providerID = strings.TrimSpace(providerID)
	if workspaceStore == nil || userID == "" || providerID == "" {
		return ModelProfile{}, errors.New("workspace model provider store, user, and provider are required")
	}
	ctx := input.Context
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ModelProfile{}, err
	}
	items, err := workspaceStore.ListModelProvidersWithContext(ctx, userID)
	if err != nil {
		return ModelProfile{}, err
	}
	if err := ctx.Err(); err != nil {
		return ModelProfile{}, err
	}
	for _, provider := range items {
		if strings.TrimSpace(provider.ID) != providerID {
			continue
		}
		if !provider.Enabled {
			return ModelProfile{}, fmt.Errorf("model provider %s is disabled", providerID)
		}
		return buildModelProfile(provider, secretStore, userID, input)
	}
	return ModelProfile{}, fmt.Errorf("model provider %s not found", providerID)
}
