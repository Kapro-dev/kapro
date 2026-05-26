package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
	"kapro.io/kapro/internal/cli"
)

func TestDoctorReportPassesHealthyInstall(t *testing.T) {
	c := fakeDoctorClient(t, append(healthyDoctorObjects(), validatingWebhookObjects()...)...)
	report := collectDoctorReport(context.Background(), c, doctorOptions{
		Namespace:  "kapro-system",
		Deployment: "kapro-kapro-operator",
	}, allowAllSAR)

	if report.Overall != doctorStatusPass {
		t.Fatalf("overall=%s, want pass: %#v", report.Overall, report.Checks)
	}
	for _, check := range report.Checks {
		if check.Status == doctorStatusFail {
			t.Fatalf("unexpected failing check %#v", check)
		}
	}
}

func TestDoctorReportFailsMissingCRD(t *testing.T) {
	objects := healthyDoctorObjects()
	objects = objects[1:] // drop one CRD
	c := fakeDoctorClient(t, append(objects, validatingWebhookObjects()...)...)
	report := collectDoctorReport(context.Background(), c, doctorOptions{
		Namespace:  "kapro-system",
		Deployment: "kapro-kapro-operator",
	}, allowAllSAR)

	if report.Overall != doctorStatusFail {
		t.Fatalf("overall=%s, want fail", report.Overall)
	}
	crds := findDoctorCheck(report, "crds")
	if crds.Status != doctorStatusFail || !strings.Contains(strings.Join(crds.Details, ","), "missing:") {
		t.Fatalf("CRD check did not report missing CRD: %#v", crds)
	}
}

func TestDoctorReportFailsMissingPullSecret(t *testing.T) {
	fleet := &kaprov1alpha1.Fleet{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout"},
		Spec: kaprov1alpha1.FleetSpec{
			Registry: kaprov1alpha1.KaproRegistry{URL: "oci://registry.example.com/platform", SecretRef: "registry-auth"},
			Delivery: kaprov1alpha1.SubstrateBindingSpec{
				Mode: kaprov1alpha1.SubstrateModePull,
				Ref:  "oci",
			},
			Clusters: []kaprov1alpha1.ClusterRef{{Name: "dev", Labels: map[string]string{"env": "dev"}}},
			Plan: kaprov1alpha1.KaproPlan{Stages: []kaprov1alpha1.StageSpec{{
				Name:     "dev",
				Selector: map[string]string{"env": "dev"},
			}}},
		},
	}
	c := fakeDoctorClient(t, append(append(healthyDoctorObjects(), validatingWebhookObjects()...), fleet)...)
	report := collectDoctorReport(context.Background(), c, doctorOptions{
		Namespace:  "kapro-system",
		Deployment: "kapro-kapro-operator",
	}, allowAllSAR)

	pullSecrets := findDoctorCheck(report, "pull-secrets")
	if pullSecrets.Status != doctorStatusFail || !strings.Contains(strings.Join(pullSecrets.Details, ","), "kapro-system/registry-auth") {
		t.Fatalf("pull-secret check did not report missing secret: %#v", pullSecrets)
	}
}

func TestDoctorReportHonorsTriggerSecretNamespace(t *testing.T) {
	trigger := &kaprov1alpha1.Trigger{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-trigger"},
		Spec: kaprov1alpha1.TriggerSpec{
			Source: kaprov1alpha1.TriggerSource{
				Type: "oci",
				OCI: &kaprov1alpha1.OCITriggerSource{
					Repository: "oci://registry.example.com/platform",
					TagPattern: ".*",
					SecretRef:  &corev1.SecretReference{Namespace: "delivery", Name: "registry-auth"},
				},
			},
			PromotionTemplate: kaprov1alpha1.TriggerTemplate{FleetRef: "checkout"},
		},
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "registry-auth", Namespace: "delivery"}}
	c := fakeDoctorClient(t, append(append(healthyDoctorObjects(), validatingWebhookObjects()...), trigger, secret)...)
	report := collectDoctorReport(context.Background(), c, doctorOptions{
		Namespace:  "kapro-system",
		Deployment: "kapro-kapro-operator",
	}, allowAllSAR)

	pullSecrets := findDoctorCheck(report, "pull-secrets")
	if pullSecrets.Status != doctorStatusPass {
		t.Fatalf("pull-secret check should honor trigger namespace: %#v", pullSecrets)
	}
}

func TestDoctorReportSummarizesGitOpsSubstrates(t *testing.T) {
	substrate := &kaprov1alpha1.Substrate{
		ObjectMeta: metav1.ObjectMeta{Name: "flux"},
		Spec: kaprov1alpha1.SubstrateSpec{
			ClassRef:  &kaprov1alpha1.SubstrateClassReference{Name: "flux"},
			Execution: &kaprov1alpha1.SubstrateExecutionSpec{Mode: kaprov1alpha1.ExecutionModeHubPush},
			Parameters: map[string]string{
				"namespace": "flux-system",
			},
			Discovery: &kaprov1alpha1.SubstrateDiscoverySpec{Suspended: false, ManagementPolicy: "Observe"},
		},
	}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "flux-system"}}
	c := fakeDoctorClient(t, append(append(healthyDoctorObjects(), validatingWebhookObjects()...), substrate, ns)...)
	report := collectDoctorReport(context.Background(), c, doctorOptions{
		Namespace:  "kapro-system",
		Deployment: "kapro-kapro-operator",
	}, allowAllSAR)

	substrates := findDoctorCheck(report, "gitops-substrates")
	if substrates.Status != doctorStatusPass || !strings.Contains(strings.Join(substrates.Details, ","), "substrate=flux") {
		t.Fatalf("expected passing gitops substrate summary, got %#v", substrates)
	}
}

func TestDoctorReportUsesTypedConfigNamespace(t *testing.T) {
	config := &unstructured.Unstructured{}
	config.SetGroupVersionKind(schema.GroupVersionKind{Group: "flux.substrate.kapro.io", Version: "v1alpha1", Kind: "FluxSubstrateConfig"})
	config.SetName("checkout")
	if err := unstructured.SetNestedField(config.Object, "flux-managed", "spec", "namespace"); err != nil {
		t.Fatal(err)
	}
	substrate := &kaprov1alpha1.Substrate{
		ObjectMeta: metav1.ObjectMeta{Name: "flux"},
		Spec: kaprov1alpha1.SubstrateSpec{
			ClassRef: &kaprov1alpha1.SubstrateClassReference{Name: "flux"},
			ConfigRef: &kaprov1alpha1.SubstrateObjectReference{
				APIVersion: "flux.substrate.kapro.io/v1alpha1",
				Kind:       "FluxSubstrateConfig",
				Name:       "checkout",
			},
			Execution:  &kaprov1alpha1.SubstrateExecutionSpec{Mode: kaprov1alpha1.ExecutionModeHubPush},
			Parameters: map[string]string{"namespace": "wrong-namespace"},
			Discovery:  &kaprov1alpha1.SubstrateDiscoverySpec{Suspended: false, ManagementPolicy: "Observe"},
		},
	}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "flux-managed"}}
	c := fakeDoctorClient(t, append(append(healthyDoctorObjects(), validatingWebhookObjects()...), substrate, config, ns)...)
	report := collectDoctorReport(context.Background(), c, doctorOptions{
		Namespace:  "kapro-system",
		Deployment: "kapro-kapro-operator",
	}, allowAllSAR)

	substrates := findDoctorCheck(report, "gitops-substrates")
	details := strings.Join(substrates.Details, ",")
	if substrates.Status != doctorStatusPass || !strings.Contains(details, "namespace=flux-managed") {
		t.Fatalf("expected typed config namespace in gitops substrate summary, got %#v", substrates)
	}
	if strings.Contains(details, "wrong-namespace") {
		t.Fatalf("doctor should prefer typed config namespace over parameters, got %#v", substrates)
	}
}

func TestDoctorReportWarnsOnMissingGitOpsNamespace(t *testing.T) {
	substrate := &kaprov1alpha1.Substrate{
		ObjectMeta: metav1.ObjectMeta{Name: "argo"},
		Spec: kaprov1alpha1.SubstrateSpec{
			ClassRef:  &kaprov1alpha1.SubstrateClassReference{Name: "argo"},
			Execution: &kaprov1alpha1.SubstrateExecutionSpec{Mode: kaprov1alpha1.ExecutionModeHubPush},
			Parameters: map[string]string{
				"namespace": "argocd",
			},
		},
	}
	c := fakeDoctorClient(t, append(append(healthyDoctorObjects(), validatingWebhookObjects()...), substrate)...)
	report := collectDoctorReport(context.Background(), c, doctorOptions{
		Namespace:  "kapro-system",
		Deployment: "kapro-kapro-operator",
	}, allowAllSAR)

	substrates := findDoctorCheck(report, "gitops-substrates")
	if substrates.Status != doctorStatusWarn || !strings.Contains(strings.Join(substrates.Details, ","), "missing namespace argocd") {
		t.Fatalf("expected missing namespace warning, got %#v", substrates)
	}
}

func TestDoctorReportFailsTriggerSecretWithoutNamespace(t *testing.T) {
	trigger := &kaprov1alpha1.Trigger{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-trigger"},
		Spec: kaprov1alpha1.TriggerSpec{
			Source: kaprov1alpha1.TriggerSource{
				Type: "oci",
				OCI: &kaprov1alpha1.OCITriggerSource{
					Repository: "oci://registry.example.com/platform",
					TagPattern: ".*",
					SecretRef:  &corev1.SecretReference{Name: "registry-auth"},
				},
			},
			PromotionTemplate: kaprov1alpha1.TriggerTemplate{FleetRef: "checkout"},
		},
	}
	c := fakeDoctorClient(t, append(append(healthyDoctorObjects(), validatingWebhookObjects()...), trigger)...)
	report := collectDoctorReport(context.Background(), c, doctorOptions{
		Namespace:  "kapro-system",
		Deployment: "kapro-kapro-operator",
	}, allowAllSAR)

	pullSecrets := findDoctorCheck(report, "pull-secrets")
	if pullSecrets.Status != doctorStatusFail || !strings.Contains(strings.Join(pullSecrets.Details, ","), "secretRef.namespace") {
		t.Fatalf("pull-secret check should require trigger secret namespace: %#v", pullSecrets)
	}
}

func TestRunDoctorJSONOutputIsStable(t *testing.T) {
	c := fakeDoctorClient(t, append(healthyDoctorObjects(), validatingWebhookObjects()...)...)
	prev := cli.OutputFormat
	defer func() { cli.OutputFormat = prev }()
	cli.OutputFormat = "json"

	out := withCapturedOutput(t, func() {
		err := runDoctorWithClient(context.Background(), c, doctorOptions{
			Namespace:  "kapro-system",
			Deployment: "kapro-kapro-operator",
		}, allowAllSAR)
		if err != nil {
			t.Fatalf("runDoctorWithClient: %v", err)
		}
	})

	var report doctorReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("json unmarshal: %v\nraw: %s", err, out)
	}
	if report.Overall != doctorStatusPass || len(report.Checks) == 0 {
		t.Fatalf("unexpected report: %#v", report)
	}
}

func TestCheckOperatorRBACReportsDeniedAccess(t *testing.T) {
	deploy := readyDoctorDeployment()
	report := checkOperatorRBAC(context.Background(), func(_ context.Context, _ string, attrs authv1.ResourceAttributes) (bool, string, error) {
		if attrs.Resource == "targets" && attrs.Verb == "create" {
			return false, "missing test grant", nil
		}
		return true, "", nil
	}, "kapro-system", deploy)

	if report.Status != doctorStatusFail || !strings.Contains(strings.Join(report.Details, ","), "missing test grant") {
		t.Fatalf("expected denied SAR detail, got %#v", report)
	}
}

func fakeDoctorClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	return fake.NewClientBuilder().
		WithScheme(diagTestSchemeWithExtensions(t)).
		WithObjects(objects...).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				createOpts := &client.CreateOptions{}
				createOpts.ApplyOptions(opts)
				if _, ok := obj.(*kaprov1alpha1.Plan); ok && len(createOpts.DryRun) > 0 {
					return apierrors.NewInvalid(
						schema.GroupKind{Group: "kapro.io", Kind: "Plan"},
						obj.GetName(),
						field.ErrorList{field.Duplicate(field.NewPath("spec", "stages").Index(1).Child("name"), "duplicate")},
					)
				}
				return c.Create(ctx, obj, opts...)
			},
		}).
		Build()
}

func diagTestSchemeWithExtensions(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := diagTestScheme(t)
	if err := apiextensionsv1.AddToScheme(s); err != nil {
		t.Fatalf("apiextensions AddToScheme: %v", err)
	}
	return s
}

func healthyDoctorObjects() []client.Object {
	objects := make([]client.Object, 0, len(expectedKaproCRDs)+3)
	for _, name := range expectedKaproCRDs {
		objects = append(objects, establishedDoctorCRD(name))
	}
	objects = append(objects, readyDoctorDeployment(), &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "kapro-kapro-operator-webhook", Namespace: "kapro-system"},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{Name: "webhook", Port: 443, TargetPort: intstr.FromInt(9443)}},
		},
	})
	return objects
}

func establishedDoctorCRD(name string) *apiextensionsv1.CustomResourceDefinition {
	plural, group, _ := strings.Cut(name, ".")
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: group,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural: plural,
				Kind:   "DoctorKind",
			},
			Scope: apiextensionsv1.ClusterScoped,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{
				Name:    "v1alpha1",
				Served:  true,
				Storage: true,
			}},
		},
		Status: apiextensionsv1.CustomResourceDefinitionStatus{
			Conditions: []apiextensionsv1.CustomResourceDefinitionCondition{
				{Type: apiextensionsv1.Established, Status: apiextensionsv1.ConditionTrue},
				{Type: apiextensionsv1.NamesAccepted, Status: apiextensionsv1.ConditionTrue},
			},
		},
	}
}

func readyDoctorDeployment() *appsv1.Deployment {
	replicas := int32(1)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "kapro-kapro-operator", Namespace: "kapro-system", Generation: 1},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{ServiceAccountName: "kapro-kapro-operator"}},
		},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 1,
			ReadyReplicas:      1,
			Conditions: []appsv1.DeploymentCondition{{
				Type:   appsv1.DeploymentAvailable,
				Status: corev1.ConditionTrue,
			}},
		},
	}
}

func validatingWebhookObjects() []client.Object {
	path := "/validate-kapro-io-v1alpha1-plan"
	return []client.Object{&admissionv1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "kapro-kapro-operator"},
		Webhooks: []admissionv1.ValidatingWebhook{{
			Name: "validate.plan.kapro.io",
			Rules: []admissionv1.RuleWithOperations{{
				Rule: admissionv1.Rule{
					APIGroups:   []string{"kapro.io"},
					APIVersions: []string{"v1alpha1"},
					Resources:   []string{"plans"},
				},
				Operations: []admissionv1.OperationType{admissionv1.Create, admissionv1.Update},
			}},
			ClientConfig: admissionv1.WebhookClientConfig{
				Service: &admissionv1.ServiceReference{
					Namespace: "kapro-system",
					Name:      "kapro-kapro-operator-webhook",
					Path:      &path,
				},
			},
		}, {
			Name: "validate.unrelated.example.io",
			Rules: []admissionv1.RuleWithOperations{{
				Rule: admissionv1.Rule{
					APIGroups:   []string{"example.io"},
					APIVersions: []string{"v1"},
					Resources:   []string{"widgets"},
				},
				Operations: []admissionv1.OperationType{admissionv1.Create},
			}},
			ClientConfig: admissionv1.WebhookClientConfig{
				Service: &admissionv1.ServiceReference{
					Namespace: "missing",
					Name:      "unrelated-webhook",
				},
			},
		}},
	}}
}

func allowAllSAR(context.Context, string, authv1.ResourceAttributes) (bool, string, error) {
	return true, "", nil
}

func findDoctorCheck(report doctorReport, name string) doctorFinding {
	for _, check := range report.Checks {
		if check.Name == name {
			return check
		}
	}
	return doctorFinding{}
}
