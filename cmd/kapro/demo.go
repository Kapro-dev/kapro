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

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/internal/cli"
)

const demoClusterName = "kapro-demo"

func newDemoCmd() *cobra.Command {
	var cleanup bool
	cmd := &cobra.Command{
		Use:   "demo",
		Short: "Run a local Kapro demo on a kind cluster",
		Long: `Creates a kind cluster, installs CRDs, and sets up a demo release
with 3 simulated clusters (canary, prod-eu-west, prod-eu-east) and a
progressive delivery pipeline.

After the demo starts, try:
  kapro get releases
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

	// Step 4: Create demo resources via client.
	sp = cli.NewSpinner("Creating demo fleet")
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

	now := time.Now().UTC().Format(time.RFC3339)

	// MemberClusters.
	clusters := []*kaprov1alpha1.MemberCluster{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "canary-eu",
				Labels: map[string]string{"tier": "canary", "region": "eu-west", "country": "de"},
			},
			Spec: kaprov1alpha1.MemberClusterSpec{
				Actuator: kaprov1alpha1.ActuatorSpec{
					Type: "flux-operator",
					FluxOperator: &kaprov1alpha1.FluxOperatorConfig{
						ResourceSet: "demo-apps",
						Namespace:   "flux-system",
						InputField:  "tag",
						TenantField: "tenant",
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "prod-eu-west",
				Labels: map[string]string{"tier": "prod", "region": "eu-west", "country": "de"},
			},
			Spec: kaprov1alpha1.MemberClusterSpec{
				Actuator: kaprov1alpha1.ActuatorSpec{
					Type: "flux-operator",
					FluxOperator: &kaprov1alpha1.FluxOperatorConfig{
						ResourceSet: "demo-apps",
						Namespace:   "flux-system",
						InputField:  "tag",
						TenantField: "tenant",
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "prod-eu-east",
				Labels: map[string]string{"tier": "prod", "region": "eu-east", "country": "fi"},
			},
			Spec: kaprov1alpha1.MemberClusterSpec{
				Actuator: kaprov1alpha1.ActuatorSpec{
					Type: "flux-operator",
					FluxOperator: &kaprov1alpha1.FluxOperatorConfig{
						ResourceSet: "demo-apps",
						Namespace:   "flux-system",
						InputField:  "tag",
						TenantField: "tenant",
					},
				},
			},
		},
	}

	for _, mc := range clusters {
		if err := c.Create(ctx, mc); err != nil {
			if !isAlreadyExists(err) {
				sp.StopFail("Failed to create MemberCluster " + mc.Name)
				return err
			}
		}
		// Patch status to simulate healthy clusters.
		latest := &kaprov1alpha1.MemberCluster{}
		if err := c.Get(ctx, client.ObjectKey{Name: mc.Name}, latest); err == nil {
			patch := client.MergeFrom(latest.DeepCopy())
			latest.Status.Phase = kaprov1alpha1.ClusterPhaseConverged
			latest.Status.LastHeartbeat = now
			latest.Status.Health = kaprov1alpha1.ClusterHealth{
				AllWorkloadsReady: true,
				ReadyWorkloads:    8,
				TotalWorkloads:    8,
			}
			latest.Status.CurrentVersions = map[string]string{"default": "v1.9.0"}
			_ = c.Status().Patch(ctx, latest, patch)
		}
	}

	// ResourceSet (Flux Operator) — simulated for demo.
	// In production, this is created by the platform team.
	rsYAML := `apiVersion: fluxcd.controlplane.io/v1
kind: ResourceSet
metadata:
  name: demo-apps
  namespace: flux-system
spec:
  inputs:
    - tenant: canary-eu
      tag: v1.9.0
    - tenant: prod-eu-west
      tag: v1.9.0
    - tenant: prod-eu-east
      tag: v1.9.0
  resources: []
`
	// Install ResourceSet CRD + create the ResourceSet via kubectl.
	rsPath := filepath.Join(os.TempDir(), "kapro-demo-resourceset.yaml")
	os.WriteFile(rsPath, []byte(rsYAML), 0644)
	exec.Command("kubectl", "--kubeconfig", kubeconfigPath,
		"apply", "-f", rsPath).CombinedOutput()

	// Pipeline.
	pipeline := &kaprov1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "standard-rollout"},
		Spec: kaprov1alpha1.PipelineSpec{
			Stages: []kaprov1alpha1.Stage{
				{
					Name:     "canary",
					Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "canary"}},
				},
				{
					Name:      "prod",
					Selector:  metav1.LabelSelector{MatchLabels: map[string]string{"tier": "prod"}},
					DependsOn: []kaprov1alpha1.StageDependency{{Stage: "canary"}},
				},
			},
		},
	}
	if err := c.Create(ctx, pipeline); err != nil && !isAlreadyExists(err) {
		sp.StopFail("Failed to create Pipeline")
		return err
	}

	// Artifact.
	artifact := &kaprov1alpha1.Artifact{
		ObjectMeta: metav1.ObjectMeta{Name: "myapp-v2.0.0"},
		Spec: kaprov1alpha1.ArtifactSpec{
			Sources: []kaprov1alpha1.ArtifactSource{{
				Type: "oci",
				OCI: &kaprov1alpha1.OCIRef{
					Repository: "registry.example.com/myapp",
					Tag:        "v2.0.0",
					Digest:     "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
				},
			}},
		},
	}
	if err := c.Create(ctx, artifact); err != nil && !isAlreadyExists(err) {
		sp.StopFail("Failed to create Artifact")
		return err
	}

	// Release.
	release := &kaprov1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{Name: "myapp-v2.0.0"},
		Spec: kaprov1alpha1.ReleaseSpec{
			Artifact: "myapp-v2.0.0",
			Pipelines: []kaprov1alpha1.ReleasePipelineRef{
				{Name: "initial", Pipeline: "standard-rollout"},
			},
		},
	}
	if err := c.Create(ctx, release); err != nil && !isAlreadyExists(err) {
		sp.StopFail("Failed to create Release")
		return err
	}

	sp.StopSuccess("Demo fleet created")

	// Step 5: Print summary.
	fmt.Fprintln(cli.Out)
	cli.Header("Demo Ready")
	fmt.Fprintln(cli.Out)

	tbl := cli.NewTable("RESOURCE", "NAME", "DETAILS")
	tbl.AddRow("MemberCluster", "canary-eu", "tier=canary, region=eu-west")
	tbl.AddRow("MemberCluster", "prod-eu-west", "tier=prod, region=eu-west")
	tbl.AddRow("MemberCluster", "prod-eu-east", "tier=prod, region=eu-east")
	tbl.AddRow("Pipeline", "standard-rollout", "canary -> prod (manual gate)")
	tbl.AddRow("Artifact", "myapp-v2.0.0", "registry.example.com/myapp:v2.0.0")
	tbl.AddRow("Release", "myapp-v2.0.0", "pipeline: standard-rollout")
	tbl.Render()

	cli.Header("Try these commands")
	fmt.Fprintln(cli.Out)
	cli.Info("kapro get releases                          # list releases")
	cli.Info("kapro get targets                           # see rollout status")
	cli.Info("kapro approve myapp-v2.0.0/prod-eu-west     # approve production")
	cli.Info("kapro fleet                                 # fleet overview")
	cli.Info("kapro world                                 # all clusters")
	fmt.Fprintln(cli.Out)
	cli.Muted("Clean up:  kapro demo --cleanup")
	cli.Muted("Kubeconfig: export KUBECONFIG=" + kubeconfigPath)
	fmt.Fprintln(cli.Out)

	return nil
}

func splitLines(s string) []string {
	var lines []string
	for _, line := range filepath.SplitList(s) {
		lines = append(lines, line)
	}
	// filepath.SplitList uses PATH separator, use manual split instead.
	lines = nil
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
