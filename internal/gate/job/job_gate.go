// Package job implements the Kubernetes Job-based gate.
//
// Kapro creates a Job in the target cluster, waits for it to complete,
// and interprets the exit code to determine the gate result.
// Useful for smoke tests, integration tests, or any script-based gate check
// that needs to run inside Kubernetes.
package job

import (
	"context"
	"fmt"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	pkggate "kapro.io/kapro/pkg/gate"
)

const jobGateNamespace = "kapro-system"

// Gate implements the Kubernetes Job-based gate type.
// It creates a Job, polls its status, and returns the result.
type Gate struct {
	Client client.Client
}

// Evaluate creates a Job for the gate (if not already running) and reads its status.
// The gate is idempotent: calling Evaluate with the same request returns the existing Job status.
func (g *Gate) Evaluate(ctx context.Context, req pkggate.Request) (pkggate.Result, error) {
	if req.Template == nil || req.Template.Spec.Job == nil {
		return pkggate.Result{}, fmt.Errorf("job gate: GateTemplate.Spec.Job is nil")
	}
	spec := req.Template.Spec.Job

	var promotionName string
	if req.Promotion != nil {
		promotionName = req.Promotion.Name
	}

	// Deterministic job name: kapro-gate-{promotion}-{template}
	name := jobName(promotionName, req.Template.Name)
	ns := jobGateNamespace

	// Check if the Job already exists.
	var existing batchv1.Job
	err := g.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &existing)
	if err == nil {
		return resultFromJob(&existing), nil
	}
	if !apierrors.IsNotFound(err) {
		return pkggate.Result{}, fmt.Errorf("job gate: get job %s/%s: %w", ns, name, err)
	}

	// Build and create the Job.
	job := buildJob(name, ns, spec, req.Args)
	if err := g.Client.Create(ctx, job); err != nil && !apierrors.IsAlreadyExists(err) {
		return pkggate.Result{}, fmt.Errorf("job gate: create job %s/%s: %w", ns, name, err)
	}

	// Job just created — it is Running.
	return pkggate.Result{
		Passed:     false,
		Phase:      kaprov1alpha1.GatePhaseRunning,
		Message:    "job created, waiting for completion",
		RetryAfter: "15s",
	}, nil
}

func resultFromJob(job *batchv1.Job) pkggate.Result {
	if job.Status.Succeeded > 0 {
		return pkggate.Result{Passed: true, Phase: kaprov1alpha1.GatePhasePassed, Message: "job completed successfully"}
	}
	if job.Status.Failed > 0 {
		return pkggate.Result{Passed: false, Phase: kaprov1alpha1.GatePhaseFailed, Message: fmt.Sprintf("job failed: %d failure(s)", job.Status.Failed)}
	}
	return pkggate.Result{Passed: false, Phase: kaprov1alpha1.GatePhaseRunning, Message: "job running", RetryAfter: "15s"}
}

func buildJob(name, ns string, spec *kaprov1alpha1.JobGateSpec, args map[string]string) *batchv1.Job {
	env := make([]corev1.EnvVar, 0, len(spec.Env)+len(args))
	env = append(env, spec.Env...)
	// Inject gate args as env vars prefixed with KAPRO_ARG_.
	for k, v := range args {
		env = append(env, corev1.EnvVar{
			Name:  "KAPRO_ARG_" + strings.ToUpper(strings.ReplaceAll(k, "-", "_")),
			Value: v,
		})
	}

	image := spec.Image
	if image == "" {
		image = "busybox:latest"
	}

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels: map[string]string{
				"kapro.io/gate": "job",
			},
		},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{
						{
							Name:    "gate",
							Image:   image,
							Command: spec.Command,
							Args:    spec.Args,
							Env:     env,
						},
					},
				},
			},
		},
	}
}

func jobName(promotionName, templateName string) string {
	raw := "kapro-gate-" + promotionName + "-" + templateName
	raw = strings.ToLower(strings.ReplaceAll(raw, "_", "-"))
	if len(raw) > 63 {
		return raw[:63]
	}
	return raw
}
