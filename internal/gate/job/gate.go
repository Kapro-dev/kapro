// Package job implements the Kubernetes Job gate.
//
// The job gate creates a short-lived Kubernetes Job to evaluate a custom gate
// condition. The Job runs the configured container image and command; its exit
// code determines the gate result: 0 = Passed, non-zero = Failed.
//
// The gate watches the Job until it completes or the context deadline is reached.
// On timeout the gate returns Inconclusive so the SyncReconciler can retry.
package job

import (
	"context"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	pkggate "kapro.io/kapro/pkg/gate"
)

// Gate implements the job gate type.
// It creates a Kubernetes Job in the same namespace as the Sync and polls
// its status on each reconcile cycle.
type Gate struct {
	Client client.Client
}

// jobName returns a deterministic Job name for a (sync, template) pair.
// Using the Sync name + template name keeps it idempotent across reconcile loops.
func jobName(syncName, tmplName string) string {
	name := fmt.Sprintf("kapro-gate-%s-%s", syncName, tmplName)
	if len(name) > 63 {
		name = name[:63]
	}
	return name
}

// Evaluate creates or polls a Job for the configured GateTemplate.
// Returns Passed when the Job succeeded, Failed when it failed, and
// Inconclusive (with RetryAfter) while the Job is still running.
func (g *Gate) Evaluate(ctx context.Context, req pkggate.Request) (pkggate.Result, error) {
	log := log.FromContext(ctx)

	if req.Template == nil || req.Template.Job == nil {
		return pkggate.Result{}, fmt.Errorf("job gate: template Job spec is nil")
	}
	spec := req.Template.Job

	if req.Sync == nil {
		return pkggate.Result{}, fmt.Errorf("job gate: Sync is nil in request")
	}
	sync := req.Sync
	namespace := sync.Namespace
	if namespace == "" {
		namespace = "default"
	}

	name := jobName(sync.Name, req.Template.Name)

	// Check if the Job already exists.
	var existing batchv1.Job
	err := g.Client.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &existing)
	if err != nil && !apierrors.IsNotFound(err) {
		return pkggate.Result{}, fmt.Errorf("job gate: get job: %w", err)
	}

	if apierrors.IsNotFound(err) {
		// Create the Job.
		job, buildErr := buildJob(name, namespace, sync, spec, req.Args, req.Template.Timeout)
		if buildErr != nil {
			return pkggate.Result{}, fmt.Errorf("job gate: build job spec: %w", buildErr)
		}
		if createErr := g.Client.Create(ctx, job); createErr != nil && !apierrors.IsAlreadyExists(createErr) {
			return pkggate.Result{}, fmt.Errorf("job gate: create job: %w", createErr)
		}
		log.Info("created gate job", "job", name, "namespace", namespace)
		return pkggate.Result{
			Phase:      kaprov1alpha1.GatePhaseRunning,
			Message:    "gate job created, waiting for completion",
			RetryAfter: "15s",
		}, nil
	}

	// Job exists — inspect its status.
	if existing.Status.Succeeded > 0 {
		log.Info("gate job succeeded", "job", name)
		// Clean up the completed job.
		_ = g.Client.Delete(ctx, &existing, client.PropagationPolicy(metav1.DeletePropagationBackground))
		return pkggate.Result{
			Phase:   kaprov1alpha1.GatePhasePassed,
			Message: "gate job completed successfully",
		}, nil
	}
	if existing.Status.Failed > 0 {
		log.Info("gate job failed", "job", name, "failures", existing.Status.Failed)
		_ = g.Client.Delete(ctx, &existing, client.PropagationPolicy(metav1.DeletePropagationBackground))
		return pkggate.Result{
			Phase:   kaprov1alpha1.GatePhaseFailed,
			Message: fmt.Sprintf("gate job failed after %d attempt(s)", existing.Status.Failed),
		}, nil
	}

	// Still running.
	return pkggate.Result{
		Phase:      kaprov1alpha1.GatePhaseRunning,
		Message:    "gate job is still running",
		RetryAfter: "15s",
	}, nil
}

// buildJob constructs the batchv1.Job from the GateTemplate job spec.
// timeout is an optional Go duration string (e.g. "10m", "2h"); when non-empty
// it is wired into Job.Spec.ActiveDeadlineSeconds so the Job is killed by the
// Kubernetes Job controller if it runs longer than the configured limit.
func buildJob(
	name, namespace string,
	sync *kaprov1alpha1.Sync,
	spec *kaprov1alpha1.JobGateSpec,
	args map[string]string,
	timeout string,
) (*batchv1.Job, error) {
	// Inject standard context env vars so the job knows what it is evaluating.
	extraEnv := []corev1.EnvVar{
		{Name: "KAPRO_SYNC", Value: sync.Name},
		{Name: "KAPRO_ENVIRONMENT", Value: sync.Spec.EnvironmentRef},
		{Name: "KAPRO_VERSION", Value: sync.Spec.Version},
		{Name: "KAPRO_RELEASE", Value: sync.Spec.ReleaseRef},
		{Name: "KAPRO_PIPELINE", Value: sync.Spec.Pipeline},
		{Name: "KAPRO_STAGE", Value: sync.Spec.Stage},
	}
	for k, v := range args {
		extraEnv = append(extraEnv, corev1.EnvVar{Name: "KAPRO_ARG_" + k, Value: v})
	}

	env := append(spec.Env, extraEnv...)

	ttl := int32(300) // clean up 5 minutes after completion by default

	// Parse optional timeout into ActiveDeadlineSeconds.
	// A non-empty, invalid duration string is a configuration error — fail fast
	// rather than silently running without a deadline.
	var activeDeadlineSeconds *int64
	if timeout != "" {
		d, err := time.ParseDuration(timeout)
		if err != nil {
			return nil, fmt.Errorf("invalid timeout %q: %w", timeout, err)
		}
		secs := int64(d.Seconds())
		if secs <= 0 {
			return nil, fmt.Errorf("timeout %q must be positive", timeout)
		}
		activeDeadlineSeconds = &secs
	}

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"kapro.io/gate-type": "job",
				"kapro.io/sync":      sync.Name,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            ptr.To(int32(3)),
			TTLSecondsAfterFinished: &ttl,
			ActiveDeadlineSeconds:   activeDeadlineSeconds,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"kapro.io/gate-type": "job",
						"kapro.io/sync":      sync.Name,
					},
				},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{
						{
							Name:    "gate",
							Image:   spec.Image,
							Command: spec.Command,
							Args:    spec.Args,
							Env:     env,
						},
					},
				},
			},
		},
	}, nil
}
