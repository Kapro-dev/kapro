package main

import (
	"context"
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

// statusReporter periodically publishes Cluster.status with the spoke's
// observed cluster capabilities (node count, K8s version, region heuristics)
// and a best-effort health summary. The per-cluster RBAC issued during
// bootstrap (PR-2) only allows writing this cluster's own
// clusters/status subresource — enforced via resourceNames.
//
// PR-3 scope: capabilities + nodeCount + heartbeat-influenced phase. Real
// workload health (Pods/Deployments managed by Kapro) is computed in PR-4
// once the OCI Delivery Core has labelled the resources it owns.
type statusReporter struct {
	Hub         *HubClient
	Local       client.Client
	ClusterName string
	Interval    time.Duration

	// ControllerVersion is the version of the spoke binary itself. Empty for
	// dev builds; set from the version package once the spoke participates
	// in the project's release pipeline.
	ControllerVersion string
}

func (s *statusReporter) Run(ctx context.Context) {
	logger := log.Log.WithName("status").WithValues("cluster", s.ClusterName)
	if s.Interval <= 0 {
		s.Interval = 60 * time.Second
	}
	if err := s.tick(ctx); err != nil {
		logger.Error(err, "first status report failed")
	}
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.tick(ctx); err != nil {
				logger.Error(err, "status report failed")
			}
		}
	}
}

// tick computes the current status snapshot locally and patches the hub.
// One round-trip; never blocks more than a few seconds.
func (s *statusReporter) tick(ctx context.Context) error {
	tctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	caps, err := s.observeCapabilities(tctx)
	if err != nil {
		return fmt.Errorf("observe capabilities: %w", err)
	}

	hub, err := s.Hub.Client()
	if err != nil {
		return err
	}

	fc := &kaprov1alpha2.Cluster{}
	if err := hub.Get(tctx, client.ObjectKey{Name: s.ClusterName}, fc); err != nil {
		return fmt.Errorf("get Cluster %q: %w", s.ClusterName, err)
	}
	patch := client.MergeFrom(fc.DeepCopy())

	fc.Status.ObservedGeneration = fc.Generation
	fc.Status.Capabilities = caps
	if s.ControllerVersion != "" {
		fc.Status.ControllerVersion = s.ControllerVersion
	}
	// PR-3 doesn't observe workload health (no OCI Delivery Core yet); leave
	// existing Health untouched so we don't downgrade signals the hub set.
	// PR-4 will own writes to status.health.

	if err := hub.Status().Patch(tctx, fc, patch); err != nil {
		if apierrors.IsForbidden(err) {
			return fmt.Errorf("per-cluster RBAC missing or not yet granted for status patch: %w", err)
		}
		return fmt.Errorf("patch Cluster status: %w", err)
	}
	return nil
}

// observeCapabilities returns the cluster's K8s version + node inventory.
// Read from the LOCAL spoke cluster — we never call into the hub for these.
func (s *statusReporter) observeCapabilities(ctx context.Context) (kaprov1alpha2.ClusterCapabilities, error) {
	caps := kaprov1alpha2.ClusterCapabilities{}

	// Node count + a representative kubelet version. List failures are
	// surfaced as errors (callers abort the status tick) rather than
	// silently swallowed: a missing nodes RBAC binding during install
	// should be loud, not invisible. Operators can grant the binding
	// (or scope it tighter — read-only on Nodes is sufficient) and the
	// next tick succeeds.
	var nodes corev1.NodeList
	if err := s.Local.List(ctx, &nodes); err != nil {
		return caps, fmt.Errorf("list nodes: %w", err)
	}
	caps.NodeCount = len(nodes.Items)
	if v := majorityKubeletVersion(nodes.Items); v != "" {
		caps.K8sVersion = v
	}
	if region, zone := majorityRegionZone(nodes.Items); region != "" {
		caps.Region = region
		caps.Zone = zone
	}
	if cloud := guessCloud(nodes.Items); cloud != "" {
		caps.Cloud = cloud
	}
	return caps, nil
}

// majorityKubeletVersion picks the most-common kubeletVersion across nodes.
// Useful for mixed-version drain windows where a single .Items[0] is wrong.
func majorityKubeletVersion(items []corev1.Node) string {
	if len(items) == 0 {
		return ""
	}
	counts := map[string]int{}
	for _, n := range items {
		v := n.Status.NodeInfo.KubeletVersion
		if v != "" {
			counts[v]++
		}
	}
	return mostFrequent(counts)
}

// majorityRegionZone reads the well-known topology labels.
func majorityRegionZone(items []corev1.Node) (region, zone string) {
	if len(items) == 0 {
		return "", ""
	}
	regions := map[string]int{}
	zones := map[string]int{}
	for _, n := range items {
		if r := n.Labels[corev1.LabelTopologyRegion]; r != "" {
			regions[r]++
		}
		if z := n.Labels[corev1.LabelTopologyZone]; z != "" {
			zones[z]++
		}
	}
	return mostFrequent(regions), mostFrequent(zones)
}

// guessCloud derives cloud provider from Node.Spec.ProviderID.
// Examples: "gce://...", "aws://...", "azure://...". Empty when unknown.
func guessCloud(items []corev1.Node) string {
	for _, n := range items {
		switch {
		case startsWithPrefix(n.Spec.ProviderID, "gce://"):
			return "gcp"
		case startsWithPrefix(n.Spec.ProviderID, "aws://"):
			return "aws"
		case startsWithPrefix(n.Spec.ProviderID, "azure://"):
			return "azure"
		case startsWithPrefix(n.Spec.ProviderID, "digitalocean://"):
			return "digitalocean"
		}
	}
	return ""
}

func startsWithPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func mostFrequent(counts map[string]int) string {
	if len(counts) == 0 {
		return ""
	}
	// Stable tiebreak by key to keep the test-friendly behaviour deterministic.
	type kv struct {
		k string
		v int
	}
	all := make([]kv, 0, len(counts))
	for k, v := range counts {
		all = append(all, kv{k, v})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].v != all[j].v {
			return all[i].v > all[j].v
		}
		return all[i].k < all[j].k
	})
	return all[0].k
}
