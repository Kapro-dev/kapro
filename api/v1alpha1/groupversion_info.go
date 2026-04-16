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
		&Artifact{}, &ArtifactList{},
		&Environment{}, &EnvironmentList{},
		&ClusterRegistration{}, &ClusterRegistrationList{},
		&PromotionPolicy{}, &PromotionPolicyList{},
		&Pipeline{}, &PipelineList{},
		&Release{}, &ReleaseList{},
		&Promotion{}, &PromotionList{},
		&BatchRun{}, &BatchRunList{},
		&Approval{}, &ApprovalList{},
		&BootstrapToken{}, &BootstrapTokenList{},
		&PluginRegistration{}, &PluginRegistrationList{},
	)
}
