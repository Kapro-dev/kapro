// Command kapro is the CLI for the Kapro promotion engine.
//
// Usage:
//
//	kapro cluster bootstrap --name <cluster-name> [--labels key=value,...]
//	kapro cluster bootstrap --name <cluster-name> --namespace kapro-system
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

var scheme = runtime.NewScheme()

func init() {
	_ = clientgoscheme.AddToScheme(scheme)
	_ = kaprov1alpha1.AddToScheme(scheme)
}

func main() {
	root := &cobra.Command{
		Use:   "kapro",
		Short: "The Canonical Promotion Layer for Kubernetes",
		Long: `kapro — multi-cluster progressive delivery engine.

Passes versions forward. Through environments. Across clusters. In waves.`,
	}

	root.AddCommand(newClusterCmd())

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func newClusterCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cluster",
		Short: "Manage cluster registrations",
	}
	cmd.AddCommand(newBootstrapCmd())
	return cmd
}

func newBootstrapCmd() *cobra.Command {
	var (
		clusterName string
		namespace   string
		labelsRaw   []string
		ttl         time.Duration
		kubeconfig  string
	)

	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Register a workload cluster with Kapro",
		Long: `Bootstrap creates a BootstrapToken CR on the management cluster.
The Kapro operator processes it, creates a scoped ServiceAccount + RBAC, and
writes the cluster credentials to a Secret named kapro-cluster-<name>-credentials.

Steps:
  1. Run this command on a machine with access to the MANAGEMENT cluster kubeconfig.
  2. Copy the printed token to the WORKLOAD cluster.
  3. Start kapro-cluster-controller with --bootstrap-token=<token>.

Example:
  kapro cluster bootstrap --name de-prod --labels env=prod,region=eu-west`,

		RunE: func(cmd *cobra.Command, args []string) error {
			return runBootstrap(cmd.Context(), clusterName, namespace, labelsRaw, ttl, kubeconfig)
		},
	}

	cmd.Flags().StringVar(&clusterName, "name", "", "Cluster name (required, must match ClusterRegistration name)")
	cmd.Flags().StringVar(&namespace, "namespace", "kapro-system", "Namespace for the BootstrapToken CR")
	cmd.Flags().StringArrayVar(&labelsRaw, "labels", nil, "Labels for the ClusterRegistration (key=value)")
	cmd.Flags().DurationVar(&ttl, "ttl", 24*time.Hour, "Token TTL (default 24h)")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig (defaults to KUBECONFIG env / ~/.kube/config)")
	_ = cmd.MarkFlagRequired("name")

	return cmd
}

func runBootstrap(ctx context.Context, clusterName, namespace string, labelsRaw []string, ttl time.Duration, kubeconfigPath string) error {
	// Build management-cluster client.
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfigPath != "" {
		loadingRules.ExplicitPath = kubeconfigPath
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return fmt.Errorf("load kubeconfig: %w", err)
	}

	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("create client: %w", err)
	}

	// Parse labels.
	labels := map[string]string{}
	for _, kv := range labelsRaw {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid label %q (expected key=value)", kv)
		}
		labels[parts[0]] = parts[1]
	}

	// Generate a cryptographically random 32-byte token.
	rawBytes := make([]byte, 32)
	if _, err := rand.Read(rawBytes); err != nil {
		return fmt.Errorf("generate token: %w", err)
	}
	rawToken := hex.EncodeToString(rawBytes)

	// Store only the SHA-256 hash.
	hash := sha256.Sum256([]byte(rawToken))
	tokenHash := hex.EncodeToString(hash[:])

	expiresAt := metav1.NewTime(time.Now().Add(ttl))
	btName := clusterName + "-bootstrap"

	bt := &kaprov1alpha1.BootstrapToken{
		ObjectMeta: metav1.ObjectMeta{
			Name:      btName,
			Namespace: namespace,
		},
		Spec: kaprov1alpha1.BootstrapTokenSpec{
			ClusterName: clusterName,
			TokenHash:   tokenHash,
			ExpiresAt:   expiresAt,
			Labels:      labels,
		},
	}

	if err := c.Create(ctx, bt); err != nil {
		return fmt.Errorf("create BootstrapToken: %w", err)
	}

	fmt.Printf(`
✅ BootstrapToken created: %s/%s

Cluster:    %s
Token hash: %s (stored — plaintext never persisted)
Expires:    %s

🔑 Bootstrap Token (keep secret — one-time use):
   %s

Next steps:
  1. On the WORKLOAD cluster, start kapro-cluster-controller with:
       --bootstrap-token=%s
       --management-cluster-url=%s
  
  2. The operator will:
       • Create a scoped ServiceAccount (kapro-cluster-%s)
       • Create ClusterRole scoped to ClusterRegistration/%s only
       • Issue a short-lived SA token (1h, auto-renewing)
       • Write credentials to Secret: kapro-system/kapro-cluster-%s-credentials

  3. Check bootstrap status:
       kubectl get bootstraptoken %s -n %s -o yaml
`,
		namespace, btName,
		clusterName,
		tokenHash[:16]+"...",
		expiresAt.Format(time.RFC3339),
		rawToken,
		rawToken,
		cfg.Host,
		clusterName, clusterName, clusterName,
		btName, namespace,
	)

	// Wait briefly and check if operator has already processed it.
	fmt.Print("⏳ Waiting for operator to process token")
	for i := 0; i < 10; i++ {
		time.Sleep(2 * time.Second)
		fmt.Print(".")

		var check kaprov1alpha1.BootstrapToken
		if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: btName}, &check); err != nil {
			continue
		}
		if check.Status.Used {
			fmt.Printf("\n✅ Token consumed by operator. SA: %s\n", check.Status.IssuedCredentialFor)
			fmt.Printf("   Credentials secret: kapro-system/kapro-cluster-%s-credentials\n", clusterName)
			return nil
		}
	}
	fmt.Println("\n⏳ Operator has not processed the token yet — it will be processed on next reconcile.")
	return nil
}
