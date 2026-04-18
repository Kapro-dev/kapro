// Package gitops — LeaderWorkerSet health assessment.
//
// LeaderWorkerSet (LWS) is a Kubernetes API for deploying multi-node model serving
// workloads — a leader pod orchestrates N worker pods as a unit. This is the
// standard pattern for LLM serving on multi-GPU nodes (e.g. vLLM on 8×H100).
//
// LWS GVK: leaderworkerset.x-k8s.io/v1/LeaderWorkerSet
// CRD: https://github.com/kubernetes-sigs/lws
//
// Health semantics:
//   - Healthy:     readyReplicas == replicas AND leaderWorkerSet condition Available=True
//   - Progressing: readyReplicas < replicas (pods still coming up)
//   - Degraded:    any replica group has a failed leader or worker
//   - Unknown:     LWS not found or status not yet populated
package gitops

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pkghealth "kapro.io/kapro/pkg/health"
)

var lwsGVK = schema.GroupVersionResource{
	Group:    "leaderworkerset.x-k8s.io",
	Version:  "v1",
	Resource: "leaderworkersets",
}

// AssessLWS lists LeaderWorkerSet resources in the given namespace and returns
// per-resource health statuses. Integrates into the gitops.Assessor via the
// "LeaderWorkerSet" kind switch.
//
// Called by gitops.Assessor when "LeaderWorkerSet" appears in AssessRequest.Kinds.
func AssessLWS(ctx context.Context, c client.Client, ns string) ([]pkghealth.ResourceHealth, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   lwsGVK.Group,
		Version: lwsGVK.Version,
		Kind:    "LeaderWorkerSetList",
	})

	if err := c.List(ctx, list, client.InNamespace(ns)); err != nil {
		return nil, fmt.Errorf("lws: list LeaderWorkerSets in %s: %w", ns, err)
	}

	results := make([]pkghealth.ResourceHealth, 0, len(list.Items))
	for i := range list.Items {
		results = append(results, assessOne(&list.Items[i]))
	}
	return results, nil
}

// assessOne evaluates a single LWS resource.
//
// Status fields (from the LWS spec/status):
//   - spec.replicas                     → desired replica groups
//   - status.readyReplicas              → ready replica groups
//   - status.conditions[].type=Available → overall availability condition
func assessOne(lws *unstructured.Unstructured) pkghealth.ResourceHealth {
	name := lws.GetName()

	// Read desired replicas.
	desired, found, err := unstructured.NestedInt64(lws.Object, "spec", "replicas")
	if err != nil || !found {
		desired = 1
	}

	// Read ready replicas.
	ready, _, _ := unstructured.NestedInt64(lws.Object, "status", "readyReplicas")

	// Read conditions[].
	availableStatus, availableMsg := readCondition(lws, "Available")
	degradedStatus, degradedMsg := readCondition(lws, "Degraded")

	// Degraded condition present and True → Degraded.
	if degradedStatus == "True" {
		return pkghealth.ResourceHealth{
			Kind:    "LeaderWorkerSet",
			Name:    name,
			Status:  pkghealth.StatusDegraded,
			Message: fmt.Sprintf("degraded: %s", degradedMsg),
		}
	}

	// Available condition present and False → Degraded.
	if availableStatus == "False" {
		return pkghealth.ResourceHealth{
			Kind:    "LeaderWorkerSet",
			Name:    name,
			Status:  pkghealth.StatusDegraded,
			Message: fmt.Sprintf("not available: %s", availableMsg),
		}
	}

	// Some replicas not ready → Progressing.
	if ready < desired {
		return pkghealth.ResourceHealth{
			Kind:    "LeaderWorkerSet",
			Name:    name,
			Status:  pkghealth.StatusProgressing,
			Message: fmt.Sprintf("ready %d/%d replica groups", ready, desired),
		}
	}

	// Available condition True, all replicas ready → Healthy.
	if availableStatus == "True" && ready >= desired {
		return pkghealth.ResourceHealth{
			Kind:    "LeaderWorkerSet",
			Name:    name,
			Status:  pkghealth.StatusHealthy,
			Message: fmt.Sprintf("all %d replica groups ready", desired),
		}
	}

	// Status not yet populated → Unknown.
	return pkghealth.ResourceHealth{
		Kind:    "LeaderWorkerSet",
		Name:    name,
		Status:  pkghealth.StatusUnknown,
		Message: "status not yet populated",
	}
}

// readCondition extracts status + message for the named condition from
// status.conditions[].
func readCondition(lws *unstructured.Unstructured, condType string) (status, message string) {
	conditions, found, _ := unstructured.NestedSlice(lws.Object, "status", "conditions")
	if !found {
		return "", ""
	}
	for _, c := range conditions {
		cond, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if t, _, _ := unstructured.NestedString(cond, "type"); t == condType {
			s, _, _ := unstructured.NestedString(cond, "status")
			m, _, _ := unstructured.NestedString(cond, "message")
			return s, m
		}
	}
	return "", ""
}
