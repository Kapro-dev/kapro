// Package bundle generates spoke-side Flux manifests and pushes them as OCI
// artifacts. The bundle contains everything a spoke cluster needs to reconcile
// workloads locally: HelmRepositories and HelmReleases (no kubeConfig — spoke's
// own helm-controller reconciles them).
//
// Bundle layout pushed to OCI (per-wave directories):
//
//	wave-00/
//	  helmrepository-{name}.yaml     — shared HelmRepo (wave 0 owns it)
//	  {component}-hr.yaml            — HelmReleases for wave 0
//	wave-01/
//	  {component}-hr.yaml            — HelmReleases for wave 1
//	wave-02/
//	  {component}-hr.yaml            — HelmReleases for wave 2
//
// Wave Kustomizations (dependsOn chains) are NOT in the bundle — they are
// bootstrap resources created once on the spoke by the hub's ResourceSet.
// Each wave Kustomization points to its wave-NN/ path in the bundle.
package bundle

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/yaml"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// BundleRequest holds the inputs for bundle generation.
type BundleRequest struct {
	KaproName string
	App       *kaprov1alpha1.KaproApp
	Version   string // OCI tag for the bundle
	Registry  string // OCI registry URL (e.g. oci://europe-west1-docker.pkg.dev/project/repo)
}

// GenerateAndPush builds the spoke manifests, writes them to a temp directory,
// and pushes the directory as an OCI artifact using `flux push artifact`.
// Returns the full OCI URL with tag.
func GenerateAndPush(ctx context.Context, req BundleRequest) (string, error) {
	l := log.FromContext(ctx)

	dir, err := os.MkdirTemp("", "kapro-bundle-*")
	if err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	manifests := Generate(req)
	for relPath, content := range manifests {
		absPath := filepath.Join(dir, relPath)
		if err := os.MkdirAll(filepath.Dir(absPath), 0755); err != nil {
			return "", fmt.Errorf("create dir for %s: %w", relPath, err)
		}
		if err := os.WriteFile(absPath, []byte(content), 0644); err != nil {
			return "", fmt.Errorf("write %s: %w", relPath, err)
		}
	}

	ociURL := fmt.Sprintf("%s/%s-bundle:%s", req.Registry, req.KaproName, req.Version)
	l.Info("pushing OCI bundle", "url", ociURL, "files", len(manifests))

	cmd := exec.CommandContext(ctx, "flux", "push", "artifact",
		ociURL,
		"--path", dir,
		"--source", "kapro://"+req.KaproName,
		"--revision", req.Version,
	)
	cmd.Env = append(os.Environ(), "FLUX_SYSTEM_NAMESPACE=flux-system")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("flux push artifact: %s: %w", string(out), err)
	}

	l.Info("OCI bundle pushed", "url", ociURL)
	return ociURL, nil
}

// Generate produces the spoke manifest files as a map of relative path → YAML content.
// Files are organized into per-wave directories (wave-00/, wave-01/, ...).
// Wave 0 also contains shared HelmRepositories.
func Generate(req BundleRequest) map[string]string {
	app := req.App
	defaults := app.Spec.Defaults
	if defaults == nil {
		defaults = &kaprov1alpha1.AppDefaults{}
	}

	manifests := map[string]string{}
	waves := groupByWave(app.Spec.Components)
	firstWave := sortedWaveNumbers(waves)[0]

	// HelmRepositories go into the first wave directory.
	for _, reg := range app.Spec.Registries {
		path := fmt.Sprintf("wave-%02d/helmrepository-%s.yaml", firstWave, reg.Name)
		manifests[path] = buildHelmRepository(req.KaproName, reg)
	}

	// HelmReleases go into their wave directory.
	for _, comp := range app.Spec.Components {
		path := fmt.Sprintf("wave-%02d/%s-hr.yaml", comp.Wave, comp.Name)
		manifests[path] = buildSpokeHelmRelease(req.KaproName, defaults, comp)
	}

	return manifests
}

// WaveKustomizations generates the wave Kustomization CRs (bootstrap resources).
// These are NOT part of the OCI bundle — they're created once on the spoke by
// the hub's ResourceSet. Each Kustomization points to its wave directory in the
// bundle and has dependsOn to the previous wave.
func WaveKustomizations(kaproName string, app *kaprov1alpha1.KaproApp) []map[string]any {
	waves := groupByWave(app.Spec.Components)
	sorted := sortedWaveNumbers(waves)
	result := make([]map[string]any, 0, len(sorted))

	for i, waveNum := range sorted {
		comps := waves[waveNum]

		// healthChecks: one per HelmRelease in this wave.
		healthChecks := make([]any, 0, len(comps))
		for _, comp := range comps {
			healthChecks = append(healthChecks, map[string]any{
				"apiVersion": "helm.toolkit.fluxcd.io/v2",
				"kind":       "HelmRelease",
				"name":       comp.Name,
				"namespace":  "flux-system",
			})
		}

		spec := map[string]any{
			"interval": "5m",
			"path":     fmt.Sprintf("./wave-%02d", waveNum),
			"prune":    true,
			"wait":     true,
			"sourceRef": map[string]any{
				"kind": "OCIRepository",
				"name": kaproName + "-bundle",
			},
			"healthChecks": healthChecks,
		}

		// dependsOn previous wave.
		if i > 0 {
			spec["dependsOn"] = []any{
				map[string]any{
					"name": fmt.Sprintf("%s-wave-%02d", kaproName, sorted[i-1]),
				},
			}
		}

		result = append(result, map[string]any{
			"apiVersion": "kustomize.toolkit.fluxcd.io/v1",
			"kind":       "Kustomization",
			"metadata": map[string]any{
				"name":      fmt.Sprintf("%s-wave-%02d", kaproName, waveNum),
				"namespace": "flux-system",
				"labels": map[string]any{
					"kapro.io/managed-by": kaproName,
					"kapro.io/wave":       fmt.Sprintf("%d", waveNum),
				},
			},
			"spec": spec,
		})
	}
	return result
}

// --- HelmRepository ---

func buildHelmRepository(kaproName string, reg kaprov1alpha1.AppRegistry) string {
	spec := map[string]any{
		"interval": resolveDefault(reg.Interval, "5m"),
		"url":      reg.URL,
	}
	if reg.Provider != "" && reg.Provider != "generic" {
		spec["provider"] = reg.Provider
	}
	if reg.Type == "oci" || strings.HasPrefix(reg.URL, "oci://") {
		spec["type"] = "oci"
	}

	obj := map[string]any{
		"apiVersion": "source.toolkit.fluxcd.io/v1",
		"kind":       "HelmRepository",
		"metadata": map[string]any{
			"name":      reg.Name,
			"namespace": "flux-system",
			"labels":    map[string]any{"kapro.io/managed-by": kaproName},
		},
		"spec": spec,
	}
	return mustYAML(obj)
}

// --- Spoke HelmRelease (no kubeConfig) ---

func buildSpokeHelmRelease(kaproName string, defaults *kaprov1alpha1.AppDefaults, comp kaprov1alpha1.AppComponent) string {
	chartName := comp.Name
	if comp.ChartName != "" {
		chartName = comp.ChartName
	}
	repo := resolveDefault(comp.Repo, defaults.Repo)
	targetNS := resolveDefault(comp.TargetNamespace, defaults.TargetNamespace)
	if targetNS == "" {
		targetNS = "workloads"
	}
	timeout := resolveDefault(comp.Timeout, defaults.Timeout)
	if timeout == "" {
		timeout = "10m"
	}
	retries := defaults.Retries
	if comp.Retries != nil {
		retries = *comp.Retries
	}
	if retries == 0 {
		retries = 3
	}

	hrSpec := map[string]any{
		"interval": "5m",
		"chart": map[string]any{
			"spec": map[string]any{
				"chart":             chartName,
				"version":           comp.Version,
				"reconcileStrategy": "ChartVersion",
				"sourceRef": map[string]any{
					"kind": "HelmRepository",
					"name": repo,
				},
			},
		},
		"targetNamespace": targetNS,
		"releaseName":     comp.Name,
		"install": map[string]any{
			"createNamespace": true,
			"timeout":         timeout,
			"remediation":     map[string]any{"retries": retries},
		},
		"upgrade": map[string]any{
			"timeout":     timeout,
			"remediation": map[string]any{"retries": retries},
		},
	}

	// CRD policy.
	if comp.CRDs == "Create" || comp.CRDs == "CreateReplace" {
		hrSpec["install"].(map[string]any)["crds"] = comp.CRDs
		if comp.CRDs == "Create" {
			hrSpec["upgrade"].(map[string]any)["crds"] = "CreateReplace"
		} else {
			hrSpec["upgrade"].(map[string]any)["crds"] = comp.CRDs
		}
	}

	if comp.Suspend {
		hrSpec["suspend"] = true
	}

	obj := map[string]any{
		"apiVersion": "helm.toolkit.fluxcd.io/v2",
		"kind":       "HelmRelease",
		"metadata": map[string]any{
			"name":      comp.Name,
			"namespace": "flux-system",
			"labels": map[string]any{
				"kapro.io/managed-by": kaproName,
				"kapro.io/wave":       fmt.Sprintf("%d", comp.Wave),
			},
		},
		"spec": hrSpec,
	}
	return mustYAML(obj)
}

// --- Helpers ---

func groupByWave(components []kaprov1alpha1.AppComponent) map[int32][]kaprov1alpha1.AppComponent {
	waves := map[int32][]kaprov1alpha1.AppComponent{}
	for _, comp := range components {
		waves[comp.Wave] = append(waves[comp.Wave], comp)
	}
	return waves
}

func sortedWaveNumbers(waves map[int32][]kaprov1alpha1.AppComponent) []int32 {
	nums := make([]int32, 0, len(waves))
	for n := range waves {
		nums = append(nums, n)
	}
	sort.Slice(nums, func(i, j int) bool { return nums[i] < nums[j] })
	return nums
}

func resolveDefault(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func mustYAML(obj any) string {
	b, err := yaml.Marshal(obj)
	if err != nil {
		panic(fmt.Sprintf("yaml marshal: %v", err))
	}
	return string(b)
}
