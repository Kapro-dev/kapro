package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunSourceApplyUpdatesGitJSONFieldWithInclude(t *testing.T) {
	repo := t.TempDir()
	writeTestFile(t, repo, "argocd/environments/dev.json", `{"env":"dev","gkProjectVersion":"1.0.0"}`)
	writeTestFile(t, repo, "argocd/environments/prod.json", `{"env":"prod","gkProjectVersion":"1.0.0"}`)
	sourcePath := filepath.Join(repo, "deliveryunits/checkout.yaml")
	writeTestFile(t, repo, "deliveryunits/checkout.yaml", `apiVersion: kapro.io/v1alpha1
kind: DeliveryUnit
metadata:
  name: checkout
spec:
  source:
    units:
    - name: checkout-api
      substrateKind: GitJSONField
      versionField: argocd/environments/*.json:gkProjectVersion
`)
	initTestGitRepo(t, repo)

	err := runSourceApply(sourceApplyOptions{
		RepoPath:   repo,
		SourcePath: sourcePath,
		VersionSet: []string{"checkout-api=2.0.0"},
		Include:    []string{"argocd/environments/dev.json"},
	})
	if err != nil {
		t.Fatal(err)
	}
	dev := readFile(t, filepath.Join(repo, "argocd/environments/dev.json"))
	if !strings.Contains(dev, `"gkProjectVersion": "2.0.0"`) {
		t.Fatalf("dev json was not updated:\n%s", dev)
	}
	prod := readFile(t, filepath.Join(repo, "argocd/environments/prod.json"))
	if !strings.Contains(prod, `"gkProjectVersion":"1.0.0"`) {
		t.Fatalf("prod json should not be updated:\n%s", prod)
	}
}

func TestRunSourceApplyIncludeDoesNotFilterExactFileUnits(t *testing.T) {
	repo := t.TempDir()
	writeTestFile(t, repo, "argocd/applications/api.yaml", `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: api
spec:
  source:
    targetRevision: 1.0.0
`)
	writeTestFile(t, repo, "argocd/environments/dev.json", `{"gkProjectVersion":"1.0.0"}`)
	writeTestFile(t, repo, "argocd/environments/prod.json", `{"gkProjectVersion":"1.0.0"}`)
	sourcePath := filepath.Join(repo, "source.yaml")
	writeTestFile(t, repo, "source.yaml", `apiVersion: kapro.io/v1alpha1
kind: Source
metadata:
  name: checkout
spec:
  units:
  - name: api
    substrateKind: ArgoApplicationSource
    sourcePath: argocd/applications/api.yaml
    versionField: spec.source.targetRevision
  - name: appset
    substrateKind: GitJSONField
    versionField: argocd/environments/*.json:gkProjectVersion
`)
	initTestGitRepo(t, repo)

	err := runSourceApply(sourceApplyOptions{
		RepoPath:   repo,
		SourcePath: sourcePath,
		VersionSet: []string{"api=2.0.0", "appset=2.0.0"},
		Include:    []string{"argocd/environments/dev.json"},
	})
	if err != nil {
		t.Fatal(err)
	}
	app := readFile(t, filepath.Join(repo, "argocd/applications/api.yaml"))
	if !strings.Contains(app, "targetRevision: 2.0.0") {
		t.Fatalf("exact Application mapping was filtered by --include:\n%s", app)
	}
	dev := readFile(t, filepath.Join(repo, "argocd/environments/dev.json"))
	if !strings.Contains(dev, `"gkProjectVersion": "2.0.0"`) {
		t.Fatalf("included env json was not updated:\n%s", dev)
	}
	prod := readFile(t, filepath.Join(repo, "argocd/environments/prod.json"))
	if !strings.Contains(prod, `"gkProjectVersion":"1.0.0"`) {
		t.Fatalf("non-included env json should not be updated:\n%s", prod)
	}
}

func TestRunSourceApplyFailsClosedForMultiFileGlob(t *testing.T) {
	repo := t.TempDir()
	writeTestFile(t, repo, "env/dev.json", `{"version":"1.0.0"}`)
	writeTestFile(t, repo, "env/prod.json", `{"version":"1.0.0"}`)
	sourcePath := filepath.Join(repo, "source.yaml")
	writeTestFile(t, repo, "source.yaml", `apiVersion: kapro.io/v1alpha1
kind: Source
metadata:
  name: checkout
spec:
  units:
  - name: api
    substrateKind: GitJSONField
    versionField: env/*.json:version
`)
	initTestGitRepo(t, repo)

	err := runSourceApply(sourceApplyOptions{
		RepoPath:   repo,
		SourcePath: sourcePath,
		VersionSet: []string{"api=2.0.0"},
	})
	if err == nil || !strings.Contains(err.Error(), "use --include or --all") {
		t.Fatalf("expected multi-file guard, got %v", err)
	}
}

func TestRunSourceApplyRejectsUnknownUnit(t *testing.T) {
	repo := t.TempDir()
	writeTestFile(t, repo, "env/dev.json", `{"version":"1.0.0"}`)
	sourcePath := filepath.Join(repo, "source.yaml")
	writeTestFile(t, repo, "source.yaml", `apiVersion: kapro.io/v1alpha1
kind: Source
metadata:
  name: checkout
spec:
  units:
  - name: api
    substrateKind: GitJSONField
    versionField: env/dev.json:version
`)
	initTestGitRepo(t, repo)

	err := runSourceApply(sourceApplyOptions{
		RepoPath:   repo,
		SourcePath: sourcePath,
		VersionSet: []string{"typo=2.0.0"},
	})
	if err == nil || !strings.Contains(err.Error(), `unknown unit "typo"`) {
		t.Fatalf("expected unknown unit error, got %v", err)
	}
}

func TestRunSourceApplyRejectsConflictingWrites(t *testing.T) {
	repo := t.TempDir()
	writeTestFile(t, repo, "env/dev.json", `{"version":"1.0.0"}`)
	sourcePath := filepath.Join(repo, "source.yaml")
	writeTestFile(t, repo, "source.yaml", `apiVersion: kapro.io/v1alpha1
kind: Source
metadata:
  name: checkout
spec:
  units:
  - name: api
    substrateKind: GitJSONField
    versionField: env/dev.json:version
  - name: worker
    substrateKind: GitJSONField
    versionField: env/dev.json:version
`)
	initTestGitRepo(t, repo)

	err := runSourceApply(sourceApplyOptions{
		RepoPath:   repo,
		SourcePath: sourcePath,
		VersionSet: []string{"api=2.0.0", "worker=3.0.0"},
	})
	if err == nil || !strings.Contains(err.Error(), "conflicting writes") {
		t.Fatalf("expected conflicting write error, got %v", err)
	}
}

func TestRunSourceApplyDoesNotPartiallyWriteOnFailure(t *testing.T) {
	repo := t.TempDir()
	writeTestFile(t, repo, "app.yaml", `spec:
  good: old
  items:
  - tag: old
`)
	sourcePath := filepath.Join(repo, "source.yaml")
	writeTestFile(t, repo, "source.yaml", `apiVersion: kapro.io/v1alpha1
kind: Source
metadata:
  name: checkout
spec:
  units:
  - name: valid
    substrateKind: GitYAMLField
    sourcePath: app.yaml
    versionField: spec.good
  - name: invalid
    substrateKind: GitYAMLField
    sourcePath: app.yaml
    versionField: spec.items[9].tag
`)
	initTestGitRepo(t, repo)

	err := runSourceApply(sourceApplyOptions{
		RepoPath:   repo,
		SourcePath: sourcePath,
		VersionSet: []string{"valid=new", "invalid=new"},
	})
	if err == nil || !strings.Contains(err.Error(), "index 9 out of range") {
		t.Fatalf("expected invalid field error, got %v", err)
	}
	got := readFile(t, filepath.Join(repo, "app.yaml"))
	if !strings.Contains(got, "good: old") || strings.Contains(got, "good: new") {
		t.Fatalf("source apply partially modified file after failure:\n%s", got)
	}
}

func TestRunSourceApplyUpdatesArgoApplicationSourcePath(t *testing.T) {
	repo := t.TempDir()
	writeTestFile(t, repo, "apps/api.yaml", `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: api
spec:
  source:
    repoURL: https://example.com/repo.git
    targetRevision: 1.0.0
    path: apps/api
`)
	sourcePath := filepath.Join(repo, "source.yaml")
	writeTestFile(t, repo, "source.yaml", `apiVersion: kapro.io/v1alpha1
kind: Source
metadata:
  name: checkout
spec:
  units:
  - name: api
    substrateKind: ArgoApplicationSource
    sourcePath: apps/api.yaml
    versionField: spec.source.targetRevision
`)
	initTestGitRepo(t, repo)

	err := runSourceApply(sourceApplyOptions{
		RepoPath:   repo,
		SourcePath: sourcePath,
		VersionSet: []string{"api=main"},
	})
	if err != nil {
		t.Fatal(err)
	}
	app := readFile(t, filepath.Join(repo, "apps/api.yaml"))
	if !strings.Contains(app, "targetRevision: main") {
		t.Fatalf("application was not updated:\n%s", app)
	}
}

func TestRunSourceApplyUpdatesKustomizeImage(t *testing.T) {
	repo := t.TempDir()
	writeTestFile(t, repo, "apps/api/kustomization.yaml", `resources:
- deploy.yaml
images:
- name: example.com/api
  newTag: old
`)
	sourcePath := filepath.Join(repo, "source.yaml")
	writeTestFile(t, repo, "source.yaml", `apiVersion: kapro.io/v1alpha1
kind: Source
metadata:
  name: checkout
spec:
  units:
  - name: api
    substrateKind: KustomizeImage
    sourcePath: apps/api/kustomization.yaml
    versionField: example.com/api
`)
	initTestGitRepo(t, repo)

	err := runSourceApply(sourceApplyOptions{
		RepoPath:   repo,
		SourcePath: sourcePath,
		VersionSet: []string{"api=2.0.0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := readFile(t, filepath.Join(repo, "apps/api/kustomization.yaml"))
	if !strings.Contains(got, "newTag: 2.0.0") {
		t.Fatalf("kustomize image was not updated:\n%s", got)
	}
}

func TestRunSourceApplyIgnoresUntrackedFiles(t *testing.T) {
	repo := t.TempDir()
	writeTestFile(t, repo, "env/dev.json", `{"version":"1.0.0"}`)
	sourcePath := filepath.Join(repo, "source.yaml")
	writeTestFile(t, repo, "source.yaml", `apiVersion: kapro.io/v1alpha1
kind: Source
metadata:
  name: checkout
spec:
  units:
  - name: api
    substrateKind: GitJSONField
    versionField: env/prod.json:version
`)
	initTestGitRepo(t, repo)
	writeTestFile(t, repo, "env/prod.json", `{"version":"1.0.0"}`)

	err := runSourceApply(sourceApplyOptions{
		RepoPath:   repo,
		SourcePath: sourcePath,
		VersionSet: []string{"api=2.0.0"},
	})
	if err == nil || !strings.Contains(err.Error(), "matched no files") {
		t.Fatalf("expected untracked file to be ignored, got %v", err)
	}
}

func TestRunSourceApplyRejectsSymlinkTargets(t *testing.T) {
	repo := t.TempDir()
	external := filepath.Join(t.TempDir(), "external.yaml")
	if err := os.WriteFile(external, []byte("spec:\n  version: old\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(repo, "app.yaml")); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(repo, "source.yaml")
	writeTestFile(t, repo, "source.yaml", `apiVersion: kapro.io/v1alpha1
kind: Source
metadata:
  name: checkout
spec:
  units:
  - name: api
    substrateKind: GitYAMLField
    sourcePath: app.yaml
    versionField: spec.version
`)
	initTestGitRepo(t, repo)

	err := runSourceApply(sourceApplyOptions{
		RepoPath:   repo,
		SourcePath: sourcePath,
		VersionSet: []string{"api=new"},
	})
	if err == nil || !strings.Contains(err.Error(), "refusing to write through symlink") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
	if got := readFile(t, external); !strings.Contains(got, "version: old") {
		t.Fatalf("source apply modified external symlink target:\n%s", got)
	}
}

func TestUpdateYAMLFieldSupportsSequenceIndex(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, "app.yaml")
	if err := os.WriteFile(path, []byte(`spec:
  sources:
  - targetRevision: 1.0.0
  - targetRevision: main
`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := updateStructuredField(path, "spec.sources[0].targetRevision", "2.0.0"); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, path)
	if !strings.Contains(got, "targetRevision: 2.0.0") {
		t.Fatalf("sequence field was not updated:\n%s", got)
	}
}
