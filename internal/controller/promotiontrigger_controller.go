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
	promotionTriggerLabel        = "kapro.io/promotion-trigger"
	promotionTriggerDigestAnno   = "kapro.io/promotion-trigger-digest"
	promotionTriggerTagAnno      = "kapro.io/promotion-trigger-tag"
	promotionTriggerRepoAnno     = "kapro.io/promotion-trigger-repository"
	promotionTriggerCreatedAnno  = "kapro.io/promotion-trigger-created-at"
	defaultTriggerCooldown       = 30 * time.Minute
	defaultTriggerPoll           = 5 * time.Minute
	defaultTriggerMaxActive      = int32(1)
	defaultTagListPageSize       = 100
	conditionArtifactObserved    = "ArtifactObserved"
	conditionArtifactVerified    = "ArtifactVerified"
	conditionPromotionRunCreated = "PromotionRunCreated"
	conditionSuspended           = "Suspended"
)

// PromotionTriggerArtifactObservation is the immutable source artifact selected by a trigger.
type PromotionTriggerArtifactObservation struct {
	Tag    string
	Digest string
}

// PromotionTriggerResolver resolves the latest matching artifact for a trigger.
type PromotionTriggerResolver interface {
	Resolve(ctx context.Context, trigger *kaprov1alpha1.PromotionTrigger) (*PromotionTriggerArtifactObservation, error)
}

// PromotionTriggerVerifier verifies an artifact before promotionrun creation.
type PromotionTriggerVerifier interface {
	Verify(ctx context.Context, trigger *kaprov1alpha1.PromotionTrigger, artifact PromotionTriggerArtifactObservation) error
}

// PromotionTriggerReconciler observes artifact sources and creates guarded PromotionRuns.
type PromotionTriggerReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	Resolver PromotionTriggerResolver
	Verifier PromotionTriggerVerifier
	Now      func() time.Time
}

// +kubebuilder:rbac:groups=kapro.io,resources=promotiontriggers,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=promotiontriggers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=promotionruns,verbs=get;list;watch;create
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get

func (r *PromotionTriggerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx)

	var trigger kaprov1alpha1.PromotionTrigger
	if err := r.Get(ctx, req.NamespacedName, &trigger); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	now := r.now()
	patch := client.MergeFrom(trigger.DeepCopy())
	trigger.Status.ObservedGeneration = trigger.Generation
	trigger.Status.LastCheckedAt = now.UTC().Format(time.RFC3339)

	pollAfter, invalidReason, err := validatePromotionTriggerConfig(&trigger)
	if err != nil {
		setTriggerBlocked(&trigger, now, invalidReason, err.Error())
		return ctrl.Result{}, r.patchStatus(ctx, &trigger, patch)
	}

	active, err := r.activePromotionRuns(ctx, &trigger)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("list active promotionruns for trigger %q: %w", trigger.Name, err)
	}
	trigger.Status.ActivePromotionRuns = active
	trigger.Status.ActivePromotionRunCount = int32(len(active))

	if trigger.Spec.Suspended {
		setTriggerSuspended(&trigger, now)
		if err := r.patchStatus(ctx, &trigger, patch); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	resolver := r.Resolver
	if resolver == nil {
		resolver = OCIPromotionTriggerResolver{Client: r.Client}
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
	trigger.Status.LastArtifact = &kaprov1alpha1.PromotionTriggerArtifact{
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

	exists, err := promotionrunAlreadyCreatedForDigest(ctx, r.Client, &trigger, artifact.Digest)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("check existing promotionrun for digest %q: %w", artifact.Digest, err)
	}
	if exists {
		setTriggerReady(&trigger, now, "ArtifactAlreadyReleased", "promotionrun already exists for observed artifact")
		return ctrl.Result{RequeueAfter: pollAfter}, r.patchStatus(ctx, &trigger, patch)
	}

	remaining, err := r.cooldownRemaining(ctx, &trigger, now)
	if err != nil {
		setTriggerBlocked(&trigger, now, "InvalidCooldown", err.Error())
		return ctrl.Result{}, r.patchStatus(ctx, &trigger, patch)
	}
	if remaining > 0 {
		setTriggerReady(&trigger, now, "CooldownActive", fmt.Sprintf("cooldown active for %s", remaining.Round(time.Second)))
		setCondition(&trigger.Status.Conditions, conditionPromotionRunCreated, metav1.ConditionFalse, "CooldownActive", "promotionrun creation delayed by cooldown", trigger.Generation, now)
		return ctrl.Result{RequeueAfter: minDuration(remaining, pollAfter)}, r.patchStatus(ctx, &trigger, patch)
	}

	maxActive := trigger.Spec.MaxActive
	if maxActive == 0 {
		maxActive = defaultTriggerMaxActive
	}
	if int32(len(active)) >= maxActive {
		setTriggerReady(&trigger, now, "MaxActiveReached", fmt.Sprintf("active promotionrun count %d reached maxActive %d", len(active), maxActive))
		setCondition(&trigger.Status.Conditions, conditionPromotionRunCreated, metav1.ConditionFalse, "MaxActiveReached", "promotionrun creation delayed by maxActive", trigger.Generation, now)
		return ctrl.Result{RequeueAfter: pollAfter}, r.patchStatus(ctx, &trigger, patch)
	}

	createdAt := r.now()
	promotionrun, err := buildTriggeredPromotionRun(&trigger, *artifact, version, r.Scheme, createdAt)
	if err != nil {
		setTriggerBlocked(&trigger, now, "InvalidPromotionRunTemplate", err.Error())
		return ctrl.Result{}, r.patchStatus(ctx, &trigger, patch)
	}
	if trigger.Spec.DryRun {
		setTriggerReady(&trigger, now, "DryRun", fmt.Sprintf("would create promotionrun %s", promotionrun.Name))
		setCondition(&trigger.Status.Conditions, conditionPromotionRunCreated, metav1.ConditionFalse, "DryRun", fmt.Sprintf("dry run: would create promotionrun %s", promotionrun.Name), trigger.Generation, now)
		return ctrl.Result{RequeueAfter: pollAfter}, r.patchStatus(ctx, &trigger, patch)
	}

	if err := r.Create(ctx, promotionrun); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			setTriggerBlocked(&trigger, now, "PromotionRunCreateFailed", err.Error())
			return ctrl.Result{}, r.patchStatus(ctx, &trigger, patch)
		}
	}
	trigger.Status.LastTriggeredAt = createdAt.UTC().Format(time.RFC3339)
	trigger.Status.ActivePromotionRuns = append(active, promotionrun.Name)
	sort.Strings(trigger.Status.ActivePromotionRuns)
	trigger.Status.ActivePromotionRunCount = int32(len(trigger.Status.ActivePromotionRuns))
	setTriggerReady(&trigger, createdAt, "PromotionRunCreated", fmt.Sprintf("created promotionrun %s", promotionrun.Name))
	setCondition(&trigger.Status.Conditions, conditionPromotionRunCreated, metav1.ConditionTrue, "PromotionRunCreated", fmt.Sprintf("created promotionrun %s", promotionrun.Name), trigger.Generation, createdAt)
	if r.Recorder != nil {
		r.Recorder.Eventf(&trigger, corev1.EventTypeNormal, "PromotionRunCreated", "created promotionrun %s for %s", promotionrun.Name, version)
	}
	l.Info("promotion trigger created promotionrun", "trigger", trigger.Name, "promotionrun", promotionrun.Name, "version", version)

	return ctrl.Result{RequeueAfter: pollAfter}, r.patchStatus(ctx, &trigger, patch)
}

func (r *PromotionTriggerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kaprov1alpha1.PromotionTrigger{}).
		Owns(&kaprov1alpha1.PromotionRun{}).
		Complete(r)
}

func (r *PromotionTriggerReconciler) patchStatus(ctx context.Context, trigger *kaprov1alpha1.PromotionTrigger, patch client.Patch) error {
	if err := r.Status().Patch(ctx, trigger, patch); err != nil {
		return fmt.Errorf("patch promotion trigger status: %w", err)
	}
	return nil
}

func (r *PromotionTriggerReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r *PromotionTriggerReconciler) activePromotionRuns(ctx context.Context, trigger *kaprov1alpha1.PromotionTrigger) ([]string, error) {
	var list kaprov1alpha1.PromotionRunList
	if err := r.List(ctx, &list, client.MatchingLabels{promotionTriggerLabel: trigger.Name}); err != nil {
		return nil, err
	}
	active := make([]string, 0, len(list.Items))
	for _, promotionrun := range list.Items {
		if promotionrun.Status.Phase == kaprov1alpha1.PromotionRunPhaseComplete || promotionrun.Status.Phase == kaprov1alpha1.PromotionRunPhaseFailed {
			continue
		}
		active = append(active, promotionrun.Name)
	}
	sort.Strings(active)
	return active, nil
}

// OCIPromotionTriggerResolver observes OCI tags using ORAS.
type OCIPromotionTriggerResolver struct {
	Client client.Reader
}

func (r OCIPromotionTriggerResolver) Resolve(ctx context.Context, trigger *kaprov1alpha1.PromotionTrigger) (*PromotionTriggerArtifactObservation, error) {
	if trigger.Spec.Source.Type != "oci" || trigger.Spec.Source.OCI == nil {
		return nil, fmt.Errorf("only oci promotion trigger sources are supported")
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
		return promotionTriggerTagLess(tags[i], tags[j])
	})
	tag := tags[len(tags)-1]
	desc, err := repo.Resolve(ctx, tag)
	if err != nil {
		return nil, fmt.Errorf("resolve OCI tag %q: %w", tag, err)
	}
	return &PromotionTriggerArtifactObservation{
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

func promotionrunAlreadyCreatedForDigest(ctx context.Context, c client.Reader, trigger *kaprov1alpha1.PromotionTrigger, digest string) (bool, error) {
	var list kaprov1alpha1.PromotionRunList
	if err := c.List(ctx, &list, client.MatchingLabels{promotionTriggerLabel: trigger.Name}); err != nil {
		return false, err
	}
	for _, promotionrun := range list.Items {
		if promotionrun.Annotations[promotionTriggerDigestAnno] == digest {
			return true, nil
		}
	}
	return false, nil
}

func buildTriggeredPromotionRun(trigger *kaprov1alpha1.PromotionTrigger, artifact PromotionTriggerArtifactObservation, version string, scheme *runtime.Scheme, now time.Time) (*kaprov1alpha1.PromotionRun, error) {
	name, err := promotionrunName(trigger, artifact)
	if err != nil {
		return nil, err
	}
	labels := copyTriggerStringMap(trigger.Spec.PromotionRunTemplate.Labels)
	labels[promotionTriggerLabel] = trigger.Name
	annotations := copyTriggerStringMap(trigger.Spec.PromotionRunTemplate.Annotations)
	annotations[promotionTriggerRepoAnno] = trigger.Spec.Source.OCI.Repository
	annotations[promotionTriggerTagAnno] = artifact.Tag
	annotations[promotionTriggerDigestAnno] = artifact.Digest
	annotations[promotionTriggerCreatedAnno] = now.UTC().Format(time.RFC3339)

	promotionrun := &kaprov1alpha1.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: kaprov1alpha1.PromotionRunSpec{
			Version:        version,
			PromotionPlans: append([]kaprov1alpha1.PromotionPlanRef(nil), trigger.Spec.PromotionRunTemplate.PromotionPlans...),
			Suspended:      trigger.Spec.PromotionRunTemplate.Suspended,
			Scope:          trigger.Spec.PromotionRunTemplate.Scope.DeepCopy(),
			Timeout:        trigger.Spec.PromotionRunTemplate.Timeout,
		},
	}
	if scheme != nil {
		gvk := schema.GroupVersionKind{Group: kaprov1alpha1.GroupVersion.Group, Version: kaprov1alpha1.GroupVersion.Version, Kind: "PromotionTrigger"}
		promotionrun.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(trigger, gvk)}
	}
	return promotionrun, nil
}

func promotionrunName(trigger *kaprov1alpha1.PromotionTrigger, artifact PromotionTriggerArtifactObservation) (string, error) {
	if trigger.Spec.PromotionRunTemplate.NameTemplate == "" {
		return dnsName(fmt.Sprintf("%s-%s", trigger.Name, shortDigest(artifact.Digest))), nil
	}
	tmpl, err := template.New("promotionrun-name").Option("missingkey=error").Parse(trigger.Spec.PromotionRunTemplate.NameTemplate)
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
		return "", fmt.Errorf("nameTemplate produced invalid promotionrun name %q: %s", name, strings.Join(errs, "; "))
	}
	return name, nil
}

func artifactVersion(trigger *kaprov1alpha1.PromotionTrigger, artifact PromotionTriggerArtifactObservation) string {
	repo := trigger.Spec.Source.OCI.Repository
	if !strings.HasPrefix(repo, "oci://") {
		repo = "oci://" + repo
	}
	return repo + "@" + artifact.Digest
}

func validatePromotionTriggerConfig(trigger *kaprov1alpha1.PromotionTrigger) (time.Duration, string, error) {
	if trigger.Spec.Source.Type != "oci" {
		return 0, "InvalidSource", fmt.Errorf("unsupported source.type %q", trigger.Spec.Source.Type)
	}
	if trigger.Spec.Source.OCI == nil {
		return 0, "InvalidSource", fmt.Errorf("source.oci is required when source.type=oci")
	}
	if trigger.Spec.Source.OCI.Repository == "" {
		return 0, "InvalidSource", fmt.Errorf("source.oci.repository is required")
	}
	if trigger.Spec.Source.OCI.TagPattern == "" {
		return 0, "InvalidTagPattern", fmt.Errorf("source.oci.tagPattern is required")
	}
	if _, err := regexp.Compile(trigger.Spec.Source.OCI.TagPattern); err != nil {
		return 0, "InvalidTagPattern", fmt.Errorf("compile source.oci.tagPattern: %w", err)
	}
	if trigger.Spec.Cooldown != "" {
		if _, err := positiveDuration("cooldown", trigger.Spec.Cooldown); err != nil {
			return 0, "InvalidCooldown", err
		}
	}
	if trigger.Spec.MaxActive < 0 {
		return 0, "InvalidMaxActive", fmt.Errorf("maxActive must be at least 1 when set")
	}
	pollAfter, err := pollInterval(trigger)
	if err != nil {
		return 0, "InvalidPollInterval", err
	}
	return pollAfter, "", nil
}

func pollInterval(trigger *kaprov1alpha1.PromotionTrigger) (time.Duration, error) {
	if trigger.Spec.Source.OCI == nil || trigger.Spec.Source.OCI.PollInterval == "" {
		return defaultTriggerPoll, nil
	}
	return positiveDuration("source.oci.pollInterval", trigger.Spec.Source.OCI.PollInterval)
}

func positiveDuration(field, value string) (time.Duration, error) {
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s %q: %w", field, value, err)
	}
	if parsed <= 0 {
		return 0, fmt.Errorf("%s must be greater than zero", field)
	}
	return parsed, nil
}

func cooldownRemaining(trigger *kaprov1alpha1.PromotionTrigger, now time.Time) (time.Duration, error) {
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

func (r *PromotionTriggerReconciler) cooldownRemaining(ctx context.Context, trigger *kaprov1alpha1.PromotionTrigger, now time.Time) (time.Duration, error) {
	lastTriggeredAt, err := lastTriggerPromotionRunCreatedAt(ctx, r.Client, trigger)
	if err != nil {
		return 0, err
	}
	if trigger.Status.LastTriggeredAt != "" {
		statusLastTriggeredAt, err := time.Parse(time.RFC3339, trigger.Status.LastTriggeredAt)
		if err != nil {
			return 0, fmt.Errorf("parse status.lastTriggeredAt %q: %w", trigger.Status.LastTriggeredAt, err)
		}
		if statusLastTriggeredAt.After(lastTriggeredAt) {
			lastTriggeredAt = statusLastTriggeredAt
		}
	}
	if lastTriggeredAt.IsZero() {
		return 0, nil
	}
	check := trigger.DeepCopy()
	check.Status.LastTriggeredAt = lastTriggeredAt.UTC().Format(time.RFC3339)
	return cooldownRemaining(check, now)
}

func lastTriggerPromotionRunCreatedAt(ctx context.Context, c client.Reader, trigger *kaprov1alpha1.PromotionTrigger) (time.Time, error) {
	var list kaprov1alpha1.PromotionRunList
	if err := c.List(ctx, &list, client.MatchingLabels{promotionTriggerLabel: trigger.Name}); err != nil {
		return time.Time{}, fmt.Errorf("list promotionruns for cooldown: %w", err)
	}
	var latest time.Time
	for _, promotionrun := range list.Items {
		createdAt := promotionrun.CreationTimestamp.Time
		if raw := promotionrun.Annotations[promotionTriggerCreatedAnno]; raw != "" {
			parsed, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				return time.Time{}, fmt.Errorf("parse promotionrun %s annotation %s %q: %w", promotionrun.Name, promotionTriggerCreatedAnno, raw, err)
			}
			createdAt = parsed
		}
		if createdAt.After(latest) {
			latest = createdAt
		}
	}
	return latest, nil
}

func promotionTriggerTagLess(left, right string) bool {
	leftCanonical := promotionTriggerSemverCanonical(left)
	rightCanonical := promotionTriggerSemverCanonical(right)
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

func promotionTriggerSemverCanonical(tag string) string {
	if canonical := semver.Canonical(tag); canonical != "" {
		return canonical
	}
	return semver.Canonical("v" + tag)
}

func setTriggerSuspended(trigger *kaprov1alpha1.PromotionTrigger, now time.Time) {
	setCondition(&trigger.Status.Conditions, conditionSuspended, metav1.ConditionTrue, "Suspended", "promotion trigger is suspended", trigger.Generation, now)
	setCondition(&trigger.Status.Conditions, "Ready", metav1.ConditionFalse, "Suspended", "promotion trigger is suspended", trigger.Generation, now)
	setCondition(&trigger.Status.Conditions, kaprov1alpha1.ConditionTypeReconciling, metav1.ConditionFalse, "Suspended", "promotion trigger is suspended", trigger.Generation, now)
	apimeta.RemoveStatusCondition(&trigger.Status.Conditions, kaprov1alpha1.ConditionTypeStalled)
}

func setTriggerNoArtifact(trigger *kaprov1alpha1.PromotionTrigger, now time.Time) {
	setCondition(&trigger.Status.Conditions, conditionSuspended, metav1.ConditionFalse, "Active", "promotion trigger is active", trigger.Generation, now)
	setCondition(&trigger.Status.Conditions, "Ready", metav1.ConditionFalse, "NoMatchingArtifact", "no matching artifact observed", trigger.Generation, now)
	setCondition(&trigger.Status.Conditions, conditionArtifactObserved, metav1.ConditionFalse, "NoMatchingArtifact", "no matching artifact observed", trigger.Generation, now)
	setCondition(&trigger.Status.Conditions, kaprov1alpha1.ConditionTypeReconciling, metav1.ConditionFalse, "NoMatchingArtifact", "source check completed", trigger.Generation, now)
	apimeta.RemoveStatusCondition(&trigger.Status.Conditions, kaprov1alpha1.ConditionTypeStalled)
}

func setTriggerReady(trigger *kaprov1alpha1.PromotionTrigger, now time.Time, reason, message string) {
	setCondition(&trigger.Status.Conditions, conditionSuspended, metav1.ConditionFalse, "Active", "promotion trigger is active", trigger.Generation, now)
	setCondition(&trigger.Status.Conditions, "Ready", metav1.ConditionTrue, reason, message, trigger.Generation, now)
	setCondition(&trigger.Status.Conditions, kaprov1alpha1.ConditionTypeReconciling, metav1.ConditionFalse, reason, "source check completed", trigger.Generation, now)
	apimeta.RemoveStatusCondition(&trigger.Status.Conditions, kaprov1alpha1.ConditionTypeStalled)
}

func setTriggerBlocked(trigger *kaprov1alpha1.PromotionTrigger, now time.Time, reason, message string) {
	setCondition(&trigger.Status.Conditions, conditionSuspended, metav1.ConditionFalse, "Active", "promotion trigger is active", trigger.Generation, now)
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
