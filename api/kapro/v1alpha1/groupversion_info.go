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
		&SubstrateDiscoveryPolicy{}, &SubstrateDiscoveryPolicyList{},
		&Approval{}, &ApprovalList{},
		&Substrate{}, &SubstrateList{},
		&Cluster{}, &ClusterList{},
		&ClusterTemplate{}, &ClusterTemplateList{},
		&Fleet{}, &FleetList{},
		&Plan{}, &PlanList{},
		&Plugin{}, &PluginList{},
		&Policy{}, &PolicyList{},
		&Promotion{}, &PromotionList{},
		&Source{}, &SourceList{},
		&SubstrateClass{}, &SubstrateClassList{},
		&Trigger{}, &TriggerList{},
	)
}
