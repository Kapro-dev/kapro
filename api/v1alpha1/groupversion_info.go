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
		&Pipeline{}, &PipelineList{},
		&Release{}, &ReleaseList{},
		&ReleaseTarget{}, &ReleaseTargetList{},
		// Discovery layer
		&Source{}, &SourceList{},
		// Lean fleet registry (MemberCluster = legacy target inventory split)
		&MemberCluster{}, &MemberClusterList{},
		// Internal / system objects
		&Approval{}, &ApprovalList{},
	)
}
