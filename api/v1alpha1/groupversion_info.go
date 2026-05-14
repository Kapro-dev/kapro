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
		// User-facing delivery objects
		&Pipeline{}, &PipelineList{},
		&Release{}, &ReleaseList{},
		&ReleaseTrigger{}, &ReleaseTriggerList{},
		&ReleaseTarget{}, &ReleaseTargetList{},
		// Lean fleet registry (MemberCluster = legacy target inventory split)
		&MemberCluster{}, &MemberClusterList{},
		&PluginRegistration{}, &PluginRegistrationList{},
		// Internal / system objects
		&Approval{}, &ApprovalList{},

		// AI agent trust boundaries
		&AgentPolicy{}, &AgentPolicyList{},

		// Kapro entry point + app bundle
		&Kapro{}, &KaproList{},
		&KaproApp{}, &KaproAppList{},
	)
}
