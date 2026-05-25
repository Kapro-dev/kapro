package controller

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
)

var errSourceNameConflict = errors.New("source name conflict")

// DeliveryUnitReconciler derives controller-managed Source and Trigger
// machinery from the canonical user-authored DeliveryUnit.
type DeliveryUnitReconciler struct {
	client.Client
	Recorder record.EventRecorder
	Scheme   *runtime.Scheme
}

// +kubebuilder:rbac:groups=kapro.io,resources=deliveryunits,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=deliveryunits/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=sources,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kapro.io,resources=triggers,verbs=get;list;watch;create;update;patch;delete

func (r *DeliveryUnitReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var unit kaprov1alpha1.DeliveryUnit
	if err := r.Get(ctx, req.NamespacedName, &unit); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !unit.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	if unit.Spec.Suspended {
		if err := r.suspendDerivedTriggers(ctx, &unit); err != nil {
			return r.patchStatus(ctx, &unit, unit.Status.SourceRef, unit.Status.TriggerRefs, metav1.ConditionFalse, "SuspendDerivedTriggersFailed", err.Error())
		}
		return r.patchStatus(ctx, &unit, unit.Status.SourceRef, unit.Status.TriggerRefs, metav1.ConditionFalse, "Suspended", "DeliveryUnit derivation is suspended")
	}

	sourceName, err := r.reconcileSource(ctx, &unit)
	if err != nil {
		reason := "SourceDeriveFailed"
		if errors.Is(err, errSourceNameConflict) {
			reason = "SourceNameConflict"
		}
		return r.patchStatus(ctx, &unit, "", nil, metav1.ConditionFalse, reason, err.Error())
	}
	triggerNames, err := r.reconcileTriggers(ctx, &unit)
	if err != nil {
		return r.patchStatus(ctx, &unit, sourceName, triggerNames, metav1.ConditionFalse, "TriggerDeriveFailed", err.Error())
	}
	if err := r.pruneStaleTriggers(ctx, &unit, triggerNames); err != nil {
		return r.patchStatus(ctx, &unit, sourceName, triggerNames, metav1.ConditionFalse, "TriggerPruneFailed", err.Error())
	}
	return r.patchStatus(ctx, &unit, sourceName, triggerNames, metav1.ConditionTrue, "Derived", "derived Source and Trigger machinery is ready")
}

func (r *DeliveryUnitReconciler) reconcileSource(ctx context.Context, unit *kaprov1alpha1.DeliveryUnit) (string, error) {
	var existing kaprov1alpha1.Source
	if err := r.Get(ctx, client.ObjectKey{Name: unit.Name}, &existing); err != nil {
		if !apierrors.IsNotFound(err) {
			return "", fmt.Errorf("get Source %s before derivation: %w", unit.Name, err)
		}
	} else if !sourceOwnedByDeliveryUnit(&existing, unit) {
		return "", fmt.Errorf("%w: Source %s already exists and is not owned by DeliveryUnit %s", errSourceNameConflict, unit.Name, unit.Name)
	}

	source := &kaprov1alpha1.Source{
		TypeMeta: metav1.TypeMeta{
			APIVersion: kaprov1alpha1.GroupVersion.String(),
			Kind:       "Source",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:   unit.Name,
			Labels: deliveryUnitManagedLabels(unit),
		},
		Spec: *unit.Spec.Source.DeepCopy(),
	}
	if err := controllerutil.SetControllerReference(unit, source, r.Scheme); err != nil {
		return "", fmt.Errorf("set DeliveryUnit owner on Source: %w", err)
	}
	if err := r.Patch(ctx, source,
		client.Apply, //nolint:staticcheck // server-side apply is the existing controller convention.
		client.FieldOwner("kapro-deliveryunit-controller"),
		client.ForceOwnership,
	); err != nil {
		return "", fmt.Errorf("apply derived Source %s: %w", source.Name, err)
	}
	return source.Name, nil
}

func (r *DeliveryUnitReconciler) reconcileTriggers(ctx context.Context, unit *kaprov1alpha1.DeliveryUnit) ([]string, error) {
	triggerNames := make([]string, 0, len(unit.Spec.Triggers))
	seen := make(map[string]struct{}, len(unit.Spec.Triggers))
	type derived struct {
		name string
		spec kaprov1alpha1.TriggerSpec
	}
	derivedTriggers := make([]derived, 0, len(unit.Spec.Triggers))
	for i := range unit.Spec.Triggers {
		triggerSpec, name, err := derivedTrigger(unit, unit.Spec.Triggers[i])
		if err != nil {
			return triggerNames, err
		}
		if _, ok := seen[name]; ok {
			return triggerNames, fmt.Errorf("deliveryunit %s has duplicate derived Trigger name %q", unit.Name, name)
		}
		seen[name] = struct{}{}
		derivedTriggers = append(derivedTriggers, derived{name: name, spec: triggerSpec})
	}
	for _, item := range derivedTriggers {
		trigger := &kaprov1alpha1.Trigger{
			TypeMeta: metav1.TypeMeta{
				APIVersion: kaprov1alpha1.GroupVersion.String(),
				Kind:       "Trigger",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:   item.name,
				Labels: deliveryUnitManagedLabels(unit),
			},
			Spec: item.spec,
		}
		if err := controllerutil.SetControllerReference(unit, trigger, r.Scheme); err != nil {
			return triggerNames, fmt.Errorf("set DeliveryUnit owner on Trigger %s: %w", item.name, err)
		}
		if err := r.Patch(ctx, trigger,
			client.Apply, //nolint:staticcheck
			client.FieldOwner("kapro-deliveryunit-controller"),
			client.ForceOwnership,
		); err != nil {
			return triggerNames, fmt.Errorf("apply derived Trigger %s: %w", item.name, err)
		}
		triggerNames = append(triggerNames, item.name)
	}
	sort.Strings(triggerNames)
	return triggerNames, nil
}

func derivedTrigger(unit *kaprov1alpha1.DeliveryUnit, trigger kaprov1alpha1.DeliveryUnitTrigger) (kaprov1alpha1.TriggerSpec, string, error) {
	suffix := strings.TrimSpace(trigger.Name)
	if suffix == "" {
		suffix = "default"
	}
	name := unit.Name + "-" + suffix
	fleetRef := firstNonEmpty(trigger.FleetRef, unit.Spec.DefaultFleetRef)
	if fleetRef == "" {
		return kaprov1alpha1.TriggerSpec{}, name, fmt.Errorf("deliveryunit %s trigger %s requires fleetRef or spec.defaultFleetRef", unit.Name, suffix)
	}
	planRef := firstNonEmpty(trigger.PlanRef, unit.Spec.DefaultPlanRef)
	labels := copyStringMap(unit.Labels)
	if labels == nil {
		labels = map[string]string{}
	}
	for k, v := range trigger.Labels {
		labels[k] = v
	}
	labels[kaprov1alpha1.LabelUnit] = unit.Name
	labels[kaprov1alpha1.LabelManagedBy] = kaprov1alpha1.ManagedByKapro
	template := kaprov1alpha1.TriggerTemplate{
		DeliveryUnitRef: unit.Name,
		FleetRef:        fleetRef,
		PlanRef:         planRef,
		Suspended:       trigger.Suspended,
		Labels:          labels,
		Annotations:     copyStringMap(trigger.Annotations),
	}
	if planRef != "" {
		template.Plans = []kaprov1alpha1.PlanRef{{Name: "default", Plan: planRef}}
	}
	return kaprov1alpha1.TriggerSpec{
		Suspended:         trigger.Suspended,
		Source:            trigger.Source,
		PromotionTemplate: template,
		Cooldown:          trigger.Cooldown,
		MaxActive:         trigger.MaxActive,
		DryRun:            trigger.DryRun,
		Parameters:        copyStringMap(trigger.Parameters),
	}, name, nil
}

func (r *DeliveryUnitReconciler) suspendDerivedTriggers(ctx context.Context, unit *kaprov1alpha1.DeliveryUnit) error {
	var triggers kaprov1alpha1.TriggerList
	if err := r.List(ctx, &triggers, client.MatchingLabels{kaprov1alpha1.LabelUnit: unit.Name, kaprov1alpha1.LabelManagedBy: kaprov1alpha1.ManagedByKapro}); err != nil {
		return fmt.Errorf("list derived Triggers: %w", err)
	}
	suspended := true
	for i := range triggers.Items {
		existing := &triggers.Items[i]
		if !metav1.IsControlledBy(existing, unit) {
			continue
		}
		if existing.Spec.Suspended != nil && *existing.Spec.Suspended {
			continue
		}
		trigger := &kaprov1alpha1.Trigger{
			TypeMeta: metav1.TypeMeta{
				APIVersion: kaprov1alpha1.GroupVersion.String(),
				Kind:       "Trigger",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:   existing.Name,
				Labels: copyStringMap(existing.Labels),
			},
			Spec: *existing.Spec.DeepCopy(),
		}
		trigger.Spec.Suspended = &suspended
		if err := controllerutil.SetControllerReference(unit, trigger, r.Scheme); err != nil {
			return fmt.Errorf("set DeliveryUnit owner on Trigger %s: %w", existing.Name, err)
		}
		if err := r.Patch(ctx, trigger,
			client.Apply, //nolint:staticcheck
			client.FieldOwner("kapro-deliveryunit-controller"),
			client.ForceOwnership,
		); err != nil {
			return fmt.Errorf("suspend derived Trigger %s: %w", existing.Name, err)
		}
	}
	return nil
}

func (r *DeliveryUnitReconciler) pruneStaleTriggers(ctx context.Context, unit *kaprov1alpha1.DeliveryUnit, keep []string) error {
	keepSet := make(map[string]struct{}, len(keep))
	for _, name := range keep {
		keepSet[name] = struct{}{}
	}
	var triggers kaprov1alpha1.TriggerList
	if err := r.List(ctx, &triggers, client.MatchingLabels{kaprov1alpha1.LabelUnit: unit.Name, kaprov1alpha1.LabelManagedBy: kaprov1alpha1.ManagedByKapro}); err != nil {
		return fmt.Errorf("list derived Triggers: %w", err)
	}
	for i := range triggers.Items {
		trigger := &triggers.Items[i]
		if !metav1.IsControlledBy(trigger, unit) {
			continue
		}
		if _, ok := keepSet[trigger.Name]; ok {
			continue
		}
		if err := r.Delete(ctx, trigger); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete stale Trigger %s: %w", trigger.Name, err)
		}
	}
	return nil
}

func (r *DeliveryUnitReconciler) patchStatus(ctx context.Context, unit *kaprov1alpha1.DeliveryUnit, sourceName string, triggerNames []string,
	status metav1.ConditionStatus, reason, message string) (ctrl.Result, error) {
	patch := client.MergeFrom(unit.DeepCopy())
	previous := apimeta.FindStatusCondition(unit.Status.Conditions, "Ready")
	unit.Status.ObservedGeneration = unit.Generation
	unit.Status.SourceRef = sourceName
	unit.Status.TriggerRefs = append([]string(nil), triggerNames...)
	apimeta.SetStatusCondition(&unit.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: unit.Generation,
	})
	if err := r.Status().Patch(ctx, unit, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch DeliveryUnit status: %w", err)
	}
	r.recordReadyEvent(unit, previous, status, reason, message)
	if status != metav1.ConditionTrue {
		if reason == "Suspended" {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{RequeueAfter: requeueNormal}, nil
	}
	return ctrl.Result{}, nil
}

func (r *DeliveryUnitReconciler) recordReadyEvent(unit *kaprov1alpha1.DeliveryUnit, previous *metav1.Condition, status metav1.ConditionStatus, reason, message string) {
	if r.Recorder == nil {
		return
	}
	if previous != nil && previous.Status == status && previous.Reason == reason && previous.ObservedGeneration == unit.Generation {
		return
	}
	eventType := corev1.EventTypeNormal
	if status != metav1.ConditionTrue && reason != "Suspended" {
		eventType = corev1.EventTypeWarning
	}
	r.Recorder.Eventf(unit, eventType, reason, message)
}

func sourceOwnedByDeliveryUnit(source *kaprov1alpha1.Source, unit *kaprov1alpha1.DeliveryUnit) bool {
	for _, ref := range source.OwnerReferences {
		if ref.APIVersion != kaprov1alpha1.GroupVersion.String() || ref.Kind != "DeliveryUnit" || ref.Name != unit.Name {
			continue
		}
		if unit.UID != "" && ref.UID != unit.UID {
			continue
		}
		return true
	}
	return false
}

func (r *DeliveryUnitReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kaprov1alpha1.DeliveryUnit{}).
		Owns(&kaprov1alpha1.Source{}).
		Owns(&kaprov1alpha1.Trigger{}).
		Complete(r)
}

func deliveryUnitManagedLabels(unit *kaprov1alpha1.DeliveryUnit) map[string]string {
	labels := copyStringMap(unit.Labels)
	if labels == nil {
		labels = map[string]string{}
	}
	labels[kaprov1alpha1.LabelUnit] = unit.Name
	labels[kaprov1alpha1.LabelManagedBy] = kaprov1alpha1.ManagedByKapro
	return labels
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
