// Package bundle generates spoke-side Flux manifests and pushes them as OCI
// artifacts. The bundle contains everything a spoke cluster needs to reconcile
// workloads locally: HelmRepositories and HelmReleases (no kubeConfig — spoke's
// own helm-controller reconciles them).
//
// Bundle layout pushed to OCI (per-wave directories):
//
//	wave-00/
//	  helmrepository-{name}.yaml     — shared HelmRepo (wave 0 owns it)
//	  {unit}-hr.yaml            — HelmReleases for wave 0
//	wave-01/
//	  {unit}-hr.yaml            — HelmReleases for wave 1
//	wave-02/
//	  {unit}-hr.yaml            — HelmReleases for wave 2
//
// Wave Kustomizations (dependsOn chains) are NOT in the bundle — they are
// bootstrap resources created once on the spoke by the hub's ResourceSet.
// Each wave Kustomization points to its wave-NN/ path in the bundle.
package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/memory"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"sigs.k8s.io/yaml"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	"kapro.io/kapro/internal/provider"
)

// BundleRequest holds the inputs for bundle generation.
type BundleRequest struct {
	KaproName string
	Source    *kaprov1alpha2.Source
	Version   string // OCI tag for the bundle
	Registry  string // OCI registry URL (e.g. oci://europe-west1-docker.pkg.dev/project/repo)
}

// Push pushes an already-generated bundle directory to an OCI registry.
// Uses ORAS Go SDK with GCP Workload Identity — no flux CLI or gcloud dependency.
// Returns the full OCI URL with tag.
func Push(ctx context.Context, dir string, req BundleRequest) (string, error) {
	registryURL := strings.TrimPrefix(req.Registry, "oci://")
	repoRef := fmt.Sprintf("%s/%s-bundle", registryURL, req.KaproName)
	tag := req.Version

	// Create a tar.gz of the bundle directory.
	layerData, err := tarGzDir(dir)
	if err != nil {
		return "", fmt.Errorf("create tar.gz: %w", err)
	}

	// Build OCI manifest using ORAS.
	store := memory.New()

	// Push the layer.
	layerDesc := ocispec.Descriptor{
		MediaType: "application/vnd.cncf.flux.content.v1.tar+gzip",
		Digest:    digestOf(layerData),
		Size:      int64(len(layerData)),
		Annotations: map[string]string{
			ocispec.AnnotationTitle: "bundle.tar.gz",
		},
	}
	if err := store.Push(ctx, layerDesc, bytes.NewReader(layerData)); err != nil {
		return "", fmt.Errorf("push layer to memory store: %w", err)
	}

	// Build config.
	configData := []byte("{}")
	configDesc := ocispec.Descriptor{
		MediaType: "application/vnd.cncf.flux.config.v1+json",
		Digest:    digestOf(configData),
		Size:      int64(len(configData)),
	}
	if err := store.Push(ctx, configDesc, bytes.NewReader(configData)); err != nil {
		return "", fmt.Errorf("push config to memory store: %w", err)
	}

	// Build manifest.
	manifest := ocispec.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    configDesc,
		Layers:    []ocispec.Descriptor{layerDesc},
		Annotations: map[string]string{
			"org.opencontainers.image.source":   "kapro://" + req.KaproName,
			"org.opencontainers.image.revision": req.Version,
			"org.opencontainers.image.created":  time.Now().UTC().Format(time.RFC3339),
		},
	}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		return "", fmt.Errorf("marshal manifest: %w", err)
	}
	manifestDesc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageManifest,
		Digest:    digestOf(manifestData),
		Size:      int64(len(manifestData)),
	}
	if err := store.Push(ctx, manifestDesc, bytes.NewReader(manifestData)); err != nil {
		return "", fmt.Errorf("push manifest to memory store: %w", err)
	}
	if err := store.Tag(ctx, manifestDesc, tag); err != nil {
		return "", fmt.Errorf("tag manifest: %w", err)
	}

	// Connect to remote registry with GCP auth.
	repo, err := remote.NewRepository(repoRef)
	if err != nil {
		return "", fmt.Errorf("create remote repository %s: %w", repoRef, err)
	}

	// GCP auth: use WI/ADC token as password with "oauth2accesstoken" as username.
	token, err := provider.GetAccessToken(ctx)
	if err != nil {
		return "", fmt.Errorf("get GCP access token: %w", err)
	}
	repo.Client = &auth.Client{
		Credential: auth.StaticCredential(registryHost(registryURL), auth.Credential{
			Username: "oauth2accesstoken",
			Password: token,
		}),
	}

	// Copy from memory store to remote.
	if _, err := oras.Copy(ctx, store, tag, repo, tag, oras.DefaultCopyOptions); err != nil {
		return "", fmt.Errorf("push to %s:%s: %w", repoRef, tag, err)
	}

	return fmt.Sprintf("oci://%s:%s", repoRef, tag), nil
}

// registryHost extracts the host from a registry URL.
// "europe-west1-docker.pkg.dev/project/repo" → "europe-west1-docker.pkg.dev"
func registryHost(url string) string {
	parts := strings.SplitN(url, "/", 2)
	return parts[0]
}

// tarGzDir creates a tar.gz archive of a directory.
func tarGzDir(dir string) ([]byte, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = rel

		if err := tw.WriteHeader(header); err != nil {
			return err
		}

		if d.IsDir() {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, err = tw.Write(data)
		return err
	})
	if err != nil {
		return nil, err
	}

	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func digestOf(data []byte) digest.Digest {
	h := sha256.Sum256(data)
	return digest.NewDigestFromBytes(digest.SHA256, h[:])
}

// Generate produces the spoke manifest files as a map of relative path → YAML content.
// Files are organized into per-wave directories (wave-00/, wave-01/, ...).
// Wave 0 also contains shared HelmRepositories.
func Generate(req BundleRequest) map[string]string {
	source := req.Source
	defaults := source.Spec.Defaults
	if defaults == nil {
		defaults = &kaprov1alpha2.SourceDefaults{}
	}

	manifests := map[string]string{}
	waves := groupByWave(source.Spec.Units)
	firstWave := sortedWaveNumbers(waves)[0]

	// HelmRepositories go into the first wave directory.
	for _, reg := range source.Spec.Registries {
		path := fmt.Sprintf("wave-%02d/helmrepository-%s.yaml", firstWave, reg.Name)
		manifests[path] = buildHelmRepository(req.KaproName, reg)
	}

	// HelmReleases go into their wave directory.
	for _, comp := range source.Spec.Units {
		path := fmt.Sprintf("wave-%02d/%s-hr.yaml", comp.Wave, comp.Name)
		manifests[path] = buildSpokeHelmRelease(req.KaproName, defaults, comp)
	}

	return manifests
}

// WaveKustomizations generates the wave Kustomization CRs (bootstrap resources).
// These are NOT part of the OCI bundle — they're created once on the spoke by
// the hub's ResourceSet. Each Kustomization points to its wave directory in the
// bundle and has dependsOn to the previous wave.
func WaveKustomizations(kaproName string, app *kaprov1alpha2.Source) []map[string]any {
	waves := groupByWave(app.Spec.Units)
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

func buildHelmRepository(kaproName string, reg kaprov1alpha2.SourceRegistry) string {
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

func buildSpokeHelmRelease(kaproName string, defaults *kaprov1alpha2.SourceDefaults, comp kaprov1alpha2.Unit) string {
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
		"targetNamespace":  targetNS,
		"promotionrunName": comp.Name,
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

func groupByWave(units []kaprov1alpha2.Unit) map[int32][]kaprov1alpha2.Unit {
	waves := map[int32][]kaprov1alpha2.Unit{}
	for _, comp := range units {
		waves[comp.Wave] = append(waves[comp.Wave], comp)
	}
	return waves
}

func sortedWaveNumbers(waves map[int32][]kaprov1alpha2.Unit) []int32 {
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
