package main

import (
	"context"
	"fmt"
	"os"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"kapro.io/kapro/internal/cli"
	"kapro.io/kapro/internal/provider"
)

// spokeBootstrap prepares a spoke cluster's delivery system.
// Called after MemberCluster registration (the existing bootstrap flow).
// The actuator type determines what gets installed — the user never
// specifies "Flux" or "ArgoCD" directly.
func spokeBootstrap(ctx context.Context, clusterName string, opts spokeBootstrapOpts) error {
	sp := cli.NewSpinner("Connecting to spoke")
	sp.Start()

	spokeKubeconfig, err := resolveSpokeKubeconfig(ctx, clusterName, opts)
	if err != nil {
		sp.StopFail("Failed to get spoke kubeconfig")
		return err
	}

	cfg, err := clientcmd.RESTConfigFromKubeConfig(spokeKubeconfig)
	if err != nil {
		sp.StopFail("Invalid kubeconfig")
		return fmt.Errorf("parse kubeconfig: %w", err)
	}
	spokeClient, err := client.New(cfg, client.Options{})
	if err != nil {
		sp.StopFail("Failed to connect")
		return fmt.Errorf("create spoke client: %w", err)
	}
	sp.StopSuccess("Connected to spoke")

	// Install delivery system based on actuator type.
	// Currently only flux-operator is supported; future actuators
	// (argocd, sveltos) would add their own setup here.
	sp = cli.NewSpinner("Installing delivery system")
	sp.Start()

	fi := buildDeliverySystemResources()
	if err := spokeClient.Patch(ctx, fi,
		client.Apply,
		client.FieldOwner("kapro-bootstrap"),
		client.ForceOwnership,
	); err != nil {
		sp.StopFail("Failed to install delivery system")
		return fmt.Errorf("apply delivery system: %w", err)
	}
	sp.StopSuccess("Delivery system ready")

	return nil
}

type spokeBootstrapOpts struct {
	kubeconfig   string
	providerName string
	project      string
	location     string
}

func resolveSpokeKubeconfig(ctx context.Context, clusterName string, opts spokeBootstrapOpts) ([]byte, error) {
	if opts.kubeconfig != "" {
		return os.ReadFile(opts.kubeconfig)
	}
	providerName := opts.providerName
	if providerName == "" {
		providerName = provider.Detect()
	}
	p, err := provider.New(providerName, provider.Options{
		KubeconfigPath: opts.kubeconfig,
		Project:        opts.project,
		Location:       opts.location,
		ClusterName:    clusterName,
	})
	if err != nil {
		return nil, err
	}
	return p.GenerateKubeConfig(ctx, clusterName)
}

// buildDeliverySystemResources creates the resources needed to bootstrap
// the delivery system on a spoke. Currently generates a FluxInstance —
// future actuator types would generate different resources.
func buildDeliverySystemResources() *unstructured.Unstructured {
	fi := &unstructured.Unstructured{}
	fi.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "fluxcd.controlplane.io",
		Version: "v1",
		Kind:    "FluxInstance",
	})
	fi.SetName("flux")
	fi.SetNamespace("flux-system")
	fi.Object["apiVersion"] = "fluxcd.controlplane.io/v1"
	fi.Object["kind"] = "FluxInstance"
	fi.Object["spec"] = map[string]interface{}{
		"distribution": map[string]interface{}{
			"version":  "2.x",
			"registry": "ghcr.io/fluxcd",
		},
		"components": []interface{}{
			"source-controller",
			"kustomize-controller",
			"helm-controller",
		},
		"cluster": map[string]interface{}{
			"type": "kubernetes",
		},
	}
	return fi
}
