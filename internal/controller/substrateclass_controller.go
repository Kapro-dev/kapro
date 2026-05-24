package controller

import (
	"context"
	"fmt"
	"strings"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	ctrl "sigs.k8s.io/controller-runtime"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

// SubstrateClassReconciler publishes the built-in Kapro substrate class
// contract. External substrate controllers should own their own controllerName
// and write status for those classes themselves.
type SubstrateClassReconciler struct {
	client.Client
}

// +kubebuilder:rbac:groups=kapro.io,resources=substrateclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=substrateclasses/status,verbs=get;update;patch

func (r *SubstrateClassReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var class kaprov1alpha2.SubstrateClass
	if err := r.Get(ctx, req.NamespacedName, &class); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	profile, ok := builtInSubstrateClassProfile(class.Spec.ControllerName)
	if !ok {
		if !isKaproControllerName(class.Spec.ControllerName) {
			return ctrl.Result{}, nil
		}
		profile = substrateClassProfile{
			reason:  "UnknownController",
			message: fmt.Sprintf("controllerName %q is not a built-in Kapro substrate controller", class.Spec.ControllerName),
		}
	}

	patch := client.MergeFrom(class.DeepCopy())
	now := metav1.Now()
	class.Status.ObservedGeneration = class.Generation
	class.Status.ExecutionModes = profile.executionModes
	class.Status.AcceptedConfigKinds = profile.acceptedConfigKinds
	class.Status.Capabilities = profile.capabilities
	status := metav1.ConditionTrue
	reason := profile.reason
	message := profile.message
	if profile.capabilities == nil {
		status = metav1.ConditionFalse
	}
	apimeta.SetStatusCondition(&class.Status.Conditions, metav1.Condition{
		Type:               "Accepted",
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: class.Generation,
		LastTransitionTime: now,
	})

	if err := r.Status().Patch(ctx, &class, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch SubstrateClass status: %w", err)
	}
	return ctrl.Result{}, nil
}

func (r *SubstrateClassReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kaprov1alpha2.SubstrateClass{}).
		Complete(r)
}

type substrateClassProfile struct {
	executionModes      *kaprov1alpha2.SubstrateClassExecutionModesStatus
	acceptedConfigKinds []kaprov1alpha2.SubstrateObjectKindReference
	capabilities        *kaprov1alpha2.SubstrateCapabilities
	reason              string
	message             string
}

func builtInSubstrateClassProfile(controllerName string) (substrateClassProfile, bool) {
	switch controllerName {
	case "kapro.io/argo-cd":
		return substrateClassProfile{
			executionModes: supportedExecutionModes(kaprov1alpha2.ExecutionModeHubPush, kaprov1alpha2.ExecutionModeSpokePull),
			acceptedConfigKinds: []kaprov1alpha2.SubstrateObjectKindReference{{
				APIVersion: "argocd.substrate.kapro.io/v1alpha1",
				Kind:       "ArgoCDSubstrateConfig",
			}},
			capabilities: operations(true, true, true, true, true, false, "git-revision", "helm-chart"),
			reason:       "BuiltInClassAccepted",
			message:      "built-in Argo CD substrate class is accepted",
		}, true
	case "kapro.io/flux":
		return substrateClassProfile{
			executionModes: supportedExecutionModes(kaprov1alpha2.ExecutionModeHubPush, kaprov1alpha2.ExecutionModeSpokePull),
			acceptedConfigKinds: []kaprov1alpha2.SubstrateObjectKindReference{{
				APIVersion: "flux.substrate.kapro.io/v1alpha1",
				Kind:       "FluxSubstrateConfig",
			}},
			capabilities: operations(true, true, true, true, true, false, "git-revision", "oci-artifact", "helm-chart"),
			reason:       "BuiltInClassAccepted",
			message:      "built-in Flux substrate class is accepted",
		}, true
	case "kapro.io/oci":
		return substrateClassProfile{
			executionModes: supportedExecutionModes(kaprov1alpha2.ExecutionModeSpokePull),
			acceptedConfigKinds: []kaprov1alpha2.SubstrateObjectKindReference{{
				APIVersion: "oci.substrate.kapro.io/v1alpha1",
				Kind:       "OCIBundleApplyConfig",
			}},
			capabilities: operations(true, true, false, true, false, false, "oci-artifact", "raw-yaml", "kustomize", "helm-chart"),
			reason:       "BuiltInClassAccepted",
			message:      "built-in OCI bundle substrate class is accepted",
		}, true
	case "kapro.io/kubernetes-apply":
		return substrateClassProfile{
			executionModes: supportedExecutionModes(kaprov1alpha2.ExecutionModeHubPush, kaprov1alpha2.ExecutionModeSpokePull),
			acceptedConfigKinds: []kaprov1alpha2.SubstrateObjectKindReference{{
				APIVersion: "kubernetes.substrate.kapro.io/v1alpha1",
				Kind:       "KubernetesApplyConfig",
			}},
			capabilities: operations(true, true, true, false, false, false, "raw-yaml", "kustomize"),
			reason:       "BuiltInClassAccepted",
			message:      "built-in Kubernetes apply substrate class is accepted",
		}, true
	case "kapro.io/webhook":
		return substrateClassProfile{
			executionModes: supportedExecutionModes(kaprov1alpha2.ExecutionModeHubPush, kaprov1alpha2.ExecutionModeExternalPull),
			acceptedConfigKinds: []kaprov1alpha2.SubstrateObjectKindReference{{
				APIVersion: "webhook.substrate.kapro.io/v1alpha1",
				Kind:       "WebhookSubstrateConfig",
			}},
			capabilities: operations(true, false, false, false, false, false, "webhook-payload"),
			reason:       "BuiltInClassAccepted",
			message:      "built-in webhook substrate class is accepted",
		}, true
	default:
		return substrateClassProfile{}, false
	}
}

func supportedExecutionModes(modes ...kaprov1alpha2.ExecutionMode) *kaprov1alpha2.SubstrateClassExecutionModesStatus {
	return &kaprov1alpha2.SubstrateClassExecutionModesStatus{Supported: modes}
}

func operations(apply, observe, dryRun, rollback, discover, twoPhase bool, inputTypes ...string) *kaprov1alpha2.SubstrateCapabilities {
	return &kaprov1alpha2.SubstrateCapabilities{
		Operations: &kaprov1alpha2.SubstrateOperationCapabilities{
			Apply:    apply,
			Observe:  observe,
			DryRun:   dryRun,
			Rollback: rollback,
			Discover: discover,
		},
		Staging:    &kaprov1alpha2.SubstrateStagingCapabilities{TwoPhase: twoPhase},
		InputTypes: inputTypes,
	}
}

func isKaproControllerName(controllerName string) bool {
	return strings.HasPrefix(controllerName, "kapro.io/")
}
