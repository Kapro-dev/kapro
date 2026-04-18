// Package kagent implements the KAgent controller — Kapro's autonomous release trigger.
//
// KAgent is a first-class Kapro controller, managed by the Kapro operator lifecycle
// alongside Promotion, Batch, Approval, and BootstrapToken controllers.
// It is a "producer" plugin: it detects new versions from external systems and emits
// Release CRs. Kapro owns the lifecycle — KAgent is just the detection layer.
//
// The same way Kubernetes owns the Pod lifecycle and delegates execution to containerd,
// Kapro owns the Release lifecycle and delegates detection to KAgent.
//
//	MLflow pushes new model to Production stage
//	  → KAgent detects new version via polling
//	    → KAgent creates a Kapro Release CR
//	      → Kapro promotes it through dev → prod waves
//
// The controller is a polling reconciler: it reconciles on a fixed interval
// (spec.pollInterval, default 60s) and on KAgent object changes.
// It is intentionally stateless between reconciles — the only durable state
// is status.lastVersion, which prevents duplicate Release creation.
//
// Source types supported:
//   - mlflow:      MLflow Model Registry (Production/Staging stage filter)
//   - oci:         OCI registry tag watcher (regexp tag matching)
//   - prometheus:  Prometheus metric threshold crossing
package kagent

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

const (
	conditionReady  = "Ready"
	conditionFailed = "Failed"

	defaultPollInterval = 60 * time.Second
)

// KAgentReconciler drives the KAgent polling loop.
type KAgentReconciler struct {
	client.Client
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=kapro.io,resources=kagents,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kapro.io,resources=kagents/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=releases,verbs=get;list;watch;create
// +kubebuilder:rbac:groups=kapro.io,resources=artifacts,verbs=get;list;watch;create

func (r *KAgentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var agent kaprov1alpha1.KAgent
	if err := r.Get(ctx, req.NamespacedName, &agent); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if agent.Spec.Suspend {
		log.Info("agent suspended, skipping")
		return ctrl.Result{RequeueAfter: pollInterval(&agent)}, nil
	}

	version, err := r.detectVersion(ctx, &agent)
	if err != nil {
		r.setCondition(&agent, conditionFailed, metav1.ConditionTrue, "DetectFailed", err.Error())
		_ = r.Status().Update(ctx, &agent)
		r.Recorder.Eventf(&agent, "Warning", "DetectFailed", "version detection failed: %v", err)
		return ctrl.Result{RequeueAfter: pollInterval(&agent)}, nil
	}

	agent.Status.ObservedVersion = version
	agent.Status.Active = true
	r.setCondition(&agent, conditionFailed, metav1.ConditionFalse, "OK", "")

	// Only trigger if version changed since last trigger.
	if version == "" || version == agent.Status.LastVersion {
		_ = r.Status().Update(ctx, &agent)
		return ctrl.Result{RequeueAfter: pollInterval(&agent)}, nil
	}

	log.Info("new version detected, creating Release", "version", version)

	releaseName, err := r.createRelease(ctx, &agent, version)
	if err != nil {
		r.setCondition(&agent, conditionFailed, metav1.ConditionTrue, "ReleaseFailed", err.Error())
		_ = r.Status().Update(ctx, &agent)
		r.Recorder.Eventf(&agent, "Warning", "ReleaseFailed", "create Release failed: %v", err)
		return ctrl.Result{RequeueAfter: pollInterval(&agent)}, nil
	}

	agent.Status.LastVersion = version
	agent.Status.LastRelease = releaseName
	agent.Status.LastTriggerAt = time.Now().UTC().Format(time.RFC3339)
	r.setCondition(&agent, conditionReady, metav1.ConditionTrue, "Triggered", fmt.Sprintf("Release %s created for version %s", releaseName, version))
	_ = r.Status().Update(ctx, &agent)

	r.Recorder.Eventf(&agent, "Normal", "ReleasedCreated", "created Release %s for version %s", releaseName, version)
	return ctrl.Result{RequeueAfter: pollInterval(&agent)}, nil
}

// detectVersion queries the configured source and returns the latest version string.
// Returns "" when no new version is available.
func (r *KAgentReconciler) detectVersion(ctx context.Context, agent *kaprov1alpha1.KAgent) (string, error) {
	switch agent.Spec.Source.Type {
	case "mlflow":
		return detectMLflow(ctx, r.Client, agent)
	case "oci":
		return detectOCI(ctx, r.Client, agent)
	case "prometheus":
		return detectPrometheus(ctx, agent)
	default:
		return "", fmt.Errorf("unknown source type %q", agent.Spec.Source.Type)
	}
}

// createRelease creates a Release CR for the detected version.
// It is idempotent: if a Release with the same name already exists, it returns its name.
func (r *KAgentReconciler) createRelease(ctx context.Context, agent *kaprov1alpha1.KAgent, version string) (string, error) {
	tmpl := agent.Spec.ReleaseTemplate
	releaseName := tmpl.ArtifactPrefix + version
	// Sanitise: Release names must be valid DNS labels.
	releaseName = sanitiseName(releaseName)

	artifactName := releaseName

	// Ensure the Artifact CR exists (idempotent).
	artifact := &kaprov1alpha1.Artifact{
		ObjectMeta: metav1.ObjectMeta{
			Name:      artifactName,
			Namespace: agent.Namespace,
			Labels:    tmpl.Labels,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(agent, kaprov1alpha1.GroupVersion.WithKind("KAgent")),
			},
		},
		Spec: kaprov1alpha1.ArtifactSpec{
			Sources: []kaprov1alpha1.ArtifactSource{
				sourceFromAgent(agent, version),
			},
			Metadata: kaprov1alpha1.ArtifactMeta{
				ReleasedBy:  "kagent/" + agent.Name,
				Description: fmt.Sprintf("auto-detected by KAgent %s: version %s", agent.Name, version),
			},
		},
	}
	if err := r.Create(ctx, artifact); err != nil && !apierrors.IsAlreadyExists(err) {
		return "", fmt.Errorf("create Artifact %s: %w", artifactName, err)
	}

	// Create the Release CR.
	release := &kaprov1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:      releaseName,
			Namespace: agent.Namespace,
			Labels:    tmpl.Labels,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(agent, kaprov1alpha1.GroupVersion.WithKind("KAgent")),
			},
		},
		Spec: kaprov1alpha1.ReleaseSpec{
			Artifact:    artifactName,
			Scope:       tmpl.Scope,
			PipelineRef: tmpl.PipelineRef,
		},
	}
	if err := r.Create(ctx, release); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return releaseName, nil
		}
		return "", fmt.Errorf("create Release %s: %w", releaseName, err)
	}

	return releaseName, nil
}

func (r *KAgentReconciler) setCondition(agent *kaprov1alpha1.KAgent, condType string, status metav1.ConditionStatus, reason, message string) {
	apimeta.SetStatusCondition(&agent.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: agent.Generation,
	})
}

func pollInterval(agent *kaprov1alpha1.KAgent) time.Duration {
	if agent.Spec.PollInterval == "" {
		return defaultPollInterval
	}
	d, err := time.ParseDuration(agent.Spec.PollInterval)
	if err != nil || d <= 0 {
		return defaultPollInterval
	}
	return d
}

// sanitiseName converts a version string to a valid Kubernetes name.
func sanitiseName(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '.':
			out = append(out, c)
		case c >= 'A' && c <= 'Z':
			out = append(out, c+32) // toLower
		default:
			out = append(out, '-')
		}
	}
	if len(out) > 253 {
		out = out[:253]
	}
	return string(out)
}

// sourceFromAgent builds an ArtifactSource from the KAgent source config + detected version.
func sourceFromAgent(agent *kaprov1alpha1.KAgent, version string) kaprov1alpha1.ArtifactSource {
	switch agent.Spec.Source.Type {
	case "oci":
		if s := agent.Spec.Source.OCI; s != nil {
			return kaprov1alpha1.ArtifactSource{
				Type: "oci",
				OCI: &kaprov1alpha1.OCIRef{
					Repository: s.Repository,
					Tag:        version,
				},
			}
		}
	case "mlflow":
		if s := agent.Spec.Source.MLflow; s != nil {
			return kaprov1alpha1.ArtifactSource{
				Type: "mlflow",
				OCI: &kaprov1alpha1.OCIRef{
					Repository: s.TrackingServerURL + "/models/" + s.ModelName,
					Tag:        version,
				},
			}
		}
	}
	return kaprov1alpha1.ArtifactSource{Type: agent.Spec.Source.Type}
}

// SetupWithManager registers the KAgent controller.
func (r *KAgentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kaprov1alpha1.KAgent{}).
		Complete(r)
}
