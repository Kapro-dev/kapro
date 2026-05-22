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
		"kapro bootstrap greenfield ./promotion-repo --backend flux --mode pull --name checkout",
		"kapro bootstrap brownfield argo . --out ./kapro-connect --name checkout",
		"brownfield starts in Observe mode",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("guide missing %q:\n%s", want, got)
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
