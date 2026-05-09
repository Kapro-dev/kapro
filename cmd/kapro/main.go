// Command kapro is the CLI for the Kapro promotion engine.
//
// Usage:
//
//	kapro cluster bootstrap --name <cluster-name> [--labels key=value,...]
//	kapro get releases [-n namespace]
//	kapro get targets [-n namespace]
//	kapro approve <release>/<target> [-n namespace] [--comment text]
//	kapro rollback <release-name> --to <digest> [-n namespace]
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/internal/cli"
	internalgate "kapro.io/kapro/internal/gate"
)

var scheme = runtime.NewScheme()

func init() {
	_ = clientgoscheme.AddToScheme(scheme)
	_ = kaprov1alpha1.AddToScheme(scheme)
}

func main() {
	root := &cobra.Command{
		Use:   "kapro",
		Short: "The Canonical Promotion Layer for Kubernetes",
		Long: `kapro — multi-cluster progressive delivery engine.

Passes versions forward. Across targets. Across clusters. In waves.`,
	}

	root.PersistentFlags().StringVarP(&cli.OutputFormat, "output", "o", "", "Output format (json for machine-readable)")

	root.AddCommand(newDemoCmd())
	root.AddCommand(newClusterCmd())
	root.AddCommand(newGetCmd())
	root.AddCommand(newFleetCmd())
	root.AddCommand(newApproveCmd())
	root.AddCommand(newRejectCmd())
	root.AddCommand(newRollbackCmd())
	root.AddCommand(newArtifactCmd())
	root.AddCommand(newReleaseCmd())
	root.AddCommand(newPromoteCmd())
	root.AddCommand(newWorldCmd())

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func newClusterCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cluster",
		Short: "Manage cluster registrations",
	}
	cmd.AddCommand(newBootstrapCmd())
	return cmd
}

func newBootstrapCmd() *cobra.Command {
	var (
		clusterName string
		namespace   string
		labelsRaw   []string
		ttl         time.Duration
		kubeconfig  string
	)

	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Register a workload cluster with Kapro",
		Long: `Bootstrap creates a MemberCluster CR on the management cluster with a bootstrap token.
The Kapro operator processes it, creates a scoped ServiceAccount + RBAC, and
writes the cluster credentials to a Secret named kapro-cluster-<name>-credentials.

Steps:
  1. Run this command on a machine with access to the MANAGEMENT cluster kubeconfig.
  2. Copy the printed token to the WORKLOAD cluster.
  3. Start kapro-cluster-controller with --bootstrap-token=<token>.

Example:
  kapro cluster bootstrap --name de-prod --labels env=prod,region=eu-west`,

		RunE: func(cmd *cobra.Command, args []string) error {
			return runBootstrap(cmd.Context(), clusterName, namespace, labelsRaw, ttl, kubeconfig)
		},
	}

	cmd.Flags().StringVar(&clusterName, "name", "", "Cluster name (required, must match MemberCluster name)")
	cmd.Flags().StringVar(&namespace, "namespace", "kapro-system", "Namespace for bootstrap (unused — MemberCluster is cluster-scoped)")
	cmd.Flags().StringArrayVar(&labelsRaw, "labels", nil, "Labels for the MemberCluster (key=value)")
	cmd.Flags().DurationVar(&ttl, "ttl", 24*time.Hour, "Token TTL (default 24h)")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig (defaults to KUBECONFIG env / ~/.kube/config)")
	_ = cmd.MarkFlagRequired("name")

	return cmd
}

func runBootstrap(ctx context.Context, clusterName, namespace string, labelsRaw []string, ttl time.Duration, kubeconfigPath string) error {
	// Build management-cluster client.
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfigPath != "" {
		loadingRules.ExplicitPath = kubeconfigPath
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return fmt.Errorf("load kubeconfig: %w", err)
	}

	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("create client: %w", err)
	}

	// Parse labels.
	labels := map[string]string{}
	for _, kv := range labelsRaw {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid label %q (expected key=value)", kv)
		}
		labels[parts[0]] = parts[1]
	}

	// Generate a cryptographically random 32-byte token.
	rawBytes := make([]byte, 32)
	if _, err := rand.Read(rawBytes); err != nil {
		return fmt.Errorf("generate token: %w", err)
	}
	rawToken := hex.EncodeToString(rawBytes)

	// Store only the SHA-256 hash.
	hash := sha256.Sum256([]byte(rawToken))
	tokenHash := hex.EncodeToString(hash[:])

	expiresAt := metav1.NewTime(time.Now().Add(ttl))

	mc := &kaprov1alpha1.MemberCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:   clusterName,
			Labels: labels,
		},
		Spec: kaprov1alpha1.MemberClusterSpec{
			Actuator: kaprov1alpha1.ActuatorSpec{
				Type: "flux",
				Flux: &kaprov1alpha1.FluxActuator{
					Namespace:         "flux-system",
					OCIRepository:     clusterName,
					KustomizationPath: ".",
				},
			},
			Bootstrap: &kaprov1alpha1.MemberClusterBootstrapSpec{
				TokenHash: tokenHash,
				ExpiresAt: &expiresAt,
			},
		},
	}

	sp := cli.NewSpinner("Creating MemberCluster")
	sp.Start()
	if err := c.Create(ctx, mc); err != nil {
		sp.StopFail("Failed to create MemberCluster")
		return fmt.Errorf("create MemberCluster: %w", err)
	}
	sp.StopSuccess("MemberCluster created")

	cli.KV("Cluster", clusterName)
	cli.KV("Actuator", "flux-operator")
	cli.KV("Labels", fmt.Sprintf("%v", labels))
	fmt.Fprintln(cli.Out)
	cli.Info("Flux Operator handles spoke delivery — no kapro component needed on the spoke.")
	cli.Muted("Check status: kubectl get membercluster " + clusterName)
	return nil
}

// ─── kapro get ────────────────────────────────────────────────────────────────

func newGetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <releases|targets>",
		Short: "Display Kapro resources",
	}
	cmd.AddCommand(newGetReleasesCmd())
	cmd.AddCommand(newGetTargetsCmd())
	return cmd
}

func newGetReleasesCmd() *cobra.Command {
	var (
		namespace     string
		allNamespaces bool
		kubeconfig    string
	)
	cmd := &cobra.Command{
		Use:   "releases",
		Short: "List Release objects",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runGetReleases(cmd.Context(), namespace, allNamespaces, kubeconfig)
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Namespace (empty = all namespaces)")
	cmd.Flags().BoolVarP(&allNamespaces, "all-namespaces", "A", false, "List across all namespaces")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	return cmd
}

func runGetReleases(ctx context.Context, namespace string, allNamespaces bool, kubeconfigPath string) error {
	sp := cli.NewSpinner("Fetching releases")
	sp.Start()

	c, err := buildClient(kubeconfigPath)
	if err != nil {
		sp.StopFail("Failed to connect to cluster")
		return err
	}

	var list kaprov1alpha1.ReleaseList
	opts := listOpts(namespace, allNamespaces)
	if err := c.List(ctx, &list, opts...); err != nil {
		sp.StopFail("Failed to list releases")
		return fmt.Errorf("list releases: %w", err)
	}
	sp.Stop()

	if cli.IsJSON() {
		return cli.JSON(list.Items)
	}

	if len(list.Items) == 0 {
		cli.Muted("No releases found.")
		return nil
	}

	// Count summary.
	progressing, complete, failed := 0, 0, 0
	for _, r := range list.Items {
		switch r.Status.Phase {
		case kaprov1alpha1.ReleasePhaseProgressing:
			progressing++
		case kaprov1alpha1.ReleasePhaseComplete:
			complete++
		case kaprov1alpha1.ReleasePhaseFailed:
			failed++
		}
	}
	cli.Header("Releases")
	cli.Infof("%d total  %s  %s  %s",
		len(list.Items),
		cli.Theme.PhaseProgressing.Render(fmt.Sprintf("%d progressing", progressing)),
		cli.Theme.PhaseComplete.Render(fmt.Sprintf("%d complete", complete)),
		cli.Theme.PhaseFailed.Render(fmt.Sprintf("%d failed", failed)),
	)

	tbl := cli.NewTable("NAME", "ARTIFACT", "PHASE", "PIPELINES", "AGE")
	for _, r := range list.Items {
		pipelines := ""
		for i, p := range r.Spec.Pipelines {
			if i > 0 {
				pipelines += ", "
			}
			pipelines += p.Pipeline
		}
		tbl.AddRow(r.Name, r.Spec.Artifact, string(r.Status.Phase), pipelines, cli.Age(r.CreationTimestamp.Time))
	}
	tbl.Render()
	return nil
}

func newGetTargetsCmd() *cobra.Command {
	var (
		namespace     string
		allNamespaces bool
		phase         string
		kubeconfig    string
	)
	cmd := &cobra.Command{
		Use:   "targets",
		Short: "List target cluster rollout status across all Releases",
		Long: `List target cluster rollout entries from ReleaseTarget objects.

ReleaseTarget is the authoritative per-target execution state store.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runGetTargets(cmd.Context(), namespace, allNamespaces, phase, kubeconfig)
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Namespace (empty = all namespaces)")
	cmd.Flags().BoolVarP(&allNamespaces, "all-namespaces", "A", false, "List across all namespaces")
	cmd.Flags().StringVar(&phase, "phase", "", "Filter by phase (e.g. WaitingApproval, Applying, Converged)")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	return cmd
}

func runGetTargets(ctx context.Context, namespace string, allNamespaces bool, phase, kubeconfigPath string) error {
	sp := cli.NewSpinner("Fetching targets")
	sp.Start()

	c, err := buildClient(kubeconfigPath)
	if err != nil {
		sp.StopFail("Failed to connect to cluster")
		return err
	}

	var targetList kaprov1alpha1.ReleaseTargetList
	if err := c.List(ctx, &targetList); err != nil {
		sp.StopFail("Failed to list targets")
		return fmt.Errorf("list release targets: %w", err)
	}
	sp.Stop()

	// Filter by phase if specified.
	var filtered []kaprov1alpha1.ReleaseTarget
	for _, t := range targetList.Items {
		if phase == "" || string(t.Status.Phase) == phase {
			filtered = append(filtered, t)
		}
	}

	if cli.IsJSON() {
		return cli.JSON(filtered)
	}

	if len(filtered) == 0 {
		cli.Muted("No targets found.")
		return nil
	}

	cli.Header("Release Targets")

	tbl := cli.NewTable("RELEASE", "TARGET", "STAGE", "PHASE", "VERSION", "AGE")
	for _, t := range filtered {
		version := t.Spec.Version
		if len(version) > 20 {
			version = version[:17] + "..."
		}
		tbl.AddRow(
			t.Spec.ReleaseRef, t.Spec.Target, t.Spec.Stage,
			string(t.Status.Phase), version, cli.Age(t.CreationTimestamp.Time),
		)
	}
	tbl.Render()
	return nil
}

// ─── kapro approve ────────────────────────────────────────────────────────────

func newApproveCmd() *cobra.Command {
	var (
		namespace  string
		comment    string
		kubeconfig string
	)
	cmd := &cobra.Command{
		Use:   "approve <release>/<target>",
		Short: "Approve a target cluster waiting for human gate",
		Long: `Create an Approval for a specific target cluster in WaitingApproval phase.

The argument format is <release-name>/<target-cluster>.

Examples:
  kapro approve v1.2.3/de-prod
  kapro approve v1.2.3/de-prod --comment "checked canary metrics, all green"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runApprove(cmd.Context(), args[0], namespace, comment, kubeconfig)
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "Namespace of the Release")
	cmd.Flags().StringVar(&comment, "comment", "", "Optional approval comment (required for bypass)")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	return cmd
}

func runApprove(ctx context.Context, releaseTarget, namespace, comment, kubeconfigPath string) error {
	parts := strings.SplitN(releaseTarget, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("argument must be <release>/<target>, got %q", releaseTarget)
	}
	releaseName, targetName := parts[0], parts[1]

	c, err := buildClient(kubeconfigPath)
	if err != nil {
		return err
	}

	var rel kaprov1alpha1.Release
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: releaseName}, &rel); err != nil {
		return fmt.Errorf("get release %q: %w", releaseName, err)
	}

	targets, err := listReleaseTargetsForRelease(ctx, c, namespace, releaseName)
	if err != nil {
		return err
	}
	selected := selectApprovalTarget(targets, targetName)
	if selected == nil {
		return fmt.Errorf("target %q not found in release %q", targetName, releaseName)
	}
	if selected.Status.Phase != kaprov1alpha1.TargetPhaseWaitingApproval {
		fmt.Printf("⚠️  Target %q is in phase %q (not WaitingApproval) — approving anyway.\n",
			targetName, selected.Status.Phase)
	}

	ref := approvalRefForTarget(*selected)
	approvalName := internalgate.ApprovalName(releaseName, ref)
	approval := &kaprov1alpha1.Approval{
		ObjectMeta: metav1.ObjectMeta{
			Name: approvalName,
		},
		Spec: kaprov1alpha1.ApprovalSpec{
			Ref:     ref,
			Release: releaseName,
			Target:  targetName,
			Comment: comment,
			// approvedBy will be overwritten by the ApprovalMutator webhook
			// with the real Kubernetes username from the admission request.
		},
	}

	sp := cli.NewSpinner("Creating approval")
	sp.Start()
	if err := c.Create(ctx, approval); err != nil {
		sp.StopFail("Failed to create approval")
		return fmt.Errorf("create approval: %w", err)
	}
	sp.StopSuccess("Approval created")

	cli.KV("Approval", approvalName)
	cli.KV("Release", releaseName)
	cli.KV("Target", targetName)
	if comment != "" {
		cli.KV("Comment", comment)
	}
	return nil
}

// ─── kapro rollback ───────────────────────────────────────────────────────────

func newRollbackCmd() *cobra.Command {
	var (
		toDigest   string
		namespace  string
		targets    []string
		kubeconfig string
	)
	cmd := &cobra.Command{
		Use:   "rollback <release-name>",
		Short: "Roll back a release to a previous OCI digest",
		Long: `Create a new Release pointing at a previous OCI digest.

The original Release is never modified (immutable). A new Artifact CR is
created with the provided digest and a new Release is created referencing it.

Use --target to scope rollback to specific clusters (hotfix / partial rollback).

Examples:
  kapro rollback my-release --to sha256:abc123def456
  kapro rollback my-release --to sha256:abc123def456 --target de-prod --target fi-prod`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRollback(cmd.Context(), args[0], toDigest, namespace, targets, kubeconfig)
		},
	}
	cmd.Flags().StringVar(&toDigest, "to", "", "OCI digest to roll back to (required)")
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "Namespace of the Release")
	cmd.Flags().StringArrayVar(&targets, "target", nil, "Restrict rollback to specific target clusters (repeatable)")
	cmd.Flags().StringArrayVar(&targets, "env", nil, "Deprecated alias for --target")
	_ = cmd.Flags().MarkHidden("env")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	_ = cmd.MarkFlagRequired("to")
	return cmd
}

func runRollback(ctx context.Context, releaseName, toDigest, namespace string, targets []string, kubeconfigPath string) error {
	c, err := buildClient(kubeconfigPath)
	if err != nil {
		return err
	}

	// Fetch the original Release.
	var orig kaprov1alpha1.Release
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: releaseName}, &orig); err != nil {
		return fmt.Errorf("get release %q: %w", releaseName, err)
	}

	// Fetch the Artifact to extract the OCI repository URL.
	var origArtifact kaprov1alpha1.Artifact
	if err := c.Get(ctx, types.NamespacedName{Name: orig.Spec.Artifact}, &origArtifact); err != nil {
		return fmt.Errorf("get artifact %q: %w", orig.Spec.Artifact, err)
	}
	if len(origArtifact.Spec.Sources) == 0 || origArtifact.Spec.Sources[0].OCI == nil {
		return fmt.Errorf("artifact %q has no OCI source", orig.Spec.Artifact)
	}
	repo := origArtifact.Spec.Sources[0].OCI.Repository

	// Derive short suffix from digest for readable names.
	suffix := shortHash(toDigest)

	// Create a rollback Artifact CR pinned at the requested digest.
	rbArtifactName := orig.Spec.Artifact + "-rb-" + suffix
	rbArtifact := &kaprov1alpha1.Artifact{
		ObjectMeta: metav1.ObjectMeta{Name: rbArtifactName},
		Spec: kaprov1alpha1.ArtifactSpec{
			Sources: []kaprov1alpha1.ArtifactSource{
				{
					Type: "oci",
					OCI: &kaprov1alpha1.OCIRef{
						Repository: repo,
						Tag:        "rollback-" + suffix,
						Digest:     toDigest,
					},
				},
			},
			Metadata: kaprov1alpha1.ArtifactMeta{
				ReleasedBy:  "kapro-cli-rollback",
				Description: "Rollback from " + releaseName + " to " + toDigest,
			},
		},
	}
	if err := c.Create(ctx, rbArtifact); err != nil {
		return fmt.Errorf("create rollback artifact: %w", err)
	}

	// Create a rollback Release with the same pipelines but the rollback Artifact.
	rbReleaseName := releaseName + "-rb-" + suffix
	rbSpec := kaprov1alpha1.ReleaseSpec{
		Artifact:    rbArtifactName,
		Pipelines:   orig.Spec.Pipelines,
		AppKey:      orig.Spec.AppKey,
		DerivedFrom: releaseName,
	}
	if len(targets) > 0 {
		rbSpec.Scope = &kaprov1alpha1.ReleaseScope{Targets: targets}
	}
	rbRelease := &kaprov1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:      rbReleaseName,
			Namespace: namespace,
			Annotations: map[string]string{
				"kapro.io/rollback-from":   releaseName,
				"kapro.io/rollback-digest": toDigest,
			},
		},
		Spec: rbSpec,
	}
	if err := c.Create(ctx, rbRelease); err != nil {
		// Clean up the artifact we just created to avoid orphan objects.
		_ = c.Delete(ctx, rbArtifact)
		return fmt.Errorf("create rollback release: %w", err)
	}

	fmt.Printf("✅ Rollback release created: %s/%s\n", namespace, rbReleaseName)
	fmt.Printf("   Original release: %s\n", releaseName)
	fmt.Printf("   Rollback digest:  %s\n", toDigest)
	fmt.Printf("   Rollback artifact: %s\n", rbArtifactName)
	if len(targets) > 0 {
		fmt.Printf("   Scoped to targets: %s\n", strings.Join(targets, ", "))
	}
	fmt.Printf("\nMonitor progress:\n  kapro get targets -n %s\n", namespace)
	return nil
}

// ─── shared helpers ───────────────────────────────────────────────────────────

func buildClient(kubeconfigPath string) (client.Client, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfigPath != "" {
		loadingRules.ExplicitPath = kubeconfigPath
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules, &clientcmd.ConfigOverrides{},
	).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("create client: %w", err)
	}
	return c, nil
}

func listOpts(namespace string, allNamespaces bool) []client.ListOption {
	if allNamespaces || namespace == "" {
		return nil
	}
	return []client.ListOption{client.InNamespace(namespace)}
}

func age(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func shortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])[:8]
}

func approvalRefForTarget(target kaprov1alpha1.ReleaseTarget) string {
	return target.Name
}

func selectApprovalTarget(targets []kaprov1alpha1.ReleaseTarget, targetName string) *kaprov1alpha1.ReleaseTarget {
	for i := range targets {
		target := &targets[i]
		if target.Spec.Target == targetName && target.Status.Phase == kaprov1alpha1.TargetPhaseWaitingApproval {
			return target
		}
	}
	for i := range targets {
		target := &targets[i]
		if target.Spec.Target == targetName {
			return target
		}
	}
	return nil
}

func listReleaseTargetsForRelease(ctx context.Context, c client.Client, namespace, releaseName string) ([]kaprov1alpha1.ReleaseTarget, error) {
	var targetList kaprov1alpha1.ReleaseTargetList
	if err := c.List(ctx, &targetList); err != nil {
		return nil, fmt.Errorf("list release targets: %w", err)
	}
	targets := make([]kaprov1alpha1.ReleaseTarget, 0)
	for _, target := range targetList.Items {
		if target.Spec.ReleaseRef == releaseName {
			targets = append(targets, target)
		}
	}
	return targets, nil
}

// ─── kapro artifact ───────────────────────────────────────────────────────────

func newArtifactCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "artifact",
		Short: "Manage Kapro Artifact CRs",
	}
	cmd.AddCommand(newArtifactPushCmd())
	return cmd
}

func newArtifactPushCmd() *cobra.Command {
	var (
		name        string
		repository  string
		tag         string
		digest      string
		releasedBy  string
		desc        string
		derivedFrom string
		changed     []string
		kubeconfig  string
	)
	cmd := &cobra.Command{
		Use:   "push",
		Short: "Create an Artifact CR from an OCI bundle",
		Long: `Register an OCI bundle as a Kapro Artifact CR.

Run 'flux push artifact' first to push the bundle to the registry, then use
this command to create the Artifact CR that Kapro releases reference.

Example:
  # Step 1 — push the bundle (flux CLI)
  flux push artifact oci://registry.example.com/my-app:v1.2.3 --path=./bundle

  # Step 2 — register with Kapro
  kapro artifact push \
    --name=my-app-v1.2.3 \
    --repository=registry.example.com/my-app \
    --tag=v1.2.3 \
    --digest=sha256:abc123 \
    --released-by=ci-pipeline`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runArtifactPush(cmd.Context(), name, repository, tag, digest,
				releasedBy, desc, derivedFrom, changed, kubeconfig)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Artifact CR name (required)")
	cmd.Flags().StringVar(&repository, "repository", "", "OCI repository URL (required)")
	cmd.Flags().StringVar(&tag, "tag", "", "OCI tag (required)")
	cmd.Flags().StringVar(&digest, "digest", "", "OCI digest sha256:... (required)")
	cmd.Flags().StringVar(&releasedBy, "released-by", "ci", "Who/what released this artifact")
	cmd.Flags().StringVar(&desc, "description", "", "Human-readable description")
	cmd.Flags().StringVar(&derivedFrom, "derived-from", "", "Parent Artifact name (for hotfix lineage)")
	cmd.Flags().StringArrayVar(&changed, "changed", nil, "Changed component (repeatable: --changed svc-a --changed svc-b)")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("repository")
	_ = cmd.MarkFlagRequired("tag")
	_ = cmd.MarkFlagRequired("digest")
	return cmd
}

func runArtifactPush(ctx context.Context, name, repository, tag, digest,
	releasedBy, description, derivedFrom string, changed []string, kubeconfigPath string) error {
	c, err := buildClient(kubeconfigPath)
	if err != nil {
		return err
	}

	artifact := &kaprov1alpha1.Artifact{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: kaprov1alpha1.ArtifactSpec{
			Sources: []kaprov1alpha1.ArtifactSource{
				{
					Type: "oci",
					OCI: &kaprov1alpha1.OCIRef{
						Repository: repository,
						Tag:        tag,
						Digest:     digest,
					},
				},
			},
			Metadata: kaprov1alpha1.ArtifactMeta{
				ReleasedBy:        releasedBy,
				Description:       description,
				DerivedFrom:       derivedFrom,
				ChangedComponents: changed,
			},
		},
	}

	if err := c.Create(ctx, artifact); err != nil {
		return fmt.Errorf("create Artifact: %w", err)
	}

	fmt.Printf("✅ Artifact created: %s\n", name)
	fmt.Printf("   Repository: %s\n", repository)
	fmt.Printf("   Tag:        %s\n", tag)
	fmt.Printf("   Digest:     %s\n", digest)
	if derivedFrom != "" {
		fmt.Printf("   Derived from: %s\n", derivedFrom)
	}
	if len(changed) > 0 {
		fmt.Printf("   Changed:    %s\n", strings.Join(changed, ", "))
	}
	return nil
}

// ─── kapro release ────────────────────────────────────────────────────────────

func newReleaseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "release",
		Short: "Manage Kapro Releases",
	}
	cmd.AddCommand(newReleaseCreateCmd())
	return cmd
}

func newReleaseCreateCmd() *cobra.Command {
	var (
		name        string
		artifact    string
		pipelines   []string
		scope       []string
		derivedFrom string
		namespace   string
		appKey      string
		kubeconfig  string
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a Release to deliver an artifact across the fleet",
		Long: `Create a Kapro Release CR that drives progressive delivery.

Examples:
  # Full-fleet release
  kapro release create --name v1.2.3 --artifact my-app-v1.2.3 --pipeline global

  # Hotfix targeting specific clusters only
  kapro release create --name v1.2.3-hotfix --artifact my-app-v1.2.3-hotfix \
    --pipeline global --scope de-prod --scope fi-prod \
    --derived-from v1.2.3`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runReleaseCreate(cmd.Context(), name, artifact, pipelines, scope, derivedFrom, namespace, appKey, kubeconfig)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Release name (required)")
	cmd.Flags().StringVar(&artifact, "artifact", "", "Artifact CR name to deliver (required)")
	cmd.Flags().StringArrayVar(&pipelines, "pipeline", nil, "Pipeline name (repeatable; required at least once)")
	cmd.Flags().StringArrayVar(&scope, "scope", nil, "Restrict to target cluster (repeatable: --scope de-prod --scope fi-prod)")
	cmd.Flags().StringVar(&derivedFrom, "derived-from", "", "Parent Release name (for hotfix lineage)")
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "Namespace for the Release")
	cmd.Flags().StringVar(&appKey, "app-key", "", "App key for MemberCluster version tracking (defaults to artifact name)")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("artifact")
	return cmd
}

func runReleaseCreate(ctx context.Context, name, artifact string, pipelines, scope []string,
	derivedFrom, namespace, appKey, kubeconfigPath string) error {
	c, err := buildClient(kubeconfigPath)
	if err != nil {
		return err
	}

	if len(pipelines) == 0 {
		return fmt.Errorf("at least one --pipeline is required")
	}

	refs := make([]kaprov1alpha1.ReleasePipelineRef, 0, len(pipelines))
	for i, p := range pipelines {
		refs = append(refs, kaprov1alpha1.ReleasePipelineRef{
			Name:     fmt.Sprintf("p%d", i+1),
			Pipeline: p,
		})
	}

	spec := kaprov1alpha1.ReleaseSpec{
		Artifact:    artifact,
		Pipelines:   refs,
		AppKey:      appKey,
		DerivedFrom: derivedFrom,
	}
	if len(scope) > 0 {
		spec.Scope = &kaprov1alpha1.ReleaseScope{Targets: scope}
	}

	rel := &kaprov1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: spec,
	}

	if err := c.Create(ctx, rel); err != nil {
		return fmt.Errorf("create Release: %w", err)
	}

	fmt.Printf("✅ Release created: %s/%s\n", namespace, name)
	fmt.Printf("   Artifact:  %s\n", artifact)
	pipelineNames := make([]string, len(pipelines))
	copy(pipelineNames, pipelines)
	fmt.Printf("   Pipelines: %s\n", strings.Join(pipelineNames, ", "))
	if len(scope) > 0 {
		fmt.Printf("   Scope:     %s\n", strings.Join(scope, ", "))
	}
	if derivedFrom != "" {
		fmt.Printf("   Derived from: %s\n", derivedFrom)
	}
	fmt.Printf("\nMonitor progress:\n  kapro get releases -n %s\n", namespace)
	return nil
}

// ─── kapro promote ────────────────────────────────────────────────────────────

func newPromoteCmd() *cobra.Command {
	var (
		releaseName string
		pipeline    string
		stage       string
		targets     []string
		comment     string
		namespace   string
		kubeconfig  string
	)
	cmd := &cobra.Command{
		Use:   "promote",
		Short: "Manually advance a stage past its gate",
		Long: `Create Approval CRs for all Syncs in a stage that are waiting for human approval.

This is the bulk equivalent of 'kapro approve' — it targets every cluster
in a stage rather than a single Sync by name.

Examples:
  # Approve all targets in canary stage
  kapro promote --release v1.2.3 --pipeline global --stage canary --comment "LGTM"

  # Approve specific targets only
  kapro promote --release v1.2.3 --pipeline global --stage canary \
    --target de-prod --target fi-prod --comment "approved for EU first"`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPromote(cmd.Context(), releaseName, pipeline, stage, targets, comment, namespace, kubeconfig)
		},
	}
	cmd.Flags().StringVar(&releaseName, "release", "", "Release name (required)")
	cmd.Flags().StringVar(&pipeline, "pipeline", "", "Pipeline name the stage belongs to")
	cmd.Flags().StringVar(&stage, "stage", "", "Stage name (required)")
	cmd.Flags().StringArrayVar(&targets, "target", nil, "Target specific clusters only (repeatable)")
	cmd.Flags().StringArrayVar(&targets, "env", nil, "Deprecated alias for --target")
	_ = cmd.Flags().MarkHidden("env")
	cmd.Flags().StringVar(&comment, "comment", "", "Approval comment (required if bypass was requested)")
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "Namespace")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	_ = cmd.MarkFlagRequired("release")
	_ = cmd.MarkFlagRequired("stage")
	return cmd
}

func runPromote(ctx context.Context, releaseName, pipeline, stage string, targets []string,
	comment, namespace, kubeconfigPath string) error {
	c, err := buildClient(kubeconfigPath)
	if err != nil {
		return err
	}

	targetsForRelease, err := listReleaseTargetsForRelease(ctx, c, namespace, releaseName)
	if err != nil {
		return err
	}

	// Build target allowlist if provided.
	targetSet := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		targetSet[target] = struct{}{}
	}

	approved := 0
	skipped := 0
	total := 0
	for _, target := range targetsForRelease {
		if target.Spec.Stage != stage {
			continue
		}
		if pipeline != "" && target.Spec.Pipeline != pipeline {
			continue
		}
		total++
		if target.Status.Phase != kaprov1alpha1.TargetPhaseWaitingApproval {
			skipped++
			continue
		}
		if len(targetSet) > 0 {
			if _, ok := targetSet[target.Spec.Target]; !ok {
				skipped++
				continue
			}
		}

		ref := approvalRefForTarget(target)
		approvalName := internalgate.ApprovalName(releaseName, ref)
		approval := &kaprov1alpha1.Approval{
			ObjectMeta: metav1.ObjectMeta{
				Name: approvalName,
			},
			Spec: kaprov1alpha1.ApprovalSpec{
				Ref:     ref,
				Release: releaseName,
				Target:  target.Spec.Target,
				Comment: comment,
			},
		}
		if err := c.Create(ctx, approval); client.IgnoreAlreadyExists(err) != nil {
			fmt.Printf("⚠️  Failed to create approval for %s: %v\n", target.Spec.Target, err)
			continue
		}
		fmt.Printf("✅ Approved: %s/%s (stage: %s)\n", releaseName, target.Spec.Target, stage)
		approved++
	}

	if approved == 0 && total == 0 {
		fmt.Printf("ℹ️  No targets in stage=%s for release=%s\n", stage, releaseName)
		return nil
	}
	if approved == 0 {
		fmt.Printf("ℹ️  No targets in WaitingApproval for release=%s stage=%s\n", releaseName, stage)
		return nil
	}
	fmt.Printf("\n%d approval(s) created, %d skipped\n", approved, skipped)
	return nil
}

// ─── kapro world ──────────────────────────────────────────────────────────────

func newWorldCmd() *cobra.Command {
	var (
		env        string
		kubeconfig string
	)
	cmd := &cobra.Command{
		Use:   "world",
		Short: "Fleet-wide status: all clusters, versions, and health",
		Long: `Show a table of every MemberCluster with its current version, phase, and health.

Equivalent to 'kubectl get memberclusters' but formatted for delivery monitoring.

Examples:
  kapro world
  kapro world --env prod`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runWorld(cmd.Context(), env, kubeconfig)
		},
	}
	cmd.Flags().StringVar(&env, "target", "", "Filter by target cluster name")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	return cmd
}

func runWorld(ctx context.Context, envFilter, kubeconfigPath string) error {
	sp := cli.NewSpinner("Fetching fleet status")
	sp.Start()

	c, err := buildClient(kubeconfigPath)
	if err != nil {
		sp.StopFail("Failed to connect to cluster")
		return err
	}

	var list kaprov1alpha1.MemberClusterList
	opts := []client.ListOption{client.Limit(2000)}
	if envFilter != "" {
		opts = append(opts, client.MatchingLabels{"kapro.io/target": envFilter})
	}
	if err := c.List(ctx, &list, opts...); err != nil {
		sp.StopFail("Failed to list clusters")
		return fmt.Errorf("list member clusters: %w", err)
	}
	sp.Stop()

	if cli.IsJSON() {
		return cli.JSON(list.Items)
	}

	if len(list.Items) == 0 {
		cli.Muted("No member clusters found.")
		return nil
	}

	healthy, degraded, unknown := 0, 0, 0
	for _, mc := range list.Items {
		switch {
		case mc.Status.Health.AllWorkloadsReady:
			healthy++
		case mc.Status.LastHeartbeat != "":
			degraded++
		default:
			unknown++
		}
	}

	cli.Header("Fleet")
	cli.Infof("%d clusters  %s  %s  %s",
		len(list.Items),
		cli.Theme.PhaseComplete.Render(fmt.Sprintf("%d healthy", healthy)),
		cli.Theme.PhaseFailed.Render(fmt.Sprintf("%d degraded", degraded)),
		cli.Theme.Muted.Render(fmt.Sprintf("%d unknown", unknown)),
	)

	tbl := cli.NewTable("CLUSTER", "PHASE", "HEALTHY", "ACTIVE RELEASE", "HEARTBEAT", "AGE")
	for _, mc := range list.Items {
		healthStr := "?"
		if mc.Status.Health.AllWorkloadsReady {
			healthStr = "Healthy"
		} else if mc.Status.LastHeartbeat != "" {
			healthStr = "Degraded"
		}
		heartbeat := "-"
		if mc.Status.LastHeartbeat != "" {
			if t, err := time.Parse(time.RFC3339, mc.Status.LastHeartbeat); err == nil {
				heartbeat = cli.Age(t) + " ago"
			}
		}
		activeRelease := mc.Status.ActiveRelease
		if activeRelease == "" {
			activeRelease = "-"
		}
		tbl.AddRow(mc.Name, string(mc.Status.Phase), healthStr, activeRelease, heartbeat, cli.Age(mc.CreationTimestamp.Time))
	}
	tbl.Render()
	return nil
}

// ─── kapro fleet ─────────────────────────────────────────────────────────────

func newFleetCmd() *cobra.Command {
	var kubeconfig string
	cmd := &cobra.Command{
		Use:   "fleet",
		Short: "Fleet summary: clusters, releases, and pending decisions",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runFleet(cmd.Context(), kubeconfig)
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	return cmd
}

func runFleet(ctx context.Context, kubeconfigPath string) error {
	sp := cli.NewSpinner("Loading fleet state")
	sp.Start()

	c, err := buildClient(kubeconfigPath)
	if err != nil {
		sp.StopFail("Failed to connect")
		return err
	}

	var clusters kaprov1alpha1.MemberClusterList
	var releases kaprov1alpha1.ReleaseList
	var targets kaprov1alpha1.ReleaseTargetList
	if err := c.List(ctx, &clusters); err != nil {
		sp.StopFail("Failed to list clusters")
		return err
	}
	if err := c.List(ctx, &releases); err != nil {
		sp.StopFail("Failed to list releases")
		return err
	}
	if err := c.List(ctx, &targets); err != nil {
		sp.StopFail("Failed to list targets")
		return err
	}
	sp.Stop()

	healthy, degraded := 0, 0
	for _, mc := range clusters.Items {
		if mc.Status.Health.AllWorkloadsReady {
			healthy++
		} else {
			degraded++
		}
	}
	activeReleases, pendingDecisions := 0, 0
	for _, r := range releases.Items {
		if r.Status.Phase == kaprov1alpha1.ReleasePhaseProgressing {
			activeReleases++
		}
	}
	for _, t := range targets.Items {
		if t.Status.Phase == kaprov1alpha1.TargetPhaseWaitingApproval {
			pendingDecisions++
		}
	}

	if cli.IsJSON() {
		return cli.JSON(map[string]any{
			"clusters":         len(clusters.Items),
			"healthyClusters":  healthy,
			"degradedClusters": degraded,
			"activeReleases":   activeReleases,
			"pendingDecisions": pendingDecisions,
			"totalReleases":    len(releases.Items),
			"totalTargets":     len(targets.Items),
		})
	}

	cli.Header("Fleet Overview")
	fmt.Fprintln(cli.Out)
	cli.KV("Clusters", fmt.Sprintf("%d total, %s, %s",
		len(clusters.Items),
		cli.Theme.PhaseComplete.Render(fmt.Sprintf("%d healthy", healthy)),
		cli.Theme.PhaseFailed.Render(fmt.Sprintf("%d degraded", degraded)),
	))
	cli.KV("Releases", fmt.Sprintf("%d total, %s",
		len(releases.Items),
		cli.Theme.PhaseProgressing.Render(fmt.Sprintf("%d active", activeReleases)),
	))
	if pendingDecisions > 0 {
		cli.KV("Pending", cli.Theme.PhaseWaiting.Render(fmt.Sprintf("%d targets waiting for approval", pendingDecisions)))
	} else {
		cli.KV("Pending", cli.Theme.Muted.Render("none"))
	}
	fmt.Fprintln(cli.Out)
	return nil
}

// ─── kapro reject ────────────────────────────────────────────────────────────

func newRejectCmd() *cobra.Command {
	var (
		reason     string
		kubeconfig string
	)
	cmd := &cobra.Command{
		Use:   "reject <release>/<target>",
		Short: "Reject a target cluster waiting for approval",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReject(cmd.Context(), args[0], reason, kubeconfig)
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "Reason for rejection (required)")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	_ = cmd.MarkFlagRequired("reason")
	return cmd
}

func runReject(ctx context.Context, releaseTarget, reason, kubeconfigPath string) error {
	parts := strings.SplitN(releaseTarget, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("argument must be <release>/<target>, got %q", releaseTarget)
	}
	releaseName, targetName := parts[0], parts[1]

	c, err := buildClient(kubeconfigPath)
	if err != nil {
		return err
	}

	targets, err := listReleaseTargetsForRelease(ctx, c, "", releaseName)
	if err != nil {
		return err
	}
	selected := selectApprovalTarget(targets, targetName)
	if selected == nil {
		return fmt.Errorf("target %q not found in release %q", targetName, releaseName)
	}

	sp := cli.NewSpinner("Rejecting target")
	sp.Start()

	patch := client.MergeFrom(selected.DeepCopy())
	selected.Status.Rejected = true
	selected.Status.RejectedBy = "cli"
	selected.Status.Message = "rejected: " + reason
	if err := c.Status().Patch(ctx, selected, patch); err != nil {
		sp.StopFail("Failed to reject target")
		return fmt.Errorf("patch rejection: %w", err)
	}
	sp.StopSuccess("Target rejected")

	cli.KV("Release", releaseName)
	cli.KV("Target", targetName)
	cli.KV("Reason", reason)
	return nil
}
