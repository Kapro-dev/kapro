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
	sourcePath := filepath.Join(repo, "sources/checkout.yaml")
	writeTestFile(t, repo, "sources/checkout.yaml", `apiVersion: kapro.io/v1alpha1
kind: PromotionSource
metadata:
  name: checkout
spec:
  units:
  - name: checkout-api
    backendKind: GitJSONField
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

func TestRunSourceApplyFailsClosedForMultiFileGlob(t *testing.T) {
	repo := t.TempDir()
	writeTestFile(t, repo, "env/dev.json", `{"version":"1.0.0"}`)
	writeTestFile(t, repo, "env/prod.json", `{"version":"1.0.0"}`)
	sourcePath := filepath.Join(repo, "source.yaml")
	writeTestFile(t, repo, "source.yaml", `apiVersion: kapro.io/v1alpha1
kind: PromotionSource
metadata:
  name: checkout
spec:
  units:
  - name: api
    backendKind: GitJSONField
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
kind: PromotionSource
metadata:
  name: checkout
spec:
  units:
  - name: api
    backendKind: GitJSONField
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
kind: PromotionSource
metadata:
  name: checkout
spec:
  units:
  - name: api
    backendKind: GitJSONField
    versionField: env/dev.json:version
  - name: worker
    backendKind: GitJSONField
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
kind: PromotionSource
metadata:
  name: checkout
spec:
  units:
  - name: api
    backendKind: ArgoApplicationSource
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
kind: PromotionSource
metadata:
  name: checkout
spec:
  units:
  - name: api
    backendKind: KustomizeImage
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
kind: PromotionSource
metadata:
  name: checkout
spec:
  units:
  - name: api
    backendKind: GitJSONField
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
