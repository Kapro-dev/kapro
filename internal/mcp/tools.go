package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// allTools returns the full list of MCP tools Kapro exposes to AI assistants.
func allTools() []Tool {
	return []Tool{
		{
			Name:        "kapro_list_releases",
			Description: "List all Kapro Release objects with their current phase and artifact version.",
			InputSchema: jsonSchema{
				Type: "object",
				Properties: map[string]schemaProp{
					"namespace": {Type: "string", Description: "Kubernetes namespace. Leave empty for all namespaces."},
				},
			},
		},
		{
			Name:        "kapro_get_release_status",
			Description: "Get detailed status for a specific Release including per-environment promotion state.",
			InputSchema: jsonSchema{
				Type:     "object",
				Required: []string{"name"},
				Properties: map[string]schemaProp{
					"name":      {Type: "string", Description: "Release name."},
					"namespace": {Type: "string", Description: "Kubernetes namespace."},
				},
			},
		},
		{
			Name:        "kapro_list_promotions",
			Description: "List Promotion objects. Filter by release or show only pending-approval ones.",
			InputSchema: jsonSchema{
				Type: "object",
				Properties: map[string]schemaProp{
					"namespace":        {Type: "string", Description: "Kubernetes namespace."},
					"release":          {Type: "string", Description: "Filter by release name."},
					"pending_approval": {Type: "string", Description: "Set to 'true' to show only promotions awaiting approval."},
				},
			},
		},
		{
			Name:        "kapro_approve_promotion",
			Description: "Create an Approval object to unblock a manual promotion gate.",
			InputSchema: jsonSchema{
				Type:     "object",
				Required: []string{"name", "release", "approved_by"},
				Properties: map[string]schemaProp{
					"name":        {Type: "string", Description: "Environment or Batch name to approve."},
					"namespace":   {Type: "string", Description: "Kubernetes namespace."},
					"release":     {Type: "string", Description: "Release name being approved."},
					"approved_by": {Type: "string", Description: "Username or identity of the approver."},
					"comment":     {Type: "string", Description: "Optional approval comment."},
				},
			},
		},
		{
			Name:        "kapro_get_environment_health",
			Description: "Get the health and active release status of a Kapro Environment.",
			InputSchema: jsonSchema{
				Type:     "object",
				Required: []string{"name"},
				Properties: map[string]schemaProp{
					"name":      {Type: "string", Description: "Environment name."},
					"namespace": {Type: "string", Description: "Kubernetes namespace."},
				},
			},
		},
		{
			Name:        "kapro_rollback",
			Description: "Trigger rollback for an active Release in a specific Environment by creating a rollback Approval.",
			InputSchema: jsonSchema{
				Type:     "object",
				Required: []string{"release", "environment"},
				Properties: map[string]schemaProp{
					"release":     {Type: "string", Description: "Release name to roll back."},
					"environment": {Type: "string", Description: "Environment name."},
					"namespace":   {Type: "string", Description: "Kubernetes namespace."},
					"reason":      {Type: "string", Description: "Reason for rollback."},
				},
			},
		},
	}
}

// tools executes MCP tool calls against the Kubernetes API.
type tools struct {
	client client.Client
}

// call dispatches a tool call by name and returns a JSON string result.
func (t *tools) call(ctx context.Context, name string, args map[string]interface{}) (string, error) {
	switch name {
	case "kapro_list_releases":
		return t.listReleases(ctx, strArg(args, "namespace"))
	case "kapro_get_release_status":
		return t.getReleaseStatus(ctx, strArg(args, "name"), strArg(args, "namespace"))
	case "kapro_list_promotions":
		return t.listPromotions(ctx, strArg(args, "namespace"), strArg(args, "release"), strArg(args, "pending_approval") == "true")
	case "kapro_approve_promotion":
		return t.approvePromotion(ctx, args)
	case "kapro_get_environment_health":
		return t.getEnvironmentHealth(ctx, strArg(args, "name"), strArg(args, "namespace"))
	case "kapro_rollback":
		return t.rollback(ctx, args)
	default:
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}

func (t *tools) listReleases(ctx context.Context, namespace string) (string, error) {
	var list kaprov1alpha1.ReleaseList
	opts := []client.ListOption{}
	if namespace != "" {
		opts = append(opts, client.InNamespace(namespace))
	}
	if err := t.client.List(ctx, &list, opts...); err != nil {
		return "", fmt.Errorf("list releases: %w", err)
	}

	type row struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
		Artifact  string `json:"artifact"`
		Phase     string `json:"phase"`
	}
	rows := make([]row, 0, len(list.Items))
	for _, r := range list.Items {
		rows = append(rows, row{
			Name:      r.Name,
			Namespace: r.Namespace,
			Artifact:  r.Spec.Artifact,
			Phase:     string(r.Status.Phase),
		})
	}
	return toJSON(rows)
}

func (t *tools) getReleaseStatus(ctx context.Context, name, namespace string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	if namespace == "" {
		namespace = "default"
	}
	var rel kaprov1alpha1.Release
	if err := t.client.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, &rel); err != nil {
		return "", fmt.Errorf("get release %s/%s: %w", namespace, name, err)
	}

	result := map[string]interface{}{
		"name":      rel.Name,
		"namespace": rel.Namespace,
		"artifact":  rel.Spec.Artifact,
		"phase":     string(rel.Status.Phase),
		"pipeline":  rel.Spec.PipelineRef,
	}
	return toJSON(result)
}

func (t *tools) listPromotions(ctx context.Context, namespace, release string, pendingApprovalOnly bool) (string, error) {
	var list kaprov1alpha1.PromotionList
	opts := []client.ListOption{}
	if namespace != "" {
		opts = append(opts, client.InNamespace(namespace))
	}
	if err := t.client.List(ctx, &list, opts...); err != nil {
		return "", fmt.Errorf("list promotions: %w", err)
	}

	type row struct {
		Name        string `json:"name"`
		Namespace   string `json:"namespace"`
		Release     string `json:"release"`
		Environment string `json:"environment"`
		Phase       string `json:"phase"`
	}
	rows := make([]row, 0)
	for _, p := range list.Items {
		if release != "" && p.Spec.ReleaseRef != release {
			continue
		}
		if pendingApprovalOnly && string(p.Status.Phase) != "WaitingApproval" {
			continue
		}
		rows = append(rows, row{
			Name:        p.Name,
			Namespace:   p.Namespace,
			Release:     p.Spec.ReleaseRef,
			Environment: p.Spec.EnvironmentRef,
			Phase:       string(p.Status.Phase),
		})
	}
	return toJSON(rows)
}

func (t *tools) approvePromotion(ctx context.Context, args map[string]interface{}) (string, error) {
	name := strArg(args, "name")
	release := strArg(args, "release")
	approvedBy := strArg(args, "approved_by")
	comment := strArg(args, "comment")
	namespace := strArg(args, "namespace")
	if namespace == "" {
		namespace = "default"
	}
	if name == "" || release == "" || approvedBy == "" {
		return "", fmt.Errorf("name, release, and approved_by are required")
	}

	approval := &kaprov1alpha1.Approval{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%s", name, release),
			Namespace: namespace,
		},
		Spec: kaprov1alpha1.ApprovalSpec{
			Kind:       kaprov1alpha1.ApprovalKindPromotion,
			Ref:        name,
			Release:    release,
			ApprovedBy: approvedBy,
			Comment:    comment,
		},
	}
	if err := t.client.Create(ctx, approval); err != nil {
		return "", fmt.Errorf("create approval: %w", err)
	}
	return toJSON(map[string]string{
		"status":  "created",
		"approval": approval.Name,
		"message": fmt.Sprintf("Approval %s created — promotion for %s in %s will proceed.", approval.Name, release, name),
	})
}

func (t *tools) getEnvironmentHealth(ctx context.Context, name, namespace string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	// Environment is cluster-scoped in Kapro.
	var env kaprov1alpha1.Environment
	if err := t.client.Get(ctx, client.ObjectKey{Name: name}, &env); err != nil {
		return "", fmt.Errorf("get environment %s: %w", name, err)
	}

	result := map[string]interface{}{
		"name":          env.Name,
		"phase":         env.Status.Phase,
		"activeRelease": env.Status.ActiveRelease,
		"labels":        env.Labels,
	}
	return toJSON(result)
}

func (t *tools) rollback(ctx context.Context, args map[string]interface{}) (string, error) {
	release := strArg(args, "release")
	environment := strArg(args, "environment")
	reason := strArg(args, "reason")
	namespace := strArg(args, "namespace")
	if namespace == "" {
		namespace = "default"
	}
	if release == "" || environment == "" {
		return "", fmt.Errorf("release and environment are required")
	}

	// Rollback is signalled by creating an Approval with Bypass=true (skip gate, force rollback).
	approval := &kaprov1alpha1.Approval{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("rollback-%s-%s", environment, release),
			Namespace: namespace,
		},
		Spec: kaprov1alpha1.ApprovalSpec{
			Kind:       kaprov1alpha1.ApprovalKindPromotion,
			Ref:        environment,
			Release:    release,
			ApprovedBy: "mcp-rollback",
			Bypass:     true,
			Comment:    fmt.Sprintf("ROLLBACK triggered via MCP. Reason: %s", reason),
		},
	}
	if err := t.client.Create(ctx, approval); err != nil {
		return "", fmt.Errorf("create rollback approval: %w", err)
	}
	return toJSON(map[string]string{
		"status":  "rollback_triggered",
		"message": fmt.Sprintf("Rollback triggered for release %s in environment %s.", release, environment),
	})
}

// strArg safely extracts a string value from tool arguments.
func strArg(args map[string]interface{}, key string) string {
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// toJSON serialises a value to a pretty-printed JSON string.
func toJSON(v interface{}) (string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}
