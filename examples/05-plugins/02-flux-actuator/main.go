package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"strings"

	"kapro.io/kapro/pkg/plugincompat"
	kaiv1alpha1 "kapro.io/kapro/spec/kai/v1alpha1"

	"google.golang.org/grpc"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	pluginVersion = "0.3.7"

	defaultListenAddr = ":9090"
	defaultNamespace  = "flux-system"
)

var helmReleaseGVR = schema.GroupVersionResource{
	Group:    "helm.toolkit.fluxcd.io",
	Version:  "v2",
	Resource: "helmreleases",
}

type fluxActuatorServer struct {
	kaiv1alpha1.UnimplementedActuatorServiceServer

	client           dynamic.Interface
	defaultNamespace string
}

func (s *fluxActuatorServer) GetCapabilities(context.Context, *kaiv1alpha1.GetCapabilitiesRequest) (*kaiv1alpha1.GetCapabilitiesResponse, error) {
	return &kaiv1alpha1.GetCapabilitiesResponse{
		ContractVersion: plugincompat.VersionV1Alpha1,
		PluginVersion:   pluginVersion,
		Capabilities: []string{
			"flux.helmrelease.chart-version.apply",
			"flux.helmrelease.ready.convergence",
			"flux.helmrelease.chart-version.rollback",
		},
	}, nil
}

func (s *fluxActuatorServer) Apply(ctx context.Context, req *kaiv1alpha1.ApplyRequest) (*kaiv1alpha1.ApplyResponse, error) {
	if req.GetVersion() == "" {
		return nil, fmt.Errorf("version is required")
	}
	ref, err := s.helmReleaseRef(req.GetTarget(), req.GetParameters())
	if err != nil {
		return nil, err
	}
	if err := s.setChartVersion(ctx, ref, req.GetVersion()); err != nil {
		return nil, err
	}
	return &kaiv1alpha1.ApplyResponse{
		Accepted: true,
		Message:  fmt.Sprintf("patched Flux HelmRelease %s/%s to chart version %s", ref.namespace, ref.name, req.GetVersion()),
	}, nil
}

func (s *fluxActuatorServer) IsConverged(ctx context.Context, req *kaiv1alpha1.IsConvergedRequest) (*kaiv1alpha1.IsConvergedResponse, error) {
	if req.GetVersion() == "" {
		return nil, fmt.Errorf("version is required")
	}
	ref, err := s.helmReleaseRef(req.GetTarget(), req.GetParameters())
	if err != nil {
		return nil, err
	}
	hr, err := s.client.Resource(helmReleaseGVR).Namespace(ref.namespace).Get(ctx, ref.name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get Flux HelmRelease %s/%s: %w", ref.namespace, ref.name, err)
	}
	version, _, _ := unstructured.NestedString(hr.Object, "spec", "chart", "spec", "version")
	if version != req.GetVersion() {
		return &kaiv1alpha1.IsConvergedResponse{
			Converged: false,
			Message:   fmt.Sprintf("chart version=%q, want %q", version, req.GetVersion()),
		}, nil
	}
	if readyConditionTrue(hr) {
		return &kaiv1alpha1.IsConvergedResponse{
			Converged: true,
			Message:   "helmrelease Ready condition is True",
		}, nil
	}
	return &kaiv1alpha1.IsConvergedResponse{
		Converged: false,
		Message:   "helmrelease Ready condition is not True",
	}, nil
}

func (s *fluxActuatorServer) Rollback(ctx context.Context, req *kaiv1alpha1.RollbackRequest) (*kaiv1alpha1.RollbackResponse, error) {
	if req.GetPreviousVersion() == "" {
		return nil, fmt.Errorf("previousVersion is required")
	}
	ref, err := s.helmReleaseRef(req.GetTarget(), req.GetParameters())
	if err != nil {
		return nil, err
	}
	if err := s.setChartVersion(ctx, ref, req.GetPreviousVersion()); err != nil {
		return nil, err
	}
	return &kaiv1alpha1.RollbackResponse{
		Accepted: true,
		Message:  fmt.Sprintf("patched Flux HelmRelease %s/%s to previous chart version %s", ref.namespace, ref.name, req.GetPreviousVersion()),
	}, nil
}

func (s *fluxActuatorServer) setChartVersion(ctx context.Context, ref helmReleaseRef, version string) error {
	patch, err := json.Marshal(map[string]any{
		"spec": map[string]any{
			"chart": map[string]any{
				"spec": map[string]any{
					"version": version,
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("marshal chart version patch: %w", err)
	}
	if _, err := s.client.Resource(helmReleaseGVR).Namespace(ref.namespace).Patch(ctx, ref.name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("patch Flux HelmRelease %s/%s: %w", ref.namespace, ref.name, err)
	}
	return nil
}

type helmReleaseRef struct {
	namespace string
	name      string
}

func (s *fluxActuatorServer) helmReleaseRef(target string, params map[string]string) (helmReleaseRef, error) {
	namespace := firstNonEmpty(params["fluxNamespace"], params["namespace"], s.defaultNamespace, defaultNamespace)
	name := firstNonEmpty(params["helmRelease"], params["helmReleaseName"], params["fluxHelmRelease"], params["appKey"], target)
	if strings.Contains(name, "/") {
		parts := strings.SplitN(name, "/", 2)
		namespace = firstNonEmpty(parts[0], namespace)
		name = parts[1]
	}
	if namespace == "" {
		return helmReleaseRef{}, fmt.Errorf("flux namespace is required")
	}
	if name == "" {
		return helmReleaseRef{}, fmt.Errorf("helmRelease parameter or target is required")
	}
	return helmReleaseRef{namespace: namespace, name: name}, nil
}

func readyConditionTrue(hr *unstructured.Unstructured) bool {
	conditions, _, _ := unstructured.NestedSlice(hr.Object, "status", "conditions")
	for _, raw := range conditions {
		condition, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if condition["type"] == "Ready" && condition["status"] == "True" {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func main() {
	listenAddr := flag.String("listen", defaultListenAddr, "gRPC listen address")
	namespace := flag.String("namespace", defaultNamespace, "default Flux HelmRelease namespace")
	kubeconfig := flag.String("kubeconfig", "", "path to kubeconfig; defaults to in-cluster config, then local kubeconfig")
	flag.Parse()

	config, err := kubernetesConfig(*kubeconfig)
	if err != nil {
		log.Fatalf("load Kubernetes config: %v", err)
	}
	client, err := dynamic.NewForConfig(config)
	if err != nil {
		log.Fatalf("create dynamic client: %v", err)
	}
	listener, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		log.Fatalf("listen on %s: %v", *listenAddr, err)
	}
	grpcServer := grpc.NewServer()
	kaiv1alpha1.RegisterActuatorServiceServer(grpcServer, &fluxActuatorServer{
		client:           client,
		defaultNamespace: *namespace,
	})
	log.Printf("flux actuator plugin listening on %s", *listenAddr)
	if err := grpcServer.Serve(listener); err != nil {
		log.Fatalf("serve grpc: %v", err)
	}
}

func kubernetesConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	if config, err := rest.InClusterConfig(); err == nil {
		return config, nil
	}
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides).ClientConfig()
}
