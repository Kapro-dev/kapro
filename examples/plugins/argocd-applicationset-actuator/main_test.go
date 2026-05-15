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
	client := newTestClient(t, newServerWithObjects(t, "argocd", "checkout-set", "checkout-prod", "old", "Synced", "Healthy"))
	scenario := actuatorconformance.DefaultScenario()
	scenario.Apply.Parameters["argocdNamespace"] = "argocd"
	scenario.Apply.Parameters["applicationset"] = "checkout-set"
	scenario.Rollback.Parameters["argocdNamespace"] = "argocd"
	scenario.Rollback.Parameters["applicationset"] = "checkout-set"
	scenario.IsConverged.Parameters["argocdNamespace"] = "argocd"
	scenario.IsConverged.Parameters["applicationset"] = "checkout-set"
	scenario.IsConverged.Parameters["generatedApplication"] = "checkout-prod"

	actuatorconformance.Run(t, client, scenario)
}

func TestApplyPatchesApplicationSetTemplateTargetRevision(t *testing.T) {
	server := newServerWithObjects(t, "argocd", "checkout-set", "checkout-prod", "old", "OutOfSync", "Progressing")
	_, err := server.Apply(context.Background(), &kaiv1alpha1.ApplyRequest{
		Target:  "prod-eu",
		Version: "v1.2.3",
		Parameters: map[string]string{
			"argocdNamespace": "argocd",
			"applicationset":  "checkout-set",
		},
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	appSet := getApplicationSet(t, server, "argocd", "checkout-set")
	got, _, _ := unstructured.NestedString(appSet.Object, "spec", "template", "spec", "source", "targetRevision")
	if got != "v1.2.3" {
		t.Fatalf("targetRevision=%q, want v1.2.3", got)
	}
}

func TestRollbackPatchesPreviousVersion(t *testing.T) {
	server := newServerWithObjects(t, "argocd", "checkout-set", "checkout-prod", "v1.2.3", "Synced", "Healthy")
	_, err := server.Rollback(context.Background(), &kaiv1alpha1.RollbackRequest{
		Target:          "prod-eu",
		Version:         "v1.2.3",
		PreviousVersion: "v1.2.2",
		Parameters: map[string]string{
			"argocdNamespace": "argocd",
			"applicationset":  "checkout-set",
		},
	})
	if err != nil {
		t.Fatalf("Rollback returned error: %v", err)
	}

	appSet := getApplicationSet(t, server, "argocd", "checkout-set")
	got, _, _ := unstructured.NestedString(appSet.Object, "spec", "template", "spec", "source", "targetRevision")
	if got != "v1.2.2" {
		t.Fatalf("targetRevision=%q, want v1.2.2", got)
	}
}

func TestIsConvergedRequiresApplicationSetTemplateGeneratedRevisionSyncAndHealth(t *testing.T) {
	tests := []struct {
		name             string
		templateRevision string
		appRevision      string
		syncStatus       string
		healthStatus     string
		want             bool
	}{
		{name: "ready", templateRevision: "v1.2.3", appRevision: "v1.2.3", syncStatus: "Synced", healthStatus: "Healthy", want: true},
		{name: "template not updated", templateRevision: "v1.2.2", appRevision: "v1.2.3", syncStatus: "Synced", healthStatus: "Healthy", want: false},
		{name: "generated app not updated", templateRevision: "v1.2.3", appRevision: "v1.2.2", syncStatus: "Synced", healthStatus: "Healthy", want: false},
		{name: "out of sync", templateRevision: "v1.2.3", appRevision: "v1.2.3", syncStatus: "OutOfSync", healthStatus: "Healthy", want: false},
		{name: "unhealthy", templateRevision: "v1.2.3", appRevision: "v1.2.3", syncStatus: "Synced", healthStatus: "Degraded", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newServerWithObjects(t, "argocd", "checkout-set", "checkout-prod", tt.templateRevision, tt.syncStatus, tt.healthStatus)
			setGeneratedApplicationRevision(t, server, "argocd", "checkout-prod", tt.appRevision)
			resp, err := server.IsConverged(context.Background(), &kaiv1alpha1.IsConvergedRequest{
				Target:  "prod-eu",
				Version: "v1.2.3",
				Parameters: map[string]string{
					"argocdNamespace":      "argocd",
					"applicationset":       "checkout-set",
					"generatedApplication": "checkout-prod",
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

func TestApplicationSetParameterCanIncludeNamespace(t *testing.T) {
	server := newServerWithObjects(t, "argocd-prod", "checkout-set", "checkout-prod", "old", "Synced", "Healthy")
	_, err := server.Apply(context.Background(), &kaiv1alpha1.ApplyRequest{
		Target:  "prod-eu",
		Version: "v1.2.3",
		Parameters: map[string]string{
			"applicationset": "argocd-prod/checkout-set",
		},
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	appSet := getApplicationSet(t, server, "argocd-prod", "checkout-set")
	got, _, _ := unstructured.NestedString(appSet.Object, "spec", "template", "spec", "source", "targetRevision")
	if got != "v1.2.3" {
		t.Fatalf("targetRevision=%q, want v1.2.3", got)
	}
}

func newServerWithObjects(t *testing.T, namespace, applicationSet, generatedApplication, revision, syncStatus, healthStatus string) *argoApplicationSetActuatorServer {
	t.Helper()
	scheme := runtime.NewScheme()
	client := dynamicfake.NewSimpleDynamicClient(
		scheme,
		newApplicationSet(namespace, applicationSet, revision),
		newApplication(namespace, generatedApplication, revision, syncStatus, healthStatus),
	)
	return &argoApplicationSetActuatorServer{
		client:           client,
		defaultNamespace: defaultNamespace,
	}
}

func newApplicationSet(namespace, name, revision string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "argoproj.io/v1alpha1",
			"kind":       "ApplicationSet",
			"metadata": map[string]any{
				"namespace": namespace,
				"name":      name,
			},
			"spec": map[string]any{
				"template": map[string]any{
					"spec": map[string]any{
						"source": map[string]any{
							"targetRevision": revision,
						},
					},
				},
			},
		},
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

func getApplicationSet(t *testing.T, server *argoApplicationSetActuatorServer, namespace, name string) *unstructured.Unstructured {
	t.Helper()
	appSet, err := server.client.Resource(applicationSetGVR).Namespace(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get ApplicationSet: %v", err)
	}
	return appSet
}

func setGeneratedApplicationRevision(t *testing.T, server *argoApplicationSetActuatorServer, namespace, name, revision string) {
	t.Helper()
	app, err := server.client.Resource(applicationGVR).Namespace(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get generated Application: %v", err)
	}
	if err := unstructured.SetNestedField(app.Object, revision, "spec", "source", "targetRevision"); err != nil {
		t.Fatalf("set generated Application targetRevision: %v", err)
	}
	if _, err := server.client.Resource(applicationGVR).Namespace(namespace).Update(context.Background(), app, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update generated Application: %v", err)
	}
}

func newTestClient(t *testing.T, server *argoApplicationSetActuatorServer) kaiv1alpha1.ActuatorServiceClient {
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
