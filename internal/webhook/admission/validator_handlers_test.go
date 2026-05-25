package admission_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	ctrladmission "sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
	"kapro.io/kapro/internal/webhook/admission"
)

func TestApprovalValidatorHandle(t *testing.T) {
	validator := admission.NewApprovalValidator(newKaproAdmissionDecoder(t))

	valid := kaproAdmissionApproval("rel-1", "ref-a")
	resp := validator.Handle(context.Background(), admissionRequest(t, admissionv1.Create, valid))
	if !resp.Allowed {
		t.Fatalf("expected valid approval to be allowed, got %s", responseMessage(resp))
	}

	invalid := kaproAdmissionApproval("rel-1", "ref-a")
	invalid.Name = "wrong-name"
	resp = validator.Handle(context.Background(), admissionRequest(t, admissionv1.Create, invalid))
	if resp.Allowed || !strings.Contains(responseMessage(resp), "approval.metadata.name") {
		t.Fatalf("expected approval name denial, allowed=%t message=%q", resp.Allowed, responseMessage(resp))
	}
}

func TestPromotionPlanValidatorHandleRequiresTeamLabelOnlyOnCreate(t *testing.T) {
	validator := admission.NewPromotionPlanValidator(newKaproAdmissionDecoder(t))
	plan := kaproAdmissionPlan()

	resp := validator.Handle(context.Background(), admissionRequest(t, admissionv1.Create, plan))
	if resp.Allowed || !strings.Contains(responseMessage(resp), admission.LabelKaproTeam) {
		t.Fatalf("expected create without team label to be denied, allowed=%t message=%q", resp.Allowed, responseMessage(resp))
	}

	plan.Labels = map[string]string{admission.LabelKaproTeam: "checkout"}
	resp = validator.Handle(context.Background(), admissionRequest(t, admissionv1.Create, plan))
	if !resp.Allowed {
		t.Fatalf("expected create with team label to be allowed, got %s", responseMessage(resp))
	}

	updatePlan := kaproAdmissionPlan()
	resp = validator.Handle(context.Background(), admissionRequest(t, admissionv1.Update, updatePlan))
	if !resp.Allowed {
		t.Fatalf("expected update without team label to be allowed, got %s", responseMessage(resp))
	}
}

func TestPromotionTriggerValidatorHandleRequiresTeamLabelOnlyOnCreate(t *testing.T) {
	validator := admission.NewPromotionTriggerValidator(newKaproAdmissionDecoder(t))
	trigger := kaproAdmissionTrigger()

	resp := validator.Handle(context.Background(), admissionRequest(t, admissionv1.Create, trigger))
	if resp.Allowed || !strings.Contains(responseMessage(resp), admission.LabelKaproTeam) {
		t.Fatalf("expected create without team label to be denied, allowed=%t message=%q", resp.Allowed, responseMessage(resp))
	}

	trigger.Labels = map[string]string{admission.LabelKaproTeam: "checkout"}
	resp = validator.Handle(context.Background(), admissionRequest(t, admissionv1.Create, trigger))
	if !resp.Allowed {
		t.Fatalf("expected create with team label to be allowed, got %s", responseMessage(resp))
	}

	updateTrigger := kaproAdmissionTrigger()
	resp = validator.Handle(context.Background(), admissionRequest(t, admissionv1.Update, updateTrigger))
	if !resp.Allowed {
		t.Fatalf("expected update without team label to be allowed, got %s", responseMessage(resp))
	}
}

func TestPromotionTriggerValidatorHandleDeniesInvalidSource(t *testing.T) {
	validator := admission.NewPromotionTriggerValidator(newKaproAdmissionDecoder(t))
	trigger := kaproAdmissionTrigger()
	trigger.Labels = map[string]string{admission.LabelKaproTeam: "checkout"}
	trigger.Spec.Source.Type = "git"

	resp := validator.Handle(context.Background(), admissionRequest(t, admissionv1.Create, trigger))
	if resp.Allowed || !strings.Contains(responseMessage(resp), "unsupported") {
		t.Fatalf("expected unsupported trigger source denial, allowed=%t message=%q", resp.Allowed, responseMessage(resp))
	}
}

func TestPromotionTriggerValidatorHandleDeniesMissingDeliveryUnitRef(t *testing.T) {
	validator := admission.NewPromotionTriggerValidator(newKaproAdmissionDecoder(t))
	trigger := kaproAdmissionTrigger()
	trigger.Labels = map[string]string{admission.LabelKaproTeam: "checkout"}
	trigger.Spec.PromotionTemplate.DeliveryUnitRef = ""

	resp := validator.Handle(context.Background(), admissionRequest(t, admissionv1.Create, trigger))
	if resp.Allowed || !strings.Contains(responseMessage(resp), "spec.promotionTemplate.deliveryUnitRef") {
		t.Fatalf("expected missing deliveryUnitRef denial, allowed=%t message=%q", resp.Allowed, responseMessage(resp))
	}
}

func TestPromotionTriggerValidatorHandleDeniesInvalidDurations(t *testing.T) {
	validator := admission.NewPromotionTriggerValidator(newKaproAdmissionDecoder(t))
	trigger := kaproAdmissionTrigger()
	trigger.Labels = map[string]string{admission.LabelKaproTeam: "checkout"}
	trigger.Spec.Cooldown = "soon"

	resp := validator.Handle(context.Background(), admissionRequest(t, admissionv1.Create, trigger))
	if resp.Allowed || !strings.Contains(responseMessage(resp), "spec.cooldown") {
		t.Fatalf("expected invalid cooldown denial, allowed=%t message=%q", resp.Allowed, responseMessage(resp))
	}

	trigger = kaproAdmissionTrigger()
	trigger.Labels = map[string]string{admission.LabelKaproTeam: "checkout"}
	trigger.Spec.Source.OCI.PollInterval = "0s"
	resp = validator.Handle(context.Background(), admissionRequest(t, admissionv1.Create, trigger))
	if resp.Allowed || !strings.Contains(responseMessage(resp), "spec.source.oci.pollInterval") {
		t.Fatalf("expected invalid poll interval denial, allowed=%t message=%q", resp.Allowed, responseMessage(resp))
	}
}

func TestPromotionTriggerValidatorHandleDeniesInvalidMaxActive(t *testing.T) {
	validator := admission.NewPromotionTriggerValidator(newKaproAdmissionDecoder(t))
	trigger := kaproAdmissionTrigger()
	trigger.Labels = map[string]string{admission.LabelKaproTeam: "checkout"}
	trigger.Spec.MaxActive = -1

	resp := validator.Handle(context.Background(), admissionRequest(t, admissionv1.Create, trigger))
	if resp.Allowed || !strings.Contains(responseMessage(resp), "spec.maxActive") {
		t.Fatalf("expected invalid maxActive denial, allowed=%t message=%q", resp.Allowed, responseMessage(resp))
	}
}

func TestDeliveryUnitValidatorHandleDeniesInvalidTriggerDuration(t *testing.T) {
	validator := admission.NewDeliveryUnitValidator(newKaproAdmissionDecoder(t), nil)
	unit := kaproAdmissionDeliveryUnit()
	unit.Spec.Triggers[0].Cooldown = "-1m"

	resp := validator.Handle(context.Background(), admissionRequest(t, admissionv1.Create, unit))
	if resp.Allowed || !strings.Contains(responseMessage(resp), "spec.cooldown") {
		t.Fatalf("expected embedded trigger cooldown denial, allowed=%t message=%q", resp.Allowed, responseMessage(resp))
	}
}

func TestDeliveryUnitValidatorHandleDeniesWhitespaceUnitNames(t *testing.T) {
	validator := admission.NewDeliveryUnitValidator(newKaproAdmissionDecoder(t), nil)
	unit := kaproAdmissionDeliveryUnit()
	unit.Spec.Source.Units = append(unit.Spec.Source.Units, kaprov1alpha1.Unit{Name: "api "})

	resp := validator.Handle(context.Background(), admissionRequest(t, admissionv1.Create, unit))
	if resp.Allowed || !strings.Contains(responseMessage(resp), "leading or trailing whitespace") {
		t.Fatalf("expected whitespace unit-name denial, allowed=%t message=%q", resp.Allowed, responseMessage(resp))
	}
}

func TestDeliveryUnitValidatorHandleDeniesInvalidTriggerMaxActive(t *testing.T) {
	validator := admission.NewDeliveryUnitValidator(newKaproAdmissionDecoder(t), nil)
	unit := kaproAdmissionDeliveryUnit()
	unit.Spec.Triggers[0].MaxActive = -1

	resp := validator.Handle(context.Background(), admissionRequest(t, admissionv1.Create, unit))
	if resp.Allowed || !strings.Contains(responseMessage(resp), "spec.maxActive") {
		t.Fatalf("expected embedded trigger maxActive denial, allowed=%t message=%q", resp.Allowed, responseMessage(resp))
	}
}

func TestDeliveryUnitValidatorHandleRequiresTeamLabelWhenTriggersDeclared(t *testing.T) {
	validator := admission.NewDeliveryUnitValidator(newKaproAdmissionDecoder(t), nil)
	unit := kaproAdmissionDeliveryUnit()
	unit.Labels = nil

	resp := validator.Handle(context.Background(), admissionRequest(t, admissionv1.Create, unit))
	if resp.Allowed || !strings.Contains(responseMessage(resp), admission.LabelKaproTeam) {
		t.Fatalf("expected deliveryunit trigger team-label denial, allowed=%t message=%q", resp.Allowed, responseMessage(resp))
	}

	unit.Labels = map[string]string{admission.LabelKaproTeam: "checkout"}
	resp = validator.Handle(context.Background(), admissionRequest(t, admissionv1.Create, unit))
	if !resp.Allowed {
		t.Fatalf("expected deliveryunit trigger with team label to be allowed, got %s", responseMessage(resp))
	}
}

func TestDeliveryUnitValidatorHandleAllowsSourceOnlyWithoutTeamLabel(t *testing.T) {
	validator := admission.NewDeliveryUnitValidator(newKaproAdmissionDecoder(t), nil)
	unit := kaproAdmissionDeliveryUnit()
	unit.Labels = nil
	unit.Spec.Triggers = nil

	resp := validator.Handle(context.Background(), admissionRequest(t, admissionv1.Create, unit))
	if !resp.Allowed {
		t.Fatalf("expected source-only deliveryunit without team label to be allowed, got %s", responseMessage(resp))
	}
}

func TestDeliveryUnitValidatorHandleDeniesDuplicateDefaultTriggerNames(t *testing.T) {
	validator := admission.NewDeliveryUnitValidator(newKaproAdmissionDecoder(t), nil)
	unit := kaproAdmissionDeliveryUnit()
	unit.Spec.Triggers = []kaprov1alpha1.DeliveryUnitTrigger{
		{
			Source: kaprov1alpha1.TriggerSource{
				Type: "oci",
				OCI:  &kaprov1alpha1.OCITriggerSource{Repository: "oci://registry.example.com/checkout", TagPattern: "v.*"},
			},
		},
		{
			Source: kaprov1alpha1.TriggerSource{
				Type: "oci",
				OCI:  &kaprov1alpha1.OCITriggerSource{Repository: "oci://registry.example.com/checkout", TagPattern: "v.*"},
			},
		},
	}

	resp := validator.Handle(context.Background(), admissionRequest(t, admissionv1.Create, unit))
	if resp.Allowed || !strings.Contains(responseMessage(resp), "derives duplicate Trigger") {
		t.Fatalf("expected duplicate default-trigger denial, allowed=%t message=%q", resp.Allowed, responseMessage(resp))
	}
}

func TestDeliveryUnitValidatorHandleDeniesTriggerWithoutFleetDefault(t *testing.T) {
	validator := admission.NewDeliveryUnitValidator(newKaproAdmissionDecoder(t), nil)
	unit := kaproAdmissionDeliveryUnit()
	unit.Spec.DefaultFleetRef = ""
	unit.Spec.Triggers[0].FleetRef = ""

	resp := validator.Handle(context.Background(), admissionRequest(t, admissionv1.Create, unit))
	if resp.Allowed || !strings.Contains(responseMessage(resp), "requires fleetRef or spec.defaultFleetRef") {
		t.Fatalf("expected missing trigger fleet denial, allowed=%t message=%q", resp.Allowed, responseMessage(resp))
	}
}

func TestDeliveryUnitValidatorHandle(t *testing.T) {
	validator := admission.NewDeliveryUnitValidator(newKaproAdmissionDecoder(t), nil)
	unit := kaproAdmissionDeliveryUnit()

	resp := validator.Handle(context.Background(), admissionRequest(t, admissionv1.Create, unit))
	if !resp.Allowed {
		t.Fatalf("expected valid deliveryunit to be allowed, got %s", responseMessage(resp))
	}

	unit.Spec.Triggers[0].Name = "bad/name"
	resp = validator.Handle(context.Background(), admissionRequest(t, admissionv1.Create, unit))
	if resp.Allowed || !strings.Contains(responseMessage(resp), "DNS-1123") {
		t.Fatalf("expected invalid trigger suffix denial, allowed=%t message=%q", resp.Allowed, responseMessage(resp))
	}
}

func TestDeliveryUnitValidatorHandleDeniesSourceConflict(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add kapro scheme: %v", err)
	}
	reader := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(&kaprov1alpha1.Source{
			ObjectMeta: metav1.ObjectMeta{Name: "checkout"},
			Spec:       kaprov1alpha1.SourceSpec{Units: []kaprov1alpha1.Unit{{Name: "manual"}}},
		}).
		Build()
	validator := admission.NewDeliveryUnitValidator(newKaproAdmissionDecoder(t), reader)

	resp := validator.Handle(context.Background(), admissionRequest(t, admissionv1.Create, kaproAdmissionDeliveryUnit()))
	if resp.Allowed || !strings.Contains(responseMessage(resp), "conflicts with an existing Source") {
		t.Fatalf("expected source conflict denial, allowed=%t message=%q", resp.Allowed, responseMessage(resp))
	}
}

func TestDeliveryUnitValidatorHandleDeniesTriggerConflict(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add kapro scheme: %v", err)
	}
	reader := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(&kaprov1alpha1.Trigger{
			ObjectMeta: metav1.ObjectMeta{Name: "checkout-tags"},
			Spec: kaprov1alpha1.TriggerSpec{
				Source:            kaprov1alpha1.TriggerSource{Type: "oci", OCI: &kaprov1alpha1.OCITriggerSource{Repository: "oci://registry.example.com/manual", TagPattern: "v.*"}},
				PromotionTemplate: kaprov1alpha1.TriggerTemplate{FleetRef: "manual", DeliveryUnitRef: "manual"},
			},
		}).
		Build()
	validator := admission.NewDeliveryUnitValidator(newKaproAdmissionDecoder(t), reader)

	resp := validator.Handle(context.Background(), admissionRequest(t, admissionv1.Create, kaproAdmissionDeliveryUnit()))
	if resp.Allowed || !strings.Contains(responseMessage(resp), "conflicts with an existing Trigger") {
		t.Fatalf("expected trigger conflict denial, allowed=%t message=%q", resp.Allowed, responseMessage(resp))
	}
}

func newKaproAdmissionDecoder(t *testing.T) ctrladmission.Decoder {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add kapro scheme: %v", err)
	}
	return ctrladmission.NewDecoder(scheme)
}

func admissionRequest(t *testing.T, operation admissionv1.Operation, obj runtime.Object) ctrladmission.Request {
	t.Helper()
	raw, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshal admission object: %v", err)
	}
	return ctrladmission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Operation: operation,
			Object:    runtime.RawExtension{Raw: raw},
		},
	}
}

func responseMessage(resp ctrladmission.Response) string {
	if resp.Result == nil {
		return ""
	}
	return resp.Result.Message
}

func kaproAdmissionApproval(promotionRun, ref string) *kaprov1alpha1.Approval {
	return &kaprov1alpha1.Approval{
		TypeMeta: metav1.TypeMeta{APIVersion: "kapro.io/v1alpha1", Kind: "Approval"},
		ObjectMeta: metav1.ObjectMeta{
			Name: promotionRun + "-" + ref,
		},
		Spec: kaprov1alpha1.ApprovalSpec{
			PromotionRun: promotionRun,
			Target:       "cluster-a",
			Ref:          ref,
			ApprovedBy:   "alice",
		},
	}
}

func kaproAdmissionPlan() *kaprov1alpha1.Plan {
	return &kaprov1alpha1.Plan{
		TypeMeta:   metav1.TypeMeta{APIVersion: "kapro.io/v1alpha1", Kind: "Plan"},
		ObjectMeta: metav1.ObjectMeta{Name: "standard"},
		Spec: kaprov1alpha1.PlanSpec{
			Stages: []kaprov1alpha1.Stage{{
				Name:     "dev",
				Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "dev"}},
			}},
		},
	}
}

func kaproAdmissionTrigger() *kaprov1alpha1.Trigger {
	return &kaprov1alpha1.Trigger{
		TypeMeta:   metav1.TypeMeta{APIVersion: "kapro.io/v1alpha1", Kind: "Trigger"},
		ObjectMeta: metav1.ObjectMeta{Name: "checkout"},
		Spec: kaprov1alpha1.TriggerSpec{
			Source: kaprov1alpha1.TriggerSource{
				Type: "oci",
				OCI: &kaprov1alpha1.OCITriggerSource{
					Repository: "oci://registry.example.com/checkout",
					TagPattern: "v.*",
				},
			},
			PromotionTemplate: kaprov1alpha1.TriggerTemplate{
				DeliveryUnitRef: "checkout",
				FleetRef:        "checkout",
				Plans:           []kaprov1alpha1.PlanRef{{Name: "standard"}},
			},
		},
	}
}

func kaproAdmissionDeliveryUnit() *kaprov1alpha1.DeliveryUnit {
	return &kaprov1alpha1.DeliveryUnit{
		TypeMeta:   metav1.TypeMeta{APIVersion: "kapro.io/v1alpha1", Kind: "DeliveryUnit"},
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Labels: map[string]string{admission.LabelKaproTeam: "checkout"}},
		Spec: kaprov1alpha1.DeliveryUnitSpec{
			DefaultFleetRef: "checkout",
			Source: kaprov1alpha1.SourceSpec{
				Units: []kaprov1alpha1.Unit{{Name: "api"}},
			},
			Triggers: []kaprov1alpha1.DeliveryUnitTrigger{{
				Name: "tags",
				Source: kaprov1alpha1.TriggerSource{
					Type: "oci",
					OCI: &kaprov1alpha1.OCITriggerSource{
						Repository: "oci://registry.example.com/checkout",
						TagPattern: "v.*",
					},
				},
			}},
		},
	}
}
