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
		// v1alpha2 Kinds declared in PR 1 of the migration. The shared-
		// name Kinds (Approval, Promotion, PromotionRun) are intentionally
		// NOT registered yet — they would force a multi-version CRD that
		// kube-apiserver refuses to establish without a conversion
		// strategy. They are added back in PRs 5-9 when the controllers
		// migrate and v1alpha1 stops being served.
		&Fleet{}, &FleetList{},
		&Plan{}, &PlanList{},
		&Source{}, &SourceList{},
		&Trigger{}, &TriggerList{},
		&Target{}, &TargetList{},
		&Backend{}, &BackendList{},
		&Cluster{}, &ClusterList{},
		&ClusterTemplate{}, &ClusterTemplateList{},
		&Plugin{}, &PluginList{},
		&Policy{}, &PolicyList{},
	)
}
