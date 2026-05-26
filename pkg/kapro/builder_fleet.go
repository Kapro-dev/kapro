package kapro

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
)

// FleetBuilder constructs a kapro.io/v1alpha1 Fleet for programmatic clients.
type FleetBuilder struct {
	name      string
	substrate string
	clusters  []kaprov1alpha1.ClusterRef
}

// NewFleet starts a Fleet builder.
func NewFleet(name string) *FleetBuilder {
	return &FleetBuilder{name: name}
}

// WithSubstrate sets spec.delivery.ref. The SDK leaves mode unset so the
// API defaulting/validation path remains the single source of truth.
func (b *FleetBuilder) WithSubstrate(substrate string) *FleetBuilder {
	b.substrate = substrate
	return b
}

// WithCluster adds one cluster by name and selector labels.
func (b *FleetBuilder) WithCluster(name string, labels map[string]string) *FleetBuilder {
	b.clusters = append(b.clusters, kaprov1alpha1.ClusterRef{
		Name:   name,
		Labels: copyStringMap(labels),
	})
	return b
}

// Build returns a new Fleet object.
func (b *FleetBuilder) Build() *kaprov1alpha1.Fleet {
	return &kaprov1alpha1.Fleet{
		TypeMeta: metav1.TypeMeta{
			APIVersion: kaprov1alpha1.GroupVersion.String(),
			Kind:       "Fleet",
		},
		ObjectMeta: metav1.ObjectMeta{Name: b.name},
		Spec: kaprov1alpha1.FleetSpec{
			Delivery: kaprov1alpha1.SubstrateBindingSpec{Ref: b.substrate},
			Clusters: copyClusterRefs(b.clusters),
		},
	}
}

func copyClusterRefs(in []kaprov1alpha1.ClusterRef) []kaprov1alpha1.ClusterRef {
	if len(in) == 0 {
		return nil
	}
	out := make([]kaprov1alpha1.ClusterRef, 0, len(in))
	for _, cluster := range in {
		cluster.Labels = copyStringMap(cluster.Labels)
		if cluster.GCP != nil {
			gcp := *cluster.GCP
			cluster.GCP = &gcp
		}
		out = append(out, cluster)
	}
	return out
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
