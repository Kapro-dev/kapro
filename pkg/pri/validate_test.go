package pri

import (
	"strings"
	"testing"
)

func TestDecodeDocumentsValidatesMinimalPromotion(t *testing.T) {
	raw := `
apiVersion: pri/v0.1
kind: Promotion
metadata:
  name: hello-world
spec:
  unit: hello
  artifacts:
    - name: hello
      version: v1.0.0
  targets:
    - name: dev
`
	docs, err := DecodeDocuments(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("DecodeDocuments: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("docs len = %d, want 1", len(docs))
	}
	if got := KindOf(docs[0].Object); got != KindPromotion {
		t.Fatalf("kind = %q, want %q", got, KindPromotion)
	}
}

func TestDecodeDocumentsRejectsUnknownSpecField(t *testing.T) {
	raw := `
apiVersion: pri/v0.1
kind: Promotion
metadata:
  name: hello-world
spec:
  unit: hello
  typo: should-fail
  artifacts:
    - name: hello
  targets:
    - name: dev
`
	if _, err := DecodeDocuments(strings.NewReader(raw)); err == nil {
		t.Fatal("DecodeDocuments succeeded, want unknown field error")
	}
}

func TestValidatePromotionRunRejectsNonPortablePhase(t *testing.T) {
	run := &PromotionRun{
		TypeMeta: TypeMeta{APIVersion: APIVersion, Kind: KindPromotionRun},
		Metadata: Metadata{Name: "hello-run"},
		Spec:     PromotionRunSpec{PromotionRef: "hello"},
		Status:   PromotionRunStatus{Phase: "Progressing"},
	}
	if err := Validate(run); err == nil {
		t.Fatal("Validate succeeded, want invalid phase error")
	}
}

func TestValidateBindingAndConformanceProfile(t *testing.T) {
	binding := &Binding{
		TypeMeta: TypeMeta{APIVersion: APIVersion, Kind: KindBinding},
		Metadata: Metadata{Name: "example-binding"},
		Spec: BindingSpec{
			Category:      "platform",
			PRIVersions:   []string{"v0.1"},
			AdoptionModes: []string{"emission"},
			RoundTrip:     "lossy",
		},
	}
	if err := Validate(binding); err != nil {
		t.Fatalf("Validate(binding): %v", err)
	}

	profile := &ConformanceProfile{
		TypeMeta: TypeMeta{APIVersion: APIVersion, Kind: KindConformanceProfile},
		Metadata: Metadata{Name: "example"},
		Spec: ConformanceProfileSpec{
			PRIVersion:   "v0.1",
			AdoptionMode: "emission",
			Conformance:  ConformanceStatement{Document: true, Runtime: true},
		},
	}
	if err := Validate(profile); err != nil {
		t.Fatalf("Validate(profile): %v", err)
	}
}
