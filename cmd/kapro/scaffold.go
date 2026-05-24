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

const (
	argoCDSubstrateConfigAPIVersion     = "argocd.substrate.kapro.io/" + "v1alpha1"
	fluxSubstrateConfigAPIVersion       = "flux.substrate.kapro.io/" + "v1alpha1"
	kubernetesSubstrateConfigAPIVersion = "kubernetes.substrate.kapro.io/" + "v1alpha1"
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
	Path              string
	Name              string
	Backend           string
	Profile           string
	Mode              string
	Registry          string
	Namespace         string
	Clusters          string
	Team              string
	Force             bool
	UseSubstrateClass bool
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
	if opts.Profile != "" {
		opts.Profile = strings.ToLower(opts.Profile)
	}
	if opts.UseSubstrateClass {
		switch scaffoldProfile(opts) {
		case "direct", "argocd", "flux":
		default:
			return fmt.Errorf("--profile must be direct, argocd, or flux")
		}
	} else if opts.Backend != "argo" && opts.Backend != "flux" && opts.Backend != "oci" {
		return fmt.Errorf("--backend must be argo, flux, or oci")
	}
	if opts.Mode != "push" && opts.Mode != "pull" {
		return fmt.Errorf("--mode must be push or pull")
	}
	if opts.Backend == "oci" && opts.Mode != "pull" {
		return fmt.Errorf("--backend oci requires --mode pull")
	}
	if scaffoldProfile(opts) == "direct" && opts.Mode != "push" {
		return fmt.Errorf("--profile direct requires --mode push")
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
		printIndentedApplyInstructions(opts)
		fmt.Fprintf(os.Stderr, "  add clusters/, then create fleets/%s.yaml and promotions/%s-promotion.yaml\n", opts.Name, opts.Name)
		printScaffoldFooter(opts)
		return
	}
	fmt.Fprintf(os.Stderr, "Shape: Backend, Fleet, Plan, Promotion, and backend-native sample manifests.\n")
	printIndentedApplyInstructions(opts)
	nextVersion := nextScaffoldVersion(opts)
	fmt.Fprintf(os.Stderr, "  kapro promote %s --version %s  # creates/updates Promotion intent; controller stamps PromotionRun\n", opts.Name, nextVersion)
	fmt.Fprintf(os.Stderr, "  kapro diag %s\n", defaultPromotionRunName(opts.Name, nextVersion, nil))
	printScaffoldFooter(opts)
}

func printScaffoldFooter(opts scaffoldOptions) {
	if !opts.UseSubstrateClass {
		printAdoptionFooter(opts.Path)
		return
	}
	fmt.Fprintln(os.Stderr, "\nAdoption tip: run `kapro doctor` after installing the chart, then apply generated Backends and wait for Backend Ready before applying generated Clusters.")
}

func printIndentedApplyInstructions(opts scaffoldOptions) {
	instructions := renderApplyInstructions(opts)
	for _, line := range strings.Split(instructions, "\n") {
		line = prefixKubectlFileArgs(line, opts.Path)
		fmt.Fprintf(os.Stderr, "  %s\n", line)
	}
}

func prefixKubectlFileArgs(line, root string) string {
	if root == "" || root == "." || !strings.HasPrefix(line, "kubectl apply") {
		return line
	}
	fields := strings.Fields(line)
	for i := 0; i+1 < len(fields); i++ {
		if fields[i] == "-f" && !filepath.IsAbs(fields[i+1]) {
			fields[i+1] = filepath.Join(root, fields[i+1])
		}
	}
	return strings.Join(fields, " ")
}

func greenfieldFiles(opts scaffoldOptions) map[string]string {
	files := map[string]string{
		filepath.Join("backends", opts.Backend+".yaml"):              renderGreenfieldBackend(opts),
		filepath.Join("plans", opts.Name+".yaml"):                    renderPlan(opts),
		filepath.Join("README.md"):                                   renderGreenfieldReadme(opts),
		filepath.Join(".github", "workflows", "kapro-validate.yaml"): renderValidationWorkflow(),
		filepath.Join(".gitignore"):                                  ".DS_Store\n",
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
		files[filepath.Join("argo", "applications", opts.Name+".yaml")] = renderArgoApplications(opts, clusters)
		files[filepath.Join("apps", opts.Name, "deployment.yaml")] = renderDirectDeployment(opts)
		files[filepath.Join("apps", opts.Name, "service.yaml")] = renderDirectService(opts)
	case "flux":
		files[filepath.Join("apps", opts.Name, "deployment.yaml")] = renderDirectDeployment(opts)
		files[filepath.Join("apps", opts.Name, "kustomization.yaml")] = renderAppKustomization(opts)
		files[filepath.Join("flux", "kustomizations", opts.Name+".yaml")] = renderFluxKustomization(opts)
	case "direct":
		files[filepath.Join("apps", opts.Name, "deployment.yaml")] = renderDirectDeployment(opts)
		files[filepath.Join("apps", opts.Name, "service.yaml")] = renderDirectService(opts)
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
	if backend == "argo" || backend == "argocd" {
		return "argocd"
	}
	if backend == "direct" {
		return "default"
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
	if opts.UseSubstrateClass {
		return renderSubstrateClassBackend(opts)
	}
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

func renderSubstrateClassBackend(opts scaffoldOptions) string {
	switch scaffoldProfile(opts) {
	case "direct":
		return fmt.Sprintf(`apiVersion: kapro.io/v1alpha2
kind: SubstrateClass
metadata:
  name: kubernetes-apply
  labels:
    kapro.io/family: direct
    kapro.io/ledger: kubernetes-api
spec:
  controllerName: kapro.io/kubernetes-apply
  executionModes:
    default: hub-push
---
apiVersion: %s
kind: KubernetesApplyConfig
metadata:
  name: %s
spec:
  serverSideApply: true
  fieldManager: kapro
  namespace: %s
---
apiVersion: kapro.io/v1alpha2
kind: Backend
metadata:
  name: %s
spec:
  classRef:
    name: kubernetes-apply
  configRef:
    apiVersion: %s
    kind: KubernetesApplyConfig
    name: %s
  execution:
    mode: %s
`, kubernetesSubstrateConfigAPIVersion, opts.Backend, opts.Namespace, opts.Backend, kubernetesSubstrateConfigAPIVersion, opts.Backend, executionModeForDelivery(opts.Mode))
	case "argocd":
		return fmt.Sprintf(`apiVersion: kapro.io/v1alpha2
kind: SubstrateClass
metadata:
  name: argo-cd
  labels:
    kapro.io/family: gitops
    kapro.io/ledger: git
spec:
  controllerName: kapro.io/argo-cd
  executionModes:
    default: hub-push
---
apiVersion: %s
kind: ArgoCDSubstrateConfig
metadata:
  name: %s
spec:
  endpoint: https://argocd.example.com
  namespace: %s
  defaultProject: platform
---
apiVersion: kapro.io/v1alpha2
kind: Backend
metadata:
  name: %s
spec:
  classRef:
    name: argo-cd
  configRef:
    apiVersion: %s
    kind: ArgoCDSubstrateConfig
    name: %s
  execution:
    mode: %s
`, argoCDSubstrateConfigAPIVersion, opts.Backend, opts.Namespace, opts.Backend, argoCDSubstrateConfigAPIVersion, opts.Backend, executionModeForDelivery(opts.Mode))
	default:
		return fmt.Sprintf(`apiVersion: kapro.io/v1alpha2
kind: SubstrateClass
metadata:
  name: flux
  labels:
    kapro.io/family: gitops
    kapro.io/ledger: git
spec:
  controllerName: kapro.io/flux
  executionModes:
    default: spoke-pull
---
apiVersion: %s
kind: FluxSubstrateConfig
metadata:
  name: %s
spec:
  namespace: %s
---
apiVersion: kapro.io/v1alpha2
kind: Backend
metadata:
  name: %s
spec:
  classRef:
    name: flux
  configRef:
    apiVersion: %s
    kind: FluxSubstrateConfig
    name: %s
  execution:
    mode: %s
`, fluxSubstrateConfigAPIVersion, opts.Backend, opts.Namespace, opts.Backend, fluxSubstrateConfigAPIVersion, opts.Backend, executionModeForDelivery(opts.Mode))
	}
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
	case "direct":
		return fmt.Sprintf("      deployment: %s\n      container: app\n      manifestPath: apps/%s\n", opts.Name, opts.Name)
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
	if scaffoldProfile(opts) == "direct" {
		return fmt.Sprintf(`apiVersion: kapro.io/v1alpha2
kind: Source
metadata:
  name: %s
  labels:
    kapro.io/team: %s
spec:
  backendRef: %s
  defaults:
    targetNamespace: %s
  units:
    - name: %s
      backendKind: KubernetesManifest
      sourcePath: apps/%s/deployment.yaml
      versionField: spec.template.spec.containers[0].image
      version: %s
`, opts.Name, opts.Team, opts.Backend, opts.Namespace, opts.Name, opts.Name, defaultScaffoldVersion(opts))
	}
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
	if scaffoldProfile(opts) == "direct" {
		return fmt.Sprintf(`apiVersion: kapro.io/v1alpha2
kind: Fleet
metadata:
  name: %s
  labels:
    kapro.io/team: %s
spec:
  source:
    backendRef: %s
    defaults:
      targetNamespace: %s
    units:
      - name: %s
        backendKind: KubernetesManifest
        sourcePath: apps/%s/deployment.yaml
        versionField: spec.template.spec.containers[0].image
        version: %s
  delivery:
    mode: %s
    backendRef: %s
    parameters:
      deployment: %s
      container: app
      manifestPath: apps/%s
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
`, opts.Name, opts.Team, opts.Backend, opts.Namespace, opts.Name, opts.Name, defaultScaffoldVersion(opts), opts.Mode, opts.Backend, opts.Name, opts.Name, opts.Namespace, clusterItems.String())
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
  version: %s
  timeout: 30m
`, opts.Name, opts.Team, opts.Name, defaultScaffoldVersion(opts))
}

func renderDirectDeployment(opts scaffoldOptions) string {
	return fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
  namespace: %s
  labels:
    app.kubernetes.io/name: %s
spec:
  replicas: 2
  selector:
    matchLabels:
      app.kubernetes.io/name: %s
  template:
    metadata:
      labels:
        app.kubernetes.io/name: %s
    spec:
      containers:
        - name: app
          image: %s
          ports:
            - containerPort: 8080
`, opts.Name, opts.Namespace, opts.Name, opts.Name, opts.Name, defaultScaffoldVersion(opts))
}

func renderDirectService(opts scaffoldOptions) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Service
metadata:
  name: %s
  namespace: %s
  labels:
    app.kubernetes.io/name: %s
spec:
  selector:
    app.kubernetes.io/name: %s
  ports:
    - name: http
      port: 80
      targetPort: 8080
`, opts.Name, opts.Namespace, opts.Name, opts.Name)
}

func renderArgoApplications(opts scaffoldOptions, clusters []scaffoldCluster) string {
	if len(clusters) == 0 {
		return renderArgoApplication(opts, opts.Name)
	}
	var b strings.Builder
	for i, cluster := range clusters {
		if i > 0 {
			b.WriteString("---\n")
		}
		b.WriteString(renderArgoApplication(opts, fmt.Sprintf("%s-%s", opts.Name, cluster.Name)))
	}
	return b.String()
}

func renderArgoApplication(opts scaffoldOptions, appName string) string {
	return fmt.Sprintf(`apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: %s
  namespace: %s
  labels:
    kapro.io/import: "true"
    kapro.io/managed-by: kapro
    kapro.io/authorized-unit: "*"
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
`, appName, opts.Namespace, opts.Name, opts.Name, opts.Name)
}

func renderFluxKustomization(opts scaffoldOptions) string {
	return fmt.Sprintf(`apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
metadata:
  name: %s
  namespace: %s
  labels:
    kapro.io/import: "true"
spec:
  interval: 1m
  url: https://github.com/example/%s-config.git
  ref:
    branch: main
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
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
`, opts.Name, opts.Namespace, opts.Name, opts.Name, opts.Namespace, opts.Name, opts.Name)
}

func renderAppKustomization(opts scaffoldOptions) string {
	return `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - deployment.yaml
  - service.yaml
`
}

func renderGreenfieldReadme(opts scaffoldOptions) string {
	if len(parseClusterScaffold(opts.Clusters)) == 0 {
		backendStep := ""
		if opts.Backend == "argo" || opts.Backend == "flux" || opts.Backend == "direct" {
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
%s
`+"```"+`

Clusters are intentionally not generated yet. Add clusters later, then add
clusters/, fleets/, and promotions/ when real target clusters exist.

Kapro coordinates promotion. The %s backend owns local sync and rollout mechanics.

Before expecting Argo CD or Flux to sync, push this repo and update the generated
backend-native Git URL placeholders to point at your repository.
`, opts.Name, opts.Backend, backendStep, renderApplyInstructions(opts), opts.Backend)
	}
	if scaffoldProfile(opts) == "direct" {
		return fmt.Sprintf(`# %s Kapro Direct Profile Repo

This repo is a greenfield Kapro scaffold for direct Kubernetes apply.

Apply order:

1. backends/
2. apps/
3. clusters/
4. plans/
5. fleets/
6. promotions/

Apply with:

`+"```bash"+`
%s
`+"```"+`

Kapro coordinates promotion. The direct profile applies the starter workload
manifests during bootstrap and updates Deployment images through the Kubernetes
API during promotion.
`, opts.Name, renderApplyInstructions(opts))
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
%s
`+"```"+`

Kapro coordinates promotion. The %s backend owns local sync and rollout mechanics.

Before expecting Argo CD or Flux to sync, push this repo and update the generated
backend-native Git URL placeholders to point at your repository.
`, opts.Name, opts.Backend, renderApplyInstructions(opts), opts.Backend)
}

func renderApplyInstructions(opts scaffoldOptions) string {
	if !opts.UseSubstrateClass {
		return "kubectl apply --recursive -f ."
	}
	paths := []string{"apps"}
	switch opts.Backend {
	case "argo":
		paths = append(paths, "argo")
	case "flux":
		paths = append(paths, "flux")
	}
	if len(parseClusterScaffold(opts.Clusters)) == 0 {
		paths = append(paths, "sources", "plans")
	} else {
		paths = append(paths, "clusters", "plans", "fleets", "promotions")
	}
	args := make([]string, 0, len(paths)*2)
	for _, path := range paths {
		args = append(args, "-f", path)
	}
	return fmt.Sprintf("kubectl apply -f backends/%s.yaml\nkubectl wait --for=condition=Ready backend/%s --timeout=90s\nkubectl apply --recursive %s",
		opts.Backend, opts.Backend, strings.Join(args, " "))
}

func renderValidationWorkflow() string {
	return `name: Kapro Validate

on:
  pull_request:
  push:

jobs:
  yaml:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-python@v5
        with:
          python-version: "3.x"
      - run: python -m pip install pyyaml
      - name: Parse YAML
        run: |
          python - <<'PY'
          import pathlib
          import sys
          import yaml

          failed = False
          for path in sorted(pathlib.Path(".").rglob("*.y*ml")):
              if ".git" in path.parts:
                  continue
              try:
                  with path.open() as fh:
                      list(yaml.safe_load_all(fh))
              except Exception as exc:
                  print(f"{path}: {exc}", file=sys.stderr)
                  failed = True
          if failed:
              sys.exit(1)
          PY
`
}

func scaffoldProfile(opts scaffoldOptions) string {
	if opts.Profile != "" {
		return strings.ToLower(opts.Profile)
	}
	switch opts.Backend {
	case "argo":
		return "argocd"
	default:
		return opts.Backend
	}
}

func executionModeForDelivery(mode string) string {
	if mode == "pull" {
		return "spoke-pull"
	}
	return "hub-push"
}

func defaultScaffoldVersion(opts scaffoldOptions) string {
	if scaffoldProfile(opts) == "direct" {
		return fmt.Sprintf("ghcr.io/example/%s:0.1.0", opts.Name)
	}
	return "0.1.0"
}

func nextScaffoldVersion(opts scaffoldOptions) string {
	if scaffoldProfile(opts) == "direct" {
		return fmt.Sprintf("ghcr.io/example/%s:0.1.1", opts.Name)
	}
	return "0.1.1"
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
