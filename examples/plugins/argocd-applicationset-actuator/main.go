package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"strings"

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
	contractVersion = "v1alpha1"
	pluginVersion   = "0.1.0"

	defaultListenAddr = ":9090"
	defaultNamespace  = "argocd"
)

var (
	applicationSetGVR = schema.GroupVersionResource{
		Group:    "argoproj.io",
		Version:  "v1alpha1",
		Resource: "applicationsets",
	}
	applicationGVR = schema.GroupVersionResource{
		Group:    "argoproj.io",
		Version:  "v1alpha1",
		Resource: "applications",
	}
)

type argoApplicationSetActuatorServer struct {
	kaiv1alpha1.UnimplementedActuatorServiceServer

	client           dynamic.Interface
	defaultNamespace string
}

func (s *argoApplicationSetActuatorServer) GetCapabilities(context.Context, *kaiv1alpha1.GetCapabilitiesRequest) (*kaiv1alpha1.GetCapabilitiesResponse, error) {
	return &kaiv1alpha1.GetCapabilitiesResponse{
		ContractVersion: contractVersion,
		PluginVersion:   pluginVersion,
		Capabilities: []string{
			"argocd.applicationset.template.targetRevision.apply",
			"argocd.generated-application.sync-health.convergence",
			"argocd.applicationset.template.targetRevision.rollback",
		},
	}, nil
}

func (s *argoApplicationSetActuatorServer) Apply(ctx context.Context, req *kaiv1alpha1.ApplyRequest) (*kaiv1alpha1.ApplyResponse, error) {
	if req.GetVersion() == "" {
		return nil, fmt.Errorf("version is required")
	}
	ref, err := s.applicationSetRef(req.GetTarget(), req.GetParameters())
	if err != nil {
		return nil, err
	}
	if err := s.setTemplateTargetRevision(ctx, ref, req.GetVersion()); err != nil {
		return nil, err
	}
	return &kaiv1alpha1.ApplyResponse{
		Accepted: true,
		Message:  fmt.Sprintf("patched Argo CD ApplicationSet %s/%s template to targetRevision %s", ref.namespace, ref.name, req.GetVersion()),
	}, nil
}

func (s *argoApplicationSetActuatorServer) IsConverged(ctx context.Context, req *kaiv1alpha1.IsConvergedRequest) (*kaiv1alpha1.IsConvergedResponse, error) {
	if req.GetVersion() == "" {
		return nil, fmt.Errorf("version is required")
	}
	setRef, err := s.applicationSetRef(req.GetTarget(), req.GetParameters())
	if err != nil {
		return nil, err
	}
	appSet, err := s.client.Resource(applicationSetGVR).Namespace(setRef.namespace).Get(ctx, setRef.name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get Argo CD ApplicationSet %s/%s: %w", setRef.namespace, setRef.name, err)
	}
	templateRevision, _, _ := unstructured.NestedString(appSet.Object, "spec", "template", "spec", "source", "targetRevision")
	if templateRevision != req.GetVersion() {
		return &kaiv1alpha1.IsConvergedResponse{
			Converged: false,
			Message:   fmt.Sprintf("ApplicationSet template targetRevision=%q, want %q", templateRevision, req.GetVersion()),
		}, nil
	}

	appRef, err := s.generatedApplicationRef(req.GetTarget(), req.GetParameters())
	if err != nil {
		return nil, err
	}
	app, err := s.client.Resource(applicationGVR).Namespace(appRef.namespace).Get(ctx, appRef.name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get generated Argo CD Application %s/%s: %w", appRef.namespace, appRef.name, err)
	}
	appRevision, _, _ := unstructured.NestedString(app.Object, "spec", "source", "targetRevision")
	if appRevision != req.GetVersion() {
		return &kaiv1alpha1.IsConvergedResponse{
			Converged: false,
			Message:   fmt.Sprintf("generated Application targetRevision=%q, want %q", appRevision, req.GetVersion()),
		}, nil
	}
	syncStatus, _, _ := unstructured.NestedString(app.Object, "status", "sync", "status")
	healthStatus, _, _ := unstructured.NestedString(app.Object, "status", "health", "status")
	if syncStatus == "Synced" && healthStatus == "Healthy" {
		return &kaiv1alpha1.IsConvergedResponse{
			Converged: true,
			Message:   "generated application is synced and healthy",
		}, nil
	}
	return &kaiv1alpha1.IsConvergedResponse{
		Converged: false,
		Message:   fmt.Sprintf("generated Application sync=%q health=%q", syncStatus, healthStatus),
	}, nil
}

func (s *argoApplicationSetActuatorServer) Rollback(ctx context.Context, req *kaiv1alpha1.RollbackRequest) (*kaiv1alpha1.RollbackResponse, error) {
	if req.GetPreviousVersion() == "" {
		return nil, fmt.Errorf("previousVersion is required")
	}
	ref, err := s.applicationSetRef(req.GetTarget(), req.GetParameters())
	if err != nil {
		return nil, err
	}
	if err := s.setTemplateTargetRevision(ctx, ref, req.GetPreviousVersion()); err != nil {
		return nil, err
	}
	return &kaiv1alpha1.RollbackResponse{
		Accepted: true,
		Message:  fmt.Sprintf("patched Argo CD ApplicationSet %s/%s template to previous targetRevision %s", ref.namespace, ref.name, req.GetPreviousVersion()),
	}, nil
}

func (s *argoApplicationSetActuatorServer) setTemplateTargetRevision(ctx context.Context, ref resourceRef, revision string) error {
	patch, err := json.Marshal(map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{
					"source": map[string]any{
						"targetRevision": revision,
					},
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("marshal targetRevision patch: %w", err)
	}
	if _, err := s.client.Resource(applicationSetGVR).Namespace(ref.namespace).Patch(ctx, ref.name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("patch Argo CD ApplicationSet %s/%s: %w", ref.namespace, ref.name, err)
	}
	return nil
}

type resourceRef struct {
	namespace string
	name      string
}

func (s *argoApplicationSetActuatorServer) applicationSetRef(target string, params map[string]string) (resourceRef, error) {
	return resourceRefFromParams(
		firstNonEmpty(params["argocdNamespace"], params["namespace"], s.defaultNamespace, defaultNamespace),
		firstNonEmpty(params["applicationset"], params["applicationSet"], params["applicationSetName"], params["appKey"], target),
		"applicationset parameter or target is required",
	)
}

func (s *argoApplicationSetActuatorServer) generatedApplicationRef(target string, params map[string]string) (resourceRef, error) {
	return resourceRefFromParams(
		firstNonEmpty(params["argocdNamespace"], params["namespace"], s.defaultNamespace, defaultNamespace),
		firstNonEmpty(params["generatedApplication"], params["application"], params["applicationName"], params["argocdApplication"], params["appKey"], target),
		"generatedApplication parameter or target is required",
	)
}

func resourceRefFromParams(namespace, name, emptyNameMessage string) (resourceRef, error) {
	if strings.Contains(name, "/") {
		parts := strings.SplitN(name, "/", 2)
		namespace = firstNonEmpty(parts[0], namespace)
		name = parts[1]
	}
	if namespace == "" {
		return resourceRef{}, fmt.Errorf("argocd namespace is required")
	}
	if name == "" {
		return resourceRef{}, fmt.Errorf("%s", emptyNameMessage)
	}
	return resourceRef{namespace: namespace, name: name}, nil
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
	namespace := flag.String("namespace", defaultNamespace, "default Argo CD namespace")
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
	kaiv1alpha1.RegisterActuatorServiceServer(grpcServer, &argoApplicationSetActuatorServer{
		client:           client,
		defaultNamespace: *namespace,
	})
	log.Printf("argocd applicationset actuator plugin listening on %s", *listenAddr)
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
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{}).ClientConfig()
}
