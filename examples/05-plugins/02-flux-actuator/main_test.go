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
	client := newTestClient(t, newServerWithHelmRelease(t, "flux-system", "checkout", "1.0.0", "True"))
	scenario := actuatorconformance.DefaultScenario()
	scenario.Apply.Parameters["fluxNamespace"] = "flux-system"
	scenario.Apply.Parameters["helmRelease"] = "checkout"
	scenario.IsConverged.Parameters["fluxNamespace"] = "flux-system"
	scenario.IsConverged.Parameters["helmRelease"] = "checkout"
	scenario.Rollback.Parameters["fluxNamespace"] = "flux-system"
	scenario.Rollback.Parameters["helmRelease"] = "checkout"

	actuatorconformance.Run(t, client, scenario)
}

func TestApplyPatchesHelmReleaseChartVersion(t *testing.T) {
	server := newServerWithHelmRelease(t, "flux-system", "checkout", "1.0.0", "False")
	_, err := server.Apply(context.Background(), &kaiv1alpha1.ApplyRequest{
		Target:  "prod-eu",
		Version: "1.2.3",
		Parameters: map[string]string{
			"fluxNamespace": "flux-system",
			"helmRelease":   "checkout",
		},
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	hr := getHelmRelease(t, server, "flux-system", "checkout")
	got, _, _ := unstructured.NestedString(hr.Object, "spec", "chart", "spec", "version")
	if got != "1.2.3" {
		t.Fatalf("chart version=%q, want 1.2.3", got)
	}
}

func TestRollbackPatchesPreviousVersion(t *testing.T) {
	server := newServerWithHelmRelease(t, "flux-system", "checkout", "1.2.3", "True")
	_, err := server.Rollback(context.Background(), &kaiv1alpha1.RollbackRequest{
		Target:          "prod-eu",
		Version:         "1.2.3",
		PreviousVersion: "1.2.2",
		Parameters: map[string]string{
			"fluxNamespace": "flux-system",
			"helmRelease":   "checkout",
		},
	})
	if err != nil {
		t.Fatalf("Rollback returned error: %v", err)
	}

	hr := getHelmRelease(t, server, "flux-system", "checkout")
	got, _, _ := unstructured.NestedString(hr.Object, "spec", "chart", "spec", "version")
	if got != "1.2.2" {
		t.Fatalf("chart version=%q, want 1.2.2", got)
	}
}

func TestIsConvergedRequiresChartVersionAndReadyCondition(t *testing.T) {
	tests := []struct {
		name    string
		version string
		ready   string
		want    bool
	}{
		{name: "ready", version: "1.2.3", ready: "True", want: true},
		{name: "wrong version", version: "1.2.2", ready: "True", want: false},
		{name: "not ready", version: "1.2.3", ready: "False", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newServerWithHelmRelease(t, "flux-system", "checkout", tt.version, tt.ready)
			resp, err := server.IsConverged(context.Background(), &kaiv1alpha1.IsConvergedRequest{
				Target:  "prod-eu",
				Version: "1.2.3",
				Parameters: map[string]string{
					"fluxNamespace": "flux-system",
					"helmRelease":   "checkout",
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

func TestHelmReleaseParameterCanIncludeNamespace(t *testing.T) {
	server := newServerWithHelmRelease(t, "team-a", "checkout", "1.0.0", "True")
	_, err := server.Apply(context.Background(), &kaiv1alpha1.ApplyRequest{
		Target:  "prod-eu",
		Version: "1.2.3",
		Parameters: map[string]string{
			"helmRelease": "team-a/checkout",
		},
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	hr := getHelmRelease(t, server, "team-a", "checkout")
	got, _, _ := unstructured.NestedString(hr.Object, "spec", "chart", "spec", "version")
	if got != "1.2.3" {
		t.Fatalf("chart version=%q, want 1.2.3", got)
	}
}

func newServerWithHelmRelease(t *testing.T, namespace, name, version, ready string) *fluxActuatorServer {
	t.Helper()
	scheme := runtime.NewScheme()
	client := dynamicfake.NewSimpleDynamicClient(scheme, newHelmRelease(namespace, name, version, ready))
	return &fluxActuatorServer{
		client:           client,
		defaultNamespace: defaultNamespace,
	}
}

func newHelmRelease(namespace, name, version, ready string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "helm.toolkit.fluxcd.io/v2",
			"kind":       "HelmRelease",
			"metadata": map[string]any{
				"namespace": namespace,
				"name":      name,
			},
			"spec": map[string]any{
				"chart": map[string]any{
					"spec": map[string]any{
						"version": version,
					},
				},
			},
			"status": map[string]any{
				"conditions": []any{
					map[string]any{
						"type":   "Ready",
						"status": ready,
					},
				},
			},
		},
	}
}

func getHelmRelease(t *testing.T, server *fluxActuatorServer, namespace, name string) *unstructured.Unstructured {
	t.Helper()
	hr, err := server.client.Resource(helmReleaseGVR).Namespace(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get HelmRelease: %v", err)
	}
	return hr
}

func newTestClient(t *testing.T, server *fluxActuatorServer) kaiv1alpha1.ActuatorServiceClient {
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
