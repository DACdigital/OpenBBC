package bifrost

import (
	"context"
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
)

// account is the in-memory Bifrost Account: exactly one provider and at most
// one key, both read from env at boot (config.LLMConfig). Nothing is
// persisted and no Bifrost config file is read.
type account struct {
	provider schemas.ModelProvider
	apiKey   string
	baseURL  string
}

func (a *account) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return []schemas.ModelProvider{a.provider}, nil
}

// GetKeysForProvider returns no keys when the key is unset; Generate fails
// before sending any request in that case.
func (a *account) GetKeysForProvider(_ context.Context, p schemas.ModelProvider) ([]schemas.Key, error) {
	if p != a.provider || a.apiKey == "" {
		return nil, nil
	}
	return []schemas.Key{{
		ID:   "openbbc-" + string(a.provider),
		Name: "openbbc",
		// Literal value: schemas.NewSecretVar would treat env./vault.
		// prefixes as references.
		Value: schemas.SecretVar{Val: a.apiKey},
		// An empty whitelist allows no model at all.
		Models: schemas.WhiteList{"*"},
		Weight: 1,
	}}, nil
}

func (a *account) GetConfigForProvider(p schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	if p != a.provider {
		return nil, fmt.Errorf("bifrost: provider %q is not configured", p)
	}
	nc := schemas.DefaultNetworkConfig
	nc.BaseURL = a.baseURL
	return &schemas.ProviderConfig{
		NetworkConfig: nc,
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: schemas.DefaultConcurrency,
			BufferSize:  schemas.DefaultBufferSize,
		},
	}, nil
}
