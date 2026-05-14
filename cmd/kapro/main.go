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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/internal/bootstrap"
	"kapro.io/kapro/internal/cli"
	kaproconfig "kapro.io/kapro/internal/config"
	internalgate "kapro.io/kapro/internal/gate"
	"kapro.io/kapro/internal/provider"
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

	root.AddCommand(newHubCmd())
	root.AddCommand(newSpokeCmd())
	root.AddCommand(newFleetMgmtCmd())
	root.AddCommand(newBundleCmd())
	root.AddCommand(newStatusCmd())
	root.AddCommand(newReleaseCmd())
	root.AddCommand(newApproveCmd())
	root.AddCommand(newRejectCmd())
	root.AddCommand(newRollbackCmd())
	root.AddCommand(newGetCmd())
	root.AddCommand(newDemoCmd())

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func newSpokeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "spoke",
		Short: "Manage spoke clusters",
	}
	cmd.AddCommand(newSpokeAddCmd())
	return cmd
}

func newSpokeAddCmd() *cobra.Command {
	var (
		providerName string
		labelsRaw    []string
		kubeconfig   string
		project      string
		location     string
	)
	cmd := &cobra.Command{
		Use:   "add <cluster-name>",
		Short: "Add a spoke cluster to the fleet",
		Long: `Adds a spoke cluster: installs Flux, registers in Fleet, configures IAM.

Provider modes:
  kubeconfig:  Static kubeconfig (any cloud, kind, on-prem)
  gcp-fleet:   GKE Fleet API (auto-discovery, recommended)
  gcp:         GKE direct (Workload Identity)

Examples:
  kapro spoke add de-prod-01 --labels tier=prod
  kapro spoke add de-prod-01 --provider gcp-fleet --project my-project
  kapro spoke add de-prod-01 --kubeconfig /path/to/config`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runClusterAdd(cmd.Context(), args[0], providerName, labelsRaw, kubeconfig, project, location)
		},
	}
	cmd.Flags().StringVar(&providerName, "provider", "", "Provider mode: kubeconfig, gcp, gcp-fleet (auto-detected if empty)")
	cmd.Flags().StringSliceVar(&labelsRaw, "labels", nil, "Labels (key=value)")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig (provider=kubeconfig)")
	cmd.Flags().StringVar(&project, "project", "", "GCP project (provider=gcp or gcp-fleet)")
	cmd.Flags().StringVar(&location, "location", "", "GKE region (provider=gcp)")
	return cmd
}

func runClusterAdd(ctx context.Context, clusterName, providerName string, labelsRaw []string, kubeconfigPath, project, location string) error {
	// Fall back to config for project.
	if project == "" {
		cfg, _ := kaproconfig.Load()
		project = cfg.Hub.Project
	}

	// Auto-detect provider if not specified.
	if providerName == "" {
		if kubeconfigPath != "" {
			providerName = "kubeconfig"
		} else if project != "" {
			providerName = "gcp"
		} else {
			providerName = provider.Detect()
		}
	}

	p, err := provider.New(providerName, provider.Options{
		KubeconfigPath: kubeconfigPath,
		Project:        project,
		Location:       location,
		ClusterName:    clusterName,
	})
	if err != nil {
		return err
	}

	sp := cli.NewSpinner(fmt.Sprintf("Registering cluster %s (provider: %s)", clusterName, p.Name()))
	sp.Start()

	// Generate kubeConfig for this cluster.
	kubeconfigData, err := p.GenerateKubeConfig(ctx, clusterName)
	if err != nil {
		sp.StopFail("Failed to generate kubeconfig")
		return err
	}

	// Build hub client.
	c, err := buildClient("")
	if err != nil {
		sp.StopFail("Failed to connect to hub")
		return err
	}

	// Parse labels.
	labels := map[string]string{}
	for _, kv := range labelsRaw {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) == 2 {
			labels[parts[0]] = parts[1]
		}
	}

	// Create kubeConfig Secret.
	secretName := clusterName + "-kubeconfig"
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: "flux-system",
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"value": kubeconfigData,
		},
	}
	if err := c.Create(ctx, secret); err != nil {
		if !strings.Contains(err.Error(), "already exists") {
			sp.StopFail("Failed to create kubeconfig Secret")
			return err
		}
		// Update existing.
		patch := client.MergeFrom(secret.DeepCopy())
		secret.Data = map[string][]byte{"value": kubeconfigData}
		_ = c.Patch(ctx, secret, patch)
	}

	// Create MemberCluster with spoke-local actuator.
	mc := &kaprov1alpha1.MemberCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:   clusterName,
			Labels: labels,
		},
		Spec: kaprov1alpha1.MemberClusterSpec{
			Actuator: kaprov1alpha1.ActuatorSpec{
				Mode: "pull", Backend: "flux",
				Flux: &kaprov1alpha1.FluxActuator{
					Namespace:     "flux-system",
					OCIRepository: clusterName + "-bundle",
				},
			},
		},
	}
	if err := c.Create(ctx, mc); err != nil {
		if !strings.Contains(err.Error(), "already exists") {
			sp.StopFail("Failed to create MemberCluster")
			return err
		}
	}

	sp.StopSuccess("Cluster registered on hub")

	// Bootstrap spoke: install flux-operator + FluxInstance.
	sp2 := cli.NewSpinner("Bootstrapping spoke Flux controllers")
	sp2.Start()

	spokeClient, err := buildSpokeClient(kubeconfigData)
	if err != nil {
		sp2.StopFail("Failed to connect to spoke")
		return err
	}

	if err := bootstrap.EnsureNamespace(ctx, spokeClient, "flux-system"); err != nil {
		sp2.StopFail("Failed to create flux-system namespace")
		return err
	}
	if err := bootstrap.InstallFluxOperator(ctx, spokeClient); err != nil {
		sp2.StopFail("Failed to install flux-operator")
		return err
	}
	if err := bootstrap.InstallFluxInstance(ctx, spokeClient); err != nil {
		sp2.StopFail("Failed to create FluxInstance")
		return err
	}

	sp2.StopSuccess("Spoke bootstrapped")

	// GCP-specific setup (if applicable).
	if providerName == "gcp" || providerName == "gcp-fleet" || providerName == "gcp-basic" {
		// Auto-detect location if not provided (needed for Fleet + IAM).
		if location == "" && project != "" {
			if detected, err := detectClusterLocation(ctx, project, clusterName); err == nil {
				location = detected
			}
		}

		// Register in Fleet.
		sp3 := cli.NewSpinner("Registering in GKE Fleet")
		sp3.Start()
		if err := bootstrap.RegisterFleetMembership(ctx, project, clusterName, location); err != nil {
			sp3.StopFail(fmt.Sprintf("Fleet registration: %v", err))
		} else {
			sp3.StopSuccess("Fleet membership registered")
		}

		// IAM + Workload Identity.
		sp4 := cli.NewSpinner("Configuring GCP (IAM, Workload Identity)")
		sp4.Start()
		gcpOpts := bootstrap.GCPSetupOptions{
			HubProject:    project,
			SpokeProject:  project,
			SpokeCluster:  clusterName,
			SpokeLocation: location,
		}
		if err := bootstrap.SetupGCPSpoke(ctx, gcpOpts); err != nil {
			sp4.StopFail(fmt.Sprintf("GCP setup warning: %v", err))
		} else {
			sp4.StopSuccess("GCP configured")
		}
	}

	cli.KV("Cluster", clusterName)
	cli.KV("Provider", p.Name())
	cli.KV("Secret", secretName)
	cli.KV("Labels", fmt.Sprintf("%v", labels))
	cli.KV("Flux", "flux-operator + FluxInstance installed on spoke")
	cli.KV("Actuator", "spoke (spoke-local Flux)")
	return nil
}

func buildSpokeClient(kubeconfigData []byte) (client.Client, error) {
	restConfig, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigData)
	if err != nil {
		return nil, fmt.Errorf("parse kubeconfig: %w", err)
	}
	return client.New(restConfig, client.Options{})
}

func runClusterSync(ctx context.Context, project string) error {
	if project == "" {
		cfg, _ := kaproconfig.Load()
		project = cfg.Hub.Project
	}

	p, err := provider.New("gcp-fleet", provider.Options{Project: project})
	if err != nil {
		return err
	}

	sp := cli.NewSpinner("Discovering Fleet clusters")
	sp.Start()

	clusters, err := p.ListClusters(ctx)
	if err != nil {
		sp.StopFail("Failed to list Fleet clusters")
		return err
	}
	sp.StopSuccess(fmt.Sprintf("Found %d clusters", len(clusters)))

	c, err := buildClient("")
	if err != nil {
		return err
	}

	registered := 0
	for _, cluster := range clusters {
		sp := cli.NewSpinner(fmt.Sprintf("Registering %s", cluster.Name))
		sp.Start()

		// Generate kubeConfig via Connect Gateway.
		kubeconfigData, err := p.GenerateKubeConfig(ctx, cluster.Name)
		if err != nil {
			sp.StopFail("Failed: " + err.Error())
			continue
		}

		// Create/update kubeConfig Secret.
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cluster.Name + "-kubeconfig",
				Namespace: "flux-system",
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{"value": kubeconfigData},
		}
		if err := c.Create(ctx, secret); err != nil {
			if strings.Contains(err.Error(), "already exists") {
				patch := client.MergeFrom(secret.DeepCopy())
				secret.Data = map[string][]byte{"value": kubeconfigData}
				_ = c.Patch(ctx, secret, patch)
			}
		}

		// Create/update MemberCluster with Fleet labels.
		mc := &kaprov1alpha1.MemberCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:   cluster.Name,
				Labels: cluster.Labels,
			},
			Spec: kaprov1alpha1.MemberClusterSpec{
				Actuator: kaprov1alpha1.ActuatorSpec{
					Mode: "push", Backend: "flux",
					FluxOperator: &kaprov1alpha1.FluxOperatorConfig{
						ResourceSet: "fleet-workloads",
						Namespace:   "flux-system",
					},
				},
			},
		}
		if err := c.Create(ctx, mc); err != nil {
			if strings.Contains(err.Error(), "already exists") {
				existing := &kaprov1alpha1.MemberCluster{}
				if getErr := c.Get(ctx, client.ObjectKey{Name: cluster.Name}, existing); getErr == nil {
					patch := client.MergeFrom(existing.DeepCopy())
					existing.Labels = cluster.Labels
					_ = c.Patch(ctx, existing, patch)
				}
			}
		}

		sp.StopSuccess(cluster.Name)
		registered++
	}

	fmt.Fprintln(cli.Out)
	cli.Successf("%d clusters registered from GKE Fleet", registered)
	cli.Muted("Run: kapro fleet")
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

	tbl := cli.NewTable("NAME", "VERSION", "PHASE", "PIPELINES", "AGE")
	for _, r := range list.Items {
		pipelines := ""
		for i, p := range r.Spec.Pipelines {
			if i > 0 {
				pipelines += ", "
			}
			pipelines += p.Pipeline
		}
		tbl.AddRow(r.Name, r.Spec.Version, string(r.Status.Phase), pipelines, cli.Age(r.CreationTimestamp.Time))
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

	// Derive short suffix from digest for readable names.
	suffix := shortHash(toDigest)

	// Create a rollback Release with the same pipelines but the rollback version.
	rbReleaseName := releaseName + "-rb-" + suffix
	rbSpec := kaprov1alpha1.ReleaseSpec{
		Version:   toDigest,
		Pipelines: orig.Spec.Pipelines,
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
		return fmt.Errorf("create rollback release: %w", err)
	}

	fmt.Printf("✅ Rollback release created: %s/%s\n", namespace, rbReleaseName)
	fmt.Printf("   Original release: %s\n", releaseName)
	fmt.Printf("   Rollback version: %s\n", toDigest)
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
		name       string
		version    string
		pipelines  []string
		scope      []string
		namespace  string
		kubeconfig string
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a Release to deliver a version across the fleet",
		Long: `Create a Kapro Release CR that drives progressive delivery.

Examples:
  # Full-fleet release
  kapro release create --name v1.2.3 --version sha256:abc123 --pipeline global

  # Hotfix targeting specific clusters only
  kapro release create --name v1.2.3-hotfix --version sha256:def456 \
    --pipeline global --scope de-prod --scope fi-prod`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runReleaseCreate(cmd.Context(), name, version, pipelines, scope, namespace, kubeconfig)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Release name (required)")
	cmd.Flags().StringVar(&version, "version", "", "OCI digest or tag to deliver (required)")
	cmd.Flags().StringArrayVar(&pipelines, "pipeline", nil, "Pipeline name (repeatable; required at least once)")
	cmd.Flags().StringArrayVar(&scope, "scope", nil, "Restrict to target cluster (repeatable: --scope de-prod --scope fi-prod)")
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "Namespace for the Release")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("version")
	return cmd
}

func runReleaseCreate(ctx context.Context, name, version string, pipelines, scope []string,
	namespace, kubeconfigPath string) error {
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
		Version:   version,
		Pipelines: refs,
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
	fmt.Printf("   Version:   %s\n", version)
	pipelineNames := make([]string, len(pipelines))
	copy(pipelineNames, pipelines)
	fmt.Printf("   Pipelines: %s\n", strings.Join(pipelineNames, ", "))
	if len(scope) > 0 {
		fmt.Printf("   Scope:     %s\n", strings.Join(scope, ", "))
	}
	fmt.Printf("\nMonitor progress:\n  kapro get releases -n %s\n", namespace)
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
