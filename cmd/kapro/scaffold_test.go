package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRunInitScaffoldArgo(t *testing.T) {
	dir := t.TempDir()
	err := runInitScaffold(scaffoldOptions{
		Path:              dir,
		Name:              "checkout",
		Substrate:         "argo",
		Mode:              "push",
		Registry:          "oci://registry.example.com/platform",
		UseSubstrateClass: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, relPath := range []string{
		"substrates/argo.yaml",
		"deliveryunits/checkout.yaml",
		"plans/checkout.yaml",
		"fleets/checkout.yaml",
		"argo/applications/checkout.yaml",
		"apps/checkout/00-namespace.yaml",
		"apps/checkout/deployment.yaml",
		"apps/checkout/service.yaml",
	} {
		if _, err := os.Stat(filepath.Join(dir, relPath)); err != nil {
			t.Fatalf("%s not generated: %v", relPath, err)
		}
	}
	content := readFile(t, filepath.Join(dir, "substrates/argo.yaml"))
	if !strings.Contains(content, "kind: ArgoCDSubstrateConfig") || !strings.Contains(content, "classRef:") {
		t.Fatalf("substrate file missing argo class/config:\n%s", content)
	}
	unit := readFile(t, filepath.Join(dir, "deliveryunits/checkout.yaml"))
	for _, want := range []string{
		"source:",
		"defaultFleetRef: checkout",
		"defaultPlanRef: checkout",
		"substrateRef: argo",
		"kapro.io/team: platform",
		"name: checkout",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("delivery unit file missing %q:\n%s", want, unit)
		}
	}
	kapro := readFile(t, filepath.Join(dir, "fleets/checkout.yaml"))
	for _, want := range []string{
		"substrateRef: argo",
		"kapro.io/team: platform",
		"kapro.io/stage: canary",
		"kapro.io/stage: production",
	} {
		if !strings.Contains(kapro, want) {
			t.Fatalf("fleet file missing %q:\n%s", want, kapro)
		}
	}
	if strings.Contains(kapro, "source:") || strings.Contains(kapro, "sourceRef:") {
		t.Fatalf("fleet scaffold should not own source intent, got:\n%s", kapro)
	}
	if _, err := os.Stat(filepath.Join(dir, "sources/checkout.yaml")); !os.IsNotExist(err) {
		t.Fatalf("sources/checkout.yaml should not be generated because DeliveryUnit owns source intent")
	}
	deployment := readFile(t, filepath.Join(dir, "apps/checkout/deployment.yaml"))
	if !strings.Contains(deployment, "namespace: checkout") || strings.Contains(deployment, "namespace: argocd") {
		t.Fatalf("argo starter workload should use app namespace checkout, got:\n%s", deployment)
	}
	service := readFile(t, filepath.Join(dir, "apps/checkout/service.yaml"))
	if !strings.Contains(service, "namespace: checkout") || strings.Contains(service, "namespace: argocd") {
		t.Fatalf("argo starter service should use app namespace checkout, got:\n%s", service)
	}
	app := readFile(t, filepath.Join(dir, "argo/applications/checkout.yaml"))
	for _, want := range []string{
		"namespace: argocd",
		"namespace: checkout",
		"path: apps/checkout",
	} {
		if !strings.Contains(app, want) {
			t.Fatalf("argo application missing %q:\n%s", want, app)
		}
	}
}

func TestRunInitScaffoldFluxWritesCompleteApp(t *testing.T) {
	dir := t.TempDir()
	err := runInitScaffold(scaffoldOptions{
		Path:              dir,
		Name:              "checkout",
		Substrate:         "flux",
		Mode:              "pull",
		Registry:          "oci://registry.example.com/platform",
		UseSubstrateClass: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, relPath := range []string{
		"substrates/flux.yaml",
		"plans/checkout.yaml",
		"fleets/checkout.yaml",
		"flux/kustomizations/checkout.yaml",
		"apps/checkout/00-namespace.yaml",
		"apps/checkout/deployment.yaml",
		"apps/checkout/service.yaml",
		"apps/checkout/kustomization.yaml",
	} {
		if _, err := os.Stat(filepath.Join(dir, relPath)); err != nil {
			t.Fatalf("%s not generated: %v", relPath, err)
		}
	}
	deployment := readFile(t, filepath.Join(dir, "apps/checkout/deployment.yaml"))
	if !strings.Contains(deployment, "namespace: checkout") || strings.Contains(deployment, "namespace: flux-system") {
		t.Fatalf("flux starter workload should use app namespace checkout, got:\n%s", deployment)
	}
	service := readFile(t, filepath.Join(dir, "apps/checkout/service.yaml"))
	if !strings.Contains(service, "namespace: checkout") || strings.Contains(service, "namespace: flux-system") {
		t.Fatalf("flux starter service should use app namespace checkout, got:\n%s", service)
	}
	kustomization := readFile(t, filepath.Join(dir, "apps/checkout/kustomization.yaml"))
	for _, want := range []string{
		"  - 00-namespace.yaml",
		"  - deployment.yaml",
		"  - service.yaml",
	} {
		if !strings.Contains(kustomization, want) {
			t.Fatalf("flux app kustomization missing %q:\n%s", want, kustomization)
		}
	}
	native := readFile(t, filepath.Join(dir, "flux/kustomizations/checkout.yaml"))
	for _, want := range []string{
		"namespace: flux-system",
		"path: ./apps/checkout",
		"name: checkout",
	} {
		if !strings.Contains(native, want) {
			t.Fatalf("flux native kustomization missing %q:\n%s", want, native)
		}
	}
}

func TestRunInitScaffoldRepoOnly(t *testing.T) {
	dir := t.TempDir()
	err := runInitScaffold(scaffoldOptions{
		Path:              dir,
		Name:              "checkout",
		Substrate:         "argo",
		Mode:              "push",
		Registry:          "oci://registry.example.com/platform",
		Clusters:          "none",
		UseSubstrateClass: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, relPath := range []string{
		"substrates/argo.yaml",
		"deliveryunits/checkout.yaml",
		"plans/checkout.yaml",
		"argo/applications/checkout.yaml",
	} {
		if _, err := os.Stat(filepath.Join(dir, relPath)); err != nil {
			t.Fatalf("%s not generated: %v", relPath, err)
		}
	}
	for _, relPath := range []string{
		"clusters/canary.yaml",
		"fleets/checkout.yaml",
		"promotions/checkout-promotion.yaml",
	} {
		if _, err := os.Stat(filepath.Join(dir, relPath)); !os.IsNotExist(err) {
			t.Fatalf("%s should not be generated before clusters exist", relPath)
		}
	}
}

func TestRunInitScaffoldOCIPull(t *testing.T) {
	dir := t.TempDir()
	err := runInitScaffold(scaffoldOptions{
		Path:              dir,
		Name:              "checkout",
		Substrate:         "oci",
		Mode:              "pull",
		Registry:          "oci://registry.example.com/platform",
		UseSubstrateClass: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, relPath := range []string{
		"substrates/oci.yaml",
		"plans/checkout.yaml",
		"clusters/canary-eu.yaml",
		"clusters/prod-eu.yaml",
		"fleets/checkout.yaml",
		"promotions/checkout-promotion.yaml",
	} {
		if _, err := os.Stat(filepath.Join(dir, relPath)); err != nil {
			t.Fatalf("%s not generated: %v", relPath, err)
		}
	}
	for _, relPath := range []string{
		"argo/applications/checkout.yaml",
		"flux/kustomizations/checkout.yaml",
	} {
		if _, err := os.Stat(filepath.Join(dir, relPath)); !os.IsNotExist(err) {
			t.Fatalf("%s should not be generated for oci substrate", relPath)
		}
	}
	substrate := readFile(t, filepath.Join(dir, "substrates/oci.yaml"))
	for _, want := range []string{
		"kind: OCIBundleApplyConfig",
		"classRef:",
		"mode: spoke-pull",
		"repository: registry.example.com/platform/{appKey}",
		"tag: \"{version}\"",
	} {
		if !strings.Contains(substrate, want) {
			t.Fatalf("substrate file missing %q:\n%s", want, substrate)
		}
	}
	cluster := readFile(t, filepath.Join(dir, "clusters/canary-eu.yaml"))
	for _, want := range []string{
		"name: canary-eu",
		"kapro.io/stage: canary",
		"mode: pull",
		"substrateRef: oci",
		"namespace: kapro-system",
	} {
		if !strings.Contains(cluster, want) {
			t.Fatalf("cluster file missing %q:\n%s", want, cluster)
		}
	}
	promotion := readFile(t, filepath.Join(dir, "promotions/checkout-promotion.yaml"))
	for _, want := range []string{
		"kind: Promotion",
		"kapro.io/team: platform",
		"fleetRef: checkout",
		"version: 0.1.0",
		"timeout: 30m",
	} {
		if !strings.Contains(promotion, want) {
			t.Fatalf("promotion file missing %q:\n%s", want, promotion)
		}
	}
}

func TestRunInitScaffoldPublicPreviewContractForAllProfiles(t *testing.T) {
	for _, tc := range []struct {
		name      string
		substrate string
		mode      string
	}{
		{name: "direct", substrate: "direct", mode: "push"},
		{name: "argo", substrate: "argo", mode: "push"},
		{name: "flux", substrate: "flux", mode: "pull"},
		{name: "oci", substrate: "oci", mode: "pull"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := runInitScaffold(scaffoldOptions{
				Path:      dir,
				Name:      "checkout",
				Substrate: tc.substrate,
				Mode:      tc.mode,
				Registry:  "oci://registry.example.com/platform",
			}); err != nil {
				t.Fatal(err)
			}

			for _, relPath := range []string{
				"deliveryunits/checkout.yaml",
				"plans/checkout.yaml",
				"fleets/checkout.yaml",
				"promotions/checkout-promotion.yaml",
			} {
				if _, err := os.Stat(filepath.Join(dir, relPath)); err != nil {
					t.Fatalf("%s not generated: %v", relPath, err)
				}
			}
			if _, err := os.Stat(filepath.Join(dir, "sources", "checkout.yaml")); !os.IsNotExist(err) {
				t.Fatalf("legacy sources/checkout.yaml should not be generated for public-preview scaffold")
			}

			unit := readFile(t, filepath.Join(dir, "deliveryunits/checkout.yaml"))
			for _, want := range []string{
				"kind: DeliveryUnit",
				"defaultFleetRef: checkout",
				"defaultPlanRef: checkout",
				"source:",
				"substrateRef: " + tc.substrate,
				"kapro.io/team: platform",
			} {
				if !strings.Contains(unit, want) {
					t.Fatalf("delivery unit missing %q:\n%s", want, unit)
				}
			}

			fleet := readFile(t, filepath.Join(dir, "fleets/checkout.yaml"))
			if strings.Contains(fleet, "sourceRef:") || strings.Contains(fleet, "\n  source:") {
				t.Fatalf("fleet should not own source intent in public-preview scaffold:\n%s", fleet)
			}

			promotion := readFile(t, filepath.Join(dir, "promotions/checkout-promotion.yaml"))
			for _, want := range []string{
				"kind: Promotion",
				"deliveryUnitRef: checkout",
				"fleetRef: checkout",
				"planRef: checkout",
				"version:",
				"timeout: 30m",
			} {
				if !strings.Contains(promotion, want) {
					t.Fatalf("promotion missing %q:\n%s", want, promotion)
				}
			}
		})
	}
}

func TestRunInitScaffoldOCIRejectsPushMode(t *testing.T) {
	err := runInitScaffold(scaffoldOptions{
		Path:      t.TempDir(),
		Name:      "checkout",
		Substrate: "oci",
		Mode:      "push",
		Registry:  "oci://registry.example.com/platform",
	})
	if err == nil || !strings.Contains(err.Error(), "--substrate oci requires --mode pull") {
		t.Fatalf("err=%v, want oci pull-mode error", err)
	}
}

func TestRunInitScaffoldRejectsUnsafeName(t *testing.T) {
	err := runInitScaffold(scaffoldOptions{
		Path:      t.TempDir(),
		Name:      "../../outside",
		Substrate: "direct",
		Mode:      "push",
		Registry:  "oci://registry.example.com/platform",
	})
	if err == nil || !strings.Contains(err.Error(), "--name must match") {
		t.Fatalf("err=%v, want unsafe name validation error", err)
	}

	err = runInitScaffold(scaffoldOptions{
		Path:      t.TempDir(),
		Name:      "checkout-",
		Substrate: "direct",
		Mode:      "push",
		Registry:  "oci://registry.example.com/platform",
	})
	if err == nil || !strings.Contains(err.Error(), "--name must match") {
		t.Fatalf("err=%v, want trailing hyphen validation error", err)
	}
}

func TestRunInitScaffoldRejectsUnsafeClusterName(t *testing.T) {
	err := runInitScaffold(scaffoldOptions{
		Path:      t.TempDir(),
		Name:      "checkout",
		Substrate: "direct",
		Mode:      "push",
		Registry:  "oci://registry.example.com/platform",
		Clusters:  "canary/../../outside:canary",
	})
	if err == nil || !strings.Contains(err.Error(), "--clusters name must match") {
		t.Fatalf("err=%v, want unsafe cluster validation error", err)
	}

	err = runInitScaffold(scaffoldOptions{
		Path:      t.TempDir(),
		Name:      "checkout",
		Substrate: "direct",
		Mode:      "push",
		Registry:  "oci://registry.example.com/platform",
		Clusters:  "canary-:canary",
	})
	if err == nil || !strings.Contains(err.Error(), "--clusters name must match") {
		t.Fatalf("err=%v, want trailing hyphen cluster validation error", err)
	}
}

func TestRunInitScaffoldRejectsEmptyClusterList(t *testing.T) {
	err := runInitScaffold(scaffoldOptions{
		Path:      t.TempDir(),
		Name:      "checkout",
		Substrate: "direct",
		Mode:      "push",
		Registry:  "oci://registry.example.com/platform",
		Clusters:  ",",
	})
	if err == nil || !strings.Contains(err.Error(), "--clusters must be name:stage pairs") {
		t.Fatalf("err=%v, want empty cluster list validation error", err)
	}
}

func TestWriteScaffoldFilesRejectsEscapedPath(t *testing.T) {
	err := writeScaffoldFiles(t.TempDir(), map[string]string{
		filepath.Join("apps", "..", "..", "outside.yaml"): "pwn",
	}, false)
	if err == nil || !strings.Contains(err.Error(), "outside scaffold root") {
		t.Fatalf("err=%v, want path escape validation error", err)
	}
}

func TestWriteScaffoldFilesRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "apps")); err != nil {
		t.Fatal(err)
	}
	err := writeScaffoldFiles(root, map[string]string{
		filepath.Join("apps", "checkout", "deployment.yaml"): "pwn",
	}, false)
	if err == nil || !strings.Contains(err.Error(), "through symlink outside scaffold root") {
		t.Fatalf("err=%v, want symlink escape validation error", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "checkout", "deployment.yaml")); !os.IsNotExist(err) {
		t.Fatalf("outside file should not be written, stat err=%v", err)
	}
}

func TestWriteScaffoldFilesRejectsFinalSymlinkWithForce(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "deployment.yaml")
	if err := os.WriteFile(outside, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	appDir := filepath.Join(root, "apps", "checkout")
	if err := os.MkdirAll(appDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(appDir, "deployment.yaml")); err != nil {
		t.Fatal(err)
	}

	err := writeScaffoldFiles(root, map[string]string{
		filepath.Join("apps", "checkout", "deployment.yaml"): "pwn",
	}, true)
	if err == nil || !strings.Contains(err.Error(), "through symlink scaffold path") {
		t.Fatalf("err=%v, want final symlink validation error", err)
	}
	if got := readFile(t, outside); got != "original" {
		t.Fatalf("outside file should not be overwritten, got %q", got)
	}
}

func TestRunConnectScaffoldFlux(t *testing.T) {
	dir := t.TempDir()
	err := runConnectScaffold(connectOptions{
		Path:      dir,
		Name:      "flux",
		Substrate: "flux",
		Namespace: "flux-system",
		Selector:  "kapro.io/import=true,team=checkout",
	})
	if err != nil {
		t.Fatal(err)
	}
	content := readFile(t, filepath.Join(dir, "substrates/flux-observe.yaml"))
	for _, want := range []string{
		"kind: flux",
		"actuator: flux",
		"mode: hub-push",
		"managementPolicy: Observe",
		"kapro.io/import: \"true\"",
		"team: \"checkout\"",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("missing %q in:\n%s", want, content)
		}
	}
}

func TestParsePromotionRunVersionsRejectsDuplicateUnits(t *testing.T) {
	if _, err := parsePromotionRunVersions([]string{"api=v1", "api=v2"}); err == nil {
		t.Fatal("expected duplicate unit error")
	}
}

func TestDefaultPromotionRunNameIsDNSLabel(t *testing.T) {
	got := defaultPromotionRunName("Checkout.API", "v1.2.3+build.4", nil)
	if got != "checkout-api-v1-2-3-build-4" {
		t.Fatalf("defaultPromotionRunName()=%q", got)
	}
}

func TestDefaultPromotionRunNameAddsHashWhenTruncated(t *testing.T) {
	first := defaultPromotionRunName("checkout", "sha256:"+strings.Repeat("a", 80), nil)
	second := defaultPromotionRunName("checkout", "sha256:"+strings.Repeat("a", 79)+"b", nil)
	if len(first) > 63 || len(second) > 63 {
		t.Fatalf("names exceed DNS label length: %q %q", first, second)
	}
	if first == second {
		t.Fatalf("long versions should keep unique hashed names, got %q", first)
	}
}

func TestDNSLabelWithSuffixStaysWithinLabelLimit(t *testing.T) {
	base := dnsLabel(strings.Repeat("a", 70))
	got := dnsLabelWithSuffix(base, "2")
	if len(got) > 63 {
		t.Fatalf("dnsLabelWithSuffix length=%d, want <=63: %q", len(got), got)
	}
	if !strings.HasSuffix(got, "-2") {
		t.Fatalf("dnsLabelWithSuffix()=%q, want suffixed name", got)
	}
}

func TestUniquePlanRefNameUsesPlanDefaultForInvalidInput(t *testing.T) {
	used := map[string]struct{}{}
	got := uniquePlanRefName("@@@", used)
	if got != "plan" {
		t.Fatalf("uniquePlanRefName()=%q, want plan", got)
	}
}

func TestUniquePlanRefNameAvoidsFinalNameCollisions(t *testing.T) {
	used := map[string]struct{}{}
	got := []string{
		uniquePlanRefName("foo-2", used),
		uniquePlanRefName("foo", used),
		uniquePlanRefName("foo", used),
	}
	want := []string{"foo-2", "foo", "foo-3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("uniquePlanRefName()=%v, want %v", got, want)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
