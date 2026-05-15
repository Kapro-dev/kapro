package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverArgoRepoApplicationSetGitFiles(t *testing.T) {
	repo := t.TempDir()
	writeTestFile(t, repo, "argocd/app-of-apps.yaml", `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: platform-root
  namespace: argocd
spec:
  source:
    repoURL: https://example.com/platform.git
    targetRevision: main
    path: argocd/applicationsets
`)
	writeTestFile(t, repo, "argocd/applicationsets/pos-server.yaml", `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: pos-server
  namespace: argocd
spec:
  generators:
  - matrix:
      generators:
      - git:
          repoURL: https://example.com/platform.git
          revision: dev
          files:
          - path: argocd/environments/*.json
      - list:
          elements:
          - appName: pos-server
  template:
    metadata:
      name: '{{.appName}}-{{.env}}'
      labels:
        kapro.io/import: "true"
        service: pos-server
    spec:
      sources:
      - repoURL: oci://example.com/pos-server
        targetRevision: '{{.gkProjectVersion}}'
        path: .
      - repoURL: '{{.repoUrl}}'
        targetRevision: '{{.branch}}'
        ref: values
`)
	writeTestFile(t, repo, "argocd/environments/dev.json", `{"env":"dev","gkProjectVersion":"1.0.0"}`)

	result, err := discoverArgoRepo(repo)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(result.ApplicationSets); got != 1 {
		t.Fatalf("ApplicationSets=%d, want 1", got)
	}
	if got := len(result.SelectedUnits); got != 1 {
		t.Fatalf("SelectedUnits=%d, want 1: %#v", got, result.SelectedUnits)
	}
	unit := result.SelectedUnits[0]
	if unit.Name != "pos-server" {
		t.Fatalf("unit name=%q", unit.Name)
	}
	if unit.BackendKind != "GitJSONField" {
		t.Fatalf("backendKind=%q", unit.BackendKind)
	}
	if unit.VersionField != "argocd/environments/*.json:gkProjectVersion" {
		t.Fatalf("versionField=%q", unit.VersionField)
	}
	if unit.Confidence != "high" {
		t.Fatalf("confidence=%q", unit.Confidence)
	}
	if got := len(result.SkippedObjects); got != 1 {
		t.Fatalf("SkippedObjects=%d, want app-of-apps root", got)
	}
}

func TestRunArgoDiscoverWritesMapping(t *testing.T) {
	repo := t.TempDir()
	out := t.TempDir()
	writeTestFile(t, repo, "apps/api.yaml", `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: checkout-api-prod
  namespace: argocd
  labels:
    app.kubernetes.io/name: checkout-api
spec:
  source:
    repoURL: https://example.com/checkout.git
    targetRevision: 1.0.0
    path: apps/api
`)

	err := runArgoDiscover(argoDiscoverOptions{
		RepoPath:  repo,
		OutPath:   out,
		Name:      "checkout",
		Namespace: "argocd",
		Selector:  "kapro.io/import=true",
	})
	if err != nil {
		t.Fatal(err)
	}
	source := readFile(t, filepath.Join(out, "sources/checkout.yaml"))
	for _, want := range []string{
		"kind: PromotionSource",
		"name: checkout-api",
		"backendKind: ArgoApplicationSource",
		"sourcePath: apps/api.yaml",
		"versionField: spec.source.targetRevision",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("source missing %q:\n%s", want, source)
		}
	}
	gitMap := readFile(t, filepath.Join(out, "discovery/kapro-git-map.yaml"))
	for _, want := range []string{
		"schemaVersion: kapro.io/git-adoption/v1alpha1",
		"confidence: high",
		"sourcePath: apps/api.yaml",
	} {
		if !strings.Contains(gitMap, want) {
			t.Fatalf("git map missing %q:\n%s", want, gitMap)
		}
	}
}

func TestLooksLikeGitRemote(t *testing.T) {
	tests := map[string]bool{
		"https://github.com/Kapro-dev/kapro.git": true,
		"ssh://git@example.com/repo.git":         true,
		"git@example.com:org/repo.git":           true,
		"/tmp/local-checkout":                    false,
		"./relative-checkout":                    false,
	}
	for input, want := range tests {
		if got := looksLikeGitRemote(input); got != want {
			t.Fatalf("looksLikeGitRemote(%q)=%v, want %v", input, got, want)
		}
	}
}

func writeTestFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
