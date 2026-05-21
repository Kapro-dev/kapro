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
		&Approval{}, &ApprovalList{},
		&Backend{}, &BackendList{},
		&Cluster{}, &ClusterList{},
		&ClusterTemplate{}, &ClusterTemplateList{},
		&Fleet{}, &FleetList{},
		&GateExpression{}, &GateExpressionList{},
		&Plan{}, &PlanList{},
		&Plugin{}, &PluginList{},
		&Policy{}, &PolicyList{},
		&Promotion{}, &PromotionList{},
		&PromotionRun{}, &PromotionRunList{},
		&Source{}, &SourceList{},
		&Target{}, &TargetList{},
		&Trigger{}, &TriggerList{},
	)
}
