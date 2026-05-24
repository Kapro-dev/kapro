package main

import (
	"context"
	"os"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	"kapro.io/kapro/internal/cli"
)

type migrateBackendOptions struct {
	Kubeconfig string
}

func newMigrateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Render migrated Kapro manifests",
	}
	cmd.AddCommand(newMigrateBackendCmd())
	return cmd
}

func newMigrateBackendCmd() *cobra.Command {
	opts := migrateBackendOptions{}
	cmd := &cobra.Command{
		Use:   "backend NAME",
		Short: "Render a Backend using substrate/execution fields",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMigrateBackend(cmd.Context(), opts, args[0])
		},
	}
	cmd.Flags().StringVar(&opts.Kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	return cmd
}

func runMigrateBackend(ctx context.Context, opts migrateBackendOptions, name string) error {
	c, err := buildClient(opts.Kubeconfig)
	if err != nil {
		return err
	}
	var backend kaprov1alpha2.Backend
	if err := c.Get(ctx, client.ObjectKey{Name: name}, &backend); err != nil {
		return err
	}
	migrated := migrateBackendObject(&backend)
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

func migrateBackendObject(in *kaprov1alpha2.Backend) *kaprov1alpha2.Backend {
	out := in.DeepCopy()
	out.TypeMeta = metav1.TypeMeta{APIVersion: "kapro.io/v1alpha2", Kind: "Backend"}
	out.ObjectMeta = metav1.ObjectMeta{
		Name:        in.Name,
		Labels:      cloneStringMap(in.Labels),
		Annotations: cloneStringMap(in.Annotations),
	}
	out.Spec.Substrate = in.Spec.CanonicalSubstrate()
	out.Spec.Execution = in.Spec.CanonicalExecution()
	out.Spec.Driver = ""
	out.Spec.Adapter = ""
	out.Spec.Runtime = ""
	out.Status = kaprov1alpha2.BackendStatus{}
	return out
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
