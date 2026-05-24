package main

import (
	"context"
	"os"

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

func newMigrateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Render migrated Kapro manifests",
	}
	cmd.AddCommand(newMigrateSubstrateCmd())
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
	out.Spec.Substrate = in.Spec.CanonicalSubstrate()
	out.Spec.Execution = in.Spec.CanonicalExecution()
	out.Status = kaprov1alpha1.SubstrateStatus{}
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
