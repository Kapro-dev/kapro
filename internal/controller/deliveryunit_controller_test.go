package controller

import (
	"context"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
)

func TestDeliveryUnitDerivesSourceAndTrigger(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	unit := &kaprov1alpha1.DeliveryUnit{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "checkout",
			Labels:      map[string]string{"kapro.io/team": "platform"},
			Annotations: map[string]string{"kapro.io/cost-center": "4711"},
		},
		Spec: kaprov1alpha1.DeliveryUnitSpec{
			DefaultFleetRef: "checkout-fleet",
			DefaultPlanRef:  "checkout-plan",
			Source: kaprov1alpha1.SourceSpec{
				SubstrateRef: "flux",
				Units: []kaprov1alpha1.Unit{{
					Name:          "api",
					SubstrateKind: "GitYAMLField",
					SourcePath:    "flux/apps/api.yaml",
					VersionField:  "spec.ref.tag",
				}},
			},
			Triggers: []kaprov1alpha1.DeliveryUnitTrigger{{
				Name: "tags",
				Source: kaprov1alpha1.TriggerSource{
					Type: "oci",
					OCI:  &kaprov1alpha1.OCITriggerSource{Repository: "oci://registry.example.com/checkout", TagPattern: "v.*"},
				},
			}},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(unit).
		WithStatusSubresource(&kaprov1alpha1.DeliveryUnit{}).
		Build()
	r := &DeliveryUnitReconciler{Client: c, Scheme: scheme, Recorder: record.NewFakeRecorder(8)}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}

	var source kaprov1alpha1.Source
	if err := c.Get(ctx, client.ObjectKey{Name: "checkout"}, &source); err != nil {
		t.Fatal(err)
	}
	if source.Labels[kaprov1alpha1.LabelUnit] != "checkout" || source.Labels[kaprov1alpha1.LabelManagedBy] != kaprov1alpha1.ManagedByKapro {
		t.Fatalf("derived source labels = %#v", source.Labels)
	}
	if source.Annotations["kapro.io/cost-center"] != "4711" {
		t.Fatalf("derived source annotations = %#v", source.Annotations)
	}
	if !metav1.IsControlledBy(&source, unit) {
		t.Fatalf("derived source owner references = %#v", source.OwnerReferences)
	}
	if source.Spec.SubstrateRef != "flux" || len(source.Spec.Units) != 1 || source.Spec.Units[0].Name != "api" {
		t.Fatalf("derived source spec = %#v", source.Spec)
	}

	var trigger kaprov1alpha1.Trigger
	if err := c.Get(ctx, client.ObjectKey{Name: "checkout-tags"}, &trigger); err != nil {
		t.Fatal(err)
	}
	if trigger.Spec.PromotionTemplate.DeliveryUnitRef != "checkout" {
		t.Fatalf("trigger deliveryUnitRef = %q", trigger.Spec.PromotionTemplate.DeliveryUnitRef)
	}
	if trigger.Spec.PromotionTemplate.FleetRef != "checkout-fleet" || trigger.Spec.PromotionTemplate.PlanRef != "checkout-plan" {
		t.Fatalf("trigger promotion template = %#v", trigger.Spec.PromotionTemplate)
	}
	if trigger.Spec.PromotionTemplate.Labels[kaprov1alpha1.LabelTeam] != "platform" {
		t.Fatalf("trigger promotion labels = %#v, want team propagated", trigger.Spec.PromotionTemplate.Labels)
	}
	if trigger.Labels[kaprov1alpha1.LabelUnit] != "checkout" || trigger.Labels[kaprov1alpha1.LabelManagedBy] != kaprov1alpha1.ManagedByKapro {
		t.Fatalf("derived trigger labels = %#v", trigger.Labels)
	}
	if trigger.Annotations["kapro.io/cost-center"] != "4711" {
		t.Fatalf("derived trigger annotations = %#v", trigger.Annotations)
	}

	var got kaprov1alpha1.DeliveryUnit
	if err := c.Get(ctx, client.ObjectKey{Name: "checkout"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.SourceRef != "checkout" || len(got.Status.TriggerRefs) != 1 || got.Status.TriggerRefs[0] != "checkout-tags" {
		t.Fatalf("deliveryunit status = %#v", got.Status)
	}
	cond := apimeta.FindStatusCondition(got.Status.Conditions, "Ready")
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Fatalf("Ready condition = %#v", cond)
	}
}

func TestDeliveryUnitSuspendedSuspendsExistingDerivedTriggers(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	notSuspended := false
	unit := &kaprov1alpha1.DeliveryUnit{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Labels: map[string]string{kaprov1alpha1.LabelTeam: "platform"}},
		Spec: kaprov1alpha1.DeliveryUnitSpec{
			DefaultFleetRef: "checkout-fleet",
			Source: kaprov1alpha1.SourceSpec{
				Units: []kaprov1alpha1.Unit{{Name: "api"}},
			},
			Triggers: []kaprov1alpha1.DeliveryUnitTrigger{{
				Name:      "tags",
				Suspended: &notSuspended,
				Source: kaprov1alpha1.TriggerSource{
					Type: "oci",
					OCI:  &kaprov1alpha1.OCITriggerSource{Repository: "oci://registry.example.com/checkout", TagPattern: "v.*"},
				},
			}},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(unit).
		WithStatusSubresource(&kaprov1alpha1.DeliveryUnit{}).
		Build()
	r := &DeliveryUnitReconciler{Client: c, Scheme: scheme, Recorder: record.NewFakeRecorder(8)}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	var trigger kaprov1alpha1.Trigger
	if err := c.Get(ctx, client.ObjectKey{Name: "checkout-tags"}, &trigger); err != nil {
		t.Fatal(err)
	}
	if trigger.Spec.Suspended == nil || *trigger.Spec.Suspended {
		t.Fatalf("initial trigger suspended = %#v, want false", trigger.Spec.Suspended)
	}

	var latest kaprov1alpha1.DeliveryUnit
	if err := c.Get(ctx, client.ObjectKey{Name: "checkout"}, &latest); err != nil {
		t.Fatal(err)
	}
	latest.Spec.Suspended = true
	if err := c.Update(ctx, &latest); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKey{Name: "checkout-tags"}, &trigger); err != nil {
		t.Fatal(err)
	}
	if trigger.Spec.Suspended == nil || !*trigger.Spec.Suspended {
		t.Fatalf("suspended DeliveryUnit left trigger active: %#v", trigger.Spec.Suspended)
	}
}

func TestDeliveryUnitUnsuspendRestoresTriggers(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	notSuspended := false
	unit := &kaprov1alpha1.DeliveryUnit{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Labels: map[string]string{kaprov1alpha1.LabelTeam: "platform"}},
		Spec: kaprov1alpha1.DeliveryUnitSpec{
			DefaultFleetRef: "checkout-fleet",
			Source:          kaprov1alpha1.SourceSpec{Units: []kaprov1alpha1.Unit{{Name: "api"}}},
			Triggers: []kaprov1alpha1.DeliveryUnitTrigger{{
				Name:      "tags",
				Suspended: &notSuspended,
				Source: kaprov1alpha1.TriggerSource{
					Type: "oci",
					OCI:  &kaprov1alpha1.OCITriggerSource{Repository: "oci://registry.example.com/checkout", TagPattern: "v.*"},
				},
			}},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(unit).
		WithStatusSubresource(&kaprov1alpha1.DeliveryUnit{}).
		Build()
	r := &DeliveryUnitReconciler{Client: c, Scheme: scheme, Recorder: record.NewFakeRecorder(8)}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	var latest kaprov1alpha1.DeliveryUnit
	if err := c.Get(ctx, client.ObjectKey{Name: "checkout"}, &latest); err != nil {
		t.Fatal(err)
	}
	latest.Spec.Suspended = true
	if err := c.Update(ctx, &latest); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	var trigger kaprov1alpha1.Trigger
	if err := c.Get(ctx, client.ObjectKey{Name: "checkout-tags"}, &trigger); err != nil {
		t.Fatal(err)
	}
	if trigger.Spec.Suspended == nil || !*trigger.Spec.Suspended {
		t.Fatalf("suspend did not pause trigger: %#v", trigger.Spec.Suspended)
	}

	if err := c.Get(ctx, client.ObjectKey{Name: "checkout"}, &latest); err != nil {
		t.Fatal(err)
	}
	latest.Spec.Suspended = false
	if err := c.Update(ctx, &latest); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKey{Name: "checkout-tags"}, &trigger); err != nil {
		t.Fatal(err)
	}
	if trigger.Spec.Suspended == nil || *trigger.Spec.Suspended {
		t.Fatalf("unsuspend did not restore trigger active state: %#v", trigger.Spec.Suspended)
	}
}

func TestDeliveryUnitSourceNameConflict(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	existing := &kaprov1alpha1.Source{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout"},
		Spec:       kaprov1alpha1.SourceSpec{SubstrateRef: "manual", Units: []kaprov1alpha1.Unit{{Name: "manual"}}},
	}
	unit := &kaprov1alpha1.DeliveryUnit{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Labels: map[string]string{kaprov1alpha1.LabelTeam: "platform"}},
		Spec: kaprov1alpha1.DeliveryUnitSpec{
			DefaultFleetRef: "checkout-fleet",
			Source:          kaprov1alpha1.SourceSpec{SubstrateRef: "flux", Units: []kaprov1alpha1.Unit{{Name: "api"}}},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(existing, unit).
		WithStatusSubresource(&kaprov1alpha1.DeliveryUnit{}).
		Build()
	r := &DeliveryUnitReconciler{Client: c, Scheme: scheme, Recorder: record.NewFakeRecorder(8)}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	var source kaprov1alpha1.Source
	if err := c.Get(ctx, client.ObjectKey{Name: "checkout"}, &source); err != nil {
		t.Fatal(err)
	}
	if source.Spec.SubstrateRef != "manual" || source.Spec.Units[0].Name != "manual" {
		t.Fatalf("pre-existing Source was overwritten: %#v", source.Spec)
	}
	var got kaprov1alpha1.DeliveryUnit
	if err := c.Get(ctx, client.ObjectKey{Name: "checkout"}, &got); err != nil {
		t.Fatal(err)
	}
	cond := apimeta.FindStatusCondition(got.Status.Conditions, "Ready")
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "SourceNameConflict" || !strings.Contains(cond.Message, "not owned") {
		t.Fatalf("Ready condition = %#v", cond)
	}
}

func TestDeliveryUnitTriggerNameConflict(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	existing := &kaprov1alpha1.Trigger{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-tags"},
		Spec: kaprov1alpha1.TriggerSpec{
			Source:            kaprov1alpha1.TriggerSource{Type: "oci", OCI: &kaprov1alpha1.OCITriggerSource{Repository: "oci://registry.example.com/manual", TagPattern: "v.*"}},
			PromotionTemplate: kaprov1alpha1.TriggerTemplate{FleetRef: "manual", DeliveryUnitRef: "manual"},
		},
	}
	unit := &kaprov1alpha1.DeliveryUnit{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Labels: map[string]string{kaprov1alpha1.LabelTeam: "platform"}},
		Spec: kaprov1alpha1.DeliveryUnitSpec{
			DefaultFleetRef: "checkout-fleet",
			Source:          kaprov1alpha1.SourceSpec{Units: []kaprov1alpha1.Unit{{Name: "api"}}},
			Triggers: []kaprov1alpha1.DeliveryUnitTrigger{{
				Name:   "tags",
				Source: kaprov1alpha1.TriggerSource{Type: "oci", OCI: &kaprov1alpha1.OCITriggerSource{Repository: "oci://registry.example.com/checkout", TagPattern: "v.*"}},
			}},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(existing, unit).
		WithStatusSubresource(&kaprov1alpha1.DeliveryUnit{}).
		Build()
	r := &DeliveryUnitReconciler{Client: c, Scheme: scheme, Recorder: record.NewFakeRecorder(8)}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	var trigger kaprov1alpha1.Trigger
	if err := c.Get(ctx, client.ObjectKey{Name: "checkout-tags"}, &trigger); err != nil {
		t.Fatal(err)
	}
	if trigger.Spec.PromotionTemplate.DeliveryUnitRef != "manual" || trigger.Spec.PromotionTemplate.FleetRef != "manual" {
		t.Fatalf("pre-existing Trigger was overwritten: %#v", trigger.Spec.PromotionTemplate)
	}
	var got kaprov1alpha1.DeliveryUnit
	if err := c.Get(ctx, client.ObjectKey{Name: "checkout"}, &got); err != nil {
		t.Fatal(err)
	}
	cond := apimeta.FindStatusCondition(got.Status.Conditions, "Ready")
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "TriggerNameConflict" || !strings.Contains(cond.Message, "not owned") {
		t.Fatalf("Ready condition = %#v", cond)
	}
}

func TestDeliveryUnitTriggerRenamePrunesStale(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	unit := &kaprov1alpha1.DeliveryUnit{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Labels: map[string]string{kaprov1alpha1.LabelTeam: "platform"}},
		Spec: kaprov1alpha1.DeliveryUnitSpec{
			DefaultFleetRef: "checkout-fleet",
			Source:          kaprov1alpha1.SourceSpec{Units: []kaprov1alpha1.Unit{{Name: "api"}}},
			Triggers: []kaprov1alpha1.DeliveryUnitTrigger{{
				Name:   "old",
				Source: kaprov1alpha1.TriggerSource{Type: "oci", OCI: &kaprov1alpha1.OCITriggerSource{Repository: "oci://registry.example.com/checkout", TagPattern: "v.*"}},
			}},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(unit).
		WithStatusSubresource(&kaprov1alpha1.DeliveryUnit{}).
		Build()
	r := &DeliveryUnitReconciler{Client: c, Scheme: scheme, Recorder: record.NewFakeRecorder(8)}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	var latest kaprov1alpha1.DeliveryUnit
	if err := c.Get(ctx, client.ObjectKey{Name: "checkout"}, &latest); err != nil {
		t.Fatal(err)
	}
	latest.Spec.Triggers[0].Name = "new"
	if err := c.Update(ctx, &latest); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	var oldTrigger kaprov1alpha1.Trigger
	if err := c.Get(ctx, client.ObjectKey{Name: "checkout-old"}, &oldTrigger); err == nil {
		t.Fatalf("stale trigger still exists: %#v", oldTrigger)
	}
	var newTrigger kaprov1alpha1.Trigger
	if err := c.Get(ctx, client.ObjectKey{Name: "checkout-new"}, &newTrigger); err != nil {
		t.Fatalf("new trigger missing: %v", err)
	}
}

func TestDeliveryUnitDefaultTriggerName(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	unit := &kaprov1alpha1.DeliveryUnit{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Labels: map[string]string{kaprov1alpha1.LabelTeam: "platform"}},
		Spec: kaprov1alpha1.DeliveryUnitSpec{
			DefaultFleetRef: "checkout-fleet",
			Source:          kaprov1alpha1.SourceSpec{Units: []kaprov1alpha1.Unit{{Name: "api"}}},
			Triggers: []kaprov1alpha1.DeliveryUnitTrigger{{
				Source: kaprov1alpha1.TriggerSource{
					Type: "oci",
					OCI:  &kaprov1alpha1.OCITriggerSource{Repository: "oci://registry.example.com/checkout", TagPattern: "v.*"},
				},
			}},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(unit).
		WithStatusSubresource(&kaprov1alpha1.DeliveryUnit{}).
		Build()
	r := &DeliveryUnitReconciler{Client: c, Scheme: scheme, Recorder: record.NewFakeRecorder(8)}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	var trigger kaprov1alpha1.Trigger
	if err := c.Get(ctx, client.ObjectKey{Name: "checkout-default"}, &trigger); err != nil {
		t.Fatalf("default trigger missing: %v", err)
	}
	var got kaprov1alpha1.DeliveryUnit
	if err := c.Get(ctx, client.ObjectKey{Name: "checkout"}, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Status.TriggerRefs) != 1 || got.Status.TriggerRefs[0] != "checkout-default" {
		t.Fatalf("deliveryunit status triggerRefs = %#v", got.Status.TriggerRefs)
	}
}

func TestDeliveryUnitTriggerOverridesTemplateFields(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	notSuspended := false
	unit := &kaprov1alpha1.DeliveryUnit{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "checkout",
			Labels:      map[string]string{kaprov1alpha1.LabelTeam: "platform", "app.kubernetes.io/name": "checkout"},
			Annotations: map[string]string{"kapro.io/cost-center": "4711"},
		},
		Spec: kaprov1alpha1.DeliveryUnitSpec{
			DefaultFleetRef: "default-fleet",
			DefaultPlanRef:  "default-plan",
			Source:          kaprov1alpha1.SourceSpec{Units: []kaprov1alpha1.Unit{{Name: "api"}}},
			Triggers: []kaprov1alpha1.DeliveryUnitTrigger{{
				Name:      "release",
				Suspended: &notSuspended,
				FleetRef:  "prod-fleet",
				PlanRef:   "prod-plan",
				Cooldown:  "10m",
				MaxActive: 2,
				DryRun:    true,
				Parameters: map[string]string{
					"channel": "stable",
				},
				Labels: map[string]string{
					"promotion.kapro.io/source": "oci",
				},
				Annotations: map[string]string{
					"kapro.io/change": "auto",
				},
				Source: kaprov1alpha1.TriggerSource{
					Type: "oci",
					OCI:  &kaprov1alpha1.OCITriggerSource{Repository: "oci://registry.example.com/checkout", TagPattern: "v.*"},
				},
			}},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(unit).
		WithStatusSubresource(&kaprov1alpha1.DeliveryUnit{}).
		Build()
	r := &DeliveryUnitReconciler{Client: c, Scheme: scheme, Recorder: record.NewFakeRecorder(8)}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	var trigger kaprov1alpha1.Trigger
	if err := c.Get(ctx, client.ObjectKey{Name: "checkout-release"}, &trigger); err != nil {
		t.Fatal(err)
	}
	if trigger.Spec.Suspended == nil || *trigger.Spec.Suspended {
		t.Fatalf("trigger suspended = %#v, want false", trigger.Spec.Suspended)
	}
	if trigger.Spec.Cooldown != "10m" || trigger.Spec.MaxActive != 2 || !trigger.Spec.DryRun {
		t.Fatalf("trigger rate-control fields = cooldown %q maxActive %d dryRun %t", trigger.Spec.Cooldown, trigger.Spec.MaxActive, trigger.Spec.DryRun)
	}
	if trigger.Spec.Parameters["channel"] != "stable" {
		t.Fatalf("trigger parameters = %#v", trigger.Spec.Parameters)
	}
	template := trigger.Spec.PromotionTemplate
	if template.DeliveryUnitRef != "checkout" || template.FleetRef != "prod-fleet" || template.PlanRef != "prod-plan" {
		t.Fatalf("promotion template refs = %#v", template)
	}
	if len(template.Plans) != 1 || template.Plans[0].Name != "default" || template.Plans[0].Plan != "prod-plan" {
		t.Fatalf("promotion template plans = %#v", template.Plans)
	}
	if template.Suspended == nil || *template.Suspended {
		t.Fatalf("promotion template suspended = %#v, want false", template.Suspended)
	}
	for key, want := range map[string]string{
		kaprov1alpha1.LabelTeam:      "platform",
		kaprov1alpha1.LabelUnit:      "checkout",
		kaprov1alpha1.LabelManagedBy: kaprov1alpha1.ManagedByKapro,
		"app.kubernetes.io/name":     "checkout",
		"promotion.kapro.io/source":  "oci",
	} {
		if template.Labels[key] != want {
			t.Fatalf("promotion template labels[%q] = %q, want %q in %#v", key, template.Labels[key], want, template.Labels)
		}
	}
	if template.Annotations["kapro.io/change"] != "auto" {
		t.Fatalf("promotion template annotations = %#v", template.Annotations)
	}
	if trigger.Annotations["kapro.io/cost-center"] != "4711" {
		t.Fatalf("derived trigger annotations = %#v", trigger.Annotations)
	}
}

func TestDeliveryUnitPruneSkipsForeignTriggerWithMatchingLabels(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	unit := &kaprov1alpha1.DeliveryUnit{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Labels: map[string]string{kaprov1alpha1.LabelTeam: "platform"}},
		Spec: kaprov1alpha1.DeliveryUnitSpec{
			DefaultFleetRef: "checkout-fleet",
			Source:          kaprov1alpha1.SourceSpec{Units: []kaprov1alpha1.Unit{{Name: "api"}}},
			Triggers: []kaprov1alpha1.DeliveryUnitTrigger{{
				Name:   "old",
				Source: kaprov1alpha1.TriggerSource{Type: "oci", OCI: &kaprov1alpha1.OCITriggerSource{Repository: "oci://registry.example.com/checkout", TagPattern: "v.*"}},
			}},
		},
	}
	foreign := &kaprov1alpha1.Trigger{
		ObjectMeta: metav1.ObjectMeta{
			Name: "checkout-foreign",
			Labels: map[string]string{
				kaprov1alpha1.LabelUnit:      "checkout",
				kaprov1alpha1.LabelManagedBy: kaprov1alpha1.ManagedByKapro,
			},
		},
		Spec: kaprov1alpha1.TriggerSpec{
			Source:            kaprov1alpha1.TriggerSource{Type: "oci", OCI: &kaprov1alpha1.OCITriggerSource{Repository: "oci://registry.example.com/manual", TagPattern: "v.*"}},
			PromotionTemplate: kaprov1alpha1.TriggerTemplate{DeliveryUnitRef: "manual", FleetRef: "manual"},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(unit, foreign).
		WithStatusSubresource(&kaprov1alpha1.DeliveryUnit{}).
		Build()
	r := &DeliveryUnitReconciler{Client: c, Scheme: scheme, Recorder: record.NewFakeRecorder(8)}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	var latest kaprov1alpha1.DeliveryUnit
	if err := c.Get(ctx, client.ObjectKey{Name: "checkout"}, &latest); err != nil {
		t.Fatal(err)
	}
	latest.Spec.Triggers[0].Name = "new"
	if err := c.Update(ctx, &latest); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	var oldTrigger kaprov1alpha1.Trigger
	if err := c.Get(ctx, client.ObjectKey{Name: "checkout-old"}, &oldTrigger); !apierrors.IsNotFound(err) {
		t.Fatalf("old owned trigger should be pruned, err=%v trigger=%#v", err, oldTrigger)
	}
	var foreignAfter kaprov1alpha1.Trigger
	if err := c.Get(ctx, client.ObjectKey{Name: "checkout-foreign"}, &foreignAfter); err != nil {
		t.Fatalf("foreign trigger with matching labels should not be pruned: %v", err)
	}
	if len(foreignAfter.OwnerReferences) != 0 {
		t.Fatalf("foreign trigger owner references changed: %#v", foreignAfter.OwnerReferences)
	}
}

func TestDeliveryUnitRejectsDuplicateDerivedTriggerNames(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	unit := &kaprov1alpha1.DeliveryUnit{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Labels: map[string]string{kaprov1alpha1.LabelTeam: "platform"}},
		Spec: kaprov1alpha1.DeliveryUnitSpec{
			DefaultFleetRef: "checkout-fleet",
			Source: kaprov1alpha1.SourceSpec{
				Units: []kaprov1alpha1.Unit{{Name: "api"}},
			},
			Triggers: []kaprov1alpha1.DeliveryUnitTrigger{
				{Source: kaprov1alpha1.TriggerSource{Type: "oci", OCI: &kaprov1alpha1.OCITriggerSource{Repository: "oci://registry.example.com/checkout", TagPattern: "v.*"}}},
				{Source: kaprov1alpha1.TriggerSource{Type: "oci", OCI: &kaprov1alpha1.OCITriggerSource{Repository: "oci://registry.example.com/checkout", TagPattern: "v.*"}}},
			},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(unit).
		WithStatusSubresource(&kaprov1alpha1.DeliveryUnit{}).
		Build()
	r := &DeliveryUnitReconciler{Client: c, Scheme: scheme, Recorder: record.NewFakeRecorder(8)}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "checkout"}}); err != nil {
		t.Fatal(err)
	}
	var trigger kaprov1alpha1.Trigger
	if err := c.Get(ctx, client.ObjectKey{Name: "checkout-default"}, &trigger); err == nil {
		t.Fatalf("duplicate trigger names should fail before applying Trigger: %#v", trigger)
	}
	var got kaprov1alpha1.DeliveryUnit
	if err := c.Get(ctx, client.ObjectKey{Name: "checkout"}, &got); err != nil {
		t.Fatal(err)
	}
	cond := apimeta.FindStatusCondition(got.Status.Conditions, "Ready")
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "TriggerDeriveFailed" {
		t.Fatalf("Ready condition = %#v", cond)
	}
}
