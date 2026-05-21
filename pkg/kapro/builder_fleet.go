package kapro

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

// FleetBuilder constructs a kapro.io/v1alpha2 Fleet for programmatic clients.
type FleetBuilder struct {
	name     string
	backend  string
	clusters []kaprov1alpha2.ClusterRef
}

// NewFleet starts a Fleet builder.
func NewFleet(name string) *FleetBuilder {
	return &FleetBuilder{name: name}
}

// WithBackend sets spec.delivery.backendRef. The SDK leaves mode unset so the
// API defaulting/validation path remains the single source of truth.
func (b *FleetBuilder) WithBackend(backendRef string) *FleetBuilder {
	b.backend = backendRef
	return b
}

// WithCluster adds one cluster by name and selector labels.
func (b *FleetBuilder) WithCluster(name string, labels map[string]string) *FleetBuilder {
	b.clusters = append(b.clusters, kaprov1alpha2.ClusterRef{
		Name:   name,
		Labels: copyStringMap(labels),
	})
	return b
}

// Build returns a new Fleet object.
func (b *FleetBuilder) Build() *kaprov1alpha2.Fleet {
	return &kaprov1alpha2.Fleet{
		TypeMeta: metav1.TypeMeta{
			APIVersion: kaprov1alpha2.GroupVersion.String(),
			Kind:       "Fleet",
		},
		ObjectMeta: metav1.ObjectMeta{Name: b.name},
		Spec: kaprov1alpha2.FleetSpec{
			Delivery: kaprov1alpha2.DeliverySpec{BackendRef: b.backend},
			Clusters: copyClusterRefs(b.clusters),
		},
	}
}

func copyClusterRefs(in []kaprov1alpha2.ClusterRef) []kaprov1alpha2.ClusterRef {
	if len(in) == 0 {
		return nil
	}
	out := make([]kaprov1alpha2.ClusterRef, 0, len(in))
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
