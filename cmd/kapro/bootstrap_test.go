package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBootstrapGuideExplainsAdoptionPaths(t *testing.T) {
	var buf bytes.Buffer
	printBootstrapGuide(&buf)
	got := buf.String()
	for _, want := range []string{
		"kapro create flux ./promotion-repo --name checkout",
		"kapro create direct ./promotion-repo --name checkout",
		"kapro bootstrap generate ./promotion-repo --profile direct|argo|flux|oci --name checkout",
		"kapro import argo . --out ./kapro-connect --name checkout",
		"pull: each cluster pulls desired state",
		"existing GitOps adoption starts in Observe mode",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("guide missing %q:\n%s", want, got)
		}
	}
}

func TestBootstrapHelpShowsNewRepoNotLegacyTerm(t *testing.T) {
	var buf bytes.Buffer
	cmd := newBootstrapCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.Contains(got, "new-repo") {
		t.Fatalf("bootstrap help missing new-repo command:\n%s", got)
	}
	if strings.Contains(got, "greenfield") {
		t.Fatalf("bootstrap help should not show legacy term:\n%s", got)
	}
}

func TestBootstrapDirectAliasDefaults(t *testing.T) {
	dir := t.TempDir()
	cmd := newBootstrapSubstrateCmd("direct")
	cmd.SetArgs([]string{dir, "--name", "checkout"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	cluster := readFile(t, filepath.Join(dir, "clusters/canary-eu.yaml"))
	for _, want := range []string{"mode: push", "ref: direct", "manifestPath: apps/checkout"} {
		if !strings.Contains(cluster, want) {
			t.Fatalf("cluster missing %q:\n%s", want, cluster)
		}
	}
}

func TestBootstrapSubstrateAliasDefaults(t *testing.T) {
	dir := t.TempDir()
	cmd := newBootstrapSubstrateCmd("argo")
	cmd.SetArgs([]string{dir, "--name", "checkout"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	cluster := readFile(t, filepath.Join(dir, "clusters/canary-eu.yaml"))
	for _, want := range []string{"mode: push", "ref: argo"} {
		if !strings.Contains(cluster, want) {
			t.Fatalf("cluster missing %q:\n%s", want, cluster)
		}
	}
}

func TestBootstrapNewRepoCommandDefaultsToDirectPush(t *testing.T) {
	dir := t.TempDir()
	cmd := newBootstrapNewRepoCmd()
	cmd.SetArgs([]string{dir, "--name", "checkout"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	substrate := readFile(t, filepath.Join(dir, "substrates/direct.yaml"))
	for _, want := range []string{"kind: KubernetesApplyConfig", "namespace: default", "classRef:"} {
		if !strings.Contains(substrate, want) {
			t.Fatalf("substrate missing %q:\n%s", want, substrate)
		}
	}
	cluster := readFile(t, filepath.Join(dir, "clusters/canary-eu.yaml"))
	for _, want := range []string{"mode: push", "ref: direct", "manifestPath: apps/checkout"} {
		if !strings.Contains(cluster, want) {
			t.Fatalf("cluster missing %q:\n%s", want, cluster)
		}
	}
}

func TestBootstrapLegacyNewRepoCommandStillWorks(t *testing.T) {
	dir := t.TempDir()
	cmd := newBootstrapLegacyNewRepoCmd()
	cmd.SetArgs([]string{dir, "--name", "checkout"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "substrates/direct.yaml")); err != nil {
		t.Fatal(err)
	}
}

func TestBootstrapGenerateDirectProfileWritesSubstrateClassRepo(t *testing.T) {
	dir := t.TempDir()
	cmd := newBootstrapGenerateCmd()
	cmd.SetArgs([]string{dir, "--profile", "direct", "--name", "checkout"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	substrate := readFile(t, filepath.Join(dir, "substrates/direct.yaml"))
	for _, want := range []string{
		"name: kubernetes-apply",
		"kind: KubernetesApplyConfig",
		"name: direct",
		"classRef:",
		"name: kubernetes-apply",
		"mode: hub-push",
	} {
		if !strings.Contains(substrate, want) {
			t.Fatalf("direct substrate missing %q:\n%s", want, substrate)
		}
	}
	cluster := readFile(t, filepath.Join(dir, "clusters/canary-eu.yaml"))
	for _, want := range []string{"mode: push", "ref: direct", "manifestPath: apps/checkout"} {
		if !strings.Contains(cluster, want) {
			t.Fatalf("direct cluster missing %q:\n%s", want, cluster)
		}
	}
	deployment := readFile(t, filepath.Join(dir, "apps/checkout/deployment.yaml"))
	if !strings.Contains(deployment, "image: ghcr.io/example/checkout:0.1.0") {
		t.Fatalf("direct deployment did not include default image:\n%s", deployment)
	}
	unit := readFile(t, filepath.Join(dir, "deliveryunits/checkout.yaml"))
	for _, want := range []string{
		"kind: DeliveryUnit",
		"substrateKind: KubernetesManifest",
		"sourcePath: apps/checkout/deployment.yaml",
		"versionField: spec.template.spec.containers[0].image",
		"version: ghcr.io/example/checkout:0.1.0",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("direct delivery unit missing %q:\n%s", want, unit)
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

	substrate := readFile(t, filepath.Join(dir, "substrates/argo.yaml"))
	for _, want := range []string{
		"name: argo",
		"kind: ArgoCDSubstrateConfig",
		"name: argo",
		"namespace: argocd",
		"classRef:",
		"name: argo",
	} {
		if !strings.Contains(substrate, want) {
			t.Fatalf("argocd substrate missing %q:\n%s", want, substrate)
		}
	}
	cluster := readFile(t, filepath.Join(dir, "clusters/canary-eu.yaml"))
	for _, want := range []string{"mode: push", "ref: argo", "application: checkout-canary-eu"} {
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

	substrate := readFile(t, filepath.Join(dir, "substrates/flux.yaml"))
	for _, want := range []string{
		"name: flux",
		"kind: FluxSubstrateConfig",
		"namespace: flux-system",
		"classRef:",
		"mode: spoke-pull",
	} {
		if !strings.Contains(substrate, want) {
			t.Fatalf("flux substrate missing %q:\n%s", want, substrate)
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

func TestBootstrapGenerateOCIProfileWritesClassConfig(t *testing.T) {
	dir := t.TempDir()
	cmd := newBootstrapGenerateCmd()
	cmd.SetArgs([]string{dir, "--profile", "oci", "--name", "checkout"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	substrate := readFile(t, filepath.Join(dir, "substrates/oci.yaml"))
	for _, want := range []string{
		"name: oci",
		"kind: OCIBundleApplyConfig",
		"namespace: kapro-system",
		"classRef:",
		"mode: spoke-pull",
	} {
		if !strings.Contains(substrate, want) {
			t.Fatalf("oci substrate missing %q:\n%s", want, substrate)
		}
	}
	cluster := readFile(t, filepath.Join(dir, "clusters/canary-eu.yaml"))
	for _, want := range []string{"mode: pull", "ref: oci"} {
		if !strings.Contains(cluster, want) {
			t.Fatalf("oci cluster missing %q:\n%s", want, cluster)
		}
	}
}

func TestBootstrapGenerateRejectsUnknownProfile(t *testing.T) {
	cmd := newBootstrapGenerateCmd()
	cmd.SetArgs([]string{t.TempDir(), "--profile", "tekton"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--profile must be direct, argo, flux, or oci") {
		t.Fatalf("err=%v, want profile validation", err)
	}
}

func TestBootstrapExistingGitOpsFluxWritesObserveMapping(t *testing.T) {
	repo := t.TempDir()
	out := t.TempDir()
	writeFluxFixture(t, repo)
	initTestGitRepo(t, repo)

	if err := runBootstrapExistingGitOps(bootstrapExistingGitOpsOptions{
		Substrate: "flux",
		RepoPath:  repo,
		OutPath:   out,
		Name:      "checkout",
		Selector:  "kapro.io/import=true",
		MaxFiles:  defaultArgoDiscoveryMaxFiles,
		MaxUnits:  defaultArgoDiscoveryMaxUnits,
	}); err != nil {
		t.Fatal(err)
	}

	substrate := readFile(t, filepath.Join(out, "substrates/checkout-observe.yaml"))
	for _, want := range []string{"kind: SubstrateClass", "kind: FluxSubstrateConfig", "classRef:", "configRef:", "mode: hub-push", "managementPolicy: Observe"} {
		if !strings.Contains(substrate, want) {
			t.Fatalf("substrate missing %q:\n%s", want, substrate)
		}
	}
	if strings.Contains(substrate, "actuator:") {
		t.Fatalf("observe mapping should not emit legacy actuator field:\n%s", substrate)
	}
	source := readFile(t, filepath.Join(out, "deliveryunits/checkout.yaml"))
	for _, want := range []string{"kind: DeliveryUnit", "source:", "substrateKind: GitYAMLField", "versionField: spec.ref.tag"} {
		if !strings.Contains(source, want) {
			t.Fatalf("delivery unit missing %q:\n%s", want, source)
		}
	}
}

func TestBootstrapExistingGitOpsRejectsUnknownSubstrate(t *testing.T) {
	err := runBootstrapExistingGitOps(bootstrapExistingGitOpsOptions{Substrate: "jenkins"})
	if err == nil || !strings.Contains(err.Error(), "substrate must be argo or flux") {
		t.Fatalf("err=%v, want substrate validation", err)
	}
}
