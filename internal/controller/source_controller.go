package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/crane"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	kaprometrics "kapro.io/kapro/internal/metrics"
)

const maxSourceDiscoveryLimit = 50
const sourceCredentialNamespace = "kapro-system"

// SourceReconciler watches OCI registries and auto-creates Artifact objects
// when new semver-matching tags are discovered.
type SourceReconciler struct {
	client.Client
	Recorder record.EventRecorder
	Scheme   *runtime.Scheme

	// ListTags allows injecting a fake for testing. Defaults to crane.ListTags.
	ListTags func(repo string, opts ...crane.Option) ([]string, error)
	// ResolveDigest allows injecting a fake for testing. Defaults to crane.Digest.
	ResolveDigest func(ref string, opts ...crane.Option) (string, error)

	// ShardPredicate optionally filters objects by shard label for horizontal scaling.
	// When nil, all objects are processed.
	ShardPredicate predicate.Predicate
}

// +kubebuilder:rbac:groups=kapro.io,resources=sources,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kapro.io,resources=sources/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=sources/finalizers,verbs=update
// +kubebuilder:rbac:groups=kapro.io,resources=artifacts,verbs=get;list;watch;create

func (r *SourceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	start := time.Now()
	resultLabel := "success"
	defer func() {
		kaprometrics.ControllerReconciles.WithLabelValues("source", resultLabel).Inc()
		kaprometrics.ControllerReconcileDuration.WithLabelValues("source").Observe(time.Since(start).Seconds())
	}()

	log := log.FromContext(ctx)

	var source kaprov1alpha1.Source
	if err := r.Get(ctx, req.NamespacedName, &source); err != nil {
		resultLabel = "error"
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !source.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, &source)
	}
	if controllerutil.AddFinalizer(&source, kaprov1alpha1.SourceFinalizer) {
		if err := r.Update(ctx, &source); err != nil {
			resultLabel = "error"
			return ctrl.Result{}, fmt.Errorf("add source finalizer: %w", err)
		}
	}

	if source.Spec.Suspended {
		log.Info("source is suspended, skipping")
		return ctrl.Result{}, nil
	}

	interval := parseDurationOrDefault(source.Spec.Interval)
	craneOpts, err := r.craneOptions(ctx, &source)
	if err != nil {
		resultLabel = "error"
		return ctrl.Result{RequeueAfter: interval}, r.updateSourceFailure(ctx, &source, "RegistryAuthFailed", err.Error())
	}

	// Poll the registry for tags.
	tags, err := r.listTags(source.Spec.Registry.Repository, craneOpts...)
	if err != nil {
		resultLabel = "error"
		_ = r.updateSourceFailure(ctx, &source, "RegistryPollFailed", fmt.Sprintf("failed to list tags: %v", err))
		return ctrl.Result{RequeueAfter: interval}, nil
	}

	// Filter by semver constraint.
	var constraint *semver.Constraints
	if source.Spec.SemverConstraint != "" {
		c, err := semver.NewConstraint(source.Spec.SemverConstraint)
		if err != nil {
			resultLabel = "error"
			_ = r.updateSourceFailure(ctx, &source, "InvalidSemverConstraint", fmt.Sprintf("invalid semver constraint: %v", err))
			return ctrl.Result{RequeueAfter: interval}, nil
		}
		constraint = c
	}

	matchingVersions := filterSemverTags(tags, constraint)
	if len(matchingVersions) == 0 {
		patch := client.MergeFrom(source.DeepCopy())
		source.Status.Phase = kaprov1alpha1.SourcePhaseReady
		source.Status.LastPolledAt = time.Now().UTC().Format(time.RFC3339)
		source.Status.ObservedGeneration = source.Generation
		apimeta.SetStatusCondition(&source.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionTrue,
			Reason:             "NoNewVersions",
			Message:            "no matching semver tags found",
			ObservedGeneration: source.Generation,
			LastTransitionTime: metav1.Now(),
		})
		_ = r.Status().Patch(ctx, &source, patch)
		return ctrl.Result{RequeueAfter: interval}, nil
	}

	selectedVersions := selectSourceVersions(matchingVersions, source.Spec.Discovery)
	discoveredByTag := discoveredVersionsByTag(source.Status.DiscoveredVersions)
	now := time.Now().UTC().Format(time.RFC3339)
	newDiscoveries := make([]kaprov1alpha1.DiscoveredVersion, 0, len(selectedVersions))

	for _, version := range selectedVersions {
		tag := version.Original()
		if _, alreadyDiscovered := discoveredByTag[tag]; alreadyDiscovered {
			continue
		}

		digest, err := r.resolveDigest(fmt.Sprintf("%s:%s", source.Spec.Registry.Repository, tag), craneOpts...)
		if err != nil {
			resultLabel = "error"
			_ = r.updateSourceFailure(ctx, &source, "DigestResolveFailed", fmt.Sprintf("failed to resolve digest for %s: %v", tag, err))
			return ctrl.Result{RequeueAfter: interval}, nil
		}

		artifactName := fmt.Sprintf("%s-%s", source.Name, tag)
		artifactRef := sanitizeName(artifactName)

		appKey := source.Spec.AppKey
		if appKey == "" {
			appKey = source.Name
		}
		artifact := &kaprov1alpha1.Artifact{
			ObjectMeta: metav1.ObjectMeta{
				Name: artifactRef,
				Labels: map[string]string{
					"kapro.io/source":  source.Name,
					"kapro.io/version": tag,
					"kapro.io/app-key": appKey,
				},
			},
			Spec: kaprov1alpha1.ArtifactSpec{
				Sources: []kaprov1alpha1.ArtifactSource{
					{
						Type: "oci",
						OCI: &kaprov1alpha1.OCIRef{
							Repository: source.Spec.Registry.Repository,
							Tag:        tag,
							Digest:     strings.TrimPrefix(digest, source.Spec.Registry.Repository+"@"),
						},
					},
				},
				Metadata: kaprov1alpha1.ArtifactMeta{
					ReleasedBy:  "kapro-source/" + source.Name,
					Description: fmt.Sprintf("Auto-discovered by Source %s", source.Name),
				},
			},
		}

		if source.Spec.ArtifactTemplate != nil {
			for k, v := range source.Spec.ArtifactTemplate.Labels {
				artifact.Labels[k] = v
			}
			mergeArtifactMetadata(&artifact.Spec.Metadata, source.Spec.ArtifactTemplate.Metadata)
		}

		if err := r.Create(ctx, artifact); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				resultLabel = "error"
				return ctrl.Result{RequeueAfter: requeueFast}, fmt.Errorf("create artifact %s: %w", artifactName, err)
			}
			log.Info("artifact already exists", "artifact", artifactName)
		} else {
			log.Info("created artifact for new version",
				"artifact", artifactName, "tag", tag)
			r.Recorder.Eventf(&source, corev1.EventTypeNormal, "ArtifactCreated",
				"discovered version %s, created artifact %s", tag, artifactName)
		}

		// Update discovered versions history.
		discovered := kaprov1alpha1.DiscoveredVersion{
			Tag:          tag,
			Digest:       digest,
			DiscoveredAt: now,
			ArtifactRef:  artifactRef,
		}

		newDiscoveries = append(newDiscoveries, discovered)
		discoveredByTag[tag] = discovered
	}

	if len(newDiscoveries) > 0 {
		source.Status.DiscoveredVersions = append(newDiscoveries, source.Status.DiscoveredVersions...)
	}
	source.Status.DiscoveredVersions = trimDiscoveredVersions(source.Status.DiscoveredVersions, source.Spec.Discovery)
	latest := selectedVersions[0].Original()
	if discovered, ok := discoveredVersionsByTag(source.Status.DiscoveredVersions)[latest]; ok {
		source.Status.LatestVersion = &discovered
	}

	patch := client.MergeFrom(source.DeepCopy())
	source.Status.Phase = kaprov1alpha1.SourcePhaseReady
	source.Status.LastPolledAt = time.Now().UTC().Format(time.RFC3339)
	source.Status.ObservedGeneration = source.Generation
	apimeta.SetStatusCondition(&source.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "PollSuccess",
		Message:            fmt.Sprintf("latest: %s", latest),
		ObservedGeneration: source.Generation,
		LastTransitionTime: metav1.Now(),
	})
	if err := r.Status().Patch(ctx, &source, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch source status: %w", err)
	}

	return ctrl.Result{RequeueAfter: interval}, nil
}

func (r *SourceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	predicates := []predicate.Predicate{sourcePredicates()}
	if r.ShardPredicate != nil {
		predicates = append(predicates, r.ShardPredicate)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&kaprov1alpha1.Source{}).
		WithEventFilter(predicate.And(predicates...)).
		Complete(r)
}

// listTags wraps crane.ListTags to allow test injection.
func (r *SourceReconciler) listTags(repo string, opts ...crane.Option) ([]string, error) {
	if r.ListTags != nil {
		return r.ListTags(repo, opts...)
	}
	return crane.ListTags(repo, opts...)
}

func (r *SourceReconciler) resolveDigest(ref string, opts ...crane.Option) (string, error) {
	if r.ResolveDigest != nil {
		return r.ResolveDigest(ref, opts...)
	}
	return crane.Digest(ref, opts...)
}

// filterSemverTags parses tags as semver, filters by constraint, and returns sorted ascending.
func filterSemverTags(tags []string, constraint *semver.Constraints) []*semver.Version {
	var versions []*semver.Version
	for _, tag := range tags {
		v, err := semver.NewVersion(tag)
		if err != nil {
			continue // skip non-semver tags
		}
		if constraint != nil && !constraint.Check(v) {
			continue
		}
		versions = append(versions, v)
	}
	sort.Sort(semver.Collection(versions))
	return versions
}

func selectSourceVersions(versions []*semver.Version, discovery *kaprov1alpha1.SourceDiscoverySpec) []*semver.Version {
	if len(versions) == 0 {
		return nil
	}
	limit := sourceDiscoveryLimit(discovery)
	if limit > len(versions) {
		limit = len(versions)
	}

	selected := make([]*semver.Version, 0, limit)
	for i := len(versions) - 1; i >= 0 && len(selected) < limit; i-- {
		selected = append(selected, versions[i])
	}
	return selected
}

func sourceDiscoveryLimit(discovery *kaprov1alpha1.SourceDiscoverySpec) int {
	if discovery == nil {
		return 1
	}
	switch discovery.Strategy {
	case kaprov1alpha1.SourceDiscoveryLastN:
		if discovery.Limit > 0 {
			if discovery.Limit > maxSourceDiscoveryLimit {
				return maxSourceDiscoveryLimit
			}
			return discovery.Limit
		}
		return 1
	default:
		return 1
	}
}

func discoveredVersionsByTag(discovered []kaprov1alpha1.DiscoveredVersion) map[string]kaprov1alpha1.DiscoveredVersion {
	byTag := make(map[string]kaprov1alpha1.DiscoveredVersion, len(discovered))
	for _, version := range discovered {
		if _, exists := byTag[version.Tag]; !exists {
			byTag[version.Tag] = version
		}
	}
	return byTag
}

func trimDiscoveredVersions(discovered []kaprov1alpha1.DiscoveredVersion, discovery *kaprov1alpha1.SourceDiscoverySpec) []kaprov1alpha1.DiscoveredVersion {
	limit := sourceDiscoveryLimit(discovery)
	if limit > maxSourceDiscoveryLimit {
		limit = maxSourceDiscoveryLimit
	}

	seen := make(map[string]struct{}, len(discovered))
	trimmed := make([]kaprov1alpha1.DiscoveredVersion, 0, limit)
	for _, version := range discovered {
		if _, exists := seen[version.Tag]; exists {
			continue
		}
		seen[version.Tag] = struct{}{}
		trimmed = append(trimmed, version)
		if len(trimmed) == limit {
			break
		}
	}
	return trimmed
}

// sanitizeName ensures a name is valid for Kubernetes object naming.
func sanitizeName(name string) string {
	// Replace dots and plus signs (common in semver) with dashes.
	result := make([]byte, 0, len(name))
	for i := range name {
		switch name[i] {
		case '.', '+':
			result = append(result, '-')
		default:
			result = append(result, name[i])
		}
	}
	if len(result) > 253 {
		result = result[:253]
	}
	return string(result)
}

func (r *SourceReconciler) handleDeletion(ctx context.Context, source *kaprov1alpha1.Source) (ctrl.Result, error) {
	var artifacts kaprov1alpha1.ArtifactList
	if err := r.List(ctx, &artifacts, client.MatchingLabels{"kapro.io/source": source.Name}); err != nil {
		return ctrl.Result{}, fmt.Errorf("list artifacts for source deletion: %w", err)
	}
	if len(artifacts.Items) > 0 {
		return ctrl.Result{RequeueAfter: requeueNormal}, r.updateSourceFailure(ctx, source, "ArtifactsRemain", fmt.Sprintf("cannot delete source while %d artifacts still reference it", len(artifacts.Items)))
	}
	patch := client.MergeFrom(source.DeepCopy())
	controllerutil.RemoveFinalizer(source, kaprov1alpha1.SourceFinalizer)
	if err := r.Patch(ctx, source, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("remove source finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

func (r *SourceReconciler) updateSourceFailure(ctx context.Context, source *kaprov1alpha1.Source, reason, message string) error {
	patch := client.MergeFrom(source.DeepCopy())
	source.Status.Phase = kaprov1alpha1.SourcePhaseFailed
	apimeta.SetStatusCondition(&source.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: source.Generation,
		LastTransitionTime: metav1.Now(),
	})
	source.Status.LastPolledAt = time.Now().UTC().Format(time.RFC3339)
	source.Status.ObservedGeneration = source.Generation
	return r.Status().Patch(ctx, source, patch)
}

func (r *SourceReconciler) craneOptions(ctx context.Context, source *kaprov1alpha1.Source) ([]crane.Option, error) {
	opts := []crane.Option{crane.WithContext(ctx)}
	authOpt, err := r.authOptionForSource(ctx, source)
	if err != nil {
		return nil, err
	}
	return append(opts, authOpt), nil
}

func (r *SourceReconciler) authOptionForSource(ctx context.Context, source *kaprov1alpha1.Source) (crane.Option, error) {
	if source.Spec.Registry.SecretRef == nil || source.Spec.Registry.SecretRef.Name == "" {
		return crane.WithAuthFromKeychain(authn.DefaultKeychain), nil
	}

	var secret corev1.Secret
	if err := r.Get(ctx, client.ObjectKey{Namespace: sourceCredentialNamespace, Name: source.Spec.Registry.SecretRef.Name}, &secret); err != nil {
		return nil, fmt.Errorf("get registry secret %s/%s: %w", sourceCredentialNamespace, source.Spec.Registry.SecretRef.Name, err)
	}

	if username, ok := secret.Data["username"]; ok {
		return crane.WithAuth(authn.FromConfig(authn.AuthConfig{
			Username: string(username),
			Password: string(secret.Data["password"]),
		})), nil
	}
	if tokenBytes, ok := secret.Data["token"]; ok {
		return crane.WithAuth(authn.FromConfig(authn.AuthConfig{
			RegistryToken: string(tokenBytes),
		})), nil
	}
	if dockerCfg, ok := secret.Data[corev1.DockerConfigJsonKey]; ok {
		auth, err := authFromDockerConfig(source.Spec.Registry.Repository, dockerCfg)
		if err != nil {
			return nil, err
		}
		return crane.WithAuth(auth), nil
	}

	return nil, fmt.Errorf("registry secret %s/%s must contain username/password, token, or %s", sourceCredentialNamespace, source.Spec.Registry.SecretRef.Name, corev1.DockerConfigJsonKey)
}

func authFromDockerConfig(repository string, raw []byte) (authn.Authenticator, error) {
	var cfg struct {
		Auths map[string]authn.AuthConfig `json:"auths"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse docker config json: %w", err)
	}
	host := repositoryHost(repository)
	for key, authCfg := range cfg.Auths {
		if key == host || strings.Contains(key, host) {
			return authn.FromConfig(authCfg), nil
		}
	}
	return nil, fmt.Errorf("docker config json does not contain credentials for %s", host)
}

func repositoryHost(repository string) string {
	parts := strings.Split(repository, "/")
	if len(parts) == 0 {
		return repository
	}
	return parts[0]
}

func mergeArtifactMetadata(dst *kaprov1alpha1.ArtifactMeta, src kaprov1alpha1.ArtifactMeta) {
	if src.ReleasedBy != "" {
		dst.ReleasedBy = src.ReleasedBy
	}
	if src.Description != "" {
		dst.Description = src.Description
	}
	if src.DerivedFrom != "" {
		dst.DerivedFrom = src.DerivedFrom
	}
	if len(src.ChangedComponents) > 0 {
		dst.ChangedComponents = append([]string(nil), src.ChangedComponents...)
	}
}

func sourcePredicates() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc:  func(_ event.CreateEvent) bool { return true },
		DeleteFunc:  func(_ event.DeleteEvent) bool { return true },
		GenericFunc: func(_ event.GenericEvent) bool { return true },
		UpdateFunc: func(e event.UpdateEvent) bool {
			if e.ObjectOld.GetGeneration() != e.ObjectNew.GetGeneration() {
				return true
			}
			oldDeleting := !e.ObjectOld.GetDeletionTimestamp().IsZero()
			newDeleting := !e.ObjectNew.GetDeletionTimestamp().IsZero()
			return oldDeleting != newDeleting
		},
	}
}
