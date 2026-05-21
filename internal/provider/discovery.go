package provider

import (
	"context"
	"errors"
	"fmt"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

// Discoverer enumerates clusters from one fleet source. Implementations are
// per-source (GCP Fleet API, RHACM ManagedCluster watch, CAPI Cluster watch,
// EKS DescribeClusters, etc.). The reconciler that imports clusters into
// FleetCluster CRs is discoverer-agnostic — it calls List + Provider and
// upserts.
type Discoverer interface {
	// List returns the currently-discovered clusters. Implementations
	// surface their own connectivity errors as the returned error; the
	// reconciler converts that into a Stalled condition.
	List(ctx context.Context) ([]ClusterInfo, error)

	// Provider returns the {kind, parameters} written into each imported
	// FleetCluster's spec.provider. Kind is fixed per discoverer (gcp →
	// gcp-fleet, rhacm → rhacm, etc.); parameters may include source
	// identifiers that downstream actuators need (e.g. GCP project).
	Provider() kaprov1alpha2.ClusterProvider

	// SourceKind returns the short identifier echoed on
	// FleetClusterTemplate.status.sourceKind (gcp, aws, rhacm, ...).
	SourceKind() string
}

// ErrSourceNotImplemented is returned by NewDiscoverer when the requested
// source branch is reserved for a future release. The reconciler surfaces
// this as a Stalled condition with reason=SourceNotImplemented so the
// operator sees the misconfiguration immediately rather than getting a
// silent no-op.
type ErrSourceNotImplemented struct {
	Branch string
}

func (e ErrSourceNotImplemented) Error() string {
	return fmt.Sprintf("fleet source branch %q is reserved for a future release (currently implemented: gcp)", e.Branch)
}

// IsSourceNotImplemented reports whether err is ErrSourceNotImplemented.
func IsSourceNotImplemented(err error) bool {
	var e ErrSourceNotImplemented
	return errors.As(err, &e)
}

// NewDiscoverer dispatches a FleetClusterTemplate source to its Discoverer.
// Exactly one branch must be set: zero is a config error, multiple is
// ambiguous (the spec example shows oneOf semantics). An admission webhook
// will reject these at admission time once wired (PR-7+); this function is
// defensive so the reconciler never imports from an unintended source even
// if a webhook bypass exists.
func NewDiscoverer(src kaprov1alpha2.ClusterTemplateSource) (Discoverer, error) {
	var set []string
	if src.GCP != nil {
		set = append(set, "gcp")
	}
	if src.AWS != nil {
		set = append(set, "aws")
	}
	if src.Azure != nil {
		set = append(set, "azure")
	}
	if src.RHACM != nil {
		set = append(set, "rhacm")
	}
	if src.CAPI != nil {
		set = append(set, "capi")
	}
	if src.Static != nil {
		set = append(set, "static")
	}

	switch len(set) {
	case 0:
		return nil, errors.New("no source branch set; one of gcp/aws/azure/rhacm/capi/static is required")
	case 1: // dispatch below
	default:
		return nil, fmt.Errorf("exactly one source branch must be set; got %d (%v)", len(set), set)
	}

	switch set[0] {
	case "gcp":
		return &gcpFleetDiscoverer{project: src.GCP.Project}, nil
	case "aws":
		return nil, ErrSourceNotImplemented{Branch: "aws"}
	case "azure":
		return nil, ErrSourceNotImplemented{Branch: "azure"}
	case "rhacm":
		return nil, ErrSourceNotImplemented{Branch: "rhacm"}
	case "capi":
		return nil, ErrSourceNotImplemented{Branch: "capi"}
	case "static":
		return nil, ErrSourceNotImplemented{Branch: "static"}
	default:
		return nil, fmt.Errorf("unknown source branch %q", set[0])
	}
}

// gcpFleetDiscoverer is the GCP Fleet implementation of Discoverer.
// Wraps GCPFleetProvider.ListClusters so the reconciler doesn't depend on
// the provider directly.
type gcpFleetDiscoverer struct {
	project string
}

func (d *gcpFleetDiscoverer) List(ctx context.Context) ([]ClusterInfo, error) {
	return (&GCPFleetProvider{Project: d.project}).ListClusters(ctx)
}

func (d *gcpFleetDiscoverer) Provider() kaprov1alpha2.ClusterProvider {
	return kaprov1alpha2.ClusterProvider{
		Kind:       "gcp-fleet",
		Parameters: map[string]string{"project": d.project},
	}
}

func (d *gcpFleetDiscoverer) SourceKind() string { return "gcp" }
