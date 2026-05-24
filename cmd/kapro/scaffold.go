package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"kapro.io/kapro/internal/cli"
)

func newInitCmd() *cobra.Command {
	var opts scaffoldOptions
	cmd := &cobra.Command{
		Use:   "init [directory]",
		Short: "Create a starter Kapro promotion repo",
		Long: `Create a GitOps-ready promotion repository with Backend, Fleet,
Plan, and sample Promotion manifests.

This bootstraps the promotion layer. Argo, Flux, Helm, and Kubernetes still own
local sync and rollout mechanics.

Examples:
  kapro init ./promotion-repo --backend flux --mode pull --name checkout
  kapro init ./promotion-repo --backend argo --name checkout
  kapro init ./promotion-repo --backend argo --name checkout --clusters none`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			dir := "."
			if len(args) > 0 {
				dir = args[0]
			}
			opts.Path = dir
			return runInitScaffold(opts)
		},
	}
	cmd.Flags().StringVar(&opts.Name, "name", "checkout", "Application or fleet name")
	cmd.Flags().StringVar(&opts.Backend, "backend", "argo", "Delivery backend: argo, flux, or oci")
	cmd.Flags().StringVar(&opts.Mode, "mode", "push", "Delivery mode: push or pull")
	cmd.Flags().StringVar(&opts.Registry, "registry", "oci://registry.example.com/platform", "OCI registry URL for bundles")
	cmd.Flags().StringVar(&opts.Namespace, "namespace", "", "Backend namespace (default: argocd for argo, flux-system for flux, kapro-system for oci)")
	cmd.Flags().StringVar(&opts.Clusters, "clusters", "canary-eu:canary,prod-eu:production", "Cluster scaffold list as name:stage pairs, or none for repo-only setup")
	cmd.Flags().StringVar(&opts.Team, "team", "platform", "Value for metadata.labels[kapro.io/team]")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "Overwrite existing generated files")
	return cmd
}

func newConnectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "connect",
		Short: "Scaffold observe-first connect manifests",
		Long: `Scaffolds observe-first Backend manifests for existing Argo CD
or Flux installations. Observe mode discovers existing backend objects without
taking over writes.

This command is Backend-only. Use kapro discover or kapro adopt when you also
want generated Source units and discovery review reports.`,
	}
	cmd.AddCommand(newConnectBackendCmd("argo"))
	cmd.AddCommand(newConnectBackendCmd("flux"))
	return cmd
}

func newConnectBackendCmd(backend string) *cobra.Command {
	var opts connectOptions
	opts.Backend = backend
	cmd := &cobra.Command{
		Use:   backend + " [directory]",
		Short: "Scaffold observe-first " + backend + " connect manifests",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			dir := "."
			if len(args) > 0 {
				dir = args[0]
			}
			opts.Path = dir
			return runConnectScaffold(opts)
		},
	}
	cmd.Flags().StringVar(&opts.Name, "name", backend, "Backend name")
	cmd.Flags().StringVar(&opts.Namespace, "namespace", defaultBackendNamespace(backend), "Backend namespace")
	cmd.Flags().StringVar(&opts.Selector, "selector", "kapro.io/import=true", "Label selector for imported backend objects")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "Overwrite existing generated files")
	return cmd
}

type scaffoldOptions struct {
	Path      string
	Name      string
	Backend   string
	Mode      string
	Registry  string
	Namespace string
	Clusters  string
	Team      string
	Force     bool
}

type connectOptions struct {
	Path      string
	Name      string
	Backend   string
	Namespace string
	Selector  string
	Force     bool
}

func runInitScaffold(opts scaffoldOptions) error {
	if opts.Name == "" {
		return fmt.Errorf("--name is required")
	}
	opts.Backend = strings.ToLower(opts.Backend)
	if opts.Backend != "argo" && opts.Backend != "flux" && opts.Backend != "oci" {
		return fmt.Errorf("--backend must be argo, flux, or oci")
	}
	if opts.Mode != "push" && opts.Mode != "pull" {
		return fmt.Errorf("--mode must be push or pull")
	}
	if opts.Backend == "oci" && opts.Mode != "pull" {
		return fmt.Errorf("--backend oci requires --mode pull")
	}
	if opts.Namespace == "" {
		opts.Namespace = defaultBackendNamespace(opts.Backend)
	}
	if opts.Clusters == "" {
		opts.Clusters = "canary-eu:canary,prod-eu:production"
	}
	opts.Team = strings.TrimSpace(opts.Team)
	if opts.Team == "" {
		opts.Team = "platform"
	}

	files := greenfieldFiles(opts)
	if err := writeScaffoldFiles(opts.Path, files, opts.Force); err != nil {
		return err
	}
	printInitNextSteps(opts, len(files))
	return nil
}

func runConnectScaffold(opts connectOptions) error {
	opts.Backend = strings.ToLower(opts.Backend)
	if opts.Backend != "argo" && opts.Backend != "flux" {
		return fmt.Errorf("backend must be argo or flux")
	}
	if opts.Name == "" {
		opts.Name = opts.Backend
	}
	if opts.Namespace == "" {
		opts.Namespace = defaultBackendNamespace(opts.Backend)
	}
	matchLabels, err := parseSelector(opts.Selector)
	if err != nil {
		return err
	}

	files := map[string]string{
		filepath.Join("backends", opts.Backend+"-observe.yaml"): renderConnectBackend(opts, matchLabels),
		filepath.Join("README.md"):                              renderConnectReadme(opts),
	}
	if err := writeScaffoldFiles(opts.Path, files, opts.Force); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "\nGenerated %d Kapro %s connect files in %s\n", len(files), opts.Backend, opts.Path)
	fmt.Fprintf(os.Stderr, "\nNext steps:\n  kubectl apply -f %s\n  kubectl get backend %s -o yaml\n", filepath.Join(opts.Path, "backends", opts.Backend+"-observe.yaml"), opts.Name)
	return nil
}

func writeScaffoldFiles(root string, files map[string]string, force bool) error {
	relPaths := make([]string, 0, len(files))
	for relPath := range files {
		relPaths = append(relPaths, relPath)
	}
	sort.Strings(relPaths)

	var sp *cli.Spinner
	showSpinner := cli.IsInteractive() && !cli.IsJSON()
	if showSpinner {
		sp = cli.NewSpinner(fmt.Sprintf("Writing %d files into %s", len(relPaths), root))
		sp.Start()
	}
	for _, relPath := range relPaths {
		if sp != nil {
			sp.Update("Writing " + relPath)
		}
		content := files[relPath]
		absPath := filepath.Join(root, relPath)
		if !force {
			if _, err := os.Stat(absPath); err == nil {
				if sp != nil {
					sp.StopFail("Could not write starter files")
				}
				return fmt.Errorf("%s already exists; use --force to overwrite", absPath)
			} else if !os.IsNotExist(err) {
				if sp != nil {
					sp.StopFail("Could not inspect starter files")
				}
				return err
			}
		}
		if err := os.MkdirAll(filepath.Dir(absPath), 0755); err != nil {
			if sp != nil {
				sp.StopFail("Could not create starter directories")
			}
			return err
		}
		if err := os.WriteFile(absPath, []byte(content), 0644); err != nil {
			if sp != nil {
				sp.StopFail("Could not write starter files")
			}
			return err
		}
		if !showSpinner {
			fmt.Fprintf(os.Stderr, "  created %s\n", absPath)
		}
	}
	if sp != nil {
		sp.StopSuccess(fmt.Sprintf("Wrote %d files into %s", len(relPaths), root))
	}
	return nil
}

func printInitNextSteps(opts scaffoldOptions, count int) {
	fmt.Fprintf(os.Stderr, "\nGenerated %d Kapro starter files in %s\n", count, opts.Path)
	fmt.Fprintf(os.Stderr, "\nNext steps:\n")
	if len(parseClusterScaffold(opts.Clusters)) == 0 {
		fmt.Fprintf(os.Stderr, "Shape: Backend, Source, and backend-native sample manifests. Add clusters before creating Fleet and Promotion files.\n")
		fmt.Fprintf(os.Stderr, "  kubectl apply --recursive -f %s\n", opts.Path)
		fmt.Fprintf(os.Stderr, "  add clusters/, then create fleets/%s.yaml and promotions/%s-promotion.yaml\n", opts.Name, opts.Name)
		printAdoptionFooter(opts.Path)
		return
	}
	fmt.Fprintf(os.Stderr, "Shape: Backend, Fleet, Plan, Promotion, and backend-native sample manifests.\n")
	fmt.Fprintf(os.Stderr, "  kubectl apply --recursive -f %s\n", opts.Path)
	fmt.Fprintf(os.Stderr, "  kapro promote %s --version 0.1.1  # creates/updates Promotion intent; controller stamps PromotionRun\n", opts.Name)
	fmt.Fprintf(os.Stderr, "  kapro diag %s\n", defaultPromotionRunName(opts.Name, "0.1.1", nil))
	printAdoptionFooter(opts.Path)
}

func greenfieldFiles(opts scaffoldOptions) map[string]string {
	files := map[string]string{
		filepath.Join("backends", opts.Backend+".yaml"): renderGreenfieldBackend(opts),
		filepath.Join("plans", opts.Name+".yaml"):       renderPlan(opts),
		filepath.Join("README.md"):                      renderGreenfieldReadme(opts),
		filepath.Join(".gitignore"):                     ".DS_Store\n",
	}
	clusters := parseClusterScaffold(opts.Clusters)
	for _, cluster := range clusters {
		files[filepath.Join("clusters", cluster.Name+".yaml")] = renderCluster(opts, cluster.Name, cluster.Tier)
	}
	if len(clusters) > 0 {
		files[filepath.Join("fleets", opts.Name+".yaml")] = renderKapro(opts, clusters)
		files[filepath.Join("promotions", opts.Name+"-promotion.yaml")] = renderPromotion(opts)
	} else {
		files[filepath.Join("sources", opts.Name+".yaml")] = renderPromotionSource(opts)
	}
	switch opts.Backend {
	case "argo":
		files[filepath.Join("argo", "applications", opts.Name+".yaml")] = renderArgoApplication(opts)
	case "flux":
		files[filepath.Join("flux", "kustomizations", opts.Name+".yaml")] = renderFluxKustomization(opts)
	}
	return files
}

type scaffoldCluster struct {
	Name string
	Tier string
}

func parseClusterScaffold(raw string) []scaffoldCluster {
	if strings.EqualFold(strings.TrimSpace(raw), "none") {
		return nil
	}
	var clusters []scaffoldCluster
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		name, tier, ok := strings.Cut(item, ":")
		if !ok {
			tier = name
		}
		clusters = append(clusters, scaffoldCluster{Name: strings.TrimSpace(name), Tier: strings.TrimSpace(tier)})
	}
	return clusters
}

func defaultBackendNamespace(backend string) string {
	if backend == "argo" {
		return "argocd"
	}
	if backend == "oci" {
		return "kapro-system"
	}
	return "flux-system"
}

func parseSelector(raw string) (map[string]string, error) {
	labels := map[string]string{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, value, ok := strings.Cut(part, "=")
		if !ok || strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("selector must be comma-separated key=value pairs")
		}
		labels[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return labels, nil
}

func renderGreenfieldBackend(opts scaffoldOptions) string {
	if opts.Backend == "oci" {
		return fmt.Sprintf(`apiVersion: kapro.io/v1alpha2
kind: Backend
metadata:
  name: oci
spec:
  driver: oci
  runtime: Spoke
  parameters:
    repository: %s/{appKey}
    tag: "{version}"
    auth: anonymous
`, strings.TrimSuffix(strings.TrimPrefix(opts.Registry, "oci://"), "/"))
	}
	return fmt.Sprintf(`apiVersion: kapro.io/v1alpha2
kind: Backend
metadata:
  name: %s
spec:
  driver: %s
  runtime: Hub
  parameters:
    namespace: %s
`, opts.Backend, opts.Backend, opts.Namespace)
}

func renderConnectBackend(opts connectOptions, labels map[string]string) string {
	return fmt.Sprintf(`apiVersion: kapro.io/v1alpha2
kind: Backend
metadata:
  name: %s
spec:
  driver: %s
  runtime: Hub
  parameters:
    namespace: %s
  discovery:
    enabled: true
    managementPolicy: Observe
    maxObjects: 1000
    selector:
      matchLabels:
%s`, opts.Name, opts.Backend, opts.Namespace, renderYAMLMap(labels, 8))
}

func renderCluster(opts scaffoldOptions, suffix, stage string) string {
	return fmt.Sprintf(`apiVersion: kapro.io/v1alpha2
kind: Cluster
metadata:
  name: %s
  labels:
    kapro.io/stage: %s
spec:
  delivery:
    mode: %s
    backendRef: %s
    parameters:
      namespace: %s
%s`, suffix, stage, opts.Mode, opts.Backend, opts.Namespace, renderDeliveryParameters(opts, suffix))
}

func renderDeliveryParameters(opts scaffoldOptions, suffix string) string {
	switch opts.Backend {
	case "argo":
		return fmt.Sprintf("      application: %s-%s\n", opts.Name, suffix)
	case "oci":
		return ""
	default:
		if opts.Mode == "push" {
			return fmt.Sprintf("      resourceSet: %s-workloads\n      inputField: tag\n      tenantField: tenant\n", opts.Name)
		}
		return fmt.Sprintf("      ociRepository: %s-bundle\n", opts.Name)
	}
}

func renderPromotionSource(opts scaffoldOptions) string {
	return fmt.Sprintf(`apiVersion: kapro.io/v1alpha2
kind: Source
metadata:
  name: %s
  labels:
    kapro.io/team: %s
spec:
  backendRef: %s
  registries:
    - name: app
      url: %s
      type: %s
  defaults:
    repo: app
    targetNamespace: %s
  units:
    - name: %s
      version: 0.1.0
      chartName: %s
`, opts.Name, opts.Team, opts.Backend, opts.Registry, registryType(opts.Registry), opts.Name, opts.Name, opts.Name)
}

func renderPlan(opts scaffoldOptions) string {
	return fmt.Sprintf(`apiVersion: kapro.io/v1alpha2
kind: Plan
metadata:
  name: %s
  labels:
    kapro.io/team: %s
spec:
  stages:
    - name: canary
      selector:
        matchLabels:
          kapro.io/stage: canary
      strategy:
        maxParallel: 1
    - name: production
      selector:
        matchLabels:
          kapro.io/stage: production
      dependsOn:
        - stage: canary
      strategy:
        maxParallel: 1
`, opts.Name, opts.Team)
}

func renderKapro(opts scaffoldOptions, clusters []scaffoldCluster) string {
	var clusterItems strings.Builder
	for _, cluster := range clusters {
		fmt.Fprintf(&clusterItems, `    - name: %s
      labels:
        kapro.io/stage: %s
`, cluster.Name, cluster.Tier)
	}
	return fmt.Sprintf(`apiVersion: kapro.io/v1alpha2
kind: Fleet
metadata:
  name: %s
  labels:
    kapro.io/team: %s
spec:
  registry:
    url: %s
  source:
    backendRef: %s
    registries:
      - name: app
        url: %s
        type: %s
    defaults:
      repo: app
      targetNamespace: %s
    units:
      - name: %s
        version: 0.1.0
        chartName: %s
  delivery:
    mode: %s
    backendRef: %s
    parameters:
      namespace: %s
  clusters:
%s
  plan:
    stages:
      - name: canary
        selector:
          kapro.io/stage: canary
      - name: production
        selector:
          kapro.io/stage: production
        dependsOn:
          - stage: canary
`, opts.Name, opts.Team, opts.Registry, opts.Backend, opts.Registry, registryType(opts.Registry), opts.Name, opts.Name, opts.Name, opts.Mode, opts.Backend, opts.Namespace, clusterItems.String())
}

func renderPromotion(opts scaffoldOptions) string {
	return fmt.Sprintf(`apiVersion: kapro.io/v1alpha2
kind: Promotion
metadata:
  name: %s-0-1-0
  labels:
    kapro.io/team: %s
spec:
  fleetRef: %s
  version: 0.1.0
  timeout: 30m
`, opts.Name, opts.Team, opts.Name)
}

func renderArgoApplication(opts scaffoldOptions) string {
	return fmt.Sprintf(`apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: %s-canary
  namespace: %s
  labels:
    kapro.io/import: "true"
spec:
  project: default
  source:
    repoURL: https://github.com/example/%s-config.git
    targetRevision: 0.1.0
    path: apps/%s
  destination:
    server: https://kubernetes.default.svc
    namespace: %s
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
`, opts.Name, opts.Namespace, opts.Name, opts.Name, opts.Name)
}

func renderFluxKustomization(opts scaffoldOptions) string {
	return fmt.Sprintf(`apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: %s
  namespace: %s
  labels:
    kapro.io/import: "true"
spec:
  interval: 1m
  path: ./apps/%s
  prune: true
  sourceRef:
    kind: GitRepository
    name: %s
`, opts.Name, opts.Namespace, opts.Name, opts.Name)
}

func renderGreenfieldReadme(opts scaffoldOptions) string {
	if len(parseClusterScaffold(opts.Clusters)) == 0 {
		backendStep := ""
		if opts.Backend == "argo" || opts.Backend == "flux" {
			backendStep = fmt.Sprintf("4. %s/\n", opts.Backend)
		}
		return fmt.Sprintf(`# %s Kapro Promotion Repo

This repo is a repo-first Kapro scaffold for the %s backend.

Apply order:

1. backends/
2. sources/
3. plans/
%s
Apply with:

`+"```bash"+`
kubectl apply --recursive -f .
`+"```"+`

Clusters are intentionally not generated yet. Add clusters later, then add
clusters/, fleets/, and promotions/ when real target clusters exist.

Kapro coordinates promotion. The %s backend owns local sync and rollout mechanics.
`, opts.Name, opts.Backend, backendStep, opts.Backend)
	}
	return fmt.Sprintf(`# %s Kapro Promotion Repo

This repo is a greenfield Kapro scaffold for the %s backend.

Apply order:

1. backends/
2. clusters/
3. plans/
4. fleets/
5. promotions/

Apply with:

`+"```bash"+`
kubectl apply --recursive -f .
`+"```"+`

Kapro coordinates promotion. The %s backend owns local sync and rollout mechanics.
`, opts.Name, opts.Backend, opts.Backend)
}

func renderConnectReadme(opts connectOptions) string {
	return fmt.Sprintf(`# Kapro %s Existing GitOps Connection

This scaffold starts in observe mode. Kapro discovers existing %s objects and
reports them through Backend status without taking over writes.

This is a Backend-only scaffold. Use `+"`kapro discover %s`"+` or
`+"`kapro adopt %s`"+` when you want generated Source units and discovery review
reports.

Apply:

`+"```bash"+`
kubectl apply -f backends/%s-observe.yaml
`+"```"+`

When the observed graph is correct, switch managementPolicy from Observe to
Adopt for the selected Backend. Kapro still references backend-owned
Secrets and configuration; it does not copy Argo CD or Flux credentials into
Kapro objects.
`, opts.Backend, opts.Backend, opts.Backend, opts.Backend, opts.Backend)
}

func renderYAMLMap(labels map[string]string, indent int) string {
	if len(labels) == 0 {
		return strings.Repeat(" ", indent) + "{}\n"
	}
	spaces := strings.Repeat(" ", indent)
	var b strings.Builder
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := labels[k]
		fmt.Fprintf(&b, "%s%s: %q\n", spaces, k, v)
	}
	return b.String()
}

func registryType(registry string) string {
	if strings.HasPrefix(registry, "oci://") {
		return "oci"
	}
	return "default"
}
