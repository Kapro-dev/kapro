package main

import (
	"fmt"
	"path/filepath"
	"testing"
)

func BenchmarkDiscoverFluxRepo10000Files(b *testing.B) {
	repo := b.TempDir()
	for i := 0; i < 10000; i++ {
		writeTestFile(b, repo, filepath.Join("apps", fmt.Sprintf("service-%05d", i), "kustomization.yaml"), `resources:
  - deploy.yaml
images:
  - name: ghcr.io/example/service
    newTag: 1.0.0
`)
	}
	initTestGitRepo(b, repo)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, err := discoverFluxRepo(repo, argoDiscoveryScanOptions{MaxFiles: 0, MaxUnits: 0})
		if err != nil {
			b.Fatal(err)
		}
		if len(result.SelectedUnits) != 10000 {
			b.Fatalf("units=%d, want 10000", len(result.SelectedUnits))
		}
	}
}

func BenchmarkDiscoverArgoRepo10000Files(b *testing.B) {
	repo := b.TempDir()
	for i := 0; i < 10000; i++ {
		writeTestFile(b, repo, filepath.Join("argocd", fmt.Sprintf("app-%05d.yaml", i)), fmt.Sprintf(`apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: app-%05d
  namespace: argocd
spec:
  source:
    targetRevision: 1.0.0
`, i))
	}
	initTestGitRepo(b, repo)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, err := discoverArgoRepo(repo, argoDiscoveryScanOptions{MaxFiles: 0, MaxUnits: 0})
		if err != nil {
			b.Fatal(err)
		}
		if len(result.SelectedUnits) != 10000 {
			b.Fatalf("units=%d, want 10000", len(result.SelectedUnits))
		}
	}
}
