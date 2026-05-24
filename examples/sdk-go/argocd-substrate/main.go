package main

import (
	"context"
	"fmt"
	"log"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
	"kapro.io/kapro/pkg/kapro/adapter"
	"kapro.io/kapro/pkg/kapro/adapter/argocd"
)

func main() {
	registry := adapter.NewRegistry()
	if err := registry.Register(argocd.New()); err != nil {
		log.Fatalf("register Argo CD adapter: %v", err)
	}

	argo, err := registry.Resolve(kaprov1alpha1.SubstrateKindArgo)
	if err != nil {
		log.Fatalf("resolve Argo CD adapter: %v", err)
	}

	result, err := argo.Discover(context.Background(), adapter.DiscoveryRequest{
		SubstrateKind:  kaprov1alpha1.SubstrateKindArgo,
		ExecutionScope: kaprov1alpha1.ExecutionScopeHub,
		Namespace:      "argocd",
	})
	if err != nil {
		log.Fatalf("discover Argo CD substrate: %v", err)
	}

	fmt.Printf("driver=%s runtime=%s ready=%t selected=%d skipped=%d unsupported=%d\n",
		result.SubstrateKind,
		result.ExecutionScope,
		result.Ready,
		len(result.SelectedObjects),
		len(result.SkippedObjects),
		len(result.UnsupportedPatterns),
	)
	for _, object := range result.SelectedObjects {
		fmt.Printf("selected kind=%s pattern=%s versionField=%s\n", object.Kind, object.Pattern, object.VersionField)
	}
}
