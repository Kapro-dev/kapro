package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestQuickstartFluxCreatesPullRepo(t *testing.T) {
	dir := t.TempDir()
	cmd := newQuickstartCmd()
	cmd.SetArgs([]string{"flux", dir, "--name", "checkout"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	cluster := readFile(t, filepath.Join(dir, "clusters/canary-eu.yaml"))
	for _, want := range []string{"mode: pull", "substrateRef: flux", "ociRepository: checkout-bundle"} {
		if !strings.Contains(cluster, want) {
			t.Fatalf("cluster missing %q:\n%s", want, cluster)
		}
	}
}

func TestQuickstartArgoCreatesPushRepo(t *testing.T) {
	dir := t.TempDir()
	cmd := newQuickstartCmd()
	cmd.SetArgs([]string{"argo", dir, "--name", "checkout"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	cluster := readFile(t, filepath.Join(dir, "clusters/canary-eu.yaml"))
	for _, want := range []string{"mode: push", "substrateRef: argo", "application: checkout-canary-eu"} {
		if !strings.Contains(cluster, want) {
			t.Fatalf("cluster missing %q:\n%s", want, cluster)
		}
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
