package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	GroupVersion  = schema.GroupVersion{Group: "kapro.io", Version: "v1alpha1"}
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}
	AddToScheme   = SchemeBuilder.AddToScheme
)

func init() {
	SchemeBuilder.Register(
		// Promotion-domain API
		&FleetCluster{}, &FleetClusterList{},
		&PromotionPlan{}, &PromotionPlanList{},
		&Promotion{}, &PromotionList{},
		&PromotionRun{}, &PromotionRunList{},
		&PromotionTarget{}, &PromotionTargetList{},
		&PromotionTrigger{}, &PromotionTriggerList{},
		&PromotionPolicy{}, &PromotionPolicyList{},
		&NotificationProvider{}, &NotificationProviderList{},
		&NotificationPolicy{}, &NotificationPolicyList{},
		&BackendProfile{}, &BackendProfileList{},
		&PluginRegistration{}, &PluginRegistrationList{},
		&FleetClusterTemplate{}, &FleetClusterTemplateList{},
		// Internal / system objects
		&Approval{}, &ApprovalList{},

		// AI agent trust boundaries
		&AgentPolicy{}, &AgentPolicyList{},

		// Kapro entry point + promotion source
		&Kapro{}, &KaproList{},
		&PromotionSource{}, &PromotionSourceList{},
	)
}
