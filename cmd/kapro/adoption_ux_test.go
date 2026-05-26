package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateDefaultCreatesDirectRepo(t *testing.T) {
	dir := t.TempDir()
	cmd := newCreateCmd()
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

func TestCreateBareDirectoryCreatesDirectRepo(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(cwd); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})

	cmd := newCreateCmd()
	cmd.SetArgs([]string{"promotion-repo", "--name", "checkout"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	cluster := readFile(t, filepath.Join(tmp, "promotion-repo/clusters/canary-eu.yaml"))
	for _, want := range []string{"mode: push", "ref: direct"} {
		if !strings.Contains(cluster, want) {
			t.Fatalf("cluster missing %q:\n%s", want, cluster)
		}
	}
}

func TestCreateDirectCreatesPushRepo(t *testing.T) {
	dir := t.TempDir()
	cmd := newCreateCmd()
	cmd.SetArgs([]string{"direct", dir, "--name", "checkout"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	substrate := readFile(t, filepath.Join(dir, "substrates/direct.yaml"))
	for _, want := range []string{"kind: KubernetesApplyConfig", "mode: hub-push"} {
		if !strings.Contains(substrate, want) {
			t.Fatalf("substrate missing %q:\n%s", want, substrate)
		}
	}
}

func TestCreateFluxCreatesPullRepo(t *testing.T) {
	dir := t.TempDir()
	cmd := newCreateCmd()
	cmd.SetArgs([]string{"flux", dir, "--name", "checkout"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	cluster := readFile(t, filepath.Join(dir, "clusters/canary-eu.yaml"))
	for _, want := range []string{"mode: pull", "ref: flux", "ociRepository: checkout-bundle"} {
		if !strings.Contains(cluster, want) {
			t.Fatalf("cluster missing %q:\n%s", want, cluster)
		}
	}
}

func TestCreateArgoCreatesPushRepo(t *testing.T) {
	dir := t.TempDir()
	cmd := newCreateCmd()
	cmd.SetArgs([]string{"argo", dir, "--name", "checkout"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	cluster := readFile(t, filepath.Join(dir, "clusters/canary-eu.yaml"))
	for _, want := range []string{"mode: push", "ref: argo", "application: checkout-canary-eu"} {
		if !strings.Contains(cluster, want) {
			t.Fatalf("cluster missing %q:\n%s", want, cluster)
		}
	}
}

func TestCreateRejectsUnknownProfile(t *testing.T) {
	cmd := newCreateCmd()
	cmd.SetArgs([]string{"tekton", t.TempDir()})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "create profile must be direct, argo, flux, oci, or demo") {
		t.Fatalf("err=%v, want create validation", err)
	}
}

func TestSampleMultiRegionCreatesNamedLayout(t *testing.T) {
	dir := t.TempDir()
	cmd := newSampleCmd()
	cmd.SetArgs([]string{"multi-region", dir})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{
		"clusters/canary-eu.yaml",
		"clusters/prod-eu-west.yaml",
		"clusters/prod-us-east.yaml",
		"fleets/checkout.yaml",
	} {
		if got := readFile(t, filepath.Join(dir, rel)); got == "" {
			t.Fatalf("%s was empty", rel)
		}
	}
}

func TestSampleRejectsUnknownLayout(t *testing.T) {
	cmd := newSampleCmd()
	cmd.SetArgs([]string{"unknown"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "unknown sample layout") {
		t.Fatalf("err=%v, want unknown layout validation", err)
	}
}

func TestExplainCommandIsWhyAlias(t *testing.T) {
	cmd := newExplainCmd()
	if cmd.Use != "explain <promotionrun>" {
		t.Fatalf("Use=%q", cmd.Use)
	}
	if !strings.Contains(cmd.Long, "alias for kapro why") {
		t.Fatalf("explain long help should mention why alias:\n%s", cmd.Long)
	}
}
