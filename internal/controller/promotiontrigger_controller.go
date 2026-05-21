package controller

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"

	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
)

const (
	promotionTriggerLabel             = "kapro.io/promotion-trigger"
	promotionTriggerTemplateHashLabel = "kapro.io/promotion-trigger-template-hash"
	promotionTriggerDigestAnno        = "kapro.io/promotion-trigger-digest"
	promotionTriggerTagAnno           = "kapro.io/promotion-trigger-tag"
	promotionTriggerRepoAnno          = "kapro.io/promotion-trigger-repository"
	promotionTriggerCreatedAnno       = "kapro.io/promotion-trigger-created-at"
	defaultTriggerCooldown            = 30 * time.Minute
	defaultTriggerPoll                = 5 * time.Minute
	defaultTriggerMaxActive           = int32(1)
	defaultTagListPageSize            = 100
	conditionArtifactObserved         = "ArtifactObserved"
	conditionArtifactVerified         = "ArtifactVerified"
	conditionPromotionUpdated         = "PromotionUpdated"
	conditionSuspended                = "Suspended"
)

// PromotionTriggerArtifactObservation is the immutable source artifact selected by a trigger.
type PromotionTriggerArtifactObservation struct {
	Tag    string
	Digest string
}

// PromotionTriggerResolver resolves the latest matching artifact for a trigger.
type PromotionTriggerResolver interface {
	Resolve(ctx context.Context, trigger *kaprov1alpha2.Trigger) (*PromotionTriggerArtifactObservation, error)
}

// PromotionTriggerVerifier verifies an artifact before Promotion intent is updated.
type PromotionTriggerVerifier interface {
	Verify(ctx context.Context, trigger *kaprov1alpha2.Trigger, artifact PromotionTriggerArtifactObservation) error
}

// PromotionTriggerReconciler observes artifact sources and updates guarded Promotions.
type PromotionTriggerReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	Resolver PromotionTriggerResolver
	Verifier PromotionTriggerVerifier
	Now      func() time.Time
}

// +kubebuilder:rbac:groups=kapro.io,resources=triggers,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=triggers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=promotions,verbs=get;list;watch;create;patch;update
// +kubebuilder:rbac:groups=kapro.io,resources=promotionruns,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get

func (r *PromotionTriggerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx)

	var trigger kaprov1alpha2.Trigger
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

	managedName, err := managedPromotionName(&trigger)
	if err != nil {
		setTriggerBlocked(&trigger, now, "InvalidNameTemplate", err.Error())
		return ctrl.Result{}, r.patchStatus(ctx, &trigger, patch)
	}
	trigger.Status.ManagedPromotion = managedName

	activeCount, err := r.activePromotionRunCount(ctx, managedName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("count active PromotionRuns for managed Promotion %q: %w", managedName, err)
	}
	trigger.Status.ActivePromotionRunCount = activeCount

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
	trigger.Status.LastArtifact = &kaprov1alpha2.PromotionTriggerArtifact{
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

	templateHash := triggerTemplateHash(&trigger)
	trigger.Status.RecentArtifacts = recordRecentArtifact(trigger.Status.RecentArtifacts, *trigger.Status.LastArtifact)

	// Look up the managed Promotion. Dedup: if it already targets this
	// digest AND was last stamped from the same template hash, skip.
	var managed kaprov1alpha2.Promotion
	managedExists := false
	if err := r.Get(ctx, client.ObjectKey{Name: managedName}, &managed); err == nil {
		managedExists = true
		if managed.Spec.Version == version &&
			managed.Labels[promotionTriggerTemplateHashLabel] == templateHash {
			setTriggerReady(&trigger, now, "ArtifactAlreadyReleased",
				"managed Promotion already targets this digest+template")
			return ctrl.Result{RequeueAfter: pollAfter}, r.patchStatus(ctx, &trigger, patch)
		}
	} else if !apierrors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("get managed Promotion %q: %w", managedName, err)
	}

	remaining, err := r.cooldownRemaining(ctx, &trigger, now)
	if err != nil {
		setTriggerBlocked(&trigger, now, "InvalidCooldown", err.Error())
		return ctrl.Result{}, r.patchStatus(ctx, &trigger, patch)
	}
	if remaining > 0 {
		setTriggerReady(&trigger, now, "CooldownActive", fmt.Sprintf("cooldown active for %s", remaining.Round(time.Second)))
		setCondition(&trigger.Status.Conditions, conditionPromotionUpdated, metav1.ConditionFalse, "CooldownActive", "Promotion update delayed by cooldown", trigger.Generation, now)
		return ctrl.Result{RequeueAfter: minDuration(remaining, pollAfter)}, r.patchStatus(ctx, &trigger, patch)
	}

	maxActive := trigger.Spec.MaxActive
	if maxActive == 0 {
		maxActive = defaultTriggerMaxActive
	}
	if activeCount >= maxActive {
		setTriggerReady(&trigger, now, "MaxActiveReached", fmt.Sprintf("active attempt count %d reached maxActive %d", activeCount, maxActive))
		setCondition(&trigger.Status.Conditions, conditionPromotionUpdated, metav1.ConditionFalse, "MaxActiveReached", "Promotion update delayed by maxActive", trigger.Generation, now)
		return ctrl.Result{RequeueAfter: pollAfter}, r.patchStatus(ctx, &trigger, patch)
	}

	createdAt := r.now()
	desired, err := buildTriggeredPromotion(&trigger, *artifact, version, managedName, templateHash, r.Scheme, createdAt)
	if err != nil {
		setTriggerBlocked(&trigger, now, "InvalidPromotionTemplate", err.Error())
		return ctrl.Result{}, r.patchStatus(ctx, &trigger, patch)
	}
	if trigger.Spec.DryRun {
		setTriggerReady(&trigger, now, "DryRun", fmt.Sprintf("would upsert Promotion %s for %s", desired.Name, version))
		setCondition(&trigger.Status.Conditions, conditionPromotionUpdated, metav1.ConditionFalse, "DryRun", fmt.Sprintf("dry run: would upsert Promotion %s", desired.Name), trigger.Generation, now)
		return ctrl.Result{RequeueAfter: pollAfter}, r.patchStatus(ctx, &trigger, patch)
	}

	var op string
	if !managedExists {
		if err := r.Create(ctx, desired); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				setTriggerBlocked(&trigger, now, "PromotionCreateFailed", err.Error())
				return ctrl.Result{}, r.patchStatus(ctx, &trigger, patch)
			}
			// Race: fetch and fall through to update.
			if err := r.Get(ctx, client.ObjectKey{Name: managedName}, &managed); err != nil {
				return ctrl.Result{}, fmt.Errorf("re-fetch managed Promotion after AlreadyExists: %w", err)
			}
			managedExists = true
		} else {
			op = "created"
		}
	}
	if managedExists && op == "" {
		mergePatch := client.MergeFrom(managed.DeepCopy())
		applyTriggerToPromotion(&managed, desired)
		if err := r.Patch(ctx, &managed, mergePatch); err != nil {
			setTriggerBlocked(&trigger, now, "PromotionUpdateFailed", err.Error())
			return ctrl.Result{}, r.patchStatus(ctx, &trigger, patch)
		}
		op = "updated"
	}

	trigger.Status.LastTriggeredAt = createdAt.UTC().Format(time.RFC3339)
	setTriggerReady(&trigger, createdAt, "PromotionUpdated", fmt.Sprintf("%s Promotion %s", op, desired.Name))
	setCondition(&trigger.Status.Conditions, conditionPromotionUpdated, metav1.ConditionTrue, "PromotionUpdated", fmt.Sprintf("%s Promotion %s for %s", op, desired.Name, version), trigger.Generation, createdAt)
	if r.Recorder != nil {
		r.Recorder.Eventf(&trigger, corev1.EventTypeNormal, "PromotionUpdated", "%s Promotion %s for %s", op, desired.Name, version)
	}
	l.Info("promotion trigger upserted Promotion", "trigger", trigger.Name, "promotion", desired.Name, "version", version, "op", op)

	return ctrl.Result{RequeueAfter: pollAfter}, r.patchStatus(ctx, &trigger, patch)
}

func (r *PromotionTriggerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kaprov1alpha2.Trigger{}).
		Owns(&kaprov1alpha2.Promotion{}).
		Complete(r)
}

func (r *PromotionTriggerReconciler) patchStatus(ctx context.Context, trigger *kaprov1alpha2.Trigger, patch client.Patch) error {
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

// activePromotionRunCount counts non-terminal PromotionRuns owned by the
// trigger's managed Promotion (via promotion-owner label).
func (r *PromotionTriggerReconciler) activePromotionRunCount(ctx context.Context, managedPromotion string) (int32, error) {
	if managedPromotion == "" {
		return 0, nil
	}
	var list kaprov1alpha2.PromotionRunList
	if err := r.List(ctx, &list, client.MatchingLabels{promotionOwnerLabel: managedPromotion}); err != nil {
		return 0, err
	}
	var n int32
	for _, run := range list.Items {
		if !run.Status.Phase.IsTerminal() {
			n++
		}
	}
	return n, nil
}

// OCIPromotionTriggerResolver observes OCI tags using ORAS.
type OCIPromotionTriggerResolver struct {
	Client client.Reader
}

func (r OCIPromotionTriggerResolver) Resolve(ctx context.Context, trigger *kaprov1alpha2.Trigger) (*PromotionTriggerArtifactObservation, error) {
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

// buildTriggeredPromotion constructs the desired Promotion spec for this
// trigger + observed artifact. The PromotionController owns stamping a
// PromotionRun under it.
func buildTriggeredPromotion(trigger *kaprov1alpha2.Trigger, artifact PromotionTriggerArtifactObservation, version, managedName, templateHash string, scheme *runtime.Scheme, now time.Time) (*kaprov1alpha2.Promotion, error) {
	tmpl := &trigger.Spec.PromotionTemplate
	if tmpl.FleetRef == "" {
		return nil, fmt.Errorf("spec.promotionTemplate.fleetRef is required")
	}
	labels := copyTriggerStringMap(tmpl.Labels)
	labels[promotionTriggerLabel] = trigger.Name
	labels[promotionTriggerTemplateHashLabel] = templateHash
	annotations := copyTriggerStringMap(tmpl.Annotations)
	annotations[promotionTriggerRepoAnno] = trigger.Spec.Source.OCI.Repository
	annotations[promotionTriggerTagAnno] = artifact.Tag
	annotations[promotionTriggerDigestAnno] = artifact.Digest
	annotations[promotionTriggerCreatedAnno] = now.UTC().Format(time.RFC3339)

	promo := &kaprov1alpha2.Promotion{
		ObjectMeta: metav1.ObjectMeta{
			Name:        managedName,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: kaprov1alpha2.PromotionSpec{
			FleetRef:       tmpl.FleetRef,
			Version:        version,
			PromotionPlans: append([]kaprov1alpha2.PlanRef(nil), tmpl.PromotionPlans...),
			Suspended:      tmpl.Suspended,
			Scope:          tmpl.Scope.DeepCopy(),
			Timeout:        tmpl.Timeout,
		},
	}
	if scheme != nil {
		gvk := schema.GroupVersionKind{Group: kaprov1alpha2.GroupVersion.Group, Version: kaprov1alpha2.GroupVersion.Version, Kind: "PromotionTrigger"}
		promo.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(trigger, gvk)}
	}
	return promo, nil
}

// applyTriggerToPromotion mutates `existing` toward `desired` (spec + labels
// + annotations) without disturbing fields the trigger does not own.
func applyTriggerToPromotion(existing, desired *kaprov1alpha2.Promotion) {
	existing.Spec = desired.Spec
	if existing.Labels == nil {
		existing.Labels = map[string]string{}
	}
	for k, v := range desired.Labels {
		existing.Labels[k] = v
	}
	if existing.Annotations == nil {
		existing.Annotations = map[string]string{}
	}
	for k, v := range desired.Annotations {
		existing.Annotations[k] = v
	}
}

// triggerTemplateHash is a deterministic hash of the parts of the trigger
// template that affect Promotion identity. Used to detect template drift
// and force a Promotion update even when the digest is unchanged.
func triggerTemplateHash(trigger *kaprov1alpha2.Trigger) string {
	t := trigger.Spec.PromotionTemplate
	buf, _ := json.Marshal(struct {
		FleetRef       string                           `json:"fleetRef"`
		PromotionPlans []kaprov1alpha2.PlanRef          `json:"promotionPlans"`
		Suspended      bool                             `json:"suspended"`
		Scope          *kaprov1alpha2.PromotionRunScope `json:"scope"`
		Timeout        string                           `json:"timeout"`
		Labels         map[string]string                `json:"labels"`
		Annotations    map[string]string                `json:"annotations"`
	}{
		FleetRef: t.FleetRef, PromotionPlans: t.PromotionPlans, Suspended: t.Suspended,
		Scope: t.Scope, Timeout: t.Timeout, Labels: t.Labels, Annotations: t.Annotations,
	})
	h := sha256.Sum256(buf)
	return hex.EncodeToString(h[:])[:12]
}

// recordRecentArtifact prepends a new artifact observation, dedupes by
// digest (an A→B→A flip is recorded as separate entries since each carries
// a fresh ObservedAt), and caps the slice at MaxRecentArtifacts.
func recordRecentArtifact(list []kaprov1alpha2.PromotionTriggerArtifact, current kaprov1alpha2.PromotionTriggerArtifact) []kaprov1alpha2.PromotionTriggerArtifact {
	if len(list) > 0 && list[0].Digest == current.Digest && list[0].Tag == current.Tag {
		// Same artifact, same tag — just refresh the latest entry's ObservedAt.
		list[0] = current
		return list
	}
	out := append([]kaprov1alpha2.PromotionTriggerArtifact{current}, list...)
	if len(out) > kaprov1alpha2.MaxRecentArtifacts {
		out = out[:kaprov1alpha2.MaxRecentArtifacts]
	}
	return out
}

func managedPromotionName(trigger *kaprov1alpha2.Trigger) (string, error) {
	tmpl := trigger.Spec.PromotionTemplate.NameTemplate
	if tmpl == "" {
		return dnsName(trigger.Name), nil
	}
	parsed, err := template.New("promotion-name").Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("parse nameTemplate: %w", err)
	}
	var b strings.Builder
	data := map[string]any{
		"Trigger": trigger,
		"Kapro":   trigger.Spec.PromotionTemplate.FleetRef,
	}
	if err := parsed.Execute(&b, data); err != nil {
		return "", fmt.Errorf("execute nameTemplate: %w", err)
	}
	name := dnsName(b.String())
	if errs := validation.IsDNS1123Subdomain(name); len(errs) > 0 {
		return "", fmt.Errorf("nameTemplate produced invalid Promotion name %q: %s", name, strings.Join(errs, "; "))
	}
	return name, nil
}

func artifactVersion(trigger *kaprov1alpha2.Trigger, artifact PromotionTriggerArtifactObservation) string {
	repo := trigger.Spec.Source.OCI.Repository
	if !strings.HasPrefix(repo, "oci://") {
		repo = "oci://" + repo
	}
	return repo + "@" + artifact.Digest
}

func validatePromotionTriggerConfig(trigger *kaprov1alpha2.Trigger) (time.Duration, string, error) {
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

func pollInterval(trigger *kaprov1alpha2.Trigger) (time.Duration, error) {
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

func cooldownRemaining(trigger *kaprov1alpha2.Trigger, now time.Time) (time.Duration, error) {
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

func (r *PromotionTriggerReconciler) cooldownRemaining(ctx context.Context, trigger *kaprov1alpha2.Trigger, now time.Time) (time.Duration, error) {
	lastTriggeredAt, err := lastTriggerPromotionCreatedAt(ctx, r.Client, trigger)
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

// lastTriggerPromotionCreatedAt returns the most recent stamp time from
// either the managed Promotion's annotation or a child PromotionRun's
// annotation (legacy/back-compat). Used to seed the cooldown when the
// trigger's own status.lastTriggeredAt has been cleared (e.g. operator restart).
func lastTriggerPromotionCreatedAt(ctx context.Context, c client.Reader, trigger *kaprov1alpha2.Trigger) (time.Time, error) {
	var latest time.Time
	pick := func(annotations map[string]string, fallback time.Time) (time.Time, error) {
		if raw := annotations[promotionTriggerCreatedAnno]; raw != "" {
			parsed, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				return time.Time{}, err
			}
			return parsed, nil
		}
		return fallback, nil
	}
	var promotions kaprov1alpha2.PromotionList
	if err := c.List(ctx, &promotions, client.MatchingLabels{promotionTriggerLabel: trigger.Name}); err != nil {
		return time.Time{}, fmt.Errorf("list managed Promotions for cooldown: %w", err)
	}
	for _, p := range promotions.Items {
		t, err := pick(p.Annotations, p.CreationTimestamp.Time)
		if err != nil {
			return time.Time{}, fmt.Errorf("parse Promotion %s annotation: %w", p.Name, err)
		}
		if t.After(latest) {
			latest = t
		}
	}
	// Back-compat: legacy PromotionRuns created by the old trigger model
	// still count toward cooldown until they age out.
	var runs kaprov1alpha2.PromotionRunList
	if err := c.List(ctx, &runs, client.MatchingLabels{promotionTriggerLabel: trigger.Name}); err != nil {
		return time.Time{}, fmt.Errorf("list legacy PromotionRuns for cooldown: %w", err)
	}
	for _, r := range runs.Items {
		t, err := pick(r.Annotations, r.CreationTimestamp.Time)
		if err != nil {
			return time.Time{}, fmt.Errorf("parse PromotionRun %s annotation: %w", r.Name, err)
		}
		if t.After(latest) {
			latest = t
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

func setTriggerSuspended(trigger *kaprov1alpha2.Trigger, now time.Time) {
	setCondition(&trigger.Status.Conditions, conditionSuspended, metav1.ConditionTrue, "Suspended", "promotion trigger is suspended", trigger.Generation, now)
	setCondition(&trigger.Status.Conditions, "Ready", metav1.ConditionFalse, "Suspended", "promotion trigger is suspended", trigger.Generation, now)
	setCondition(&trigger.Status.Conditions, kaprov1alpha2.ConditionTypeReconciling, metav1.ConditionFalse, "Suspended", "promotion trigger is suspended", trigger.Generation, now)
	apimeta.RemoveStatusCondition(&trigger.Status.Conditions, kaprov1alpha2.ConditionTypeStalled)
}

func setTriggerNoArtifact(trigger *kaprov1alpha2.Trigger, now time.Time) {
	setCondition(&trigger.Status.Conditions, conditionSuspended, metav1.ConditionFalse, "Active", "promotion trigger is active", trigger.Generation, now)
	setCondition(&trigger.Status.Conditions, "Ready", metav1.ConditionFalse, "NoMatchingArtifact", "no matching artifact observed", trigger.Generation, now)
	setCondition(&trigger.Status.Conditions, conditionArtifactObserved, metav1.ConditionFalse, "NoMatchingArtifact", "no matching artifact observed", trigger.Generation, now)
	setCondition(&trigger.Status.Conditions, kaprov1alpha2.ConditionTypeReconciling, metav1.ConditionFalse, "NoMatchingArtifact", "source check completed", trigger.Generation, now)
	apimeta.RemoveStatusCondition(&trigger.Status.Conditions, kaprov1alpha2.ConditionTypeStalled)
}

func setTriggerReady(trigger *kaprov1alpha2.Trigger, now time.Time, reason, message string) {
	setCondition(&trigger.Status.Conditions, conditionSuspended, metav1.ConditionFalse, "Active", "promotion trigger is active", trigger.Generation, now)
	setCondition(&trigger.Status.Conditions, "Ready", metav1.ConditionTrue, reason, message, trigger.Generation, now)
	setCondition(&trigger.Status.Conditions, kaprov1alpha2.ConditionTypeReconciling, metav1.ConditionFalse, reason, "source check completed", trigger.Generation, now)
	apimeta.RemoveStatusCondition(&trigger.Status.Conditions, kaprov1alpha2.ConditionTypeStalled)
}

func setTriggerBlocked(trigger *kaprov1alpha2.Trigger, now time.Time, reason, message string) {
	setCondition(&trigger.Status.Conditions, conditionSuspended, metav1.ConditionFalse, "Active", "promotion trigger is active", trigger.Generation, now)
	setCondition(&trigger.Status.Conditions, "Ready", metav1.ConditionFalse, reason, message, trigger.Generation, now)
	setCondition(&trigger.Status.Conditions, kaprov1alpha2.ConditionTypeReconciling, metav1.ConditionFalse, reason, "source check completed", trigger.Generation, now)
	setCondition(&trigger.Status.Conditions, kaprov1alpha2.ConditionTypeStalled, metav1.ConditionTrue, reason, message, trigger.Generation, now)
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
