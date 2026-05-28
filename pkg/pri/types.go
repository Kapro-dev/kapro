// Package pri contains Kapro's reference implementation of the PRI v0.1
// document contract.
package pri

const (
	APIVersion = "pri/v0.1"

	KindPromotion          = "Promotion"
	KindPromotionRun       = "PromotionRun"
	KindEvidence           = "Evidence"
	KindBinding            = "Binding"
	KindConformanceProfile = "ConformanceProfile"
)

// TypeMeta is the common PRI document envelope.
type TypeMeta struct {
	APIVersion string `json:"apiVersion" yaml:"apiVersion"`
	Kind       string `json:"kind" yaml:"kind"`
}

// Metadata is the common PRI metadata envelope. PRI keeps metadata extensible,
// mirroring Kubernetes-style object metadata, while keeping spec/status strict.
type Metadata struct {
	Name        string            `json:"name" yaml:"name"`
	Labels      map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty" yaml:"annotations,omitempty"`
}

// Promotion is intent to advance one logical unit and one or more artifacts
// across explicit targets.
type Promotion struct {
	TypeMeta `json:",inline" yaml:",inline"`
	Metadata Metadata      `json:"metadata" yaml:"metadata"`
	Spec     PromotionSpec `json:"spec" yaml:"spec"`
}

type PromotionSpec struct {
	Unit      string              `json:"unit" yaml:"unit"`
	Artifacts []Artifact          `json:"artifacts" yaml:"artifacts"`
	Plan      *Plan               `json:"plan,omitempty" yaml:"plan,omitempty"`
	Checks    []Check             `json:"checks,omitempty" yaml:"checks,omitempty"`
	Targets   []Target            `json:"targets" yaml:"targets"`
	Evidence  []PromotionEvidence `json:"evidence,omitempty" yaml:"evidence,omitempty"`
}

type Artifact struct {
	Name    string `json:"name" yaml:"name"`
	Version string `json:"version,omitempty" yaml:"version,omitempty"`
	Digest  string `json:"digest,omitempty" yaml:"digest,omitempty"`
	URI     string `json:"uri,omitempty" yaml:"uri,omitempty"`
}

type Plan struct {
	Ref string `json:"ref" yaml:"ref"`
}

type Check struct {
	Name         string   `json:"name" yaml:"name"`
	Required     *bool    `json:"required,omitempty" yaml:"required,omitempty"`
	PolicyRef    string   `json:"policyRef,omitempty" yaml:"policyRef,omitempty"`
	EvidenceRefs []string `json:"evidenceRefs,omitempty" yaml:"evidenceRefs,omitempty"`
}

type Target struct {
	Name     string            `json:"name" yaml:"name"`
	Labels   map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
	Delivery *Delivery         `json:"delivery,omitempty" yaml:"delivery,omitempty"`
}

type Delivery struct {
	Ref        string            `json:"ref" yaml:"ref"`
	Mode       string            `json:"mode" yaml:"mode"`
	Parameters map[string]string `json:"parameters,omitempty" yaml:"parameters,omitempty"`
}

type PromotionEvidence struct {
	Name   string `json:"name" yaml:"name"`
	Type   string `json:"type" yaml:"type"`
	URI    string `json:"uri" yaml:"uri"`
	Digest string `json:"digest,omitempty" yaml:"digest,omitempty"`
}

// PromotionRun records runtime state for executing a Promotion.
type PromotionRun struct {
	TypeMeta `json:",inline" yaml:",inline"`
	Metadata Metadata           `json:"metadata" yaml:"metadata"`
	Spec     PromotionRunSpec   `json:"spec" yaml:"spec"`
	Status   PromotionRunStatus `json:"status" yaml:"status"`
}

type PromotionRunSpec struct {
	PromotionRef string `json:"promotionRef" yaml:"promotionRef"`
}

type PromotionRunStatus struct {
	Phase               string         `json:"phase" yaml:"phase"`
	ImplementationPhase string         `json:"implementationPhase,omitempty" yaml:"implementationPhase,omitempty"`
	StartedAt           string         `json:"startedAt,omitempty" yaml:"startedAt,omitempty"`
	CompletedAt         string         `json:"completedAt,omitempty" yaml:"completedAt,omitempty"`
	Attempts            []Attempt      `json:"attempts,omitempty" yaml:"attempts,omitempty"`
	CheckResults        []CheckResult  `json:"checkResults,omitempty" yaml:"checkResults,omitempty"`
	TargetResults       []TargetResult `json:"targetResults,omitempty" yaml:"targetResults,omitempty"`
}

type Attempt struct {
	ID                  string `json:"id" yaml:"id"`
	Phase               string `json:"phase,omitempty" yaml:"phase,omitempty"`
	ImplementationPhase string `json:"implementationPhase,omitempty" yaml:"implementationPhase,omitempty"`
	StartedAt           string `json:"startedAt" yaml:"startedAt"`
	CompletedAt         string `json:"completedAt,omitempty" yaml:"completedAt,omitempty"`
}

type CheckResult struct {
	Check               string   `json:"check" yaml:"check"`
	Phase               string   `json:"phase" yaml:"phase"`
	ImplementationPhase string   `json:"implementationPhase,omitempty" yaml:"implementationPhase,omitempty"`
	EvidenceRefs        []string `json:"evidenceRefs,omitempty" yaml:"evidenceRefs,omitempty"`
}

type TargetResult struct {
	Target              string   `json:"target" yaml:"target"`
	Phase               string   `json:"phase" yaml:"phase"`
	ImplementationPhase string   `json:"implementationPhase,omitempty" yaml:"implementationPhase,omitempty"`
	EvidenceRefs        []string `json:"evidenceRefs,omitempty" yaml:"evidenceRefs,omitempty"`
}

// Evidence is a standalone evidence document.
type Evidence struct {
	TypeMeta `json:",inline" yaml:",inline"`
	Metadata Metadata     `json:"metadata" yaml:"metadata"`
	Spec     EvidenceSpec `json:"spec" yaml:"spec"`
}

type EvidenceSpec struct {
	Type        string   `json:"type" yaml:"type"`
	URI         string   `json:"uri" yaml:"uri"`
	Digest      string   `json:"digest,omitempty" yaml:"digest,omitempty"`
	SubjectRefs []string `json:"subjectRefs,omitempty" yaml:"subjectRefs,omitempty"`
}

// Binding summarizes how an implementation maps PRI to an external system.
type Binding struct {
	TypeMeta `json:",inline" yaml:",inline"`
	Metadata Metadata    `json:"metadata" yaml:"metadata"`
	Spec     BindingSpec `json:"spec" yaml:"spec"`
}

type BindingSpec struct {
	Category              string                 `json:"category" yaml:"category"`
	Summary               string                 `json:"summary,omitempty" yaml:"summary,omitempty"`
	PRIVersions           []string               `json:"priVersions" yaml:"priVersions"`
	AdoptionModes         []string               `json:"adoptionModes" yaml:"adoptionModes"`
	RoundTrip             string                 `json:"roundTrip" yaml:"roundTrip"`
	Mappings              *BindingMappings       `json:"mappings,omitempty" yaml:"mappings,omitempty"`
	RequiredConfiguration []BindingConfiguration `json:"requiredConfiguration,omitempty" yaml:"requiredConfiguration,omitempty"`
	Unsupported           []string               `json:"unsupported,omitempty" yaml:"unsupported,omitempty"`
	References            []BindingReference     `json:"references,omitempty" yaml:"references,omitempty"`
}

type BindingMappings struct {
	Objects []BindingMapping `json:"objects,omitempty" yaml:"objects,omitempty"`
	Fields  []BindingMapping `json:"fields,omitempty" yaml:"fields,omitempty"`
}

type BindingMapping struct {
	PRI      string `json:"pri" yaml:"pri"`
	External string `json:"external" yaml:"external"`
	Notes    string `json:"notes,omitempty" yaml:"notes,omitempty"`
}

type BindingConfiguration struct {
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description" yaml:"description"`
}

type BindingReference struct {
	Title string `json:"title" yaml:"title"`
	URI   string `json:"uri" yaml:"uri"`
}

// ConformanceProfile describes which PRI surfaces an implementation supports.
type ConformanceProfile struct {
	TypeMeta `json:",inline" yaml:",inline"`
	Metadata Metadata               `json:"metadata" yaml:"metadata"`
	Spec     ConformanceProfileSpec `json:"spec" yaml:"spec"`
}

type ConformanceProfileSpec struct {
	PRIVersion   string               `json:"priVersion" yaml:"priVersion"`
	AdoptionMode string               `json:"adoptionMode" yaml:"adoptionMode"`
	Conformance  ConformanceStatement `json:"conformance" yaml:"conformance"`
}

type ConformanceStatement struct {
	Document bool `json:"document" yaml:"document"`
	Runtime  bool `json:"runtime" yaml:"runtime"`
	Decision bool `json:"decision" yaml:"decision"`
}

// DecodedDocument is one parsed PRI document plus optional source path context.
type DecodedDocument struct {
	Source string
	Object any
}
