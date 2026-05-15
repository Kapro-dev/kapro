package main

import (
	"context"
	"net"
	"testing"

	actuatorconformance "kapro.io/kapro/conformance/actuator"
	kaiv1alpha1 "kapro.io/kapro/spec/kai/v1alpha1"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestKAIConformance(t *testing.T) {
	client := newTestClient(t, newServerWithApp(t, "argocd", "checkout", "old", "Synced", "Healthy"))
	scenario := actuatorconformance.DefaultScenario()
	scenario.Apply.Parameters["argocdNamespace"] = "argocd"
	scenario.Apply.Parameters["application"] = "checkout"
	scenario.IsConverged.Parameters["argocdNamespace"] = "argocd"
	scenario.IsConverged.Parameters["application"] = "checkout"
	scenario.Rollback.Parameters["argocdNamespace"] = "argocd"
	scenario.Rollback.Parameters["application"] = "checkout"

	actuatorconformance.Run(t, client, scenario)
}

func TestApplyPatchesApplicationTargetRevision(t *testing.T) {
	server := newServerWithApp(t, "argocd", "checkout", "old", "OutOfSync", "Progressing")
	_, err := server.Apply(context.Background(), &kaiv1alpha1.ApplyRequest{
		Target:  "prod-eu",
		Version: "v1.2.3",
		Parameters: map[string]string{
			"argocdNamespace": "argocd",
			"application":     "checkout",
		},
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	app := getApplication(t, server, "argocd", "checkout")
	got, _, _ := unstructured.NestedString(app.Object, "spec", "source", "targetRevision")
	if got != "v1.2.3" {
		t.Fatalf("targetRevision=%q, want v1.2.3", got)
	}
}

func TestRollbackPatchesPreviousVersion(t *testing.T) {
	server := newServerWithApp(t, "argocd", "checkout", "v1.2.3", "Synced", "Healthy")
	_, err := server.Rollback(context.Background(), &kaiv1alpha1.RollbackRequest{
		Target:          "prod-eu",
		Version:         "v1.2.3",
		PreviousVersion: "v1.2.2",
		Parameters: map[string]string{
			"argocdNamespace": "argocd",
			"application":     "checkout",
		},
	})
	if err != nil {
		t.Fatalf("Rollback returned error: %v", err)
	}

	app := getApplication(t, server, "argocd", "checkout")
	got, _, _ := unstructured.NestedString(app.Object, "spec", "source", "targetRevision")
	if got != "v1.2.2" {
		t.Fatalf("targetRevision=%q, want v1.2.2", got)
	}
}

func TestIsConvergedRequiresTargetRevisionSyncAndHealth(t *testing.T) {
	tests := []struct {
		name           string
		targetRevision string
		syncStatus     string
		healthStatus   string
		want           bool
	}{
		{name: "ready", targetRevision: "v1.2.3", syncStatus: "Synced", healthStatus: "Healthy", want: true},
		{name: "wrong revision", targetRevision: "v1.2.2", syncStatus: "Synced", healthStatus: "Healthy", want: false},
		{name: "out of sync", targetRevision: "v1.2.3", syncStatus: "OutOfSync", healthStatus: "Healthy", want: false},
		{name: "unhealthy", targetRevision: "v1.2.3", syncStatus: "Synced", healthStatus: "Degraded", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newServerWithApp(t, "argocd", "checkout", tt.targetRevision, tt.syncStatus, tt.healthStatus)
			resp, err := server.IsConverged(context.Background(), &kaiv1alpha1.IsConvergedRequest{
				Target:  "prod-eu",
				Version: "v1.2.3",
				Parameters: map[string]string{
					"argocdNamespace": "argocd",
					"application":     "checkout",
				},
			})
			if err != nil {
				t.Fatalf("IsConverged returned error: %v", err)
			}
			if resp.GetConverged() != tt.want {
				t.Fatalf("converged=%v, want %v, message=%q", resp.GetConverged(), tt.want, resp.GetMessage())
			}
		})
	}
}

func TestApplicationParameterCanIncludeNamespace(t *testing.T) {
	server := newServerWithApp(t, "argocd-prod", "checkout", "old", "Synced", "Healthy")
	_, err := server.Apply(context.Background(), &kaiv1alpha1.ApplyRequest{
		Target:  "prod-eu",
		Version: "v1.2.3",
		Parameters: map[string]string{
			"application": "argocd-prod/checkout",
		},
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	app := getApplication(t, server, "argocd-prod", "checkout")
	got, _, _ := unstructured.NestedString(app.Object, "spec", "source", "targetRevision")
	if got != "v1.2.3" {
		t.Fatalf("targetRevision=%q, want v1.2.3", got)
	}
}

func newServerWithApp(t *testing.T, namespace, name, revision, syncStatus, healthStatus string) *argoActuatorServer {
	t.Helper()
	scheme := runtime.NewScheme()
	client := dynamicfake.NewSimpleDynamicClient(scheme, newApplication(namespace, name, revision, syncStatus, healthStatus))
	return &argoActuatorServer{
		client:           client,
		defaultNamespace: defaultNamespace,
	}
}

func newApplication(namespace, name, revision, syncStatus, healthStatus string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "argoproj.io/v1alpha1",
			"kind":       "Application",
			"metadata": map[string]any{
				"namespace": namespace,
				"name":      name,
			},
			"spec": map[string]any{
				"source": map[string]any{
					"targetRevision": revision,
				},
			},
			"status": map[string]any{
				"sync": map[string]any{
					"status": syncStatus,
				},
				"health": map[string]any{
					"status": healthStatus,
				},
			},
		},
	}
}

func getApplication(t *testing.T, server *argoActuatorServer, namespace, name string) *unstructured.Unstructured {
	t.Helper()
	app, err := server.client.Resource(applicationGVR).Namespace(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get Application: %v", err)
	}
	return app
}

func newTestClient(t *testing.T, server *argoActuatorServer) kaiv1alpha1.ActuatorServiceClient {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	kaiv1alpha1.RegisterActuatorServiceServer(grpcServer, server)
	go func() {
		_ = grpcServer.Serve(listener)
	}()
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
	})

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial bufconn: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
	})
	return kaiv1alpha1.NewActuatorServiceClient(conn)
}
