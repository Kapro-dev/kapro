// rancher-plugin is a Kapro out-of-process provider plugin for Rancher-managed clusters.
//
// On start it:
//  1. Opens a Unix domain socket (or TCP if KAPRO_PLUGIN_ENDPOINT is set)
//  2. Starts a gRPC server implementing ProviderService + grpc.health.v1
//  3. Writes/updates a PluginRegistration CR in the kapro-system namespace
//     so the Kapro operator discovers and dials back
//
// Configuration (environment variables):
//
//	KAPRO_PLUGIN_SOCKET      path to Unix socket (default /tmp/kapro-rancher.sock)
//	KAPRO_PLUGIN_ENDPOINT    if set, registers TCP endpoint instead of Unix socket
//	KAPRO_NAMESPACE          namespace to write PluginRegistration (default kapro-system)
//	KUBECONFIG               standard kubeconfig for self-registration
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	rancherprovider "kapro.io/plugins/rancher/internal/provider"
	rancherserver "kapro.io/plugins/rancher/internal/server"
)

const (
	pluginName       = "rancher"
	defaultNamespace = "kapro-system"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "rancher-plugin: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Start gRPC server.
	lis, err := rancherserver.ListenUnix()
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	fmt.Printf("rancher-plugin: listening on %s\n", lis.Addr())

	connector := &rancherprovider.Connector{}
	srv := rancherserver.New(connector)

	srvErr := make(chan error, 1)
	go func() {
		srvErr <- srv.Start(ctx, lis)
	}()

	// Self-register the PluginRegistration CR so the operator discovers us.
	k8sClient, err := buildK8sClient()
	if err != nil {
		return fmt.Errorf("build k8s client: %w", err)
	}
	if err := upsertPluginRegistration(ctx, k8sClient); err != nil {
		// Non-fatal — operator will retry. Log and continue.
		fmt.Fprintf(os.Stderr, "rancher-plugin: warn: could not upsert PluginRegistration: %v\n", err)
	}

	// Wait for context cancel or server error.
	select {
	case <-ctx.Done():
		fmt.Println("rancher-plugin: shutting down")
	case err := <-srvErr:
		if err != nil {
			return fmt.Errorf("gRPC server: %w", err)
		}
	}
	return nil
}

// upsertPluginRegistration creates or updates the PluginRegistration CR.
func upsertPluginRegistration(ctx context.Context, k8sClient client.Client) error {
	ns := os.Getenv("KAPRO_NAMESPACE")
	if ns == "" {
		ns = defaultNamespace
	}

	endpoint := os.Getenv("KAPRO_PLUGIN_ENDPOINT")
	socketPath := rancherserver.SocketPath()

	spec := kaprov1alpha1.PluginRegistrationSpec{
		Type: kaprov1alpha1.PluginTypeProvider,
		HealthCheck: &kaprov1alpha1.PluginHealthCheck{
			IntervalSeconds: 30,
			TimeoutSeconds:  5,
		},
		Metadata: kaprov1alpha1.PluginMeta{
			Vendor:      "kapro.io",
			Version:     "v0.1.0",
			Description: "Rancher cluster provider — connects via Norman v3 API",
			Homepage:    "https://kapro.io/plugins/rancher",
		},
	}
	if endpoint != "" {
		spec.Endpoint = endpoint
	} else {
		spec.SocketPath = socketPath
	}

	pr := &kaprov1alpha1.PluginRegistration{
		ObjectMeta: metav1.ObjectMeta{
			Name: pluginName,
		},
		Spec: spec,
	}

	existing := &kaprov1alpha1.PluginRegistration{}
	err := k8sClient.Get(ctx, client.ObjectKey{Name: pluginName}, existing)
	if err != nil {
		// Create.
		if err2 := k8sClient.Create(ctx, pr); err2 != nil {
			return fmt.Errorf("create PluginRegistration: %w", err2)
		}
		fmt.Printf("rancher-plugin: created PluginRegistration %q\n", pluginName)
		return nil
	}

	// Update spec in place.
	existing.Spec = spec
	if err := k8sClient.Update(ctx, existing); err != nil {
		return fmt.Errorf("update PluginRegistration: %w", err)
	}
	fmt.Printf("rancher-plugin: updated PluginRegistration %q\n", pluginName)
	return nil
}

// buildK8sClient returns a controller-runtime client using the in-cluster
// config when running inside a Pod, or KUBECONFIG when running locally.
func buildK8sClient() (client.Client, error) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = kaprov1alpha1.AddToScheme(scheme)

	cfg, err := rest.InClusterConfig()
	if err != nil {
		// Fall back to kubeconfig for local development.
		cfg, err = clientcmd.BuildConfigFromFlags("", os.Getenv("KUBECONFIG"))
		if err != nil {
			return nil, fmt.Errorf("load kubeconfig: %w", err)
		}
	}

	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("new k8s client: %w", err)
	}
	return c, nil
}
