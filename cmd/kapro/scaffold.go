package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

func newInitCmd() *cobra.Command {
	var opts scaffoldOptions
	cmd := &cobra.Command{
		Use:   "init [directory]",
		Short: "Scaffold a greenfield Kapro promotion repo",
		Long: `Scaffolds a GitOps-ready promotion repository with BackendProfile,
KaproBundle, Pipeline, Kapro, and sample Release manifests.

This bootstraps the promotion layer. Argo, Flux, Helm, and Kubernetes still own
local sync and rollout mechanics.`,
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
	cmd.Flags().StringVar(&opts.Backend, "backend", "argo", "Delivery backend: argo or flux")
	cmd.Flags().StringVar(&opts.Mode, "mode", "push", "Delivery mode: push or pull")
	cmd.Flags().StringVar(&opts.Registry, "registry", "oci://registry.example.com/platform", "OCI registry URL for bundles")
	cmd.Flags().StringVar(&opts.Namespace, "namespace", "", "Backend namespace (default: argocd for argo, flux-system for flux)")
	cmd.Flags().StringVar(&opts.Clusters, "clusters", "canary:canary,prod:production", "Cluster scaffold list as name:tier pairs, or none for repo-only setup")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "Overwrite existing generated files")
	return cmd
}

func newConnectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "connect",
		Short: "Scaffold brownfield connect manifests",
		Long: `Scaffolds observe-first BackendProfile manifests for existing Argo CD
or Flux installations. Observe mode discovers existing backend objects without
taking over writes.`,
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
	cmd.Flags().StringVar(&opts.Name, "name", backend, "BackendProfile name")
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
	if opts.Backend != "argo" && opts.Backend != "flux" {
		return fmt.Errorf("--backend must be argo or flux")
	}
	if opts.Mode != "push" && opts.Mode != "pull" {
		return fmt.Errorf("--mode must be push or pull")
	}
	if opts.Namespace == "" {
		opts.Namespace = defaultBackendNamespace(opts.Backend)
	}
	if opts.Clusters == "" {
		opts.Clusters = "canary:canary,prod:production"
	}

	files := greenfieldFiles(opts)
	if err := writeScaffoldFiles(opts.Path, files, opts.Force); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Generated %d Kapro greenfield files in %s\n", len(files), opts.Path)
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
	fmt.Fprintf(os.Stderr, "Generated %d Kapro %s connect files in %s\n", len(files), opts.Backend, opts.Path)
	return nil
}

func writeScaffoldFiles(root string, files map[string]string, force bool) error {
	for relPath, content := range files {
		absPath := filepath.Join(root, relPath)
		if !force {
			if _, err := os.Stat(absPath); err == nil {
				return fmt.Errorf("%s already exists; use --force to overwrite", absPath)
			} else if !os.IsNotExist(err) {
				return err
			}
		}
		if err := os.MkdirAll(filepath.Dir(absPath), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(absPath, []byte(content), 0644); err != nil {
			return err
		}
		fmt.Println(absPath)
	}
	return nil
}

func greenfieldFiles(opts scaffoldOptions) map[string]string {
	files := map[string]string{
		filepath.Join("backends", opts.Backend+".yaml"): renderGreenfieldBackend(opts),
		filepath.Join("bundles", opts.Name+".yaml"):     renderBundle(opts),
		filepath.Join("pipelines", opts.Name+".yaml"):   renderPipeline(opts),
		filepath.Join("README.md"):                      renderGreenfieldReadme(opts),
		filepath.Join(".gitignore"):                     ".DS_Store\n",
	}
	clusters := parseClusterScaffold(opts.Clusters)
	for _, cluster := range clusters {
		files[filepath.Join("clusters", cluster.Name+".yaml")] = renderCluster(opts, cluster.Name, cluster.Tier)
	}
	if len(clusters) > 0 {
		files[filepath.Join("kapro", opts.Name+".yaml")] = renderKapro(opts, clusters)
		files[filepath.Join("releases", opts.Name+"-release.yaml")] = renderRelease(opts)
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
	return fmt.Sprintf(`apiVersion: kapro.io/v1alpha1
kind: BackendProfile
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
	return fmt.Sprintf(`apiVersion: kapro.io/v1alpha1
kind: BackendProfile
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
    selector:
      matchLabels:
%s`, opts.Name, opts.Backend, opts.Namespace, renderYAMLMap(labels, 8))
}

func renderCluster(opts scaffoldOptions, suffix, tier string) string {
	return fmt.Sprintf(`apiVersion: kapro.io/v1alpha1
kind: MemberCluster
metadata:
  name: %s-%s
  labels:
    kapro.io/tier: %s
spec:
  delivery:
    mode: %s
    backendRef: %s
    parameters:
      namespace: %s
%s`, opts.Name, suffix, tier, opts.Mode, opts.Backend, opts.Namespace, renderDeliveryParameters(opts, suffix))
}

func renderDeliveryParameters(opts scaffoldOptions, suffix string) string {
	switch opts.Backend {
	case "argo":
		return fmt.Sprintf("      application: %s-%s\n", opts.Name, suffix)
	default:
		if opts.Mode == "push" {
			return fmt.Sprintf("      resourceSet: %s-workloads\n      inputField: tag\n      tenantField: tenant\n", opts.Name)
		}
		return fmt.Sprintf("      ociRepository: %s-bundle\n", opts.Name)
	}
}

func renderBundle(opts scaffoldOptions) string {
	return fmt.Sprintf(`apiVersion: kapro.io/v1alpha1
kind: KaproBundle
metadata:
  name: %s
spec:
  registries:
    - name: default
      url: %s
  components:
    - name: %s-api
      version: 0.1.0
      repo: default
      chartName: %s-api
      targetNamespace: %s
`, opts.Name, opts.Registry, opts.Name, opts.Name, opts.Name)
}

func renderPipeline(opts scaffoldOptions) string {
	return fmt.Sprintf(`apiVersion: kapro.io/v1alpha1
kind: Pipeline
metadata:
  name: %s
spec:
  stages:
    - name: canary
      selector:
        matchLabels:
          kapro.io/tier: canary
      strategy:
        maxParallel: 1
    - name: production
      selector:
        matchLabels:
          kapro.io/tier: production
      dependsOn:
        - stage: canary
      strategy:
        maxParallel: 1
`, opts.Name)
}

func renderKapro(opts scaffoldOptions, clusters []scaffoldCluster) string {
	var clusterItems strings.Builder
	for _, cluster := range clusters {
		fmt.Fprintf(&clusterItems, `    - name: %s-%s
      labels:
        kapro.io/tier: %s
`, opts.Name, cluster.Name, cluster.Tier)
	}
	return fmt.Sprintf(`apiVersion: kapro.io/v1alpha1
kind: Kapro
metadata:
  name: %s
spec:
  registry:
    url: %s
  bundleRef: %s
  delivery:
    mode: %s
    backendRef: %s
    parameters:
      namespace: %s
  clusters:
%s
  pipeline:
    stages:
      - name: canary
        selector:
          kapro.io/tier: canary
      - name: production
        selector:
          kapro.io/tier: production
        dependsOn:
          - stage: canary
`, opts.Name, opts.Registry, opts.Name, opts.Mode, opts.Backend, opts.Namespace, clusterItems.String())
}

func renderRelease(opts scaffoldOptions) string {
	return fmt.Sprintf(`apiVersion: kapro.io/v1alpha1
kind: Release
metadata:
  name: %s-0-1-0
spec:
  version: 0.1.0
  pipelines:
    - name: main
      pipeline: %s
`, opts.Name, opts.Name)
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
		return fmt.Sprintf(`# %s Kapro Promotion Repo

This repo is a repo-first Kapro scaffold for the %s backend.

Apply order:

1. backends/
2. bundles/
3. pipelines/
4. %s/

Clusters are intentionally not generated yet. Add clusters later, then add
clusters/, kapro/, and releases/ when promotion targets exist.

Kapro coordinates promotion. The %s backend owns local sync and rollout mechanics.
`, opts.Name, opts.Backend, opts.Backend, opts.Backend)
	}
	return fmt.Sprintf(`# %s Kapro Promotion Repo

This repo is a greenfield Kapro scaffold for the %s backend.

Apply order:

1. backends/
2. bundles/
3. clusters/
4. pipelines/
5. kapro/
6. releases/

Kapro coordinates promotion. The %s backend owns local sync and rollout mechanics.
`, opts.Name, opts.Backend, opts.Backend)
}

func renderConnectReadme(opts connectOptions) string {
	return fmt.Sprintf(`# Kapro %s Brownfield Connection

This scaffold starts in observe mode. Kapro discovers existing %s objects and
reports them through BackendProfile status without taking over writes.

Apply:

`+"```bash"+`
kubectl apply -f backends/%s-observe.yaml
`+"```"+`

When the observed graph is correct, switch managementPolicy from Observe to
Adopt for the selected backend profile. Kapro still references backend-owned
Secrets and configuration; it does not copy Argo CD or Flux credentials into
Kapro objects.
`, opts.Backend, opts.Backend, opts.Backend)
}

func renderYAMLMap(labels map[string]string, indent int) string {
	if len(labels) == 0 {
		return strings.Repeat(" ", indent) + "{}\n"
	}
	spaces := strings.Repeat(" ", indent)
	var b strings.Builder
	for k, v := range labels {
		fmt.Fprintf(&b, "%s%s: %q\n", spaces, k, v)
	}
	return b.String()
}
