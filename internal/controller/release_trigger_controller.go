package controller

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"text/template"
	"time"

	"golang.org/x/mod/semver"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"

	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
)

const (
	releaseTriggerLabel       = "kapro.io/release-trigger"
	releaseTriggerDigestAnno  = "kapro.io/release-trigger-digest"
	releaseTriggerTagAnno     = "kapro.io/release-trigger-tag"
	releaseTriggerRepoAnno    = "kapro.io/release-trigger-repository"
	defaultTriggerCooldown    = 30 * time.Minute
	defaultTriggerPoll        = 5 * time.Minute
	defaultTriggerMaxActive   = int32(1)
	defaultTagListPageSize    = 100
	conditionArtifactObserved = "ArtifactObserved"
	conditionArtifactVerified = "ArtifactVerified"
	conditionReleaseCreated   = "ReleaseCreated"
	conditionSuspended        = "Suspended"
)

// ReleaseTriggerArtifactObservation is the immutable source artifact selected by a trigger.
type ReleaseTriggerArtifactObservation struct {
	Tag    string
	Digest string
}

// ReleaseTriggerResolver resolves the latest matching artifact for a trigger.
type ReleaseTriggerResolver interface {
	Resolve(ctx context.Context, trigger *kaprov1alpha1.ReleaseTrigger) (*ReleaseTriggerArtifactObservation, error)
}

// ReleaseTriggerVerifier verifies an artifact before release creation.
type ReleaseTriggerVerifier interface {
	Verify(ctx context.Context, trigger *kaprov1alpha1.ReleaseTrigger, artifact ReleaseTriggerArtifactObservation) error
}

// ReleaseTriggerReconciler observes artifact sources and creates guarded Releases.
type ReleaseTriggerReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	Resolver ReleaseTriggerResolver
	Verifier ReleaseTriggerVerifier
	Now      func() time.Time
}

// +kubebuilder:rbac:groups=kapro.io,resources=releasetriggers,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=releasetriggers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=releases,verbs=get;list;watch;create
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get

func (r *ReleaseTriggerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx)

	var trigger kaprov1alpha1.ReleaseTrigger
	if err := r.Get(ctx, req.NamespacedName, &trigger); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	now := r.now()
	patch := client.MergeFrom(trigger.DeepCopy())
	trigger.Status.ObservedGeneration = trigger.Generation
	trigger.Status.LastCheckedAt = now.UTC().Format(time.RFC3339)

	pollAfter, err := pollInterval(&trigger)
	if err != nil {
		setTriggerBlocked(&trigger, now, "InvalidPollInterval", err.Error())
		return ctrl.Result{}, r.patchStatus(ctx, &trigger, patch)
	}

	active, err := r.activeReleases(ctx, &trigger)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("list active releases for trigger %q: %w", trigger.Name, err)
	}
	trigger.Status.ActiveReleases = active
	trigger.Status.ActiveReleaseCount = int32(len(active))

	if trigger.Spec.Suspended {
		setTriggerSuspended(&trigger, now)
		if err := r.patchStatus(ctx, &trigger, patch); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	resolver := r.Resolver
	if resolver == nil {
		resolver = OCIReleaseTriggerResolver{Client: r.Client}
	}
	artifact, err := resolver.Resolve(ctx, &trigger)
	if err != nil {
		setTriggerBlocked(&trigger, now, "ResolveFailed", err.Error())
		return ctrl.Result{RequeueAfter: pollAfter}, r.patchStatus(ctx, &trigger, patch)
	}
	if artifact == nil {
		setTriggerNoArtifact(&trigger, now)
		return ctrl.Result{RequeueAfter: pollAfter}, r.patchStatus(ctx, &trigger, patch)
	}

	version := artifactVersion(&trigger, *artifact)
	trigger.Status.LastArtifact = &kaprov1alpha1.ReleaseTriggerArtifact{
		Tag:        artifact.Tag,
		Digest:     artifact.Digest,
		Version:    version,
		ObservedAt: now.UTC().Format(time.RFC3339),
	}
	setCondition(&trigger.Status.Conditions, conditionArtifactObserved, metav1.ConditionTrue, "ArtifactObserved", fmt.Sprintf("observed artifact %s@%s", artifact.Tag, artifact.Digest), trigger.Generation, now)

	if trigger.Spec.Source.OCI != nil && trigger.Spec.Source.OCI.RequireSignature {
		if r.Verifier == nil {
			setTriggerBlocked(&trigger, now, "VerifierUnavailable", "signature verification is required but no verifier is configured")
			return ctrl.Result{RequeueAfter: pollAfter}, r.patchStatus(ctx, &trigger, patch)
		}
		if err := r.Verifier.Verify(ctx, &trigger, *artifact); err != nil {
			setTriggerBlocked(&trigger, now, "SignatureVerificationFailed", err.Error())
			return ctrl.Result{RequeueAfter: pollAfter}, r.patchStatus(ctx, &trigger, patch)
		}
		trigger.Status.LastArtifact.SignatureVerified = true
		setCondition(&trigger.Status.Conditions, conditionArtifactVerified, metav1.ConditionTrue, "SignatureVerified", "artifact signature verification passed", trigger.Generation, now)
	} else {
		trigger.Status.LastArtifact.SignatureVerified = true
		setCondition(&trigger.Status.Conditions, conditionArtifactVerified, metav1.ConditionTrue, "SignatureNotRequired", "signature verification is not required for this trigger", trigger.Generation, now)
	}

	exists, err := releaseAlreadyCreatedForDigest(ctx, r.Client, &trigger, artifact.Digest)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("check existing release for digest %q: %w", artifact.Digest, err)
	}
	if exists {
		setTriggerReady(&trigger, now, "ArtifactAlreadyReleased", "release already exists for observed artifact")
		return ctrl.Result{RequeueAfter: pollAfter}, r.patchStatus(ctx, &trigger, patch)
	}

	remaining, err := cooldownRemaining(&trigger, now)
	if err != nil {
		setTriggerBlocked(&trigger, now, "InvalidCooldown", err.Error())
		return ctrl.Result{}, r.patchStatus(ctx, &trigger, patch)
	}
	if remaining > 0 {
		setTriggerReady(&trigger, now, "CooldownActive", fmt.Sprintf("cooldown active for %s", remaining.Round(time.Second)))
		setCondition(&trigger.Status.Conditions, conditionReleaseCreated, metav1.ConditionFalse, "CooldownActive", "release creation delayed by cooldown", trigger.Generation, now)
		return ctrl.Result{RequeueAfter: minDuration(remaining, pollAfter)}, r.patchStatus(ctx, &trigger, patch)
	}

	maxActive := trigger.Spec.MaxActive
	if maxActive == 0 {
		maxActive = defaultTriggerMaxActive
	}
	if int32(len(active)) >= maxActive {
		setTriggerReady(&trigger, now, "MaxActiveReached", fmt.Sprintf("active release count %d reached maxActive %d", len(active), maxActive))
		setCondition(&trigger.Status.Conditions, conditionReleaseCreated, metav1.ConditionFalse, "MaxActiveReached", "release creation delayed by maxActive", trigger.Generation, now)
		return ctrl.Result{RequeueAfter: pollAfter}, r.patchStatus(ctx, &trigger, patch)
	}

	release, err := buildTriggeredRelease(&trigger, *artifact, version, r.Scheme)
	if err != nil {
		setTriggerBlocked(&trigger, now, "InvalidReleaseTemplate", err.Error())
		return ctrl.Result{}, r.patchStatus(ctx, &trigger, patch)
	}
	if trigger.Spec.DryRun {
		setTriggerReady(&trigger, now, "DryRun", fmt.Sprintf("would create release %s", release.Name))
		setCondition(&trigger.Status.Conditions, conditionReleaseCreated, metav1.ConditionFalse, "DryRun", fmt.Sprintf("dry run: would create release %s", release.Name), trigger.Generation, now)
		return ctrl.Result{RequeueAfter: pollAfter}, r.patchStatus(ctx, &trigger, patch)
	}

	if err := r.Create(ctx, release); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			setTriggerBlocked(&trigger, now, "ReleaseCreateFailed", err.Error())
			return ctrl.Result{}, r.patchStatus(ctx, &trigger, patch)
		}
	}
	trigger.Status.LastTriggeredAt = now.UTC().Format(time.RFC3339)
	trigger.Status.ActiveReleases = append(active, release.Name)
	sort.Strings(trigger.Status.ActiveReleases)
	trigger.Status.ActiveReleaseCount = int32(len(trigger.Status.ActiveReleases))
	setTriggerReady(&trigger, now, "ReleaseCreated", fmt.Sprintf("created release %s", release.Name))
	setCondition(&trigger.Status.Conditions, conditionReleaseCreated, metav1.ConditionTrue, "ReleaseCreated", fmt.Sprintf("created release %s", release.Name), trigger.Generation, now)
	if r.Recorder != nil {
		r.Recorder.Eventf(&trigger, corev1.EventTypeNormal, "ReleaseCreated", "created release %s for %s", release.Name, version)
	}
	l.Info("release trigger created release", "trigger", trigger.Name, "release", release.Name, "version", version)

	return ctrl.Result{RequeueAfter: pollAfter}, r.patchStatus(ctx, &trigger, patch)
}

func (r *ReleaseTriggerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kaprov1alpha1.ReleaseTrigger{}).
		Owns(&kaprov1alpha1.Release{}).
		Complete(r)
}

func (r *ReleaseTriggerReconciler) patchStatus(ctx context.Context, trigger *kaprov1alpha1.ReleaseTrigger, patch client.Patch) error {
	if err := r.Status().Patch(ctx, trigger, patch); err != nil {
		return fmt.Errorf("patch release trigger status: %w", err)
	}
	return nil
}

func (r *ReleaseTriggerReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r *ReleaseTriggerReconciler) activeReleases(ctx context.Context, trigger *kaprov1alpha1.ReleaseTrigger) ([]string, error) {
	var list kaprov1alpha1.ReleaseList
	if err := r.List(ctx, &list, client.MatchingLabels{releaseTriggerLabel: trigger.Name}); err != nil {
		return nil, err
	}
	active := make([]string, 0, len(list.Items))
	for _, release := range list.Items {
		if release.Status.Phase == kaprov1alpha1.ReleasePhaseComplete || release.Status.Phase == kaprov1alpha1.ReleasePhaseFailed {
			continue
		}
		active = append(active, release.Name)
	}
	sort.Strings(active)
	return active, nil
}

// OCIReleaseTriggerResolver observes OCI tags using ORAS.
type OCIReleaseTriggerResolver struct {
	Client client.Reader
}

func (r OCIReleaseTriggerResolver) Resolve(ctx context.Context, trigger *kaprov1alpha1.ReleaseTrigger) (*ReleaseTriggerArtifactObservation, error) {
	if trigger.Spec.Source.Type != "oci" || trigger.Spec.Source.OCI == nil {
		return nil, fmt.Errorf("only oci release trigger sources are supported")
	}
	src := trigger.Spec.Source.OCI
	pattern, err := regexp.Compile(src.TagPattern)
	if err != nil {
		return nil, fmt.Errorf("compile tagPattern: %w", err)
	}
	repoRef := strings.TrimPrefix(src.Repository, "oci://")
	repo, err := remote.NewRepository(repoRef)
	if err != nil {
		return nil, fmt.Errorf("create OCI repository %q: %w", src.Repository, err)
	}
	repo.TagListPageSize = defaultTagListPageSize
	if src.SecretRef != nil {
		credential, err := registryCredential(ctx, r.Client, *src.SecretRef, repo.Reference.Registry)
		if err != nil {
			return nil, err
		}
		repo.Client = &auth.Client{
			Credential: auth.StaticCredential(repo.Reference.Registry, credential),
		}
	}

	var tags []string
	if err := repo.Tags(ctx, "", func(page []string) error {
		for _, tag := range page {
			if pattern.MatchString(tag) {
				tags = append(tags, tag)
			}
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("list OCI tags: %w", err)
	}
	if len(tags) == 0 {
		return nil, nil
	}
	sort.SliceStable(tags, func(i, j int) bool {
		return releaseTriggerTagLess(tags[i], tags[j])
	})
	tag := tags[len(tags)-1]
	desc, err := repo.Resolve(ctx, tag)
	if err != nil {
		return nil, fmt.Errorf("resolve OCI tag %q: %w", tag, err)
	}
	return &ReleaseTriggerArtifactObservation{
		Tag:    tag,
		Digest: desc.Digest.String(),
	}, nil
}

func registryCredential(ctx context.Context, c client.Reader, ref corev1.SecretReference, registry string) (auth.Credential, error) {
	if c == nil {
		return auth.EmptyCredential, fmt.Errorf("client is required when OCI secretRef is configured")
	}
	if ref.Name == "" || ref.Namespace == "" {
		return auth.EmptyCredential, fmt.Errorf("OCI secretRef requires both name and namespace")
	}
	var secret corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Name: ref.Name, Namespace: ref.Namespace}, &secret); err != nil {
		return auth.EmptyCredential, fmt.Errorf("get OCI registry secret %s/%s: %w", ref.Namespace, ref.Name, err)
	}
	if raw := secret.Data[corev1.DockerConfigJsonKey]; len(raw) > 0 {
		return credentialFromDockerConfig(raw, registry)
	}
	username := string(secret.Data["username"])
	password := string(secret.Data["password"])
	if username != "" || password != "" {
		return auth.Credential{Username: username, Password: password}, nil
	}
	if token := string(secret.Data["token"]); token != "" {
		return auth.Credential{AccessToken: token}, nil
	}
	return auth.EmptyCredential, fmt.Errorf("OCI registry secret %s/%s has no usable credentials", ref.Namespace, ref.Name)
}

type dockerConfigJSON struct {
	Auths map[string]dockerAuthConfig `json:"auths"`
}

type dockerAuthConfig struct {
	Username      string `json:"username"`
	Password      string `json:"password"`
	Auth          string `json:"auth"`
	IdentityToken string `json:"identitytoken"`
}

func credentialFromDockerConfig(raw []byte, registry string) (auth.Credential, error) {
	var cfg dockerConfigJSON
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return auth.EmptyCredential, fmt.Errorf("parse docker config json: %w", err)
	}
	for host, entry := range cfg.Auths {
		if normalizeRegistryHost(host) != normalizeRegistryHost(registry) {
			continue
		}
		if entry.Auth != "" && (entry.Username == "" || entry.Password == "") {
			decoded, err := base64.StdEncoding.DecodeString(entry.Auth)
			if err != nil {
				return auth.EmptyCredential, fmt.Errorf("decode docker auth for %s: %w", registry, err)
			}
			parts := strings.SplitN(string(decoded), ":", 2)
			if len(parts) == 2 {
				entry.Username = parts[0]
				entry.Password = parts[1]
			}
		}
		return auth.Credential{
			Username:     entry.Username,
			Password:     entry.Password,
			RefreshToken: entry.IdentityToken,
		}, nil
	}
	return auth.EmptyCredential, fmt.Errorf("docker config json does not contain credentials for %s", registry)
}

func normalizeRegistryHost(host string) string {
	host = strings.TrimPrefix(host, "https://")
	host = strings.TrimPrefix(host, "http://")
	return strings.TrimSuffix(host, "/")
}

func releaseAlreadyCreatedForDigest(ctx context.Context, c client.Reader, trigger *kaprov1alpha1.ReleaseTrigger, digest string) (bool, error) {
	var list kaprov1alpha1.ReleaseList
	if err := c.List(ctx, &list, client.MatchingLabels{releaseTriggerLabel: trigger.Name}); err != nil {
		return false, err
	}
	for _, release := range list.Items {
		if release.Annotations[releaseTriggerDigestAnno] == digest {
			return true, nil
		}
	}
	return false, nil
}

func buildTriggeredRelease(trigger *kaprov1alpha1.ReleaseTrigger, artifact ReleaseTriggerArtifactObservation, version string, scheme *runtime.Scheme) (*kaprov1alpha1.Release, error) {
	name, err := releaseName(trigger, artifact)
	if err != nil {
		return nil, err
	}
	labels := copyTriggerStringMap(trigger.Spec.ReleaseTemplate.Labels)
	labels[releaseTriggerLabel] = trigger.Name
	annotations := copyTriggerStringMap(trigger.Spec.ReleaseTemplate.Annotations)
	annotations[releaseTriggerRepoAnno] = trigger.Spec.Source.OCI.Repository
	annotations[releaseTriggerTagAnno] = artifact.Tag
	annotations[releaseTriggerDigestAnno] = artifact.Digest

	release := &kaprov1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: kaprov1alpha1.ReleaseSpec{
			Version:   version,
			Pipelines: append([]kaprov1alpha1.ReleasePipelineRef(nil), trigger.Spec.ReleaseTemplate.Pipelines...),
			Suspended: trigger.Spec.ReleaseTemplate.Suspended,
			Scope:     trigger.Spec.ReleaseTemplate.Scope.DeepCopy(),
			Timeout:   trigger.Spec.ReleaseTemplate.Timeout,
		},
	}
	if scheme != nil {
		gvk := schema.GroupVersionKind{Group: kaprov1alpha1.GroupVersion.Group, Version: kaprov1alpha1.GroupVersion.Version, Kind: "ReleaseTrigger"}
		release.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(trigger, gvk)}
	}
	return release, nil
}

func releaseName(trigger *kaprov1alpha1.ReleaseTrigger, artifact ReleaseTriggerArtifactObservation) (string, error) {
	if trigger.Spec.ReleaseTemplate.NameTemplate == "" {
		return dnsName(fmt.Sprintf("%s-%s", trigger.Name, shortDigest(artifact.Digest))), nil
	}
	tmpl, err := template.New("release-name").Option("missingkey=error").Parse(trigger.Spec.ReleaseTemplate.NameTemplate)
	if err != nil {
		return "", fmt.Errorf("parse nameTemplate: %w", err)
	}
	var b strings.Builder
	data := map[string]any{
		"Trigger":  trigger,
		"Artifact": artifact,
		"Tag":      artifact.Tag,
		"Digest":   artifact.Digest,
	}
	if err := tmpl.Execute(&b, data); err != nil {
		return "", fmt.Errorf("execute nameTemplate: %w", err)
	}
	name := dnsName(b.String())
	if errs := validation.IsDNS1123Subdomain(name); len(errs) > 0 {
		return "", fmt.Errorf("nameTemplate produced invalid release name %q: %s", name, strings.Join(errs, "; "))
	}
	return name, nil
}

func artifactVersion(trigger *kaprov1alpha1.ReleaseTrigger, artifact ReleaseTriggerArtifactObservation) string {
	repo := trigger.Spec.Source.OCI.Repository
	if !strings.HasPrefix(repo, "oci://") {
		repo = "oci://" + repo
	}
	return repo + "@" + artifact.Digest
}

func pollInterval(trigger *kaprov1alpha1.ReleaseTrigger) (time.Duration, error) {
	if trigger.Spec.Source.OCI == nil || trigger.Spec.Source.OCI.PollInterval == "" {
		return defaultTriggerPoll, nil
	}
	return time.ParseDuration(trigger.Spec.Source.OCI.PollInterval)
}

func cooldownRemaining(trigger *kaprov1alpha1.ReleaseTrigger, now time.Time) (time.Duration, error) {
	if trigger.Status.LastTriggeredAt == "" {
		return 0, nil
	}
	cooldown := defaultTriggerCooldown
	if trigger.Spec.Cooldown != "" {
		parsed, err := time.ParseDuration(trigger.Spec.Cooldown)
		if err != nil {
			return 0, fmt.Errorf("parse cooldown %q: %w", trigger.Spec.Cooldown, err)
		}
		cooldown = parsed
	}
	last, err := time.Parse(time.RFC3339, trigger.Status.LastTriggeredAt)
	if err != nil {
		return 0, fmt.Errorf("parse status.lastTriggeredAt %q: %w", trigger.Status.LastTriggeredAt, err)
	}
	if elapsed := now.Sub(last); elapsed < cooldown {
		return cooldown - elapsed, nil
	}
	return 0, nil
}

func releaseTriggerTagLess(left, right string) bool {
	leftCanonical := semver.Canonical(left)
	rightCanonical := semver.Canonical(right)
	switch {
	case leftCanonical != "" && rightCanonical != "":
		return semver.Compare(leftCanonical, rightCanonical) < 0
	case leftCanonical != "":
		return false
	case rightCanonical != "":
		return true
	default:
		return left < right
	}
}

func setTriggerSuspended(trigger *kaprov1alpha1.ReleaseTrigger, now time.Time) {
	setCondition(&trigger.Status.Conditions, conditionSuspended, metav1.ConditionTrue, "Suspended", "release trigger is suspended", trigger.Generation, now)
	setCondition(&trigger.Status.Conditions, "Ready", metav1.ConditionFalse, "Suspended", "release trigger is suspended", trigger.Generation, now)
	setCondition(&trigger.Status.Conditions, kaprov1alpha1.ConditionTypeReconciling, metav1.ConditionFalse, "Suspended", "release trigger is suspended", trigger.Generation, now)
	apimeta.RemoveStatusCondition(&trigger.Status.Conditions, kaprov1alpha1.ConditionTypeStalled)
}

func setTriggerNoArtifact(trigger *kaprov1alpha1.ReleaseTrigger, now time.Time) {
	setCondition(&trigger.Status.Conditions, conditionSuspended, metav1.ConditionFalse, "Active", "release trigger is active", trigger.Generation, now)
	setCondition(&trigger.Status.Conditions, "Ready", metav1.ConditionFalse, "NoMatchingArtifact", "no matching artifact observed", trigger.Generation, now)
	setCondition(&trigger.Status.Conditions, conditionArtifactObserved, metav1.ConditionFalse, "NoMatchingArtifact", "no matching artifact observed", trigger.Generation, now)
	setCondition(&trigger.Status.Conditions, kaprov1alpha1.ConditionTypeReconciling, metav1.ConditionFalse, "NoMatchingArtifact", "source check completed", trigger.Generation, now)
	apimeta.RemoveStatusCondition(&trigger.Status.Conditions, kaprov1alpha1.ConditionTypeStalled)
}

func setTriggerReady(trigger *kaprov1alpha1.ReleaseTrigger, now time.Time, reason, message string) {
	setCondition(&trigger.Status.Conditions, conditionSuspended, metav1.ConditionFalse, "Active", "release trigger is active", trigger.Generation, now)
	setCondition(&trigger.Status.Conditions, "Ready", metav1.ConditionTrue, reason, message, trigger.Generation, now)
	setCondition(&trigger.Status.Conditions, kaprov1alpha1.ConditionTypeReconciling, metav1.ConditionFalse, reason, "source check completed", trigger.Generation, now)
	apimeta.RemoveStatusCondition(&trigger.Status.Conditions, kaprov1alpha1.ConditionTypeStalled)
}

func setTriggerBlocked(trigger *kaprov1alpha1.ReleaseTrigger, now time.Time, reason, message string) {
	setCondition(&trigger.Status.Conditions, conditionSuspended, metav1.ConditionFalse, "Active", "release trigger is active", trigger.Generation, now)
	setCondition(&trigger.Status.Conditions, "Ready", metav1.ConditionFalse, reason, message, trigger.Generation, now)
	setCondition(&trigger.Status.Conditions, kaprov1alpha1.ConditionTypeReconciling, metav1.ConditionFalse, reason, "source check completed", trigger.Generation, now)
	setCondition(&trigger.Status.Conditions, kaprov1alpha1.ConditionTypeStalled, metav1.ConditionTrue, reason, message, trigger.Generation, now)
}

func setCondition(conditions *[]metav1.Condition, conditionType string, status metav1.ConditionStatus, reason, message string, observedGeneration int64, now time.Time) {
	apimeta.SetStatusCondition(conditions, metav1.Condition{
		Type:               conditionType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: observedGeneration,
		LastTransitionTime: metav1.NewTime(now),
	})
}

func shortDigest(digest string) string {
	parts := strings.SplitN(digest, ":", 2)
	if len(parts) == 2 {
		digest = parts[1]
	}
	if len(digest) > 12 {
		return digest[:12]
	}
	return digest
}

func dnsName(name string) string {
	name = strings.ToLower(name)
	name = regexp.MustCompile(`[^a-z0-9.-]+`).ReplaceAllString(name, "-")
	name = strings.Trim(name, "-.")
	if len(name) > 253 {
		name = name[:253]
		name = strings.TrimRight(name, "-.")
	}
	return name
}

func copyTriggerStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in)+4)
	for k, v := range in {
		out[k] = v
	}
	return out
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
