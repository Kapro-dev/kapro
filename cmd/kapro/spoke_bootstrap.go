package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/internal/cli"
)

const (
	defaultBootstrapNamespace = "kapro-system"
	caFromHubKubeconfig       = "hub-kubeconfig"
	caFromFile                = "file"
	caFromInline              = "inline"
	caFromNone                = "none"
)

func newSpokeBootstrapCmd() *cobra.Command {
	var (
		kubeconfig  string
		namespace   string
		ttl         time.Duration
		hubURL      string
		caFrom      string
		caFile      string
		caInline    string
		secretOut   string
		waitTimeout time.Duration
		spokeNS     string
		secretName  string
	)
	cmd := &cobra.Command{
		Use:   "bootstrap <cluster-name>",
		Short: "Prepare hub-side registration for a pull-mode spoke (kapro-cluster-controller)",
		Long: `Creates (or patches) a FleetCluster on the hub with a bootstrap slot, waits
for the hub reconciler to mint a bootstrap kubeconfig Secret, and emits the
Helm values + Secret needed to install kapro-cluster-controller on the spoke.

Stdout receives the Helm values YAML (pipe-clean).
--secret-out receives the bootstrap-kubeconfig Secret YAML (apply on the spoke
before helm install).

Example:
  kapro spoke bootstrap de-prod-01 \
    --hub-url https://hub.example.com:6443 \
    --secret-out /tmp/de-prod-01-bootstrap-secret.yaml \
    > /tmp/de-prod-01-values.yaml

Then on the spoke:
  kubectl apply -f /tmp/de-prod-01-bootstrap-secret.yaml
  helm install kapro-cluster-controller charts/kapro-cluster-controller \
    -f /tmp/de-prod-01-values.yaml -n kapro-system --create-namespace`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSpokeBootstrap(cmd.Context(), spokeBootstrapOptions{
				ClusterName:     args[0],
				KubeconfigPath:  kubeconfig,
				HubNamespace:    namespace,
				TTL:             ttl,
				HubURL:          hubURL,
				CAFrom:          caFrom,
				CAFile:          caFile,
				CAInline:        caInline,
				SecretOutPath:   secretOut,
				WaitTimeout:     waitTimeout,
				SpokeNamespace:  spokeNS,
				SpokeSecretName: secretName,
			})
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to hub kubeconfig (default: $KUBECONFIG)")
	cmd.Flags().StringVarP(&namespace, "namespace", "n", defaultBootstrapNamespace, "Hub namespace where the bootstrap Secret lives")
	cmd.Flags().DurationVar(&ttl, "ttl", time.Hour, "Bootstrap slot TTL written to FleetCluster.spec.bootstrap.ttl")
	cmd.Flags().StringVar(&hubURL, "hub-url", "", "Hub kube-apiserver URL reachable from the spoke (required)")
	cmd.Flags().StringVar(&caFrom, "ca-from", caFromHubKubeconfig, "Source for hub CA bundle: hub-kubeconfig | file | inline | none")
	cmd.Flags().StringVar(&caFile, "ca-file", "", "Path to PEM CA file (used when --ca-from=file)")
	cmd.Flags().StringVar(&caInline, "ca-inline", "", "Inline PEM CA bundle (used when --ca-from=inline)")
	cmd.Flags().StringVar(&secretOut, "secret-out", "", "Write the bootstrap kubeconfig Secret YAML to this path (required)")
	cmd.Flags().DurationVar(&waitTimeout, "wait-timeout", 30*time.Second, "How long to wait for the hub to provision the bootstrap kubeconfig Secret")
	cmd.Flags().StringVar(&spokeNS, "spoke-namespace", defaultBootstrapNamespace, "Namespace the rendered Secret will target on the spoke")
	cmd.Flags().StringVar(&secretName, "spoke-secret-name", "", "Name for the rendered Secret on the spoke (defaults to the hub Secret name)")
	_ = cmd.MarkFlagRequired("hub-url")
	_ = cmd.MarkFlagRequired("secret-out")
	return cmd
}

type spokeBootstrapOptions struct {
	ClusterName     string
	KubeconfigPath  string
	HubNamespace    string
	TTL             time.Duration
	HubURL          string
	CAFrom          string
	CAFile          string
	CAInline        string
	SecretOutPath   string
	WaitTimeout     time.Duration
	SpokeNamespace  string
	SpokeSecretName string
}

func runSpokeBootstrap(ctx context.Context, opts spokeBootstrapOptions) error {
	if opts.TTL <= 0 {
		return fmt.Errorf("--ttl must be > 0")
	}
	if opts.HubNamespace == "" {
		opts.HubNamespace = defaultBootstrapNamespace
	}
	if opts.SpokeNamespace == "" {
		opts.SpokeNamespace = defaultBootstrapNamespace
	}

	caBundle, err := resolveCABundle(opts)
	if err != nil {
		return err
	}

	c, err := buildClient(opts.KubeconfigPath)
	if err != nil {
		return fmt.Errorf("connect to hub: %w", err)
	}

	sp := cli.NewSpinner(fmt.Sprintf("Ensuring FleetCluster %s with bootstrap slot", opts.ClusterName))
	sp.Start()
	if err := ensureFleetClusterBootstrap(ctx, c, opts.ClusterName, opts.TTL); err != nil {
		sp.StopFail("Failed to apply FleetCluster bootstrap spec")
		return err
	}
	sp.StopSuccess("FleetCluster bootstrap slot ready")

	sp2 := cli.NewSpinner("Waiting for hub to mint bootstrap kubeconfig Secret")
	sp2.Start()
	hubSecretName, err := waitForBootstrapSecret(ctx, c, opts.ClusterName, opts.WaitTimeout)
	if err != nil {
		sp2.StopFail("Hub did not provision bootstrap kubeconfig in time")
		return err
	}
	sp2.StopSuccess(fmt.Sprintf("Got Secret: %s", hubSecretName))

	hubSecret := &corev1.Secret{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: opts.HubNamespace, Name: hubSecretName}, hubSecret); err != nil {
		return fmt.Errorf("fetch bootstrap Secret %s/%s: %w", opts.HubNamespace, hubSecretName, err)
	}

	spokeSecretName := opts.SpokeSecretName
	if spokeSecretName == "" {
		spokeSecretName = hubSecretName
	}

	if err := writeSpokeSecret(opts.SecretOutPath, hubSecret, opts.SpokeNamespace, spokeSecretName, opts.ClusterName); err != nil {
		return err
	}

	if err := writeHelmValues(os.Stdout, opts.ClusterName, opts.HubURL, caBundle, spokeSecretName); err != nil {
		return err
	}

	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Next steps (on the spoke cluster):")
	fmt.Fprintf(os.Stderr, "  kubectl apply -f %s\n", opts.SecretOutPath)
	fmt.Fprintf(os.Stderr, "  helm install kapro-cluster-controller charts/kapro-cluster-controller \\\n")
	fmt.Fprintf(os.Stderr, "    -n %s --create-namespace \\\n", opts.SpokeNamespace)
	fmt.Fprintf(os.Stderr, "    -f <values-file-this-stdout-was-written-to>\n")
	return nil
}

func ensureFleetClusterBootstrap(ctx context.Context, c client.Client, name string, ttl time.Duration) error {
	existing := &kaprov1alpha1.FleetCluster{}
	err := c.Get(ctx, client.ObjectKey{Name: name}, existing)
	if apierrors.IsNotFound(err) {
		fc := &kaprov1alpha1.FleetCluster{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: kaprov1alpha1.FleetClusterSpec{
				Bootstrap: &kaprov1alpha1.FleetClusterBootstrapSpec{
					TTL: ttl.String(),
				},
			},
		}
		return c.Create(ctx, fc)
	}
	if err != nil {
		return fmt.Errorf("get FleetCluster %q: %w", name, err)
	}
	patch := client.MergeFrom(existing.DeepCopy())
	if existing.Spec.Bootstrap == nil {
		existing.Spec.Bootstrap = &kaprov1alpha1.FleetClusterBootstrapSpec{}
	}
	if existing.Spec.Bootstrap.TTL == "" && existing.Spec.Bootstrap.ExpiresAt == nil {
		existing.Spec.Bootstrap.TTL = ttl.String()
	}
	return c.Patch(ctx, existing, patch)
}

func waitForBootstrapSecret(ctx context.Context, c client.Client, name string, timeout time.Duration) (string, error) {
	var secretName string
	pollErr := wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		fc := &kaprov1alpha1.FleetCluster{}
		if err := c.Get(ctx, client.ObjectKey{Name: name}, fc); err != nil {
			return false, err
		}
		if fc.Status.Bootstrap != nil && fc.Status.Bootstrap.IssuedBootstrapKubeconfig != "" {
			secretName = fc.Status.Bootstrap.IssuedBootstrapKubeconfig
			return true, nil
		}
		return false, nil
	})
	if pollErr != nil {
		hint := "check the operator logs (kubectl -n kapro-system logs -l app.kubernetes.io/name=kapro-operator | grep -i bootstrap)"
		return "", fmt.Errorf("status.bootstrap.issuedBootstrapKubeconfig not populated within %s — %s: %w", timeout, hint, pollErr)
	}
	return secretName, nil
}

func resolveCABundle(opts spokeBootstrapOptions) ([]byte, error) {
	switch opts.CAFrom {
	case caFromNone:
		return nil, nil
	case caFromInline:
		if opts.CAInline == "" {
			return nil, fmt.Errorf("--ca-inline is required when --ca-from=inline")
		}
		return []byte(opts.CAInline), nil
	case caFromFile:
		if opts.CAFile == "" {
			return nil, fmt.Errorf("--ca-file is required when --ca-from=file")
		}
		data, err := os.ReadFile(opts.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read --ca-file %q: %w", opts.CAFile, err)
		}
		return data, nil
	case caFromHubKubeconfig, "":
		return caFromHubRestConfig(opts.KubeconfigPath)
	default:
		return nil, fmt.Errorf("--ca-from must be one of hub-kubeconfig|file|inline|none (got %q)", opts.CAFrom)
	}
}

func caFromHubRestConfig(kubeconfigPath string) ([]byte, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfigPath != "" {
		loadingRules.ExplicitPath = kubeconfigPath
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules, &clientcmd.ConfigOverrides{},
	).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load hub kubeconfig for CA extraction: %w", err)
	}
	if len(cfg.CAData) > 0 {
		return cfg.CAData, nil
	}
	if cfg.CAFile != "" {
		data, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read hub CAFile %q: %w", cfg.CAFile, err)
		}
		return data, nil
	}
	return nil, fmt.Errorf("hub kubeconfig has no CA data — use --ca-from=file or --ca-from=none if the hub uses a publicly trusted cert")
}

func writeSpokeSecret(path string, hubSecret *corev1.Secret, spokeNS, spokeName, clusterName string) error {
	out := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      spokeName,
			Namespace: spokeNS,
			Labels: map[string]string{
				"kapro.io/fleetcluster":        clusterName,
				"app.kubernetes.io/managed-by": "kapro-cluster-controller",
			},
		},
		Type: hubSecret.Type,
		Data: hubSecret.Data,
	}
	body, err := yaml.Marshal(out)
	if err != nil {
		return fmt.Errorf("marshal Secret: %w", err)
	}
	header := fmt.Sprintf("# Generated by `kapro spoke bootstrap %s`.\n# Apply on the spoke before `helm install kapro-cluster-controller`.\n", clusterName)
	if err := os.WriteFile(path, append([]byte(header), body...), 0o600); err != nil {
		return fmt.Errorf("write --secret-out %q: %w", path, err)
	}
	return nil
}

func writeHelmValues(w *os.File, clusterName, hubURL string, caBundle []byte, secretName string) error {
	hub := map[string]any{"url": hubURL}
	if len(caBundle) > 0 {
		hub["caBundle"] = base64.StdEncoding.EncodeToString(caBundle)
	}
	values := map[string]any{
		"cluster": map[string]any{"name": clusterName},
		"hub":     hub,
		"bootstrap": map[string]any{
			"kubeconfigSecret": map[string]any{"name": secretName},
		},
	}
	body, err := yaml.Marshal(values)
	if err != nil {
		return fmt.Errorf("marshal values: %w", err)
	}
	header := strings.Join([]string{
		"# Generated by `kapro spoke bootstrap " + clusterName + "`.",
		"# Pass to `helm install kapro-cluster-controller -f <this-file>`.",
		"",
	}, "\n")
	if _, err := fmt.Fprint(w, header); err != nil {
		return err
	}
	_, err = w.Write(body)
	return err
}
