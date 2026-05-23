// Command kapro-cluster-controller is the spoke-side agent that registers a
// workload cluster with the Kapro hub, maintains a heartbeat Lease, reports
// cluster status, and reconciles Cluster.spec.desiredVersions onto the
// local cluster through a pluggable spoke Provider registry.
//
// Lifecycle:
//
//  1. Read bootstrap kubeconfig from a mounted Secret (provisioned by the hub
//     ClusterBootstrapReconciler from PR-2).
//  2. Start a `client-go` certificate.Manager configured with our CN/O,
//     SignerName = kubernetes.io/kube-apiserver-client, and a Secret-backed
//     Store so the issued cert survives pod restarts.
//  3. Wait for the first cert (the hub approver signs it).
//  4. Build a steady-state hub client using the cert.
//  5. Start three background loops:
//     - heartbeat: refresh Lease kapro-heartbeat-<name> every 30s
//     - status:    report cluster capabilities + health to Cluster.status
//     - delivery:  watch Cluster.spec.desiredVersions, dispatch to a
//     spoke Provider (oci/flux/argo/external) via the registry,
//     and write per-app progress to Cluster.status.delivery
//  6. Cert rotation is handled automatically by certificate.Manager — when the
//     cert is approaching expiry it submits a renewal CSR (Username =
//     "kapro-cluster:<name>" so the hub approver recognizes it as a renewal,
//     not a bootstrap).
//
// All work happens against ONE Cluster (KAPRO_CLUSTER_NAME). The
// per-cluster RBAC issued during bootstrap allows nothing else.
package main

import (
	"context"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	certificatesv1 "k8s.io/api/certificates/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	fluxspoke "kapro.io/kapro/internal/spokeprovider/flux"
	"kapro.io/kapro/internal/spokeprovider/outbound"
	"kapro.io/kapro/pkg/spokeprovider"
)

var scheme = runtime.NewScheme()

func init() {
	_ = clientgoscheme.AddToScheme(scheme)
	_ = kaprov1alpha2.AddToScheme(scheme)
}

// Config carries the runtime configuration of the spoke binary. Sourced from
// env vars (Helm chart populates these from values in PR-7).
type Config struct {
	// ClusterName is the Cluster name this spoke registers as.
	// Required. Set from KAPRO_CLUSTER_NAME or --cluster-name.
	ClusterName string

	// HubAPIURL is the externally-reachable hub kube-apiserver URL.
	// Required. Set from KAPRO_HUB_URL or --hub-url.
	HubAPIURL string

	// HubCAData is the PEM CA bundle for HubAPIURL. May be raw PEM, base64,
	// or empty (system trust roots only — appropriate when the hub apiserver
	// has a publicly-trusted cert).
	HubCAData []byte

	// BootstrapKubeconfigPath is a file containing the bootstrap kubeconfig
	// (with the bootstrap SA bearer token). Mounted from the Secret created
	// by the hub controller. Optional if a steady-state cert is already in
	// CredentialSecretName.
	BootstrapKubeconfigPath string

	// CredentialSecretName is the name of the local (spoke-cluster) Secret
	// where the issued cert+key are persisted. Defaults to
	// kapro-hub-credentials. Survives pod restarts.
	CredentialSecretName string

	// CredentialSecretNamespace is the local namespace for the credential
	// Secret. Defaults to kapro-system.
	CredentialSecretNamespace string

	// HeartbeatNamespace is the HUB namespace where Lease
	// kapro-heartbeat-<cluster> lives. Per-cluster RBAC from PR-2 allows
	// the spoke to write only its own Lease. Defaults to kapro-system.
	HeartbeatNamespace string

	// HeartbeatInterval is how often to refresh the Lease. Defaults to 30s.
	HeartbeatInterval time.Duration

	// StatusReportInterval is how often to publish Cluster.status.
	// Defaults to 60s — slower than heartbeat because Health probes are
	// more expensive and the status doesn't change as fast.
	StatusReportInterval time.Duration

	// DeliveryInterval is how often the delivery loop reconciles
	// Cluster.spec.desiredVersions on the local spoke cluster.
	// Defaults to 30s — same cadence as heartbeat so a freshly-promoted
	// version starts converging within one heartbeat window.
	DeliveryInterval time.Duration

	// MetricsAddr is the Prometheus listen address. Set to "off" to disable.
	MetricsAddr string
}

// loadConfig populates Config from env vars + flags.
func loadConfig() (*Config, error) {
	cfg := &Config{
		ClusterName:               os.Getenv("KAPRO_CLUSTER_NAME"),
		HubAPIURL:                 os.Getenv("KAPRO_HUB_URL"),
		HubCAData:                 decodeCABundle(os.Getenv("KAPRO_HUB_CA_BUNDLE")),
		BootstrapKubeconfigPath:   os.Getenv("KAPRO_BOOTSTRAP_KUBECONFIG_PATH"),
		CredentialSecretName:      envOrDefault("KAPRO_CREDENTIAL_SECRET_NAME", "kapro-hub-credentials"),
		CredentialSecretNamespace: envOrDefault("KAPRO_CREDENTIAL_SECRET_NAMESPACE", "kapro-system"),
		HeartbeatNamespace:        envOrDefault("KAPRO_HEARTBEAT_NAMESPACE", "kapro-system"),
		HeartbeatInterval:         envDurationOrDefault("KAPRO_HEARTBEAT_INTERVAL", 30*time.Second),
		StatusReportInterval:      envDurationOrDefault("KAPRO_STATUS_REPORT_INTERVAL", 60*time.Second),
		DeliveryInterval:          envDurationOrDefault("KAPRO_DELIVERY_INTERVAL", 30*time.Second),
		MetricsAddr:               envOrDefault("KAPRO_METRICS_ADDR", ":8080"),
	}

	flag.StringVar(&cfg.ClusterName, "cluster-name", cfg.ClusterName, "Cluster name this spoke registers as (env: KAPRO_CLUSTER_NAME)")
	flag.StringVar(&cfg.HubAPIURL, "hub-url", cfg.HubAPIURL, "Hub kube-apiserver URL (env: KAPRO_HUB_URL)")
	flag.StringVar(&cfg.BootstrapKubeconfigPath, "bootstrap-kubeconfig", cfg.BootstrapKubeconfigPath, "Path to bootstrap kubeconfig from hub (env: KAPRO_BOOTSTRAP_KUBECONFIG_PATH)")
	flag.StringVar(&cfg.MetricsAddr, "metrics-addr", cfg.MetricsAddr, "Prometheus metrics listen address, or off to disable (env: KAPRO_METRICS_ADDR)")
	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	log.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	if cfg.ClusterName == "" {
		return nil, fmt.Errorf("KAPRO_CLUSTER_NAME (or --cluster-name) is required")
	}
	if cfg.HubAPIURL == "" {
		return nil, fmt.Errorf("KAPRO_HUB_URL (or --hub-url) is required")
	}
	if cfg.HeartbeatInterval < 5*time.Second {
		return nil, fmt.Errorf("KAPRO_HEARTBEAT_INTERVAL must be ≥ 5s (got %s)", cfg.HeartbeatInterval)
	}
	if cfg.StatusReportInterval < 10*time.Second {
		return nil, fmt.Errorf("KAPRO_STATUS_REPORT_INTERVAL must be ≥ 10s (got %s)", cfg.StatusReportInterval)
	}
	if cfg.DeliveryInterval < 10*time.Second {
		return nil, fmt.Errorf("KAPRO_DELIVERY_INTERVAL must be ≥ 10s (got %s)", cfg.DeliveryInterval)
	}
	return cfg, nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	logger := log.Log.WithName("kapro-cluster-controller")
	logger.Info("starting", "cluster", cfg.ClusterName, "hubURL", cfg.HubAPIURL)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Build a local (spoke-cluster) client. Used to persist the issued
	// cert/key into a local Secret + to read node info for status reporting.
	localKubeClient, err := buildLocalClient()
	if err != nil {
		return fmt.Errorf("build local cluster client: %w", err)
	}

	// Cert manager: starts in "no cert" state, uses the bootstrap kubeconfig
	// to submit the first CSR, then auto-rotates using the issued cert for
	// renewal CSRs.
	template := &x509.CertificateRequest{
		Subject: pkix.Name{
			CommonName:   "kapro-cluster:" + cfg.ClusterName,
			Organization: []string{"kapro:cluster-controllers"},
		},
	}
	bootstrap, err := loadBootstrapKubeconfig(cfg.BootstrapKubeconfigPath)
	if err != nil {
		return fmt.Errorf("load bootstrap kubeconfig: %w", err)
	}
	store := &secretStore{
		client:    localKubeClient,
		namespace: cfg.CredentialSecretNamespace,
		name:      cfg.CredentialSecretName,
	}
	if err := store.ensureNamespace(ctx); err != nil {
		return fmt.Errorf("ensure credential namespace: %w", err)
	}
	certMgr, err := startCertManager(ctx, certManagerOptions{
		Template:            template,
		ClusterName:         cfg.ClusterName,
		SignerName:          certificatesv1.KubeAPIServerClientSignerName,
		Usages:              []certificatesv1.KeyUsage{certificatesv1.UsageClientAuth},
		BootstrapClient:     bootstrap,
		HubAPIURL:           cfg.HubAPIURL,
		HubCAData:           cfg.HubCAData,
		Store:               store,
		RequestedCertTTL:    365 * 24 * time.Hour,
		WaitForFirstCert:    5 * time.Minute,
		WaitForCertInterval: 5 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("start cert manager: %w", err)
	}
	defer certMgr.Stop()
	logger.Info("registered with hub", "cluster", cfg.ClusterName)
	if err := startMetricsServer(ctx, cfg.MetricsAddr); err != nil {
		return fmt.Errorf("start metrics server: %w", err)
	}

	hubClient := newHubClient(certMgr, cfg.HubAPIURL, cfg.HubCAData)

	// Spawn heartbeat + status + delivery loops. They run forever; main
	// exits when ctx is cancelled by SIGTERM/SIGINT. All three loops call
	// hubClient.Client() per tick so cert rotation transparently swaps the
	// underlying client without the loops needing to know.
	hb := &heartbeatLoop{
		Hub:            hubClient,
		ClusterName:    cfg.ClusterName,
		HubNamespace:   cfg.HeartbeatNamespace,
		Interval:       cfg.HeartbeatInterval,
		HolderIdentity: hostname() + "/" + cfg.ClusterName,
	}
	go hb.Run(ctx)

	sr := &statusReporter{
		Hub:         hubClient,
		Local:       localKubeClient,
		ClusterName: cfg.ClusterName,
		Interval:    cfg.StatusReportInterval,
	}
	go sr.Run(ctx)

	// Delivery loop: watches Cluster.spec.desiredVersions and reconciles
	// each (app, version) tuple via the spoke Provider registry. Two
	// first-party drivers are wired:
	//   - oci  — internal/spokeprovider/outbound: pulls OCI artifacts and
	//            applies them via the two-phase apply engine. Greenfield.
	//   - flux — internal/spokeprovider/flux: observes local Flux
	//            OCIRepository / HelmRelease status and reports back to the
	//            hub. Brownfield (Flux already installed on the spoke).
	// External drivers are loaded via PluginRegistration + the plugin
	// gateway when KAPRO_ENABLE_PLUGIN_GATEWAY=true.
	registry := spokeprovider.NewRegistry()
	if err := registry.Register(kaprov1alpha2.BackendDriverOCI, outbound.NewProvider(localKubeClient)); err != nil {
		return fmt.Errorf("register oci provider: %w", err)
	}
	if err := registry.Register(kaprov1alpha2.BackendDriverFlux, fluxspoke.NewProvider(localKubeClient)); err != nil {
		return fmt.Errorf("register flux provider: %w", err)
	}
	dl := &deliveryLoop{
		Hub:         hubClient,
		ClusterName: cfg.ClusterName,
		Interval:    cfg.DeliveryInterval,
		Registry:    registry,
	}
	go dl.Run(ctx)

	<-ctx.Done()
	logger.Info("shutting down")
	return nil
}

// envOrDefault returns the env var value or fallback when unset/empty.
func envOrDefault(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// envDurationOrDefault parses a Go duration env var with fallback.
// Invalid values fall back silently — validated in loadConfig.
func envDurationOrDefault(name string, fallback time.Duration) time.Duration {
	if v := os.Getenv(name); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

// decodeCABundle accepts raw PEM, base64 PEM, or empty.
func decodeCABundle(raw string) []byte {
	if raw == "" {
		return nil
	}
	if strings.HasPrefix(raw, "-----") {
		return []byte(raw)
	}
	if decoded, err := base64.StdEncoding.DecodeString(raw); err == nil {
		return decoded
	}
	// Treat as raw if base64 decode fails.
	return []byte(raw)
}

// hostname returns the pod's hostname for use as Lease HolderIdentity.
// Errors fall back to a static "kapro-spoke" identifier — leader election
// isn't a concern here since each cluster has a single spoke pod.
func hostname() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "kapro-spoke"
	}
	return h
}

// buildLocalClient builds a controller-runtime client for the LOCAL spoke
// cluster (the cluster this pod runs in), used to persist the cert Secret
// and read node info for status reporting.
//
// Precedence INTENTIONALLY prefers in-cluster config:
//
//   - In production the spoke runs as a Pod and KUBERNETES_SERVICE_HOST is
//     set, so in-cluster wins. This is the correct + only safe answer.
//   - Falling back to a stray KUBECONFIG env var (kubelet config, dev
//     kubeconfig left on the node) would silently make the spoke persist
//     credentials into — and read node info from — a DIFFERENT cluster.
//     That's a data-corruption-class bug: the wrong cluster's node count
//     would land on this cluster's Cluster.status.capabilities.
//   - Outside a pod (local dev) we fall back to kubeconfig loading rules
//     so `go run` against kind still works.
//
// The selected source is logged so a misconfiguration becomes visible
// rather than silently corrupting state.
func buildLocalClient() (client.Client, error) {
	logger := log.Log.WithName("local-client")
	if restCfg, err := inClusterConfig(); err == nil {
		logger.Info("using in-cluster config", "host", restCfg.Host)
		return client.New(restCfg, client.Options{Scheme: scheme})
	}
	// Not in a pod — try kubeconfig for local dev.
	cfg, err := clientcmd.NewDefaultClientConfigLoadingRules().Load()
	if err != nil || cfg == nil || len(cfg.Clusters) == 0 {
		return nil, fmt.Errorf("not running in a pod and no usable KUBECONFIG: %w", err)
	}
	restCfg, err := clientcmd.NewDefaultClientConfig(*cfg, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("build rest.Config from kubeconfig: %w", err)
	}
	logger.Info("using kubeconfig (local dev — no KUBERNETES_SERVICE_HOST set)", "host", restCfg.Host, "context", cfg.CurrentContext)
	return client.New(restCfg, client.Options{Scheme: scheme})
}
