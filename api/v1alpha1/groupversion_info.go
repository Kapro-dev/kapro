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
		&Artifact{}, &ArtifactList{},
		&Environment{}, &EnvironmentList{},
		&Pipeline{}, &PipelineList{},
		&Release{}, &ReleaseList{},
		// Gate configuration
		&GatePolicy{}, &GatePolicyList{},
		&GateTemplate{}, &GateTemplateList{},
		// Fleet registry — written by kapro-cluster-controller
		&ManagedCluster{}, &ManagedClusterList{},
		// Internal / system objects
		&Sync{}, &SyncList{},
		&Approval{}, &ApprovalList{},
		&ReleaseReport{}, &ReleaseReportList{},
		&BootstrapToken{}, &BootstrapTokenList{},
	)
}
