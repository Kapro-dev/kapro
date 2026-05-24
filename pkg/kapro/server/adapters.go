package server

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"

	kaproadapter "kapro.io/kapro/pkg/kapro/adapter"
	adapterargocd "kapro.io/kapro/pkg/kapro/adapter/argocd"
	adapterflux "kapro.io/kapro/pkg/kapro/adapter/flux"
	adapteroci "kapro.io/kapro/pkg/kapro/adapter/oci"
)

// AdapterRegistrationContext is passed to server adapter registrar functions.
type AdapterRegistrationContext struct {
	Registry *kaproadapter.Registry
	Log      logr.Logger
}

// AdapterRegistrar registers one or more public delivery adapters.
type AdapterRegistrar func(context.Context, AdapterRegistrationContext) error

// DefaultAdapterRegistrars returns the built-in reference adapter registrations.
func DefaultAdapterRegistrars() []AdapterRegistrar {
	return []AdapterRegistrar{
		RegisterArgoCDAdapter(),
		RegisterFluxAdapter(),
		RegisterOCIAdapter(),
	}
}

// RegisterAdapter adapts a public adapter implementation into a server registrar.
func RegisterAdapter(adapter kaproadapter.Adapter) AdapterRegistrar {
	return func(_ context.Context, cc AdapterRegistrationContext) error {
		if cc.Registry == nil {
			return fmt.Errorf("adapter registry is nil")
		}
		driver := "<nil>"
		if adapter != nil {
			driver = string(adapter.SubstrateKind())
		}
		if err := cc.Registry.Register(adapter); err != nil {
			return fmt.Errorf("register adapter %q: %w", driver, err)
		}
		return nil
	}
}

// RegisterArgoCDAdapter registers the built-in Argo CD reference adapter.
func RegisterArgoCDAdapter() AdapterRegistrar {
	return RegisterAdapter(adapterargocd.New())
}

// RegisterFluxAdapter registers the built-in Flux reference adapter.
func RegisterFluxAdapter() AdapterRegistrar {
	return RegisterAdapter(adapterflux.New())
}

// RegisterOCIAdapter registers the built-in OCI reference adapter.
func RegisterOCIAdapter() AdapterRegistrar {
	return RegisterAdapter(adapteroci.New())
}
