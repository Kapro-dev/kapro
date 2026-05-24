package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestBootstrapGuideExplainsAdoptionPaths(t *testing.T) {
	var buf bytes.Buffer
	printBootstrapGuide(&buf)
	got := buf.String()
	for _, want := range []string{
		"kapro bootstrap generate ./promotion-repo --profile flux --name checkout",
		"kapro bootstrap generate ./promotion-repo --profile direct --name checkout",
		"kapro adopt argo . --out ./kapro-connect --name checkout",
		"pull: each cluster pulls desired state",
		"existing GitOps adoption starts in Observe mode",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("guide missing %q:\n%s", want, got)
		}
	}
}

func TestBootstrapBackendAliasDefaults(t *testing.T) {
	dir := t.TempDir()
	cmd := newBootstrapBackendCmd("argo")
	cmd.SetArgs([]string{dir, "--name", "checkout"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	cluster := readFile(t, filepath.Join(dir, "clusters/canary-eu.yaml"))
	for _, want := range []string{"mode: push", "backendRef: argo"} {
		if !strings.Contains(cluster, want) {
			t.Fatalf("cluster missing %q:\n%s", want, cluster)
		}
	}
}

func TestBootstrapGreenfieldCommandDefaultsToFluxPull(t *testing.T) {
	dir := t.TempDir()
	cmd := newBootstrapGreenfieldCmd()
	cmd.SetArgs([]string{dir, "--name", "checkout"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	backend := readFile(t, filepath.Join(dir, "backends/flux.yaml"))
	for _, want := range []string{"driver: flux", "namespace: flux-system"} {
		if !strings.Contains(backend, want) {
			t.Fatalf("backend missing %q:\n%s", want, backend)
		}
	}
	cluster := readFile(t, filepath.Join(dir, "clusters/canary-eu.yaml"))
	for _, want := range []string{"mode: pull", "backendRef: flux", "ociRepository: checkout-bundle"} {
		if !strings.Contains(cluster, want) {
			t.Fatalf("cluster missing %q:\n%s", want, cluster)
		}
	}
}

func TestBootstrapGenerateDirectProfileWritesSubstrateClassRepo(t *testing.T) {
	dir := t.TempDir()
	cmd := newBootstrapGenerateCmd()
	cmd.SetArgs([]string{dir, "--profile", "direct", "--name", "checkout"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	backend := readFile(t, filepath.Join(dir, "backends/direct.yaml"))
	for _, want := range []string{
		"name: kubernetes-apply",
		"kind: KubernetesApplyConfig",
		"name: direct",
		"classRef:",
		"name: kubernetes-apply",
		"mode: hub-push",
	} {
		if !strings.Contains(backend, want) {
			t.Fatalf("direct backend missing %q:\n%s", want, backend)
		}
	}
	cluster := readFile(t, filepath.Join(dir, "clusters/canary-eu.yaml"))
	for _, want := range []string{"mode: push", "backendRef: direct", "manifestPath: apps/checkout"} {
		if !strings.Contains(cluster, want) {
			t.Fatalf("direct cluster missing %q:\n%s", want, cluster)
		}
	}
	deployment := readFile(t, filepath.Join(dir, "apps/checkout/deployment.yaml"))
	if !strings.Contains(deployment, "image: ghcr.io/example/checkout:0.1.0") {
		t.Fatalf("direct deployment did not include default image:\n%s", deployment)
	}
	fleet := readFile(t, filepath.Join(dir, "fleets/checkout.yaml"))
	for _, want := range []string{
		"backendKind: KubernetesManifest",
		"sourcePath: apps/checkout/deployment.yaml",
		"versionField: spec.template.spec.containers[0].image",
		"version: ghcr.io/example/checkout:0.1.0",
	} {
		if !strings.Contains(fleet, want) {
			t.Fatalf("direct fleet missing %q:\n%s", want, fleet)
		}
	}
	workflow := readFile(t, filepath.Join(dir, ".github/workflows/kapro-validate.yaml"))
	if !strings.Contains(workflow, "Parse YAML") {
		t.Fatalf("generated validation workflow missing YAML parse step:\n%s", workflow)
	}
}

func TestBootstrapGenerateArgocdProfileWritesClassConfig(t *testing.T) {
	dir := t.TempDir()
	cmd := newBootstrapGenerateCmd()
	cmd.SetArgs([]string{dir, "--profile", "argocd", "--name", "checkout"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	backend := readFile(t, filepath.Join(dir, "backends/argo.yaml"))
	for _, want := range []string{
		"name: argo-cd",
		"kind: ArgoCDSubstrateConfig",
		"name: argo",
		"namespace: argocd",
		"classRef:",
		"name: argo-cd",
	} {
		if !strings.Contains(backend, want) {
			t.Fatalf("argocd backend missing %q:\n%s", want, backend)
		}
	}
	cluster := readFile(t, filepath.Join(dir, "clusters/canary-eu.yaml"))
	for _, want := range []string{"mode: push", "backendRef: argo", "application: checkout-canary-eu"} {
		if !strings.Contains(cluster, want) {
			t.Fatalf("argocd cluster missing %q:\n%s", want, cluster)
		}
	}
	apps := readFile(t, filepath.Join(dir, "argo/applications/checkout.yaml"))
	for _, want := range []string{"name: checkout-canary-eu", "name: checkout-prod-eu", "kapro.io/managed-by: kapro"} {
		if !strings.Contains(apps, want) {
			t.Fatalf("argocd application missing %q:\n%s", want, apps)
		}
	}
	_ = readFile(t, filepath.Join(dir, "apps/checkout/deployment.yaml"))
}

func TestBootstrapGenerateFluxProfileWritesClassConfigAndWorkload(t *testing.T) {
	dir := t.TempDir()
	cmd := newBootstrapGenerateCmd()
	cmd.SetArgs([]string{dir, "--profile", "flux", "--name", "checkout"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	backend := readFile(t, filepath.Join(dir, "backends/flux.yaml"))
	for _, want := range []string{
		"name: flux",
		"kind: FluxSubstrateConfig",
		"namespace: flux-system",
		"classRef:",
		"mode: spoke-pull",
	} {
		if !strings.Contains(backend, want) {
			t.Fatalf("flux backend missing %q:\n%s", want, backend)
		}
	}
	flux := readFile(t, filepath.Join(dir, "flux/kustomizations/checkout.yaml"))
	for _, want := range []string{"kind: GitRepository", "kind: Kustomization", "path: ./apps/checkout"} {
		if !strings.Contains(flux, want) {
			t.Fatalf("flux kustomization missing %q:\n%s", want, flux)
		}
	}
	_ = readFile(t, filepath.Join(dir, "apps/checkout/deployment.yaml"))
	_ = readFile(t, filepath.Join(dir, "apps/checkout/kustomization.yaml"))
}

func TestBootstrapGenerateRejectsUnknownProfile(t *testing.T) {
	cmd := newBootstrapGenerateCmd()
	cmd.SetArgs([]string{t.TempDir(), "--profile", "tekton"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--profile must be direct, argocd, or flux") {
		t.Fatalf("err=%v, want profile validation", err)
	}
}

func TestBootstrapBrownfieldFluxWritesObserveMapping(t *testing.T) {
	repo := t.TempDir()
	out := t.TempDir()
	writeFluxFixture(t, repo)
	initTestGitRepo(t, repo)

	if err := runBootstrapBrownfield(bootstrapBrownfieldOptions{
		Backend:  "flux",
		RepoPath: repo,
		OutPath:  out,
		Name:     "checkout",
		Selector: "kapro.io/import=true",
		MaxFiles: defaultArgoDiscoveryMaxFiles,
		MaxUnits: defaultArgoDiscoveryMaxUnits,
	}); err != nil {
		t.Fatal(err)
	}

	backend := readFile(t, filepath.Join(out, "backends/checkout-observe.yaml"))
	for _, want := range []string{"driver: flux", "managementPolicy: Observe"} {
		if !strings.Contains(backend, want) {
			t.Fatalf("backend missing %q:\n%s", want, backend)
		}
	}
	source := readFile(t, filepath.Join(out, "sources/checkout.yaml"))
	for _, want := range []string{"kind: Source", "backendKind: GitYAMLField", "versionField: spec.ref.tag"} {
		if !strings.Contains(source, want) {
			t.Fatalf("source missing %q:\n%s", want, source)
		}
	}
}

func TestBootstrapBrownfieldRejectsUnknownBackend(t *testing.T) {
	err := runBootstrapBrownfield(bootstrapBrownfieldOptions{Backend: "jenkins"})
	if err == nil || !strings.Contains(err.Error(), "backend must be argo or flux") {
		t.Fatalf("err=%v, want backend validation", err)
	}
}
