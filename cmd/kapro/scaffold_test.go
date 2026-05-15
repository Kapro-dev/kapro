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
		"sources/checkout.yaml",
		"pipelines/checkout.yaml",
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
		"pipelines/checkout.yaml",
		"argo/applications/checkout.yaml",
	} {
		if _, err := os.Stat(filepath.Join(dir, relPath)); err != nil {
			t.Fatalf("%s not generated: %v", relPath, err)
		}
	}
	for _, relPath := range []string{
		"clusters/canary.yaml",
		"kapro/checkout.yaml",
		"releases/checkout-release.yaml",
	} {
		if _, err := os.Stat(filepath.Join(dir, relPath)); !os.IsNotExist(err) {
			t.Fatalf("%s should not be generated before clusters exist", relPath)
		}
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

func TestParseReleaseVersionsRejectsDuplicateUnits(t *testing.T) {
	if _, err := parseReleaseVersions([]string{"api=v1", "api=v2"}); err == nil {
		t.Fatal("expected duplicate unit error")
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
