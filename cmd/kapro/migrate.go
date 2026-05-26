package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
	"kapro.io/kapro/internal/cli"
)

type migrateSubstrateOptions struct {
	Kubeconfig string
}

type migrateV06ToV062Options struct {
	Write bool
}

func newMigrateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Render migrated Kapro manifests",
	}
	cmd.AddCommand(newMigrateSubstrateCmd())
	cmd.AddCommand(newMigrateV06Cmd())
	return cmd
}

func newMigrateSubstrateCmd() *cobra.Command {
	opts := migrateSubstrateOptions{}
	cmd := &cobra.Command{
		Use:   "substrate NAME",
		Short: "Render a Substrate using substrate/execution fields",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMigrateSubstrate(cmd.Context(), opts, args[0])
		},
	}
	cmd.Flags().StringVar(&opts.Kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	return cmd
}

func newMigrateV06Cmd() *cobra.Command {
	opts := migrateV06ToV062Options{}
	cmd := &cobra.Command{
		Use:   "v0.6 v0.6.2 [file-or-dir ...]",
		Short: "Rewrite v0.6 public-preview manifests to the v0.6.2 YAML shape",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 || args[0] != "v0.6.2" {
				return fmt.Errorf("usage: kapro migrate v0.6 v0.6.2 [file-or-dir ...]")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMigrateV06ToV062(opts, args[1:])
		},
	}
	cmd.Flags().BoolVar(&opts.Write, "write", false, "Rewrite files in place; required for directories and multiple paths")
	return cmd
}

func runMigrateSubstrate(ctx context.Context, opts migrateSubstrateOptions, name string) error {
	c, err := buildClient(opts.Kubeconfig)
	if err != nil {
		return err
	}
	var substrate kaprov1alpha1.Substrate
	if err := c.Get(ctx, client.ObjectKey{Name: name}, &substrate); err != nil {
		return err
	}
	migrated := migrateSubstrateObject(&substrate)
	if cli.IsJSON() {
		return cli.JSON(migrated)
	}
	body, err := yaml.Marshal(migrated)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(body)
	return err
}

func migrateSubstrateObject(in *kaprov1alpha1.Substrate) *kaprov1alpha1.Substrate {
	out := in.DeepCopy()
	out.TypeMeta = metav1.TypeMeta{APIVersion: "kapro.io/v1alpha1", Kind: "Substrate"}
	out.ObjectMeta = metav1.ObjectMeta{
		Name:        in.Name,
		Labels:      cloneStringMap(in.Labels),
		Annotations: cloneStringMap(in.Annotations),
	}
	if kind := in.Spec.SubstrateKind(); kind != "" {
		out.Spec.ClassRef = &kaprov1alpha1.SubstrateClassReference{Name: kind}
	}
	out.Spec.Execution = in.Spec.CanonicalExecution()
	out.Status = kaprov1alpha1.SubstrateStatus{}
	return out
}

func runMigrateV06ToV062(opts migrateV06ToV062Options, paths []string) error {
	if len(paths) == 0 {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		out, _, err := migrateV06ToV062Manifest(data)
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(out)
		return err
	}
	if !opts.Write && len(paths) > 1 {
		return fmt.Errorf("multiple paths require --write; pass one file to preview on stdout")
	}

	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if info.IsDir() {
			if !opts.Write {
				return fmt.Errorf("directory migration requires --write: %s", path)
			}
			if err := migrateV06ToV062Dir(path); err != nil {
				return err
			}
			continue
		}
		if err := migrateV06ToV062File(path, opts.Write); err != nil {
			return err
		}
	}
	return nil
}

func migrateV06ToV062Dir(root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".context", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !isYAMLPath(path) {
			return nil
		}
		return migrateV06ToV062File(path, true)
	})
}

func migrateV06ToV062File(path string, write bool) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	out, changed, err := migrateV06ToV062Manifest(data)
	if err != nil {
		return fmt.Errorf("migrate %s: %w", path, err)
	}
	if !write {
		_, err = os.Stdout.Write(out)
		return err
	}
	if !changed {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, info.Mode())
}

func migrateV06ToV062Manifest(data []byte) ([]byte, bool, error) {
	parts := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n---")
	out := make([]string, 0, len(parts))
	changed := false
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			out = append(out, part)
			continue
		}
		var obj map[string]any
		if err := yaml.Unmarshal([]byte(part), &obj); err != nil {
			return nil, false, err
		}
		if len(obj) == 0 || !isKaproAPIVersion(stringField(obj, "apiVersion")) {
			out = append(out, part)
			continue
		}
		if migrateV06ToV062Object(obj) {
			rendered, err := yaml.Marshal(obj)
			if err != nil {
				return nil, false, err
			}
			out = append(out, strings.TrimSuffix(string(rendered), "\n"))
			changed = true
			continue
		}
		out = append(out, part)
	}
	rendered := []byte(strings.Join(out, "\n---"))
	if len(rendered) > 0 && !bytes.HasSuffix(rendered, []byte("\n")) {
		rendered = append(rendered, '\n')
	}
	return rendered, changed, nil
}

func migrateV06ToV062Object(obj map[string]any) bool {
	spec, ok := objectField(obj, "spec")
	if !ok {
		return false
	}
	changed := false
	switch stringField(obj, "kind") {
	case "Cluster":
		changed = migrateClusterSpec(spec) || changed
	case "Fleet":
		changed = migrateClusterSpec(spec) || changed
	case "ClusterTemplate":
		changed = renameField(spec, "suspend", "suspended") || changed
		if template, ok := objectField(spec, "template"); ok {
			if templateSpec, ok := objectField(template, "spec"); ok {
				changed = migrateClusterSpec(templateSpec) || changed
			}
		}
	case "DeliveryUnit":
		changed = renameField(spec, "defaultFleetRef", "defaultFleet") || changed
		changed = renameField(spec, "defaultPlanRef", "defaultPlan") || changed
		changed = renameField(spec, "suspend", "suspended") || changed
		if source, ok := objectField(spec, "source"); ok {
			changed = migrateSourceSpec(source) || changed
		}
		for _, trigger := range objectSlice(spec["triggers"]) {
			changed = renameField(trigger, "fleetRef", "fleet") || changed
			changed = renameField(trigger, "planRef", "plan") || changed
			changed = renameField(trigger, "suspend", "suspended") || changed
		}
	case "Promotion", "PromotionRun":
		changed = migratePromotionLikeSpec(spec) || changed
	case "Trigger":
		changed = renameField(spec, "suspend", "suspended") || changed
		if tmpl, ok := objectField(spec, "promotionTemplate"); ok {
			changed = migratePromotionLikeSpec(tmpl) || changed
		}
	case "Source":
		changed = migrateSourceSpec(spec) || changed
	case "Substrate":
		changed = migrateSubstrateSpec(spec) || changed
	case "SubstrateDiscoveryPolicy":
		changed = renameField(spec, "substrateRef", "substrate") || changed
	case "Policy":
		changed = renameField(spec, "suspend", "suspended") || changed
	}
	return changed
}

func migrateClusterSpec(spec map[string]any) bool {
	changed := false
	changed = renameField(spec, "substrate", "delivery") || changed
	if delivery, ok := objectField(spec, "delivery"); ok {
		changed = renameField(delivery, "substrateRef", "ref") || changed
		changed = renameField(delivery, "suspend", "suspended") || changed
	}
	changed = renameField(spec, "suspend", "suspended") || changed
	return changed
}

func migratePromotionLikeSpec(spec map[string]any) bool {
	changed := false
	changed = renameField(spec, "deliveryUnitRef", "unit") || changed
	changed = renameField(spec, "fleetRef", "fleet") || changed
	changed = renameField(spec, "planRef", "plan") || changed
	changed = renameField(spec, "suspend", "suspended") || changed
	return changed
}

func migrateSourceSpec(spec map[string]any) bool {
	changed := renameField(spec, "substrateRef", "substrate")
	for _, unit := range objectSlice(spec["units"]) {
		changed = renameField(unit, "suspend", "suspended") || changed
	}
	return changed
}

func migrateSubstrateSpec(spec map[string]any) bool {
	changed := false
	if discovery, ok := objectField(spec, "discovery"); ok {
		if enabled, ok := boolField(discovery, "enabled"); ok {
			if _, exists := discovery["suspended"]; !exists {
				discovery["suspended"] = !enabled
			}
			delete(discovery, "enabled")
			changed = true
		}
		changed = renameField(discovery, "suspend", "suspended") || changed
	}
	if substrate, ok := objectField(spec, "substrate"); ok {
		if _, exists := spec["classRef"]; !exists {
			if kind := stringField(substrate, "kind"); kind != "" {
				spec["classRef"] = map[string]any{"name": kind}
			}
		}
		delete(spec, "substrate")
		changed = true
	}
	return changed
}

func renameField(obj map[string]any, oldName, newName string) bool {
	value, ok := obj[oldName]
	if !ok {
		return false
	}
	if _, exists := obj[newName]; !exists {
		obj[newName] = value
	}
	delete(obj, oldName)
	return true
}

func objectField(obj map[string]any, name string) (map[string]any, bool) {
	value, ok := obj[name]
	if !ok {
		return nil, false
	}
	nested, ok := value.(map[string]any)
	return nested, ok
}

func objectSlice(value any) []map[string]any {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if obj, ok := item.(map[string]any); ok {
			out = append(out, obj)
		}
	}
	return out
}

func stringField(obj map[string]any, name string) string {
	value, _ := obj[name].(string)
	return value
}

func boolField(obj map[string]any, name string) (bool, bool) {
	value, ok := obj[name].(bool)
	return value, ok
}

func isKaproAPIVersion(apiVersion string) bool {
	return strings.HasPrefix(apiVersion, "kapro.io/") || strings.HasPrefix(apiVersion, "runtime.kapro.io/")
}

func isYAMLPath(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".yaml" || ext == ".yml"
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
