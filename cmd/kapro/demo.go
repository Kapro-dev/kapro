package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	"kapro.io/kapro/internal/cli"
)

const demoClusterName = "kapro-demo"

func newDemoCmd() *cobra.Command {
	var cleanup bool
	cmd := &cobra.Command{
		Use:   "demo",
		Short: "Run a local Kapro demo on a kind cluster",
		Long: `Creates a kind cluster, installs CRDs, and sets up a demo promotionrun
with 3 simulated clusters (canary, prod-eu-west, prod-eu-east) and a
progressive delivery promotionplan.

After the demo starts, try:
  kapro get promotionruns
  kapro get targets
  kapro approve myapp-v2.0.0/prod-eu-west
  kapro fleet

Clean up with: kapro demo --cleanup`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if cleanup {
				return runDemoCleanup()
			}
			return runDemo(cmd.Context())
		},
	}
	cmd.Flags().BoolVar(&cleanup, "cleanup", false, "Delete the demo kind cluster")
	return cmd
}

func runDemoCleanup() error {
	sp := cli.NewSpinner("Deleting demo cluster")
	sp.Start()
	out, err := exec.Command("kind", "delete", "cluster", "--name", demoClusterName).CombinedOutput()
	if err != nil {
		sp.StopFail("Failed to delete cluster")
		return fmt.Errorf("kind delete: %s: %w", string(out), err)
	}
	sp.StopSuccess("Demo cluster deleted")
	return nil
}

func runDemo(ctx context.Context) error {
	// Check prerequisites.
	if _, err := exec.LookPath("kind"); err != nil {
		cli.Error("kind not found. Install it: https://kind.sigs.k8s.io/docs/user/quick-start/#installation")
		return fmt.Errorf("kind not in PATH")
	}
	if _, err := exec.LookPath("kubectl"); err != nil {
		cli.Error("kubectl not found. Install it: https://kubernetes.io/docs/tasks/tools/")
		return fmt.Errorf("kubectl not in PATH")
	}

	cli.Header("Kapro Demo")
	fmt.Fprintln(cli.Out)

	// Step 1: Create kind cluster.
	sp := cli.NewSpinner("Creating kind cluster")
	sp.Start()

	// Check if cluster already exists.
	checkOut, _ := exec.Command("kind", "get", "clusters").CombinedOutput()
	clusterExists := false
	for _, line := range splitLines(string(checkOut)) {
		if line == demoClusterName {
			clusterExists = true
			break
		}
	}

	if clusterExists {
		sp.StopWith(cli.Theme.Warning.Render("  Cluster already exists, reusing"))
	} else {
		out, err := exec.Command("kind", "create", "cluster", "--name", demoClusterName, "--wait", "60s").CombinedOutput()
		if err != nil {
			sp.StopFail("Failed to create cluster")
			return fmt.Errorf("kind create: %s: %w", string(out), err)
		}
		sp.StopSuccess("Kind cluster created")
	}

	// Step 2: Get kubeconfig.
	kubeconfigBytes, err := exec.Command("kind", "get", "kubeconfig", "--name", demoClusterName).Output()
	if err != nil {
		return fmt.Errorf("get kubeconfig: %w", err)
	}
	kubeconfigPath := filepath.Join(os.TempDir(), "kapro-demo-kubeconfig")
	if err := os.WriteFile(kubeconfigPath, kubeconfigBytes, 0600); err != nil {
		return fmt.Errorf("write kubeconfig: %w", err)
	}

	// Step 3: Install CRDs.
	sp = cli.NewSpinner("Installing CRDs")
	sp.Start()

	crdDir := "config/crd/bases"
	if _, err := os.Stat(crdDir); os.IsNotExist(err) {
		// Try relative to binary location.
		exePath, _ := os.Executable()
		crdDir = filepath.Join(filepath.Dir(exePath), "..", "config", "crd", "bases")
	}

	crdFiles, _ := filepath.Glob(filepath.Join(crdDir, "*.yaml"))
	if len(crdFiles) == 0 {
		sp.StopFail("No CRD files found in " + crdDir)
		return fmt.Errorf("CRD files not found — run from the kapro repo root")
	}

	for _, f := range crdFiles {
		out, err := exec.Command("kubectl", "--kubeconfig", kubeconfigPath,
			"apply", "-f", f).CombinedOutput()
		if err != nil {
			sp.StopFail("Failed to install CRDs")
			return fmt.Errorf("kubectl apply %s: %s: %w", f, string(out), err)
		}
	}
	sp.StopSuccess(fmt.Sprintf("Installed %d CRDs", len(crdFiles)))

	// Step 4: Create Kapro CRs.
	sp = cli.NewSpinner("Creating Kapro resources")
	sp.Start()

	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		sp.StopFail("Failed to build client config")
		return err
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		sp.StopFail("Failed to create client")
		return err
	}

	// Kapro — defines what and where to deploy.
	kapro := &kaprov1alpha2.Fleet{
		ObjectMeta: metav1.ObjectMeta{Name: "demo"},
		Spec: kaprov1alpha2.FleetSpec{
			Registry: kaprov1alpha2.KaproRegistry{
				URL: "oci://registry.example.com/charts",
			},
			Source: &kaprov1alpha2.SourceSpec{
				Units: []kaprov1alpha2.Unit{
					{Name: "pos-server", Version: "5.28.0"},
					{Name: "auth-service", Version: "5.28.0"},
					{Name: "sdc", Version: "5.28.0"},
					{Name: "keycloak", Version: "6.5.0"},
				},
			},
			Clusters: []kaprov1alpha2.ClusterRef{
				{Name: "canary-eu", Labels: map[string]string{"tier": "canary", "region": "eu-west"}},
				{Name: "prod-eu-west", Labels: map[string]string{"tier": "prod", "region": "eu-west"}},
				{Name: "prod-eu-east", Labels: map[string]string{"tier": "prod", "region": "eu-east"}},
			},
			Plan: kaprov1alpha2.KaproPromotionPlan{
				Stages: []kaprov1alpha2.StageSpec{
					{Name: "canary", Selector: map[string]string{"tier": "canary"}},
					{Name: "prod", Selector: map[string]string{"tier": "prod"},
						DependsOn: []kaprov1alpha2.StageDependency{{Stage: "canary"}}},
				},
			},
		},
	}
	if err := c.Create(ctx, kapro); err != nil && !isAlreadyExists(err) {
		sp.StopFail("Failed to create Kapro")
		return err
	}

	// Simulate healthy clusters (in production, Flux reports this).
	now := time.Now().UTC().Format(time.RFC3339)
	for _, cluster := range kapro.Spec.Clusters {
		mc := &kaprov1alpha2.Cluster{}
		if err := c.Get(ctx, client.ObjectKey{Name: cluster.Name}, mc); err == nil {
			patch := client.MergeFrom(mc.DeepCopy())
			mc.Status.Phase = kaprov1alpha2.ClusterPhaseConverged
			mc.Status.LastHeartbeat = now
			mc.Status.Health = kaprov1alpha2.ClusterHealth{AllWorkloadsReady: true, ReadyWorkloads: 8, TotalWorkloads: 8}
			_ = c.Status().Patch(ctx, mc, patch)
		}
	}

	// Create a compatibility PromotionRun to trigger the promotionplan.
	promotionrun := &kaprov1alpha2.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{Name: "platform-v5.28"},
		Spec: kaprov1alpha2.PromotionRunSpec{
			Version:        "sha256:abc123",
			PromotionPlans: []kaprov1alpha2.PlanRef{{Name: "initial", Plan: "demo-promotionplan"}},
		},
	}
	if err := c.Create(ctx, promotionrun); err != nil && !isAlreadyExists(err) {
		sp.StopFail("Failed to create PromotionRun")
		return err
	}

	sp.StopSuccess("Demo fleet created")

	// Step 5: Print summary.
	fmt.Fprintln(cli.Out)
	cli.Header("Demo Ready")
	fmt.Fprintln(cli.Out)

	tbl := cli.NewTable("RESOURCE", "NAME", "DETAILS")
	tbl.AddRow("Kapro", "demo", "4 units, 3 clusters, 2 stages")
	tbl.AddRow("  FleetCluster", "canary-eu", "tier=canary (generated on hub)")
	tbl.AddRow("  FleetCluster", "prod-eu-west", "tier=prod (generated on hub)")
	tbl.AddRow("  FleetCluster", "prod-eu-east", "tier=prod (generated on hub)")
	tbl.AddRow("  Plan", "demo-promotionplan", "canary → prod (generated on hub)")
	tbl.AddRow("  ResourceSet", "demo-workloads", "4 HelmReleases × 3 clusters (hub)")
	tbl.AddRow("PromotionRun", "platform-v5.28", "triggers promotionplan")
	tbl.Render()

	cli.Header("Try these commands")
	fmt.Fprintln(cli.Out)
	cli.Info("kapro get promotionruns                          # list promotionruns")
	cli.Info("kapro get targets                           # see rollout status")
	cli.Info("kapro approve platform-v5.28/prod-eu-west   # approve production")
	cli.Info("kapro fleet                                 # fleet overview")
	cli.Info("kapro world                                 # all clusters")
	cli.Info("kubectl get kapro demo -o yaml              # see the Kapro CRD")
	fmt.Fprintln(cli.Out)
	cli.Muted("Clean up:  kapro demo --cleanup")
	cli.Muted("Kubeconfig: export KUBECONFIG=" + kubeconfigPath)
	fmt.Fprintln(cli.Out)

	return nil
}

func splitLines(s string) []string {
	// filepath.SplitList uses PATH separator, use manual split instead.
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			line := s[start:i]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			if line != "" {
				lines = append(lines, line)
			}
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func isAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	return client.IgnoreAlreadyExists(err) == nil
}
