package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunInitScaffoldArgo(t *testing.T) {
	dir := t.TempDir()
	err := runInitScaffold(scaffoldOptions{
		Path:     dir,
		Name:     "checkout",
		Backend:  "argo",
		Mode:     "push",
		Registry: "oci://registry.example.com/platform",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, relPath := range []string{
		"backends/argo.yaml",
		"promotionplans/checkout.yaml",
		"kapro/checkout.yaml",
		"argo/applications/checkout.yaml",
	} {
		if _, err := os.Stat(filepath.Join(dir, relPath)); err != nil {
			t.Fatalf("%s not generated: %v", relPath, err)
		}
	}
	content := readFile(t, filepath.Join(dir, "backends/argo.yaml"))
	if !strings.Contains(content, "driver: argo") {
		t.Fatalf("backend file missing argo driver:\n%s", content)
	}
	kapro := readFile(t, filepath.Join(dir, "kapro/checkout.yaml"))
	for _, want := range []string{
		"source:",
		"backendRef: argo",
		"name: checkout-api",
	} {
		if !strings.Contains(kapro, want) {
			t.Fatalf("kapro file missing %q:\n%s", want, kapro)
		}
	}
	if strings.Contains(kapro, "sourceRef:") {
		t.Fatalf("kapro scaffold should use inline source, got:\n%s", kapro)
	}
	if _, err := os.Stat(filepath.Join(dir, "sources/checkout.yaml")); !os.IsNotExist(err) {
		t.Fatalf("sources/checkout.yaml should not be generated for the default inline-source scaffold")
	}
}

func TestRunInitScaffoldRepoOnly(t *testing.T) {
	dir := t.TempDir()
	err := runInitScaffold(scaffoldOptions{
		Path:     dir,
		Name:     "checkout",
		Backend:  "argo",
		Mode:     "push",
		Registry: "oci://registry.example.com/platform",
		Clusters: "none",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, relPath := range []string{
		"backends/argo.yaml",
		"sources/checkout.yaml",
		"promotionplans/checkout.yaml",
		"argo/applications/checkout.yaml",
	} {
		if _, err := os.Stat(filepath.Join(dir, relPath)); err != nil {
			t.Fatalf("%s not generated: %v", relPath, err)
		}
	}
	for _, relPath := range []string{
		"clusters/canary.yaml",
		"kapro/checkout.yaml",
		"promotions/checkout-promotion.yaml",
	} {
		if _, err := os.Stat(filepath.Join(dir, relPath)); !os.IsNotExist(err) {
			t.Fatalf("%s should not be generated before clusters exist", relPath)
		}
	}
}

func TestRunInitScaffoldOCIPull(t *testing.T) {
	dir := t.TempDir()
	err := runInitScaffold(scaffoldOptions{
		Path:     dir,
		Name:     "checkout",
		Backend:  "oci",
		Mode:     "pull",
		Registry: "oci://registry.example.com/platform",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, relPath := range []string{
		"backends/oci.yaml",
		"promotionplans/checkout.yaml",
		"clusters/canary.yaml",
		"clusters/prod.yaml",
		"kapro/checkout.yaml",
		"promotions/checkout-promotion.yaml",
	} {
		if _, err := os.Stat(filepath.Join(dir, relPath)); err != nil {
			t.Fatalf("%s not generated: %v", relPath, err)
		}
	}
	for _, relPath := range []string{
		"argo/applications/checkout.yaml",
		"flux/kustomizations/checkout.yaml",
	} {
		if _, err := os.Stat(filepath.Join(dir, relPath)); !os.IsNotExist(err) {
			t.Fatalf("%s should not be generated for oci backend", relPath)
		}
	}
	backend := readFile(t, filepath.Join(dir, "backends/oci.yaml"))
	for _, want := range []string{
		"driver: oci",
		"runtime: Spoke",
		"repository: registry.example.com/platform/{appKey}",
		"tag: \"{version}\"",
	} {
		if !strings.Contains(backend, want) {
			t.Fatalf("backend file missing %q:\n%s", want, backend)
		}
	}
	cluster := readFile(t, filepath.Join(dir, "clusters/canary.yaml"))
	for _, want := range []string{
		"mode: pull",
		"backendRef: oci",
		"namespace: kapro-system",
	} {
		if !strings.Contains(cluster, want) {
			t.Fatalf("cluster file missing %q:\n%s", want, cluster)
		}
	}
	promotion := readFile(t, filepath.Join(dir, "promotions/checkout-promotion.yaml"))
	for _, want := range []string{
		"kind: Promotion",
		"kaproRef: checkout",
		"version: 0.1.0",
	} {
		if !strings.Contains(promotion, want) {
			t.Fatalf("promotion file missing %q:\n%s", want, promotion)
		}
	}
}

func TestRunInitScaffoldOCIRejectsPushMode(t *testing.T) {
	err := runInitScaffold(scaffoldOptions{
		Path:     t.TempDir(),
		Name:     "checkout",
		Backend:  "oci",
		Mode:     "push",
		Registry: "oci://registry.example.com/platform",
	})
	if err == nil || !strings.Contains(err.Error(), "--backend oci requires --mode pull") {
		t.Fatalf("err=%v, want oci pull-mode error", err)
	}
}

func TestRunConnectScaffoldFlux(t *testing.T) {
	dir := t.TempDir()
	err := runConnectScaffold(connectOptions{
		Path:      dir,
		Name:      "flux",
		Backend:   "flux",
		Namespace: "flux-system",
		Selector:  "kapro.io/import=true,team=checkout",
	})
	if err != nil {
		t.Fatal(err)
	}
	content := readFile(t, filepath.Join(dir, "backends/flux-observe.yaml"))
	for _, want := range []string{
		"driver: flux",
		"managementPolicy: Observe",
		"kapro.io/import: \"true\"",
		"team: \"checkout\"",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("missing %q in:\n%s", want, content)
		}
	}
}

func TestParsePromotionRunVersionsRejectsDuplicateUnits(t *testing.T) {
	if _, err := parsePromotionRunVersions([]string{"api=v1", "api=v2"}); err == nil {
		t.Fatal("expected duplicate unit error")
	}
}

func TestDefaultPromotionRunNameIsDNSLabel(t *testing.T) {
	got := defaultPromotionRunName("Checkout.API", "v1.2.3+build.4", nil)
	if got != "checkout-api-v1-2-3-build-4" {
		t.Fatalf("defaultPromotionRunName()=%q", got)
	}
}

func TestDefaultPromotionRunNameAddsHashWhenTruncated(t *testing.T) {
	first := defaultPromotionRunName("checkout", "sha256:"+strings.Repeat("a", 80), nil)
	second := defaultPromotionRunName("checkout", "sha256:"+strings.Repeat("a", 79)+"b", nil)
	if len(first) > 63 || len(second) > 63 {
		t.Fatalf("names exceed DNS label length: %q %q", first, second)
	}
	if first == second {
		t.Fatalf("long versions should keep unique hashed names, got %q", first)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
