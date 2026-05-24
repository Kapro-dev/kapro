// Command kapro is the CLI for the Kapro promotion engine.
//
// Usage:
//
//	kapro bootstrap guide
//	kapro bootstrap greenfield ./promotion-repo --backend flux --mode pull --name checkout
//	kapro bootstrap brownfield argo . --out ./kapro-connect --name checkout
//	kapro init ./promotion-repo --backend flux --mode pull --name checkout
//	kapro promote <fleet> --version <version>
//	kapro diag <promotion>
//	kapro get promotionruns
//	kapro get targets
//	kapro approve <promotionrun>/<target> [--comment text]
//	kapro rollback <promotionrun-name> --to <digest>
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	"kapro.io/kapro/internal/bootstrap"
	"kapro.io/kapro/internal/cli"
	kaproconfig "kapro.io/kapro/internal/config"
	internalgate "kapro.io/kapro/internal/gate"
	"kapro.io/kapro/internal/provider"
)

var scheme = runtime.NewScheme()

func init() {
	_ = clientgoscheme.AddToScheme(scheme)
	_ = apiextensionsv1.AddToScheme(scheme)
	_ = kaprov1alpha2.AddToScheme(scheme)
}

func main() {
	root := &cobra.Command{
		Use:   "kapro",
		Short: "The Canonical Promotion Layer for Kubernetes",
		Long: `kapro — multi-cluster progressive delivery engine.

Pass versions forward across targets, clusters, and waves.

Start here:
  kapro bootstrap guide
  kapro bootstrap greenfield ./promotion-repo --backend flux --mode pull --name checkout
  kapro promote checkout --version v1.2.3
  kapro diag checkout-v1-2-3

For existing GitOps repositories:
  kapro bootstrap brownfield argo . --out ./kapro-connect --name checkout
  kapro bootstrap brownfield flux . --out ./kapro-connect --name checkout`,
	}

	root.PersistentFlags().StringVarP(&cli.OutputFormat, "output", "o", "", "Output format (json for machine-readable)")

	root.AddCommand(newInitCmd())
	root.AddCommand(newBootstrapCmd())
	root.AddCommand(newConnectCmd())
	root.AddCommand(newDiscoverCmd())
	root.AddCommand(newAdoptCmd())
	root.AddCommand(newHubCmd())
	root.AddCommand(newSpokeCmd())
	root.AddCommand(newFleetMgmtCmd())
	root.AddCommand(newSourceCmd())
	root.AddCommand(newStatusCmd())
	root.AddCommand(newPromoteCmd())
	root.AddCommand(newPromotionRunCmd())
	root.AddCommand(newTopCmd())
	root.AddCommand(newTreeCmd())
	root.AddCommand(newEventsCmd())
	root.AddCommand(newWhyCmd())
	root.AddCommand(newReconstructCmd())
	root.AddCommand(newVerifyCmd())
	root.AddCommand(newDoctorCmd())
	root.AddCommand(newMigrateCmd())
	root.AddCommand(newApproveCmd())
	root.AddCommand(newRejectCmd())
	root.AddCommand(newRollbackCmd())
	root.AddCommand(newGetCmd())
	root.AddCommand(newLintCmd())
	root.AddCommand(newDiagCmd())
	root.AddCommand(newSuspendCmd())
	root.AddCommand(newResumeCmd())
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
	cmd.AddCommand(newSpokeBootstrapCmd())
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

	// Create Cluster with spoke-local actuator.
	mc := &kaprov1alpha2.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:   clusterName,
			Labels: labels,
		},
		Spec: kaprov1alpha2.ClusterSpec{
			Delivery: kaprov1alpha2.DeliverySpec{
				Mode: "pull", BackendRef: "flux",
				Parameters: map[string]string{
					"namespace":     "flux-system",
					"ociRepository": clusterName + "-bundle",
				},
			},
		},
	}
	if err := c.Create(ctx, mc); err != nil {
		if !strings.Contains(err.Error(), "already exists") {
			sp.StopFail("Failed to create Cluster")
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

		// Create/update Cluster with Fleet labels.
		mc := &kaprov1alpha2.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:   cluster.Name,
				Labels: cluster.Labels,
			},
			Spec: kaprov1alpha2.ClusterSpec{
				Delivery: kaprov1alpha2.DeliverySpec{
					Mode: "push", BackendRef: "flux",
					Parameters: map[string]string{
						"resourceSet": "fleet-workloads",
						"namespace":   "flux-system",
					},
				},
			},
		}
		if err := c.Create(ctx, mc); err != nil {
			if strings.Contains(err.Error(), "already exists") {
				existing := &kaprov1alpha2.Cluster{}
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
		Use:   "get <promotion|promotionruns|targets>",
		Short: "Display Kapro resources",
	}
	cmd.AddCommand(newGetPromotionCmd())
	cmd.AddCommand(newGetPromotionRunsCmd())
	cmd.AddCommand(newGetTargetsCmd())
	return cmd
}

func newGetPromotionRunsCmd() *cobra.Command {
	var (
		kubeconfig string
	)
	cmd := &cobra.Command{
		Use:   "promotionruns",
		Short: "List PromotionRun objects",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runGetPromotionRuns(cmd.Context(), kubeconfig)
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	return cmd
}

func runGetPromotionRuns(ctx context.Context, kubeconfigPath string) error {
	sp := cli.NewSpinner("Fetching promotionruns")
	sp.Start()

	c, err := buildClient(kubeconfigPath)
	if err != nil {
		sp.StopFail("Failed to connect to cluster")
		return err
	}

	var list kaprov1alpha2.PromotionRunList
	if err := c.List(ctx, &list); err != nil {
		sp.StopFail("Failed to list promotionruns")
		return fmt.Errorf("list promotionruns: %w", err)
	}
	sp.Stop()

	if cli.IsJSON() {
		return cli.JSON(list.Items)
	}

	if len(list.Items) == 0 {
		cli.Muted("No promotionruns found.")
		return nil
	}

	// Count summary.
	progressing, complete, failed := 0, 0, 0
	for _, r := range list.Items {
		switch r.Status.Phase {
		case kaprov1alpha2.PromotionRunPhaseProgressing:
			progressing++
		case kaprov1alpha2.PromotionRunPhaseComplete:
			complete++
		case kaprov1alpha2.PromotionRunPhaseFailed:
			failed++
		}
	}
	cli.Header("PromotionRuns")
	cli.Infof("%d total  %s  %s  %s",
		len(list.Items),
		cli.Theme.PhaseProgressing.Render(fmt.Sprintf("%d progressing", progressing)),
		cli.Theme.PhaseComplete.Render(fmt.Sprintf("%d complete", complete)),
		cli.Theme.PhaseFailed.Render(fmt.Sprintf("%d failed", failed)),
	)

	tbl := cli.NewTable("NAME", "VERSION", "PHASE", "TARGETS", "SYNCED", "FAILED", "PENDING", "PIPELINES", "AGE")
	for _, r := range list.Items {
		plansText := ""
		for i, p := range r.Spec.Plans {
			if i > 0 {
				plansText += ", "
			}
			plansText += p.Plan
		}
		summary := kaprov1alpha2.PromotionRunSummary{}
		if r.Status.Summary != nil {
			summary = *r.Status.Summary
		}
		tbl.AddRow(
			r.Name,
			r.Spec.Version,
			string(r.Status.Phase),
			fmt.Sprintf("%d", summary.TotalTargets),
			fmt.Sprintf("%d", summary.SyncedTargets),
			fmt.Sprintf("%d", summary.FailedTargets),
			fmt.Sprintf("%d", summary.PendingTargets),
			plansText,
			cli.Age(r.CreationTimestamp.Time),
		)
	}
	tbl.Render()
	return nil
}

func newGetTargetsCmd() *cobra.Command {
	var (
		phase      string
		kubeconfig string
	)
	cmd := &cobra.Command{
		Use:   "targets",
		Short: "List target cluster rollout status across all PromotionRuns",
		Long: `List target cluster rollout entries from Target objects.

Target is the authoritative per-target execution state store.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runGetTargets(cmd.Context(), phase, kubeconfig)
		},
	}
	cmd.Flags().StringVar(&phase, "phase", "", "Filter by phase (e.g. WaitingApproval, Applying, Converged)")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	return cmd
}

func runGetTargets(ctx context.Context, phase, kubeconfigPath string) error {
	sp := cli.NewSpinner("Fetching targets")
	sp.Start()

	c, err := buildClient(kubeconfigPath)
	if err != nil {
		sp.StopFail("Failed to connect to cluster")
		return err
	}

	var targetList kaprov1alpha2.TargetList
	if err := c.List(ctx, &targetList); err != nil {
		sp.StopFail("Failed to list targets")
		return fmt.Errorf("list targets: %w", err)
	}
	sp.Stop()

	// Filter by phase if specified.
	var filtered []kaprov1alpha2.Target
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

	cli.Header("PromotionRun Targets")

	tbl := cli.NewTable("RELEASE", "TARGET", "STAGE", "PHASE", "VERSION", "AGE")
	for _, t := range filtered {
		version := t.Spec.Version
		if len(version) > 20 {
			version = version[:17] + "..."
		}
		tbl.AddRow(
			t.Spec.PromotionRunRef, t.Spec.Target, t.Spec.Stage,
			string(t.Status.Phase), version, cli.Age(t.CreationTimestamp.Time),
		)
	}
	tbl.Render()
	return nil
}

// ─── kapro approve ────────────────────────────────────────────────────────────

func newApproveCmd() *cobra.Command {
	var (
		comment    string
		kubeconfig string
	)
	cmd := &cobra.Command{
		Use:   "approve <promotionrun>/<target>",
		Short: "Approve a target cluster waiting for human gate",
		Long: `Create an Approval for a specific target cluster in WaitingApproval phase.

The argument format is <promotionrun-name>/<target-cluster>.

Examples:
  kapro approve v1.2.3/de-prod
  kapro approve v1.2.3/de-prod --comment "checked canary metrics, all green"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runApprove(cmd.Context(), args[0], comment, kubeconfig)
		},
	}
	cmd.Flags().StringVar(&comment, "comment", "", "Optional approval comment (required for bypass)")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	return cmd
}

func runApprove(ctx context.Context, promotionTarget, comment, kubeconfigPath string) error {
	parts := strings.SplitN(promotionTarget, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("argument must be <promotionrun>/<target>, got %q", promotionTarget)
	}
	promotionrunName, targetName := parts[0], parts[1]

	c, err := buildClient(kubeconfigPath)
	if err != nil {
		return err
	}

	var rel kaprov1alpha2.PromotionRun
	if err := c.Get(ctx, client.ObjectKey{Name: promotionrunName}, &rel); err != nil {
		return fmt.Errorf("get promotionrun %q: %w", promotionrunName, err)
	}

	targets, err := listTargetsForPromotionRun(ctx, c, promotionrunName)
	if err != nil {
		return err
	}
	selected := selectApprovalTarget(targets, targetName)
	if selected == nil {
		return fmt.Errorf("target %q not found in promotionrun %q", targetName, promotionrunName)
	}
	if selected.Status.Phase != kaprov1alpha2.TargetPhaseWaitingApproval {
		fmt.Printf("⚠️  Target %q is in phase %q (not WaitingApproval) — approving anyway.\n",
			targetName, selected.Status.Phase)
	}

	ref := approvalRefForTarget(*selected)
	approvalName := internalgate.ApprovalName(promotionrunName, ref)
	approval := &kaprov1alpha2.Approval{
		ObjectMeta: metav1.ObjectMeta{
			Name: approvalName,
		},
		Spec: kaprov1alpha2.ApprovalSpec{
			Ref:          ref,
			PromotionRun: promotionrunName,
			Target:       targetName,
			Comment:      comment,
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
	cli.KV("PromotionRun", promotionrunName)
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
		targets    []string
		kubeconfig string
	)
	cmd := &cobra.Command{
		Use:   "rollback <promotionrun-name>",
		Short: "Roll back a promotionrun to a previous OCI digest",
		Long: `Create a new PromotionRun pointing at a previous OCI digest.

The original PromotionRun is never modified (immutable). A new Artifact CR is
created with the provided digest and a new PromotionRun is created referencing it.

Use --target to scope rollback to specific clusters (hotfix / partial rollback).

Examples:
  kapro rollback my-promotionrun --to sha256:abc123def456
  kapro rollback my-promotionrun --to sha256:abc123def456 --target de-prod --target fi-prod`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRollback(cmd.Context(), args[0], toDigest, targets, kubeconfig)
		},
	}
	cmd.Flags().StringVar(&toDigest, "to", "", "OCI digest to roll back to (required)")
	cmd.Flags().StringArrayVar(&targets, "target", nil, "Restrict rollback to specific target clusters (repeatable)")
	cmd.Flags().StringArrayVar(&targets, "env", nil, "Deprecated alias for --target")
	_ = cmd.Flags().MarkHidden("env")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	_ = cmd.MarkFlagRequired("to")
	return cmd
}

func runRollback(ctx context.Context, promotionrunName, toDigest string, targets []string, kubeconfigPath string) error {
	c, err := buildClient(kubeconfigPath)
	if err != nil {
		return err
	}

	// Fetch the original PromotionRun.
	var orig kaprov1alpha2.PromotionRun
	if err := c.Get(ctx, client.ObjectKey{Name: promotionrunName}, &orig); err != nil {
		return fmt.Errorf("get promotionrun %q: %w", promotionrunName, err)
	}

	// Derive short suffix from digest for readable names.
	suffix := shortHash(toDigest)

	// Create a rollback PromotionRun with the same plans but the rollback version.
	rbPromotionRunName := promotionrunName + "-rb-" + suffix
	rbSpec := kaprov1alpha2.PromotionRunSpec{
		Version: toDigest,
		Plans:   orig.Spec.Plans,
	}
	if len(targets) > 0 {
		rbSpec.Scope = &kaprov1alpha2.PromotionRunScope{Targets: targets}
	}
	rbPromotionRun := &kaprov1alpha2.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{
			Name: rbPromotionRunName,
			Annotations: map[string]string{
				"kapro.io/rollback-from":   promotionrunName,
				"kapro.io/rollback-digest": toDigest,
			},
		},
		Spec: rbSpec,
	}
	if err := c.Create(ctx, rbPromotionRun); err != nil {
		return fmt.Errorf("create rollback promotionrun: %w", err)
	}

	fmt.Printf("✅ Rollback promotionrun created: %s\n", rbPromotionRunName)
	fmt.Printf("   Original promotionrun: %s\n", promotionrunName)
	fmt.Printf("   Rollback version: %s\n", toDigest)
	if len(targets) > 0 {
		fmt.Printf("   Scoped to targets: %s\n", strings.Join(targets, ", "))
	}
	fmt.Printf("\nMonitor progress:\n  kapro get targets\n")
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

func shortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])[:8]
}

func approvalRefForTarget(target kaprov1alpha2.Target) string {
	return target.Name
}

func selectApprovalTarget(targets []kaprov1alpha2.Target, targetName string) *kaprov1alpha2.Target {
	for i := range targets {
		target := &targets[i]
		if target.Spec.Target == targetName && target.Status.Phase == kaprov1alpha2.TargetPhaseWaitingApproval {
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

func listTargetsForPromotionRun(ctx context.Context, c client.Client, promotionrunName string) ([]kaprov1alpha2.Target, error) {
	var targetList kaprov1alpha2.TargetList
	if err := c.List(ctx, &targetList); err != nil {
		return nil, fmt.Errorf("list targets: %w", err)
	}
	targets := make([]kaprov1alpha2.Target, 0)
	for _, target := range targetList.Items {
		if target.Spec.PromotionRunRef == promotionrunName {
			targets = append(targets, target)
		}
	}
	return targets, nil
}

// ─── kapro promote ────────────────────────────────────────────────────────────────

func newPromoteCmd() *cobra.Command {
	var (
		name       string
		version    string
		versions   []string
		plans      []string
		scope      []string
		kubeconfig string
	)
	cmd := &cobra.Command{
		Use:   "promote <fleet>",
		Short: "Promote a version through a Fleet",
		Long: `Create a Promotion intent to roll a version through the named Fleet.

The controller materializes each Promotion into one or more PromotionRun
attempts. Start with this command, then use "kapro diag" when you need the
single-screen explanation of what is happening.

Examples:
  kapro promote checkout --version v1.2.3
  kapro promote checkout --version v1.2.4 --scope canary-eu
  kapro promote checkout --set api=v1.2.3 --set worker=v1.2.2
  kapro promote checkout --version v1.2.3 --plan checkout-progressive`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if version == "" && len(versions) == 0 {
				return fmt.Errorf("--version or at least one --set unit=revision is required")
			}
			promotionName := name
			if promotionName == "" {
				promotionName = defaultPromotionRunName(args[0], version, versions)
			}
			return runPromotionCreate(cmd.Context(), promotionName, args[0], version, versions, plans, scope, kubeconfig)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Promotion name; defaults to <fleet>-<version>")
	cmd.Flags().StringVar(&version, "version", "", "Default revision to deliver")
	cmd.Flags().StringArrayVar(&versions, "set", nil, "Per-unit revision (repeatable: --set api=sha256:abc)")
	cmd.Flags().StringArrayVar(&plans, "plan", nil, "Plan override (repeatable); defaults to the parent Fleet's inline plan")
	cmd.Flags().StringArrayVar(&scope, "scope", nil, "Restrict to target cluster (repeatable: --scope canary-eu --scope prod-eu)")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	return cmd
}

// runPromotionCreate creates or updates a Promotion intent. The
// PromotionController materializes each effective spec change into a
// PromotionRun attempt.
func runPromotionCreate(ctx context.Context, name, fleetRef, version string,
	versionPairs, plans, scope []string, kubeconfigPath string) error {

	versions, err := parsePromotionRunVersions(versionPairs)
	if err != nil {
		return err
	}
	c, err := buildClient(kubeconfigPath)
	if err != nil {
		return err
	}

	var planRefs []kaprov1alpha2.PlanRef
	usedPlanNames := map[string]struct{}{}
	for _, p := range plans {
		refName := uniquePlanRefName(p, usedPlanNames)
		planRefs = append(planRefs, kaprov1alpha2.PlanRef{
			Name: refName,
			Plan: p,
		})
	}

	spec := kaprov1alpha2.PromotionSpec{
		FleetRef: fleetRef,
		Version:  version,
		Versions: versions,
		Plans:    planRefs,
	}
	if len(scope) > 0 {
		spec.Scope = &kaprov1alpha2.PromotionRunScope{Targets: scope}
	}

	op := "created"
	promo := &kaprov1alpha2.Promotion{}
	if err := c.Get(ctx, client.ObjectKey{Name: name}, promo); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get Promotion: %w", err)
		}
		promo = &kaprov1alpha2.Promotion{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec:       spec,
		}
		if err := c.Create(ctx, promo); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				return fmt.Errorf("create Promotion: %w", err)
			}
			promo = &kaprov1alpha2.Promotion{}
			if err := c.Get(ctx, client.ObjectKey{Name: name}, promo); err != nil {
				return fmt.Errorf("get existing Promotion after create race: %w", err)
			}
			if err := updatePromotionSpec(ctx, c, promo, spec); err != nil {
				return err
			}
			op = "updated"
		}
	} else {
		if err := updatePromotionSpec(ctx, c, promo, spec); err != nil {
			return err
		}
		op = "updated"
	}

	fmt.Printf("Promotion intent %s: %s\n", op, name)
	fmt.Printf("   Fleet:     %s\n", fleetRef)
	if version != "" {
		fmt.Printf("   Version:   %s\n", version)
	}
	if len(versions) > 0 {
		fmt.Printf("   Versions:  %s\n", formatPromotionRunVersions(versions))
	}
	if len(plans) > 0 {
		fmt.Printf("   Plans:     %s\n", strings.Join(plans, ", "))
	}
	if len(scope) > 0 {
		fmt.Printf("   Scope:     %s\n", strings.Join(scope, ", "))
	}
	fmt.Printf("\nA PromotionRun appears after the controller reconciles this intent.\n")
	fmt.Printf("\nNext steps:\n  kapro diag %s\n  kapro status %s\n  kubectl get promotions,promotionruns,targets\n", name, fleetRef)
	return nil
}

func updatePromotionSpec(ctx context.Context, c client.Client, promo *kaprov1alpha2.Promotion, spec kaprov1alpha2.PromotionSpec) error {
	patch := client.MergeFrom(promo.DeepCopy())
	promo.Spec = spec
	if err := c.Patch(ctx, promo, patch); err != nil {
		return fmt.Errorf("update Promotion: %w", err)
	}
	return nil
}

func defaultPromotionRunName(app, version string, versions []string) string {
	suffix := version
	if suffix == "" && len(versions) > 0 {
		suffix = versions[0]
	}
	if suffix == "" {
		suffix = "promotion"
	}
	return dnsLabel(app + "-" + suffix)
}

func dnsLabel(value string) string {
	name := dnsLabelValue(value)
	if name == "" {
		return "promotion"
	}
	return name
}

func dnsLabelValue(value string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(value) {
		isNameChar := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if isNameChar {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	name := strings.Trim(b.String(), "-")
	if len(name) > 63 {
		hash := shortHash(value)
		prefix := strings.Trim(name[:54], "-")
		if prefix == "" {
			return hash
		}
		name = prefix + "-" + hash
	}
	return name
}

func uniquePlanRefName(plan string, used map[string]struct{}) string {
	base := dnsLabelValue(plan)
	if base == "" {
		base = "plan"
	}
	candidate := base
	for suffix := 2; ; suffix++ {
		if _, ok := used[candidate]; !ok {
			used[candidate] = struct{}{}
			return candidate
		}
		candidate = dnsLabelWithSuffix(base, fmt.Sprintf("%d", suffix))
	}
}

func dnsLabelWithSuffix(base, suffix string) string {
	if suffix == "" {
		return dnsLabel(base)
	}
	suffix = dnsLabel(suffix)
	if suffix == "" {
		return dnsLabel(base)
	}
	base = dnsLabel(base)
	maxBaseLen := 63 - len(suffix) - 1
	if maxBaseLen < 1 {
		return dnsLabel(suffix)
	}
	if len(base) > maxBaseLen {
		base = strings.Trim(base[:maxBaseLen], "-")
	}
	if base == "" {
		return dnsLabel(suffix)
	}
	return base + "-" + suffix
}

// ─── kapro promotionrun ────────────────────────────────────────────────────────────

func newPromotionRunCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "promotionrun",
		Short: "Manage PromotionRuns",
	}
	cmd.AddCommand(newPromotionRunCreateCmd())
	return cmd
}

func newPromotionRunCreateCmd() *cobra.Command {
	var (
		name       string
		version    string
		versions   []string
		plans      []string
		scope      []string
		kubeconfig string
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a PromotionRun directly (advanced; bypasses Promotion intent)",
		Long: `Create a PromotionRun CR directly.

Most users should use 'kapro promote' to create a Promotion; the controller
will create a PromotionRun under it. This command is for debugging, plugin
authors, and other advanced cases that need to bypass the intent layer.

Examples:
  # Full-fleet promotionrun
  kapro promotionrun create --name v1.2.3 --version sha256:abc123 --plan global

  # Hotfix targeting specific clusters only
  kapro promotionrun create --name v1.2.3-hotfix --version sha256:def456 \
    --plan global --scope de-prod --scope fi-prod

  # Brownfield/native promotionrun with per-unit revisions
  kapro promotionrun create --name checkout-2026-05-15 \
    --set api=main@sha256:abc123 --set worker=main@sha256:def456 \
    --plan global`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPromotionRunCreate(cmd.Context(), name, version, versions, plans, scope, kubeconfig)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "PromotionRun name (required)")
	cmd.Flags().StringVar(&version, "version", "", "Default revision to deliver")
	cmd.Flags().StringArrayVar(&versions, "set", nil, "Per-unit revision (repeatable: --set api=sha256:abc)")
	cmd.Flags().StringArrayVar(&plans, "plan", nil, "Plan name (repeatable; required at least once)")
	cmd.Flags().StringArrayVar(&scope, "scope", nil, "Restrict to target cluster (repeatable: --scope de-prod --scope fi-prod)")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func runPromotionRunCreate(ctx context.Context, name, version string, versionPairs, plans, scope []string,
	kubeconfigPath string) error {
	if len(plans) == 0 {
		return fmt.Errorf("at least one --plan is required")
	}
	versions, err := parsePromotionRunVersions(versionPairs)
	if err != nil {
		return err
	}
	if version == "" && len(versions) == 0 {
		return fmt.Errorf("--version or at least one --set unit=revision is required")
	}

	c, err := buildClient(kubeconfigPath)
	if err != nil {
		return err
	}

	refs := make([]kaprov1alpha2.PlanRef, 0, len(plans))
	for i, p := range plans {
		refs = append(refs, kaprov1alpha2.PlanRef{
			Name: fmt.Sprintf("p%d", i+1),
			Plan: p,
		})
	}

	spec := kaprov1alpha2.PromotionRunSpec{
		Version:  version,
		Versions: versions,
		Plans:    refs,
	}
	if len(scope) > 0 {
		spec.Scope = &kaprov1alpha2.PromotionRunScope{Targets: scope}
	}

	rel := &kaprov1alpha2.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: spec,
	}

	if err := c.Create(ctx, rel); err != nil {
		return fmt.Errorf("create PromotionRun: %w", err)
	}

	fmt.Printf("✅ PromotionRun created: %s\n", name)
	if version != "" {
		fmt.Printf("   Version:   %s\n", version)
	}
	if len(versions) > 0 {
		fmt.Printf("   Versions:  %s\n", formatPromotionRunVersions(versions))
	}
	planNames := make([]string, len(plans))
	copy(planNames, plans)
	fmt.Printf("   Plans:     %s\n", strings.Join(planNames, ", "))
	if len(scope) > 0 {
		fmt.Printf("   Scope:     %s\n", strings.Join(scope, ", "))
	}
	fmt.Printf("\nMonitor progress:\n  kapro get promotionruns\n")
	return nil
}

func parsePromotionRunVersions(pairs []string) (map[string]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	versions := make(map[string]string, len(pairs))
	for _, pair := range pairs {
		unit, version, ok := strings.Cut(pair, "=")
		unit = strings.TrimSpace(unit)
		version = strings.TrimSpace(version)
		if !ok || unit == "" || version == "" {
			return nil, fmt.Errorf("--set must use unit=revision, got %q", pair)
		}
		if _, exists := versions[unit]; exists {
			return nil, fmt.Errorf("duplicate --set for unit %q", unit)
		}
		versions[unit] = version
	}
	return versions, nil
}

func formatPromotionRunVersions(versions map[string]string) string {
	keys := make([]string, 0, len(versions))
	for unit := range versions {
		keys = append(keys, unit)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, unit := range keys {
		parts = append(parts, unit+"="+versions[unit])
	}
	return strings.Join(parts, ", ")
}

// ─── kapro reject ────────────────────────────────────────────────────────────

func newRejectCmd() *cobra.Command {
	var (
		reason     string
		kubeconfig string
	)
	cmd := &cobra.Command{
		Use:   "reject <promotionrun>/<target>",
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

func runReject(ctx context.Context, promotionTarget, reason, kubeconfigPath string) error {
	parts := strings.SplitN(promotionTarget, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("argument must be <promotionrun>/<target>, got %q", promotionTarget)
	}
	promotionrunName, targetName := parts[0], parts[1]

	c, err := buildClient(kubeconfigPath)
	if err != nil {
		return err
	}

	targets, err := listTargetsForPromotionRun(ctx, c, promotionrunName)
	if err != nil {
		return err
	}
	selected := selectApprovalTarget(targets, targetName)
	if selected == nil {
		return fmt.Errorf("target %q not found in promotionrun %q", targetName, promotionrunName)
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

	cli.KV("PromotionRun", promotionrunName)
	cli.KV("Target", targetName)
	cli.KV("Reason", reason)
	return nil
}
