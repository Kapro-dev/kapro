// Package bootstrap provides cluster-software-setup primitives used by the
// kapro CLI (cmd/kapro): install Flux Operator + FluxInstance, install Kapro
// CRDs, ensure namespaces, and optionally wire GCP-specific spoke prep (GAR
// registry, Fleet membership). All functions use the controller-runtime client
// with server-side apply — no Helm binary, no kubectl, no gcloud. Idempotent:
// safe to call repeatedly.
//
// This is NOT the Cluster CSR registration path. CSR-based cluster
// registration lives in:
//   - internal/controller/cluster_bootstrap_controller.go (hub approver)
//   - cmd/kapro-cluster-controller/bootstrap.go (spoke CSR client)
//
// internal/bootstrap is the *pre-registration* layer: it gets the right CRDs
// and controllers installed on a cluster (hub or spoke) so that the CSR
// registration flow can subsequently run. The two layers are intentionally
// separate — operators may use either or both depending on whether they want
// Kapro to manage the Flux install or run alongside an existing Flux.
package bootstrap

import (
	"bufio"
	"bytes"
	"context"
	"embed"
	"fmt"
	"io"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

//go:embed crds/*.yaml
var fluxCRDs embed.FS

//go:embed kaprocrds/*.yaml
var kaproCRDs embed.FS

const fieldOwner = "kapro-bootstrap"

// FluxOperatorVersion is the flux-operator version to install.
const FluxOperatorVersion = "v0.48.0"

// FluxDistributionVersion is the Flux distribution version.
const FluxDistributionVersion = "2.x"

// InstallFluxOperator installs the flux-operator on a cluster.
// This creates the namespace, CRDs (FluxInstance, ResourceSet), RBAC, and deployment.
// Idempotent: applies with server-side apply, updates if exists.
func InstallFluxOperator(ctx context.Context, c client.Client) error {
	// Apply embedded Flux Operator CRDs first (FluxInstance, ResourceSet).
	if err := applyEmbeddedCRDs(ctx, c, fluxCRDs, "crds"); err != nil {
		return fmt.Errorf("apply Flux Operator CRDs: %w", err)
	}

	objects := fluxOperatorManifests()
	for _, obj := range objects {
		if err := c.Patch(ctx, obj,
			client.Apply, //nolint:staticcheck // SA1019: client.Apply deprecated but replacement needs larger refactor
			client.FieldOwner(fieldOwner),
			client.ForceOwnership,
		); err != nil {
			// Immutable fields (roleRef, selector) can't be patched — delete and recreate.
			if errors.IsInvalid(err) {
				_ = c.Delete(ctx, obj)
				if createErr := c.Create(ctx, obj); createErr != nil {
					if errors.IsAlreadyExists(createErr) {
						continue // Already exists with compatible spec — skip.
					}
					return fmt.Errorf("recreate %s %s: %w", obj.GetKind(), obj.GetName(), createErr)
				}
				continue
			}
			// Already exists and is compatible — skip.
			if errors.IsAlreadyExists(err) {
				continue
			}
			return fmt.Errorf("apply %s/%s %s: %w",
				obj.GetKind(), obj.GetNamespace(), obj.GetName(), err)
		}
	}
	return nil
}

// InstallFluxInstance creates the FluxInstance CR that bootstraps
// source-controller, kustomize-controller, and helm-controller.
func InstallFluxInstance(ctx context.Context, c client.Client) error {
	fi := fluxInstanceManifest()
	if err := c.Patch(ctx, fi,
		client.Apply, //nolint:staticcheck
		client.FieldOwner(fieldOwner),
		client.ForceOwnership,
	); err != nil {
		return fmt.Errorf("apply FluxInstance: %w", err)
	}
	return nil
}

// InstallKaproCRDs installs the Kapro CRDs on a cluster.
// Used for hub init — spokes don't need Kapro CRDs.
// CRDs are embedded from config/crd/bases/ at build time.
func InstallKaproCRDs(ctx context.Context, c client.Client) error {
	return applyEmbeddedCRDs(ctx, c, kaproCRDs, "kaprocrds")
}

// EnsureNamespace creates a namespace if it doesn't exist.
func EnsureNamespace(ctx context.Context, c client.Client, name string) error {
	ns := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata":   map[string]any{"name": name},
	}}
	return c.Patch(ctx, ns,
		client.Apply, //nolint:staticcheck
		client.FieldOwner(fieldOwner),
		client.ForceOwnership,
	)
}

// --- Manifest builders ---
// These construct unstructured objects matching the flux-operator Helm chart output.
// We build them in Go instead of using Helm to avoid binary dependencies.

func fluxOperatorManifests() []*unstructured.Unstructured {
	image := fmt.Sprintf("ghcr.io/controlplaneio-fluxcd/flux-operator:%s", FluxOperatorVersion)

	return []*unstructured.Unstructured{
		// Namespace
		u(map[string]any{
			"apiVersion": "v1",
			"kind":       "Namespace",
			"metadata":   map[string]any{"name": "flux-system"},
		}),
		// ServiceAccount
		u(map[string]any{
			"apiVersion": "v1",
			"kind":       "ServiceAccount",
			"metadata": map[string]any{
				"name":      "flux-operator",
				"namespace": "flux-system",
			},
		}),
		// ClusterRole
		u(map[string]any{
			"apiVersion": "rbac.authorization.k8s.io/v1",
			"kind":       "ClusterRole",
			"metadata":   map[string]any{"name": "flux-operator"},
			"rules": []any{
				map[string]any{
					"apiGroups": []any{"*"},
					"resources": []any{"*"},
					"verbs":     []any{"*"},
				},
			},
		}),
		// ClusterRoleBinding
		u(map[string]any{
			"apiVersion": "rbac.authorization.k8s.io/v1",
			"kind":       "ClusterRoleBinding",
			"metadata":   map[string]any{"name": "flux-operator"},
			"roleRef": map[string]any{
				"apiGroup": "rbac.authorization.k8s.io",
				"kind":     "ClusterRole",
				"name":     "flux-operator",
			},
			"subjects": []any{
				map[string]any{
					"kind":      "ServiceAccount",
					"name":      "flux-operator",
					"namespace": "flux-system",
				},
			},
		}),
		// Deployment
		u(map[string]any{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata": map[string]any{
				"name":      "flux-operator",
				"namespace": "flux-system",
			},
			"spec": map[string]any{
				"replicas": int64(1),
				"selector": map[string]any{
					"matchLabels": map[string]any{"app": "flux-operator"},
				},
				"template": map[string]any{
					"metadata": map[string]any{
						"labels": map[string]any{"app": "flux-operator"},
					},
					"spec": map[string]any{
						"serviceAccountName": "flux-operator",
						"containers": []any{
							map[string]any{
								"name":  "manager",
								"image": image,
								"ports": []any{
									map[string]any{"containerPort": int64(8080), "name": "metrics"},
									map[string]any{"containerPort": int64(9080), "name": "healthz"},
								},
								"resources": map[string]any{
									"requests": map[string]any{
										"cpu":    "100m",
										"memory": "64Mi",
									},
									"limits": map[string]any{
										"cpu":    "1000m",
										"memory": "256Mi",
									},
								},
								"livenessProbe": map[string]any{
									"httpGet": map[string]any{
										"path": "/healthz",
										"port": int64(9080),
									},
									"initialDelaySeconds": int64(15),
									"periodSeconds":       int64(20),
								},
								"readinessProbe": map[string]any{
									"httpGet": map[string]any{
										"path": "/readyz",
										"port": int64(9080),
									},
									"initialDelaySeconds": int64(5),
									"periodSeconds":       int64(10),
								},
							},
						},
					},
				},
			},
		}),
	}
}

func fluxInstanceManifest() *unstructured.Unstructured {
	return u(map[string]any{
		"apiVersion": "fluxcd.controlplane.io/v1",
		"kind":       "FluxInstance",
		"metadata": map[string]any{
			"name":      "flux",
			"namespace": "flux-system",
		},
		"spec": map[string]any{
			"distribution": map[string]any{
				"version":  FluxDistributionVersion,
				"registry": "ghcr.io/fluxcd",
			},
			"components": []any{
				"source-controller",
				"kustomize-controller",
				"helm-controller",
			},
			"cluster": map[string]any{
				"type": "kubernetes",
			},
		},
	})
}

// applyEmbeddedCRDs parses multi-document YAML files from an embedded FS and
// applies each document as an unstructured object.
func applyEmbeddedCRDs(ctx context.Context, c client.Client, fsys embed.FS, dir string) error {
	entries, err := fsys.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read embedded dir %s: %w", dir, err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := fsys.ReadFile(dir + "/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read %s: %w", entry.Name(), err)
		}

		// Parse multi-document YAML.
		reader := yaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(data)))
		for {
			doc, err := reader.Read()
			if err != nil {
				if err == io.EOF {
					break
				}
				return fmt.Errorf("parse %s: %w", entry.Name(), err)
			}
			if len(bytes.TrimSpace(doc)) == 0 {
				continue
			}

			obj := &unstructured.Unstructured{}
			if err := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(doc), 4096).Decode(obj); err != nil {
				return fmt.Errorf("decode %s: %w", entry.Name(), err)
			}

			if err := c.Patch(ctx, obj,
				client.Apply, //nolint:staticcheck // SA1019: client.Apply deprecated but replacement needs larger refactor
				client.FieldOwner(fieldOwner),
				client.ForceOwnership,
			); err != nil {
				if errors.IsInvalid(err) {
					// CRD might already exist with incompatible version — skip.
					continue
				}
				return fmt.Errorf("apply CRD %s: %w", obj.GetName(), err)
			}
		}
	}
	return nil
}

func u(obj map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: obj}
}
