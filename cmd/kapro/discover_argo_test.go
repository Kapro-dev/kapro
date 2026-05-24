package main

import (
	"os"
	"os/exec"
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
	initTestGitRepo(t, repo)

	result, err := discoverArgoRepo(repo, argoDiscoveryScanOptions{})
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
	if unit.SubstrateKind != "GitJSONField" {
		t.Fatalf("substrateKind=%q", unit.SubstrateKind)
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
	initTestGitRepo(t, repo)

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
		"kind: Source",
		"name: checkout-api",
		"substrateKind: ArgoApplicationSource",
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
	review := readFile(t, filepath.Join(out, "discovery/review-summary.yaml"))
	for _, want := range []string{
		"schemaVersion: kapro.io/discovery-review/v1alpha1",
		"kind: argo",
		"readyForAdopt: true",
		"reviewRequired: false",
		"selectedUnits: 1",
		"Apply the observe Substrate first",
	} {
		if !strings.Contains(review, want) {
			t.Fatalf("review summary missing %q:\n%s", want, review)
		}
	}
}

func TestDiscoverArgoRepoMultiSourceApplication(t *testing.T) {
	repo := t.TempDir()
	writeTestFile(t, repo, "argocd/applications/api.yaml", `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: checkout-api
  namespace: argocd
  labels:
    app.kubernetes.io/name: checkout-api
spec:
  sources:
  - repoURL: https://example.com/checkout.git
    targetRevision: 1.0.0
    path: apps/api
  - repoURL: https://example.com/values.git
    targetRevision: main
    ref: values
`)
	initTestGitRepo(t, repo)

	result, err := discoverArgoRepo(repo, argoDiscoveryScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(result.SelectedUnits); got != 1 {
		t.Fatalf("SelectedUnits=%d, want 1: %#v", got, result.SelectedUnits)
	}
	unit := result.SelectedUnits[0]
	if unit.VersionField != "spec.sources[0].targetRevision" {
		t.Fatalf("versionField=%q", unit.VersionField)
	}
}

func TestDiscoverArgoRepoUsesGitPrefixes(t *testing.T) {
	repo := t.TempDir()
	writeTestFile(t, repo, "random/api.yaml", `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: skipped-api
spec:
  source:
    targetRevision: main
`)
	writeTestFile(t, repo, "argocd/api.yaml", `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: selected-api
spec:
  source:
    targetRevision: main
`)
	initTestGitRepo(t, repo)

	result, err := discoverArgoRepo(repo, argoDiscoveryScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(result.Applications); got != 1 {
		t.Fatalf("Applications=%d, want 1", got)
	}
	if result.Applications[0].Name != "selected-api" {
		t.Fatalf("selected app=%q", result.Applications[0].Name)
	}
	result, err = discoverArgoRepo(repo, argoDiscoveryScanOptions{ScanAll: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(result.Applications); got != 2 {
		t.Fatalf("scan all Applications=%d, want 2", got)
	}
}

func TestDiscoverArgoRepoReusesBlobCache(t *testing.T) {
	repo := t.TempDir()
	writeTestFile(t, repo, "argocd/api.yaml", `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: api
spec:
  source:
    targetRevision: main
`)
	initTestGitRepo(t, repo)
	cache := &argoDiscoveryCache{Version: 1, Files: map[string]argoCachedFile{}}

	if _, err := discoverArgoRepo(repo, argoDiscoveryScanOptions{Cache: cache}); err != nil {
		t.Fatal(err)
	}
	if cache.Stats.Misses == 0 {
		t.Fatalf("expected initial cache miss, got %#v", cache.Stats)
	}
	cache.Stats = argoDiscoveryCacheCounters{}
	result, err := discoverArgoRepo(repo, argoDiscoveryScanOptions{Cache: cache})
	if err != nil {
		t.Fatal(err)
	}
	if cache.Stats.Hits == 0 {
		t.Fatalf("expected cache hit, got %#v", cache.Stats)
	}
	if len(result.Applications) != 1 {
		t.Fatalf("cached applications=%d, want 1", len(result.Applications))
	}
}

func TestDiscoverArgoRepoEnforcesMaxFiles(t *testing.T) {
	repo := t.TempDir()
	writeTestFile(t, repo, "argocd/one.yaml", `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: one
spec:
  source:
    targetRevision: main
`)
	writeTestFile(t, repo, "argocd/two.yaml", `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: two
spec:
  source:
    targetRevision: main
`)
	initTestGitRepo(t, repo)

	_, err := discoverArgoRepo(repo, argoDiscoveryScanOptions{MaxFiles: 1})
	if err == nil || !strings.Contains(err.Error(), "--max-files=1") {
		t.Fatalf("expected max files error, got %v", err)
	}
}

func TestDiscoverArgoRepoEnforcesMaxUnits(t *testing.T) {
	repo := t.TempDir()
	writeTestFile(t, repo, "argocd/one.yaml", `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: one
spec:
  source:
    targetRevision: main
`)
	writeTestFile(t, repo, "argocd/two.yaml", `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: two
spec:
  source:
    targetRevision: main
`)
	initTestGitRepo(t, repo)

	_, err := discoverArgoRepo(repo, argoDiscoveryScanOptions{MaxUnits: 1})
	if err == nil || !strings.Contains(err.Error(), "--max-units=1") {
		t.Fatalf("expected max units error, got %v", err)
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

func writeTestFile(t testing.TB, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func initTestGitRepo(t testing.TB, root string) {
	t.Helper()
	runTestGit(t, root, "init")
	runTestGit(t, root, "config", "user.email", "kapro@example.com")
	runTestGit(t, root, "config", "user.name", "Kapro Test")
	runTestGit(t, root, "add", ".")
}

func runTestGit(t testing.TB, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	cmd.Env = cleanGitEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
}
