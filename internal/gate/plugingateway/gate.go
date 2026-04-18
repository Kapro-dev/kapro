// Package plugingateway implements the PluginGateway gate evaluator.
//
// A PluginGateway is a namespace-scoped, ambient-style gate integration.
// This evaluator routes gate.Evaluate() to the appropriate backend based on
// PluginGateway.Spec.Mode:
//
//   - builtin: delegates to a named built-in gate (soak, metrics, cel, etc.)
//   - job:     creates an ephemeral Kubernetes Job per evaluation
//   - remote:  makes a per-evaluation HTTP POST to the configured endpoint
//
// The Gate is constructed fresh for each GateTemplate evaluation — it is
// intentionally stateless and safe for concurrent use.
package plugingateway

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	pkggate "kapro.io/kapro/pkg/gate"
)

// Gate implements pkggate.Gate for PluginGateway-backed evaluation.
// It fetches the PluginGateway CR and routes to the appropriate backend.
type Gate struct {
	Client client.Client
}

// Evaluate implements pkggate.Gate.
func (g *Gate) Evaluate(ctx context.Context, req pkggate.Request) (pkggate.Result, error) {
	if req.Template == nil || req.Template.Spec.PluginGatewayRef == nil {
		return pkggate.Result{}, fmt.Errorf("plugin-gateway gate: GateTemplate.Spec.PluginGatewayRef is nil")
	}
	if req.Promotion == nil {
		return pkggate.Result{}, fmt.Errorf("plugin-gateway gate: req.Promotion is nil")
	}

	// Fetch the PluginGateway from the same namespace as the Promotion.
	var gw kaprov1alpha1.PluginGateway
	if err := g.Client.Get(ctx, client.ObjectKey{
		Namespace: req.Promotion.Namespace,
		Name:      req.Template.Spec.PluginGatewayRef.Name,
	}, &gw); err != nil {
		return pkggate.Result{}, fmt.Errorf("plugin-gateway gate: get PluginGateway %q: %w",
			req.Template.Spec.PluginGatewayRef.Name, err)
	}

	// Guard: refuse to evaluate if the controller has not validated this gateway.
	if !gw.Status.Ready {
		return pkggate.Result{
			Passed:     false,
			Message:    fmt.Sprintf("PluginGateway %q is not ready: %s", gw.Name, gw.Status.Message),
			RetryAfter: "30s",
		}, nil
	}

	switch gw.Spec.Mode {
	case kaprov1alpha1.PluginGatewayModeBuiltin:
		return g.evaluateBuiltin(ctx, req, &gw)
	case kaprov1alpha1.PluginGatewayModeJob:
		return g.evaluateJob(ctx, req, &gw)
	case kaprov1alpha1.PluginGatewayModeRemote:
		return g.evaluateRemote(ctx, req, &gw)
	default:
		return pkggate.Result{}, fmt.Errorf("plugin-gateway gate: unknown mode %q", gw.Spec.Mode)
	}
}

// evaluateBuiltin delegates to a synthetic built-in gate request.
// The named gate (soak, metrics, cel, etc.) is re-constructed here with the
// config from BuiltinGatewaySpec injected into req.Args.
func (g *Gate) evaluateBuiltin(_ context.Context, req pkggate.Request, gw *kaprov1alpha1.PluginGateway) (pkggate.Result, error) {
	spec := gw.Spec.Builtin
	if spec == nil {
		return pkggate.Result{}, fmt.Errorf("builtin mode: spec.builtin is nil")
	}
	// Merge BuiltinGatewaySpec.Config into the request args so the downstream
	// gate implementation can read them. Caller-supplied args take precedence.
	merged := make(map[string]string, len(spec.Config)+len(req.Args))
	for k, v := range spec.Config {
		merged[k] = v
	}
	for k, v := range req.Args {
		merged[k] = v
	}
	// We return Inconclusive with routing info — the PromotionReconciler will
	// re-dispatch to the correct built-in gate when it sees the vendor name.
	// This keeps the evaluator decoupled from importing every built-in gate.
	return pkggate.Result{
		Passed:  false,
		Phase:   kaprov1alpha1.GatePhaseRunning,
		Message: fmt.Sprintf("builtin/%s: dispatching with %d config keys", spec.Name, len(merged)),
		// RetryAfter empty — controller will use default backoff.
	}, fmt.Errorf("builtin routing not yet finalised: use GateTemplate type=%q directly for now", spec.Name)
}

// evaluateJob creates an ephemeral Kubernetes Job and returns the result.
// The job is idempotent — if a Job already exists for this promotion+template,
// the existing result is returned rather than creating a duplicate.
func (g *Gate) evaluateJob(ctx context.Context, req pkggate.Request, gw *kaprov1alpha1.PluginGateway) (pkggate.Result, error) {
	spec := gw.Spec.Job
	if spec == nil {
		return pkggate.Result{}, fmt.Errorf("job mode: spec.job is nil")
	}

	name := jobName(req.Promotion.Name, gw.Name)
	ns := req.Promotion.Namespace

	var existing batchv1.Job
	err := g.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &existing)
	if err == nil {
		return resultFromJob(&existing), nil
	}
	if !apierrors.IsNotFound(err) {
		return pkggate.Result{}, fmt.Errorf("get job %s/%s: %w", ns, name, err)
	}

	// Job does not exist yet — create it.
	job := buildJob(name, ns, spec, req.Args, req.Promotion)
	if createErr := g.Client.Create(ctx, job); createErr != nil {
		return pkggate.Result{}, fmt.Errorf("create job %s/%s: %w", ns, name, createErr)
	}

	return pkggate.Result{
		Passed:     false,
		Phase:      kaprov1alpha1.GatePhaseRunning,
		Message:    fmt.Sprintf("job %s/%s created", ns, name),
		RetryAfter: "30s",
	}, nil
}

// jobName returns a deterministic Kubernetes-safe job name.
func jobName(promotionName, gatewayName string) string {
	name := fmt.Sprintf("kapro-pgw-%s-%s", promotionName, gatewayName)
	if len(name) > 63 {
		name = name[:63]
	}
	return name
}

// buildJob constructs the Kubernetes Job for a job-mode PluginGateway evaluation.
func buildJob(name, ns string, spec *kaprov1alpha1.JobGatewaySpec, args map[string]string, promo *kaprov1alpha1.Promotion) *batchv1.Job {
	ttl := int32(300)
	if spec.TTLSecondsAfterFinished != nil {
		ttl = *spec.TTLSecondsAfterFinished
	}
	backoff := int32(0)
	if spec.BackoffLimit != nil {
		backoff = *spec.BackoffLimit
	}
	sa := spec.ServiceAccountName
	if sa == "" {
		sa = "default"
	}

	// Build env: Kapro standard vars + spec.Env + args as KAPRO_ARG_* vars.
	env := []corev1.EnvVar{
		{Name: "KAPRO_PROMOTION", Value: promo.Name},
		{Name: "KAPRO_RELEASE", Value: promo.Spec.ReleaseRef},
		{Name: "KAPRO_ENVIRONMENT", Value: promo.Spec.EnvironmentRef},
		{Name: "KAPRO_VERSION", Value: promo.Spec.Version},
	}
	for k, v := range args {
		env = append(env, corev1.EnvVar{
			Name:  "KAPRO_ARG_" + k,
			Value: v,
		})
	}
	env = append(env, spec.Env...)

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels: map[string]string{
				"kapro.io/promotion":    promo.Name,
				"kapro.io/plugin-gw":   promo.Namespace + "/" + name,
				"kapro.io/gate-runner": "plugin-gateway",
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					ServiceAccountName: sa,
					RestartPolicy:      corev1.RestartPolicyNever,
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
	}
}

// resultFromJob translates a Job's status into a gate Result.
func resultFromJob(job *batchv1.Job) pkggate.Result {
	for _, cond := range job.Status.Conditions {
		if cond.Type == batchv1.JobComplete && cond.Status == corev1.ConditionTrue {
			return pkggate.Result{Passed: true, Phase: kaprov1alpha1.GatePhasePassed, Message: "job completed successfully"}
		}
		if cond.Type == batchv1.JobFailed && cond.Status == corev1.ConditionTrue {
			return pkggate.Result{Passed: false, Phase: kaprov1alpha1.GatePhaseFailed, Message: cond.Message}
		}
	}
	return pkggate.Result{
		Passed:     false,
		Phase:      kaprov1alpha1.GatePhaseRunning,
		Message:    "job running",
		RetryAfter: "30s",
	}
}

// remoteRequest is the JSON body sent to HTTP remote endpoints.
type remoteRequest struct {
	PromotionName   string            `json:"promotion_name"`
	EnvironmentName string            `json:"environment_name"`
	ReleaseName     string            `json:"release_name"`
	Version         string            `json:"version"`
	GatewayName     string            `json:"gateway_name"`
	Args            map[string]string `json:"args,omitempty"`
}

// remoteResponse is the JSON body expected from HTTP remote endpoints.
type remoteResponse struct {
	// Status must be "passed", "failed", or "inconclusive".
	Status     string `json:"status"`
	Message    string `json:"message,omitempty"`
	RetryAfter string `json:"retry_after,omitempty"`
}

// evaluateRemote makes a POST request to the configured HTTP endpoint.
func (g *Gate) evaluateRemote(ctx context.Context, req pkggate.Request, gw *kaprov1alpha1.PluginGateway) (pkggate.Result, error) {
	spec := gw.Spec.Remote
	if spec == nil {
		return pkggate.Result{}, fmt.Errorf("remote mode: spec.remote is nil")
	}

	timeout := 30 * time.Second
	if spec.Timeout != "" {
		if d, err := time.ParseDuration(spec.Timeout); err == nil {
			timeout = d
		}
	}

	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	payload := remoteRequest{
		PromotionName:   req.Promotion.Name,
		EnvironmentName: req.Promotion.Spec.EnvironmentRef,
		ReleaseName:     req.Promotion.Spec.ReleaseRef,
		Version:         req.Promotion.Spec.Version,
		GatewayName:     gw.Name,
		Args:            req.Args,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return pkggate.Result{}, fmt.Errorf("marshal remote request: %w", err)
	}

	httpClient, err := g.buildHTTPClient(ctx, gw)
	if err != nil {
		return pkggate.Result{}, fmt.Errorf("build remote HTTP client: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, spec.URL, bytes.NewReader(body))
	if err != nil {
		return pkggate.Result{}, fmt.Errorf("build remote request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "kapro-plugin-gateway/v1alpha1")
	for k, v := range spec.Headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return pkggate.Result{
			Passed:     false,
			Phase:      kaprov1alpha1.GatePhaseInconclusive,
			Message:    fmt.Sprintf("remote call failed: %v", err),
			RetryAfter: "30s",
		}, nil
	}
	defer resp.Body.Close() //nolint:errcheck

	var result remoteResponse
	if decErr := json.NewDecoder(resp.Body).Decode(&result); decErr != nil {
		return pkggate.Result{}, fmt.Errorf("decode remote response: %w", decErr)
	}

	switch result.Status {
	case "passed":
		return pkggate.Result{Passed: true, Phase: kaprov1alpha1.GatePhasePassed, Message: result.Message}, nil
	case "failed":
		return pkggate.Result{Passed: false, Phase: kaprov1alpha1.GatePhaseFailed, Message: result.Message}, nil
	default: // "inconclusive" or anything else
		return pkggate.Result{
			Passed:     false,
			Phase:      kaprov1alpha1.GatePhaseInconclusive,
			Message:    result.Message,
			RetryAfter: result.RetryAfter,
		}, nil
	}
}

// buildHTTPClient constructs an HTTP client, optionally with mTLS.
func (g *Gate) buildHTTPClient(ctx context.Context, gw *kaprov1alpha1.PluginGateway) (*http.Client, error) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12} //nolint:gosec

	if gw.Spec.Remote != nil && gw.Spec.Remote.TLSSecretRef != "" {
		var secret corev1.Secret
		if err := g.Client.Get(ctx, client.ObjectKey{
			Namespace: gw.Namespace,
			Name:      gw.Spec.Remote.TLSSecretRef,
		}, &secret); err != nil {
			return nil, fmt.Errorf("load TLS secret %q: %w", gw.Spec.Remote.TLSSecretRef, err)
		}
		certPEM := secret.Data["tls.crt"]
		keyPEM := secret.Data["tls.key"]
		caPEM := secret.Data["ca.crt"]

		if len(certPEM) > 0 && len(keyPEM) > 0 {
			cert, err := tls.X509KeyPair(certPEM, keyPEM)
			if err != nil {
				return nil, fmt.Errorf("parse TLS cert: %w", err)
			}
			tlsCfg.Certificates = []tls.Certificate{cert}
		}
		if len(caPEM) > 0 {
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(caPEM) {
				return nil, fmt.Errorf("parse CA from secret %q", gw.Spec.Remote.TLSSecretRef)
			}
			tlsCfg.RootCAs = pool
		}
	}

	return &http.Client{
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
	}, nil
}
