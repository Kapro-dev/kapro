package v1alpha2

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	GroupVersion  = schema.GroupVersion{Group: "kapro.io", Version: "v1alpha2"}
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}
	AddToScheme   = SchemeBuilder.AddToScheme
)

func init() {
	SchemeBuilder.Register(
		// Promotion-domain API
		&Cluster{}, &ClusterList{},
		&Plan{}, &PlanList{},
		&PromotionRun{}, &PromotionRunList{},
		&Target{}, &TargetList{},
		&Trigger{}, &TriggerList{},
		&Backend{}, &BackendList{},
		&Plugin{}, &PluginList{},
		&ClusterTemplate{}, &ClusterTemplateList{},
		// Internal / system objects
		&Approval{}, &ApprovalList{},

		// AI agent trust boundaries
		&Policy{}, &PolicyList{},

		// Fleet entry point + promotion source
		&Fleet{}, &FleetList{},
		&Source{}, &SourceList{},
		&Promotion{}, &PromotionList{},
	)
}
