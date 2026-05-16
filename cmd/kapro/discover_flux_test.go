package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverFluxRepoCommonGitPatterns(t *testing.T) {
	repo := t.TempDir()
	writeFluxFixture(t, repo)
	initTestGitRepo(t, repo)

	result, err := discoverFluxRepo(repo, argoDiscoveryScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	units := map[string]argoDiscoveredUnit{}
	for _, unit := range result.SelectedUnits {
		units[unit.Name] = unit
	}
	assertFluxUnit(t, units, "api", "GitYAMLField", "flux/sources/api-gitrepository.yaml", "spec.ref.tag")
	assertFluxUnit(t, units, "worker", "GitYAMLField", "flux/sources/worker-ocirepository.yaml", "spec.ref.semver")
	assertFluxUnit(t, units, "payments", "GitYAMLField", "flux/helmreleases/payments.yaml", "spec.chart.spec.version")
	assertFluxUnit(t, units, "payments-image", "GitYAMLField", "flux/helmreleases/payments.yaml", "spec.values.image.tag")
	assertFluxUnit(t, units, "payments-containers-api-tag", "GitYAMLField", "flux/helmreleases/payments.yaml", "spec.values.containers.api.tag")
	assertFluxUnit(t, units, "web-image", "KustomizeImage", "apps/web/kustomization.yaml", "ghcr.io/example/checkout-web")
	assertFluxUnit(t, units, "checkout-chart", "GitYAMLField", "charts/checkout/Chart.yaml", "version")
	assertFluxUnit(t, units, "checkout-app", "GitYAMLField", "charts/checkout/Chart.yaml", "appVersion")
}

func TestRunFluxDiscoverWritesMapping(t *testing.T) {
	repo := t.TempDir()
	out := t.TempDir()
	writeFluxFixture(t, repo)
	initTestGitRepo(t, repo)

	err := runFluxDiscover(fluxDiscoverOptions{
		RepoPath:     repo,
		OutPath:      out,
		Name:         "checkout",
		Namespace:    "flux-system",
		Selector:     "kapro.io/import=true",
		MaxFiles:     defaultArgoDiscoveryMaxFiles,
		MaxUnits:     defaultArgoDiscoveryMaxUnits,
		ScanAll:      false,
		PathPrefixes: nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	source := readFile(t, filepath.Join(out, "sources/checkout.yaml"))
	for _, want := range []string{
		"kind: PromotionSource",
		"name: api",
		"backendKind: GitYAMLField",
		"sourcePath: flux/sources/api-gitrepository.yaml",
		"versionField: spec.ref.tag",
		"name: web-image",
		"backendKind: KustomizeImage",
		"versionField: ghcr.io/example/checkout-web",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("source missing %q:\n%s", want, source)
		}
	}
	gitMap := readFile(t, filepath.Join(out, "discovery/kapro-git-map.yaml"))
	for _, want := range []string{
		"schemaVersion: kapro.io/git-adoption/v1alpha1",
		"confidence: high",
		"confidence: needs-review",
		"sourcePath: flux/helmreleases/payments.yaml",
	} {
		if !strings.Contains(gitMap, want) {
			t.Fatalf("git map missing %q:\n%s", want, gitMap)
		}
	}
}

func writeFluxFixture(t *testing.T, repo string) {
	t.Helper()
	writeTestFile(t, repo, "flux/sources/api-gitrepository.yaml", `apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
metadata:
  name: checkout-api
  namespace: flux-system
  labels:
    kapro.io/import: "true"
    service: api
spec:
  interval: 1m
  url: https://github.com/example/checkout-api.git
  ref:
    tag: v1
`)
	writeTestFile(t, repo, "flux/sources/worker-ocirepository.yaml", `apiVersion: source.toolkit.fluxcd.io/v1
kind: OCIRepository
metadata:
  name: checkout-worker
  namespace: flux-system
  labels:
    kapro.io/import: "true"
    service: worker
spec:
  interval: 1m
  url: oci://ghcr.io/example/checkout-worker
  ref:
    semver: 1.x
`)
	writeTestFile(t, repo, "flux/helmreleases/payments.yaml", `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: checkout-payments
  namespace: flux-system
  labels:
    kapro.io/import: "true"
    service: payments
spec:
  interval: 1m
  chart:
    spec:
      chart: checkout-payments
      version: 1.0.0
      sourceRef:
        kind: GitRepository
        name: checkout-charts
        namespace: flux-system
  values:
    image:
      repository: ghcr.io/example/checkout-payments
      tag: 1.0.0
    containers:
      api:
        image: ghcr.io/example/checkout-payments-api
        tag: 1.0.0
`)
	writeTestFile(t, repo, "apps/web/kustomization.yaml", `resources:
  - deploy.yaml
images:
  - name: ghcr.io/example/checkout-web
    newTag: 1.0.0
`)
	writeTestFile(t, repo, "charts/checkout/Chart.yaml", `apiVersion: v2
name: checkout
version: 1.0.0
appVersion: 1.0.0
`)
	writeTestFile(t, repo, "flux/kustomizations/web.yaml", `apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: checkout-web
  namespace: flux-system
spec:
  interval: 1m
  path: ./apps/web
  sourceRef:
    kind: GitRepository
    name: platform
`)
}

func assertFluxUnit(t *testing.T, units map[string]argoDiscoveredUnit, name, backendKind, sourcePath, versionField string) {
	t.Helper()
	unit, ok := units[name]
	if !ok {
		t.Fatalf("missing unit %q in %#v", name, units)
	}
	if unit.BackendKind != backendKind || unit.SourcePath != sourcePath || unit.VersionField != versionField {
		t.Fatalf("unit %q = %#v, want backendKind=%q sourcePath=%q versionField=%q", name, unit, backendKind, sourcePath, versionField)
	}
}
