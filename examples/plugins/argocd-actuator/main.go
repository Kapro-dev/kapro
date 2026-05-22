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
	pluginVersion = "0.2.4"

	defaultListenAddr = ":9090"
	defaultNamespace  = "argocd"
)

var applicationGVR = schema.GroupVersionResource{
	Group:    "argoproj.io",
	Version:  "v1alpha1",
	Resource: "applications",
}

type argoActuatorServer struct {
	kaiv1alpha1.UnimplementedActuatorServiceServer

	client           dynamic.Interface
	defaultNamespace string
}

func (s *argoActuatorServer) GetCapabilities(context.Context, *kaiv1alpha1.GetCapabilitiesRequest) (*kaiv1alpha1.GetCapabilitiesResponse, error) {
	return &kaiv1alpha1.GetCapabilitiesResponse{
		ContractVersion: plugincompat.VersionV1Alpha1,
		PluginVersion:   pluginVersion,
		Capabilities: []string{
			"argocd.application.targetRevision.apply",
			"argocd.application.sync-health.convergence",
			"argocd.application.targetRevision.rollback",
		},
	}, nil
}

func (s *argoActuatorServer) Apply(ctx context.Context, req *kaiv1alpha1.ApplyRequest) (*kaiv1alpha1.ApplyResponse, error) {
	if req.GetVersion() == "" {
		return nil, fmt.Errorf("version is required")
	}
	ref, err := s.applicationRef(req.GetTarget(), req.GetParameters())
	if err != nil {
		return nil, err
	}
	if err := s.setTargetRevision(ctx, ref, req.GetVersion()); err != nil {
		return nil, err
	}
	return &kaiv1alpha1.ApplyResponse{
		Accepted: true,
		Message:  fmt.Sprintf("patched Argo CD Application %s/%s to targetRevision %s", ref.namespace, ref.name, req.GetVersion()),
	}, nil
}

func (s *argoActuatorServer) IsConverged(ctx context.Context, req *kaiv1alpha1.IsConvergedRequest) (*kaiv1alpha1.IsConvergedResponse, error) {
	if req.GetVersion() == "" {
		return nil, fmt.Errorf("version is required")
	}
	ref, err := s.applicationRef(req.GetTarget(), req.GetParameters())
	if err != nil {
		return nil, err
	}
	app, err := s.client.Resource(applicationGVR).Namespace(ref.namespace).Get(ctx, ref.name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get Argo CD Application %s/%s: %w", ref.namespace, ref.name, err)
	}
	targetRevision, _, _ := unstructured.NestedString(app.Object, "spec", "source", "targetRevision")
	if targetRevision != req.GetVersion() {
		return &kaiv1alpha1.IsConvergedResponse{
			Converged: false,
			Message:   fmt.Sprintf("targetRevision=%q, want %q", targetRevision, req.GetVersion()),
		}, nil
	}
	syncStatus, _, _ := unstructured.NestedString(app.Object, "status", "sync", "status")
	healthStatus, _, _ := unstructured.NestedString(app.Object, "status", "health", "status")
	if syncStatus == "Synced" && healthStatus == "Healthy" {
		return &kaiv1alpha1.IsConvergedResponse{
			Converged: true,
			Message:   "application is synced and healthy",
		}, nil
	}
	return &kaiv1alpha1.IsConvergedResponse{
		Converged: false,
		Message:   fmt.Sprintf("sync=%q health=%q", syncStatus, healthStatus),
	}, nil
}

func (s *argoActuatorServer) Rollback(ctx context.Context, req *kaiv1alpha1.RollbackRequest) (*kaiv1alpha1.RollbackResponse, error) {
	if req.GetPreviousVersion() == "" {
		return nil, fmt.Errorf("previousVersion is required")
	}
	ref, err := s.applicationRef(req.GetTarget(), req.GetParameters())
	if err != nil {
		return nil, err
	}
	if err := s.setTargetRevision(ctx, ref, req.GetPreviousVersion()); err != nil {
		return nil, err
	}
	return &kaiv1alpha1.RollbackResponse{
		Accepted: true,
		Message:  fmt.Sprintf("patched Argo CD Application %s/%s to previous targetRevision %s", ref.namespace, ref.name, req.GetPreviousVersion()),
	}, nil
}

func (s *argoActuatorServer) setTargetRevision(ctx context.Context, ref applicationRef, revision string) error {
	patch, err := json.Marshal(map[string]any{
		"spec": map[string]any{
			"source": map[string]any{
				"targetRevision": revision,
			},
		},
	})
	if err != nil {
		return fmt.Errorf("marshal targetRevision patch: %w", err)
	}
	if _, err := s.client.Resource(applicationGVR).Namespace(ref.namespace).Patch(ctx, ref.name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("patch Argo CD Application %s/%s: %w", ref.namespace, ref.name, err)
	}
	return nil
}

type applicationRef struct {
	namespace string
	name      string
}

func (s *argoActuatorServer) applicationRef(target string, params map[string]string) (applicationRef, error) {
	namespace := firstNonEmpty(params["argocdNamespace"], params["namespace"], s.defaultNamespace, defaultNamespace)
	name := firstNonEmpty(params["application"], params["applicationName"], params["argocdApplication"], params["appKey"], target)
	if strings.Contains(name, "/") {
		parts := strings.SplitN(name, "/", 2)
		namespace = firstNonEmpty(parts[0], namespace)
		name = parts[1]
	}
	if namespace == "" {
		return applicationRef{}, fmt.Errorf("argocd namespace is required")
	}
	if name == "" {
		return applicationRef{}, fmt.Errorf("application parameter or target is required")
	}
	return applicationRef{namespace: namespace, name: name}, nil
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
	namespace := flag.String("namespace", defaultNamespace, "default Argo CD Application namespace")
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
	kaiv1alpha1.RegisterActuatorServiceServer(grpcServer, &argoActuatorServer{
		client:           client,
		defaultNamespace: *namespace,
	})
	log.Printf("argocd actuator plugin listening on %s", *listenAddr)
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
