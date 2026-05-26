package deliveryunit_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/yaml"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
	"kapro.io/kapro/internal/webhook/admission"
)

func TestDeliveryUnitExamplesAreOrdered(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	for _, entry := range entries {
		if entry.IsDir() && len(entry.Name()) >= 3 && entry.Name()[2] == '-' {
			dirs = append(dirs, entry.Name())
		}
	}
	if len(dirs) < 6 {
		t.Fatalf("expected indexed example folders, got %v", dirs)
	}
	if !sort.StringsAreSorted(dirs) {
		t.Fatalf("example folders should be lexically ordered by index: %v", dirs)
	}
	if dirs[0] != "00-hello-world" {
		t.Fatalf("first example should be 00-hello-world, got %q", dirs[0])
	}
	seen := map[string]struct{}{}
	for i, dir := range dirs {
		index := dir[:2]
		if want := fmt.Sprintf("%02d", i); index != want {
			t.Fatalf("example folder %q has index %q, want contiguous index %q", dir, index, want)
		}
		if _, ok := seen[index]; ok {
			t.Fatalf("duplicate example index %q in %v", index, dirs)
		}
		seen[index] = struct{}{}
		if errs := validation.IsDNS1123Label(dir); len(errs) > 0 {
			t.Fatalf("example folder %q should be a DNS label: %s", dir, strings.Join(errs, "; "))
		}
	}
}

func TestDeliveryUnitExamplesValidate(t *testing.T) {
	manifests := readExampleManifests(t)
	deliveryUnits := map[string]struct{}{}

	for _, manifest := range manifests {
		switch manifest.Kind {
		case "DeliveryUnit":
			var unit kaprov1alpha1.DeliveryUnit
			unmarshalExample(t, manifest, &unit)
			validateDeliveryUnitExample(t, manifest.Path, &unit)
			deliveryUnits[unit.Name] = struct{}{}
		case "Promotion":
			var promotion kaprov1alpha1.Promotion
			unmarshalExample(t, manifest, &promotion)
			validatePromotionExample(t, manifest.Path, &promotion)
		default:
			t.Fatalf("%s: unexpected kind %q", manifest.Path, manifest.Kind)
		}
	}

	for _, manifest := range manifests {
		if manifest.Kind != "Promotion" {
			continue
		}
		var promotion kaprov1alpha1.Promotion
		unmarshalExample(t, manifest, &promotion)
		if _, ok := deliveryUnits[promotion.Spec.DeliveryUnitRef]; !ok {
			t.Fatalf("%s: promotion references missing DeliveryUnit %q", manifest.Path, promotion.Spec.DeliveryUnitRef)
		}
	}
}

type exampleManifest struct {
	Path       string
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Raw        []byte
}

func readExampleManifests(t *testing.T) []exampleManifest {
	t.Helper()
	paths, err := filepath.Glob("[0-9][0-9]-*/*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		t.Fatal("no example manifests found")
	}

	var manifests []exampleManifest
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for i, raw := range bytes.Split(data, []byte("\n---")) {
			raw = bytes.TrimSpace(raw)
			if len(raw) == 0 {
				continue
			}
			manifest := exampleManifest{Path: path, Raw: raw}
			if len(bytes.Split(data, []byte("\n---"))) > 1 {
				manifest.Path = fmt.Sprintf("%s#%d", path, i)
			}
			if err := yaml.Unmarshal(raw, &manifest); err != nil {
				t.Fatalf("unmarshal metadata for %s: %v", manifest.Path, err)
			}
			manifests = append(manifests, manifest)
		}
	}
	return manifests
}

func unmarshalExample(t *testing.T, manifest exampleManifest, obj any) {
	t.Helper()
	if err := yaml.Unmarshal(manifest.Raw, obj); err != nil {
		t.Fatalf("unmarshal %s: %v", manifest.Path, err)
	}
}

func validateDeliveryUnitExample(t *testing.T, path string, unit *kaprov1alpha1.DeliveryUnit) {
	t.Helper()
	if unit.APIVersion != kaprov1alpha1.GroupVersion.String() || unit.Kind != "DeliveryUnit" {
		t.Fatalf("%s: type meta = %s/%s", path, unit.APIVersion, unit.Kind)
	}
	if err := admission.ValidateDeliveryUnit(unit); err != nil {
		t.Fatalf("%s: admission validation failed: %v", path, err)
	}
	if strings.TrimSpace(unit.Name) == "" {
		t.Fatalf("%s: metadata.name is required", path)
	}
	if unit.Labels[kaprov1alpha1.LabelUnit] != unit.Name {
		t.Fatalf("%s: %s label = %q, want %q", path, kaprov1alpha1.LabelUnit, unit.Labels[kaprov1alpha1.LabelUnit], unit.Name)
	}
	if len(unit.Spec.Source.Units) == 0 {
		t.Fatalf("%s: spec.source.units must not be empty", path)
	}
	seenUnits := map[string]struct{}{}
	for _, sourceUnit := range unit.Spec.Source.Units {
		if sourceUnit.Name != strings.TrimSpace(sourceUnit.Name) {
			t.Fatalf("%s: unit name %q has surrounding whitespace", path, sourceUnit.Name)
		}
		if _, ok := seenUnits[sourceUnit.Name]; ok {
			t.Fatalf("%s: duplicate unit name %q", path, sourceUnit.Name)
		}
		seenUnits[sourceUnit.Name] = struct{}{}
	}
	for _, trigger := range unit.Spec.Triggers {
		validatePublicPreviewTrigger(t, path, unit.Name, trigger)
	}
}

func validatePublicPreviewTrigger(t *testing.T, path, unitName string, trigger kaprov1alpha1.DeliveryUnitTrigger) {
	t.Helper()
	suffix := strings.TrimSpace(trigger.Name)
	if suffix == "" {
		suffix = "default"
	}
	if errs := validation.IsDNS1123Label(unitName + "-" + suffix); len(errs) > 0 {
		t.Fatalf("%s: trigger derives invalid name %q: %s", path, unitName+"-"+suffix, strings.Join(errs, "; "))
	}
	if trigger.Source.Type != "oci" || trigger.Source.OCI == nil {
		t.Fatalf("%s: public preview trigger examples should use OCI source, got %#v", path, trigger.Source)
	}
	if strings.TrimSpace(trigger.Source.OCI.Repository) == "" || strings.TrimSpace(trigger.Source.OCI.TagPattern) == "" {
		t.Fatalf("%s: OCI trigger requires repository and tagPattern", path)
	}
	for field, value := range map[string]string{
		"cooldown":                trigger.Cooldown,
		"source.oci.pollInterval": trigger.Source.OCI.PollInterval,
	} {
		if value == "" {
			continue
		}
		duration, err := time.ParseDuration(value)
		if err != nil || duration <= 0 {
			t.Fatalf("%s: %s = %q, want positive duration", path, field, value)
		}
	}
	if trigger.Suspended == nil || !*trigger.Suspended {
		t.Fatalf("%s: public trigger examples must start suspended", path)
	}
	if !trigger.DryRun {
		t.Fatalf("%s: public trigger examples must start dryRun", path)
	}
}

func validatePromotionExample(t *testing.T, path string, promotion *kaprov1alpha1.Promotion) {
	t.Helper()
	if promotion.APIVersion != kaprov1alpha1.GroupVersion.String() || promotion.Kind != "Promotion" {
		t.Fatalf("%s: type meta = %s/%s", path, promotion.APIVersion, promotion.Kind)
	}
	if strings.TrimSpace(promotion.Name) == "" {
		t.Fatalf("%s: metadata.name is required", path)
	}
	if promotion.Labels[kaprov1alpha1.LabelTeam] == "" {
		t.Fatalf("%s: %s label is required for public examples", path, kaprov1alpha1.LabelTeam)
	}
	if promotion.Spec.DeliveryUnitRef == "" || promotion.Spec.FleetRef == "" {
		t.Fatalf("%s: promotion must reference unit and fleet: %#v", path, promotion.Spec)
	}
	if promotion.Spec.Version == "" && len(promotion.Spec.Versions) == 0 {
		t.Fatalf("%s: promotion must set version or versions", path)
	}
	if _, err := time.ParseDuration(promotion.Spec.Timeout); promotion.Spec.Timeout != "" && err != nil {
		t.Fatalf("%s: invalid promotion timeout %q: %v", path, promotion.Spec.Timeout, err)
	}
}
