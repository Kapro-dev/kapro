package mcp

import (
	"context"
	"fmt"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// allResources returns the list of MCP resources Kapro exposes to AI assistants.
func allResources() []Resource {
	return []Resource{
		{
			URI:         "kapro://releases",
			Name:        "releases",
			Description: "All Kapro Release objects across all namespaces.",
			MimeType:    "application/json",
		},
		{
			URI:         "kapro://environments",
			Name:        "environments",
			Description: "All Kapro Environment objects with health and active release status.",
			MimeType:    "application/json",
		},
		{
			URI:         "kapro://promotions/pending-approval",
			Name:        "promotions-pending-approval",
			Description: "All Sync objects currently in WaitingApproval phase.",
			MimeType:    "application/json",
		},
	}
}

// resources reads MCP resource content from the Kubernetes API.
type resources struct {
	client client.Client
}

// read returns the content for the given resource URI.
func (r *resources) read(ctx context.Context, uri string) (*ResourceContent, error) {
	switch {
	case uri == "kapro://releases":
		return r.readReleases(ctx, uri)
	case uri == "kapro://environments":
		return r.readEnvironments(ctx, uri)
	case uri == "kapro://promotions/pending-approval":
		return r.readPendingApprovals(ctx, uri)
	case strings.HasPrefix(uri, "kapro://releases/"):
		parts := strings.TrimPrefix(uri, "kapro://releases/")
		ns, name, ok := splitNamespacedName(parts)
		if !ok {
			return nil, fmt.Errorf("invalid release URI: %s (want kapro://releases/namespace/name)", uri)
		}
		return r.readRelease(ctx, uri, ns, name)
	default:
		return nil, fmt.Errorf("unknown resource URI: %s", uri)
	}
}

func (r *resources) readReleases(ctx context.Context, uri string) (*ResourceContent, error) {
	var list kaprov1alpha1.ReleaseList
	if err := r.client.List(ctx, &list); err != nil {
		return nil, fmt.Errorf("list releases: %w", err)
	}

	type row struct {
		Name      string   `json:"name"`
		Namespace string   `json:"namespace"`
		Artifact  string   `json:"artifact"`
		Phase     string   `json:"phase"`
		Pipelines []string `json:"pipelines"`
	}
	rows := make([]row, 0, len(list.Items))
	for _, rel := range list.Items {
		pipelineNames := make([]string, 0, len(rel.Spec.Pipelines))
		for _, p := range rel.Spec.Pipelines {
			pipelineNames = append(pipelineNames, p.Name)
		}
		rows = append(rows, row{
			Name:      rel.Name,
			Namespace: rel.Namespace,
			Artifact:  rel.Spec.Artifact,
			Phase:     string(rel.Status.Phase),
			Pipelines: pipelineNames,
		})
	}
	text, err := toJSON(rows)
	if err != nil {
		return nil, err
	}
	return &ResourceContent{URI: uri, MimeType: "application/json", Text: text}, nil
}

func (r *resources) readRelease(ctx context.Context, uri, namespace, name string) (*ResourceContent, error) {
	var rel kaprov1alpha1.Release
	if err := r.client.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, &rel); err != nil {
		return nil, fmt.Errorf("get release %s/%s: %w", namespace, name, err)
	}
	text, err := toJSON(rel)
	if err != nil {
		return nil, err
	}
	return &ResourceContent{URI: uri, MimeType: "application/json", Text: text}, nil
}

func (r *resources) readEnvironments(ctx context.Context, uri string) (*ResourceContent, error) {
	var list kaprov1alpha1.EnvironmentList
	if err := r.client.List(ctx, &list); err != nil {
		return nil, fmt.Errorf("list environments: %w", err)
	}

	type row struct {
		Name          string            `json:"name"`
		Labels        map[string]string `json:"labels"`
		Phase         string            `json:"phase"`
		ActiveRelease string            `json:"activeRelease"`
	}
	rows := make([]row, 0, len(list.Items))
	for _, env := range list.Items {
		rows = append(rows, row{
			Name:          env.Name,
			Labels:        env.Labels,
			Phase:         env.Status.Phase,
			ActiveRelease: env.Status.ActiveRelease,
		})
	}
	text, err := toJSON(rows)
	if err != nil {
		return nil, err
	}
	return &ResourceContent{URI: uri, MimeType: "application/json", Text: text}, nil
}

func (r *resources) readPendingApprovals(ctx context.Context, uri string) (*ResourceContent, error) {
	var list kaprov1alpha1.SyncList
	if err := r.client.List(ctx, &list); err != nil {
		return nil, fmt.Errorf("list promotions: %w", err)
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
		if p.Status.Phase == kaprov1alpha1.SyncPhaseWaitingApproval {
			rows = append(rows, row{
				Name:        p.Name,
				Namespace:   p.Namespace,
				Release:     p.Spec.ReleaseRef,
				Environment: p.Spec.EnvironmentRef,
				Phase:       string(p.Status.Phase),
			})
		}
	}
	text, err := toJSON(rows)
	if err != nil {
		return nil, err
	}
	return &ResourceContent{URI: uri, MimeType: "application/json", Text: text}, nil
}

// splitNamespacedName splits "namespace/name" into two parts.
func splitNamespacedName(s string) (namespace, name string, ok bool) {
	idx := strings.Index(s, "/")
	if idx < 0 {
		return "", "", false
	}
	return s[:idx], s[idx+1:], true
}
