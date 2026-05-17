package main

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/validation"
)

func TestDefaultLeaderElectionIDForShardIsDNS1123Safe(t *testing.T) {
	id := defaultLeaderElectionID("prod_eu/1")
	if id == "kapro-operator-leader-prod_eu/1.kapro.io" {
		t.Fatal("leader election ID should not embed raw shard name")
	}
	if strings.ContainsAny(id, "_/") {
		t.Fatalf("leader election ID contains invalid raw shard characters: %q", id)
	}
	if errs := validation.IsDNS1123Subdomain(id); len(errs) > 0 {
		t.Fatalf("leader election ID is not DNS-1123 safe: %q: %v", id, errs)
	}
}

func TestDefaultLeaderElectionIDKeepsUnshardedDefault(t *testing.T) {
	if got := defaultLeaderElectionID(""); got != "kapro-operator-leader.kapro.io" {
		t.Fatalf("default leader election ID = %q", got)
	}
}
