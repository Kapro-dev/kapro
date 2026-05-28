package pri

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

var (
	identifierRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)
	digestRE     = regexp.MustCompile(`^[a-z0-9][a-z0-9+._-]*:[A-Za-z0-9=_+./-]+$`)
)

// ReadDocuments reads every PRI YAML or JSON document from the supplied files
// and directories. Directories are walked recursively in lexical order.
func ReadDocuments(paths ...string) ([]DecodedDocument, error) {
	if len(paths) == 0 {
		return nil, errors.New("at least one file or directory is required")
	}
	var out []DecodedDocument
	for _, path := range paths {
		docs, err := readPath(path)
		if err != nil {
			return nil, err
		}
		out = append(out, docs...)
	}
	return out, nil
}

func readPath(path string) ([]DecodedDocument, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return readFile(path)
	}
	var files []string
	if err := filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if isDocumentPath(p) {
			files = append(files, p)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	sort.Strings(files)
	var out []DecodedDocument
	for _, file := range files {
		docs, err := readFile(file)
		if err != nil {
			return nil, err
		}
		out = append(out, docs...)
	}
	return out, nil
}

func readFile(path string) ([]DecodedDocument, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	docs, err := DecodeDocuments(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for i := range docs {
		docs[i].Source = path
	}
	return docs, nil
}

func isDocumentPath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml", ".json":
		return true
	default:
		return false
	}
}

// DecodeDocuments parses a YAML/JSON stream, validates each PRI object, and
// rejects unknown spec/status fields to match PRI's strict core contract.
func DecodeDocuments(r io.Reader) ([]DecodedDocument, error) {
	dec := yaml.NewDecoder(r)
	var out []DecodedDocument
	for {
		var node yaml.Node
		if err := dec.Decode(&node); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		if isEmptyDocument(&node) {
			continue
		}
		doc, err := decodeDocumentNode(&node)
		if err != nil {
			return nil, err
		}
		out = append(out, doc)
	}
	return out, nil
}

func isEmptyDocument(node *yaml.Node) bool {
	return node == nil || len(node.Content) == 0
}

func decodeDocumentNode(node *yaml.Node) (DecodedDocument, error) {
	var tm TypeMeta
	if err := node.Decode(&tm); err != nil {
		return DecodedDocument{}, err
	}
	if err := checkKnownFields(tm.Kind, documentMappingNode(node)); err != nil {
		return DecodedDocument{}, err
	}

	var obj any
	switch tm.Kind {
	case KindPromotion:
		var typed Promotion
		if err := node.Decode(&typed); err != nil {
			return DecodedDocument{}, err
		}
		obj = &typed
	case KindPromotionRun:
		var typed PromotionRun
		if err := node.Decode(&typed); err != nil {
			return DecodedDocument{}, err
		}
		obj = &typed
	case KindEvidence:
		var typed Evidence
		if err := node.Decode(&typed); err != nil {
			return DecodedDocument{}, err
		}
		obj = &typed
	case KindBinding:
		var typed Binding
		if err := node.Decode(&typed); err != nil {
			return DecodedDocument{}, err
		}
		obj = &typed
	case KindConformanceProfile:
		var typed ConformanceProfile
		if err := node.Decode(&typed); err != nil {
			return DecodedDocument{}, err
		}
		obj = &typed
	default:
		return DecodedDocument{}, fmt.Errorf("unsupported PRI kind %q", tm.Kind)
	}
	if err := Validate(obj); err != nil {
		return DecodedDocument{}, err
	}
	return DecodedDocument{Object: obj}, nil
}

func documentMappingNode(node *yaml.Node) *yaml.Node {
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		return node.Content[0]
	}
	return node
}

// Validate verifies the PRI v0.1 contract for one typed document.
func Validate(obj any) error {
	switch typed := obj.(type) {
	case *Promotion:
		return validatePromotion(typed)
	case Promotion:
		return validatePromotion(&typed)
	case *PromotionRun:
		return validatePromotionRun(typed)
	case PromotionRun:
		return validatePromotionRun(&typed)
	case *Evidence:
		return validateEvidence(typed)
	case Evidence:
		return validateEvidence(&typed)
	case *Binding:
		return validateBinding(typed)
	case Binding:
		return validateBinding(&typed)
	case *ConformanceProfile:
		return validateConformanceProfile(typed)
	case ConformanceProfile:
		return validateConformanceProfile(&typed)
	default:
		return fmt.Errorf("unsupported PRI object type %T", obj)
	}
}

// KindOf returns the PRI kind for a typed document.
func KindOf(obj any) string {
	switch typed := obj.(type) {
	case *Promotion:
		return typed.Kind
	case Promotion:
		return typed.Kind
	case *PromotionRun:
		return typed.Kind
	case PromotionRun:
		return typed.Kind
	case *Evidence:
		return typed.Kind
	case Evidence:
		return typed.Kind
	case *Binding:
		return typed.Kind
	case Binding:
		return typed.Kind
	case *ConformanceProfile:
		return typed.Kind
	case ConformanceProfile:
		return typed.Kind
	default:
		return ""
	}
}

// NameOf returns metadata.name for a typed PRI document.
func NameOf(obj any) string {
	switch typed := obj.(type) {
	case *Promotion:
		return typed.Metadata.Name
	case Promotion:
		return typed.Metadata.Name
	case *PromotionRun:
		return typed.Metadata.Name
	case PromotionRun:
		return typed.Metadata.Name
	case *Evidence:
		return typed.Metadata.Name
	case Evidence:
		return typed.Metadata.Name
	case *Binding:
		return typed.Metadata.Name
	case Binding:
		return typed.Metadata.Name
	case *ConformanceProfile:
		return typed.Metadata.Name
	case ConformanceProfile:
		return typed.Metadata.Name
	default:
		return ""
	}
}

func validatePromotion(p *Promotion) error {
	if err := validateEnvelope(p.APIVersion, p.Kind, KindPromotion, p.Metadata); err != nil {
		return err
	}
	if err := validateIdentifier("spec.unit", p.Spec.Unit); err != nil {
		return err
	}
	if len(p.Spec.Artifacts) == 0 {
		return errors.New("spec.artifacts must contain at least one artifact")
	}
	for i, artifact := range p.Spec.Artifacts {
		prefix := fmt.Sprintf("spec.artifacts[%d]", i)
		if err := validateIdentifier(prefix+".name", artifact.Name); err != nil {
			return err
		}
		if artifact.Digest != "" && !digestRE.MatchString(artifact.Digest) {
			return fmt.Errorf("%s.digest must be algorithm:value form", prefix)
		}
		if err := validateOptionalURI(prefix+".uri", artifact.URI); err != nil {
			return err
		}
	}
	if p.Spec.Plan != nil {
		if err := validateIdentifier("spec.plan.ref", p.Spec.Plan.Ref); err != nil {
			return err
		}
	}
	for i, check := range p.Spec.Checks {
		prefix := fmt.Sprintf("spec.checks[%d]", i)
		if err := validateIdentifier(prefix+".name", check.Name); err != nil {
			return err
		}
		if err := validateOptionalIdentifier(prefix+".policyRef", check.PolicyRef); err != nil {
			return err
		}
		if err := validateIdentifierList(prefix+".evidenceRefs", check.EvidenceRefs); err != nil {
			return err
		}
	}
	if len(p.Spec.Targets) == 0 {
		return errors.New("spec.targets must contain at least one target")
	}
	for i, target := range p.Spec.Targets {
		prefix := fmt.Sprintf("spec.targets[%d]", i)
		if err := validateIdentifier(prefix+".name", target.Name); err != nil {
			return err
		}
		if target.Delivery != nil {
			if err := validateIdentifier(prefix+".delivery.ref", target.Delivery.Ref); err != nil {
				return err
			}
			if !oneOf(target.Delivery.Mode, "push", "pull", "manual") {
				return fmt.Errorf("%s.delivery.mode must be one of push, pull, manual", prefix)
			}
		}
	}
	for i, evidence := range p.Spec.Evidence {
		prefix := fmt.Sprintf("spec.evidence[%d]", i)
		if err := validateIdentifier(prefix+".name", evidence.Name); err != nil {
			return err
		}
		if err := validateEvidenceShape(prefix, evidence.Type, evidence.URI, evidence.Digest); err != nil {
			return err
		}
	}
	return nil
}

func validatePromotionRun(run *PromotionRun) error {
	if err := validateEnvelope(run.APIVersion, run.Kind, KindPromotionRun, run.Metadata); err != nil {
		return err
	}
	if err := validateIdentifier("spec.promotionRef", run.Spec.PromotionRef); err != nil {
		return err
	}
	if !oneOf(run.Status.Phase, "Pending", "Running", "Paused", "Succeeded", "Failed", "Cancelled") {
		return fmt.Errorf("status.phase must be one of Pending, Running, Paused, Succeeded, Failed, Cancelled")
	}
	if err := validateOptionalTimestamp("status.startedAt", run.Status.StartedAt); err != nil {
		return err
	}
	if err := validateOptionalTimestamp("status.completedAt", run.Status.CompletedAt); err != nil {
		return err
	}
	for i, attempt := range run.Status.Attempts {
		prefix := fmt.Sprintf("status.attempts[%d]", i)
		if strings.TrimSpace(attempt.ID) == "" {
			return fmt.Errorf("%s.id is required", prefix)
		}
		if attempt.Phase != "" && !oneOf(attempt.Phase, "Pending", "Running", "Paused", "Succeeded", "Failed", "Cancelled") {
			return fmt.Errorf("%s.phase has invalid PRI PromotionRun phase %q", prefix, attempt.Phase)
		}
		if err := validateOptionalTimestamp(prefix+".startedAt", attempt.StartedAt); err != nil {
			return err
		}
		if attempt.StartedAt == "" {
			return fmt.Errorf("%s.startedAt is required", prefix)
		}
		if err := validateOptionalTimestamp(prefix+".completedAt", attempt.CompletedAt); err != nil {
			return err
		}
	}
	for i, result := range run.Status.CheckResults {
		prefix := fmt.Sprintf("status.checkResults[%d]", i)
		if err := validateIdentifier(prefix+".check", result.Check); err != nil {
			return err
		}
		if !oneOf(result.Phase, "Pending", "Running", "Succeeded", "Failed", "Skipped") {
			return fmt.Errorf("%s.phase must be one of Pending, Running, Succeeded, Failed, Skipped", prefix)
		}
		if err := validateIdentifierList(prefix+".evidenceRefs", result.EvidenceRefs); err != nil {
			return err
		}
	}
	for i, result := range run.Status.TargetResults {
		prefix := fmt.Sprintf("status.targetResults[%d]", i)
		if err := validateIdentifier(prefix+".target", result.Target); err != nil {
			return err
		}
		if !oneOf(result.Phase, "Pending", "Delivering", "Verifying", "Succeeded", "Failed", "Skipped") {
			return fmt.Errorf("%s.phase must be one of Pending, Delivering, Verifying, Succeeded, Failed, Skipped", prefix)
		}
		if err := validateIdentifierList(prefix+".evidenceRefs", result.EvidenceRefs); err != nil {
			return err
		}
	}
	return nil
}

func validateEvidence(e *Evidence) error {
	if err := validateEnvelope(e.APIVersion, e.Kind, KindEvidence, e.Metadata); err != nil {
		return err
	}
	if err := validateEvidenceShape("spec", e.Spec.Type, e.Spec.URI, e.Spec.Digest); err != nil {
		return err
	}
	return validateIdentifierList("spec.subjectRefs", e.Spec.SubjectRefs)
}

func validateBinding(b *Binding) error {
	if err := validateEnvelope(b.APIVersion, b.Kind, KindBinding, b.Metadata); err != nil {
		return err
	}
	if !oneOf(b.Spec.Category, "platform", "event", "evidence", "artifact", "storage") {
		return errors.New("spec.category must be one of platform, event, evidence, artifact, storage")
	}
	if len(b.Spec.PRIVersions) == 0 {
		return errors.New("spec.priVersions must contain at least one version")
	}
	for i, version := range b.Spec.PRIVersions {
		if version != "v0.1" {
			return fmt.Errorf("spec.priVersions[%d] must be v0.1 in this implementation", i)
		}
	}
	if len(b.Spec.AdoptionModes) == 0 {
		return errors.New("spec.adoptionModes must contain at least one mode")
	}
	for i, mode := range b.Spec.AdoptionModes {
		if !oneOf(mode, "native", "emission", "bridge") {
			return fmt.Errorf("spec.adoptionModes[%d] must be one of native, emission, bridge", i)
		}
	}
	if !oneOf(b.Spec.RoundTrip, "lossless", "lossy", "emission-only") {
		return errors.New("spec.roundTrip must be one of lossless, lossy, emission-only")
	}
	for i, cfg := range b.Spec.RequiredConfiguration {
		if err := validateIdentifier(fmt.Sprintf("spec.requiredConfiguration[%d].name", i), cfg.Name); err != nil {
			return err
		}
		if strings.TrimSpace(cfg.Description) == "" {
			return fmt.Errorf("spec.requiredConfiguration[%d].description is required", i)
		}
	}
	for i, ref := range b.Spec.References {
		if strings.TrimSpace(ref.Title) == "" {
			return fmt.Errorf("spec.references[%d].title is required", i)
		}
		if err := validateRequiredURI(fmt.Sprintf("spec.references[%d].uri", i), ref.URI); err != nil {
			return err
		}
	}
	return nil
}

func validateConformanceProfile(p *ConformanceProfile) error {
	if err := validateEnvelope(p.APIVersion, p.Kind, KindConformanceProfile, p.Metadata); err != nil {
		return err
	}
	if p.Spec.PRIVersion != "v0.1" {
		return errors.New("spec.priVersion must be v0.1")
	}
	if !oneOf(p.Spec.AdoptionMode, "native", "emission", "bridge") {
		return errors.New("spec.adoptionMode must be one of native, emission, bridge")
	}
	return nil
}

func validateEnvelope(apiVersion, kind, wantKind string, metadata Metadata) error {
	if apiVersion != APIVersion {
		return fmt.Errorf("apiVersion must be %q", APIVersion)
	}
	if kind != wantKind {
		return fmt.Errorf("kind must be %q", wantKind)
	}
	return validateIdentifier("metadata.name", metadata.Name)
}

func validateEvidenceShape(path, typ, uri, digest string) error {
	if !oneOf(typ, "verification", "approval", "test", "scan", "audit", "other") {
		return fmt.Errorf("%s.type must be one of verification, approval, test, scan, audit, other", path)
	}
	if err := validateRequiredURI(path+".uri", uri); err != nil {
		return err
	}
	if digest != "" && !digestRE.MatchString(digest) {
		return fmt.Errorf("%s.digest must be algorithm:value form", path)
	}
	return nil
}

func validateIdentifier(path, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", path)
	}
	if len(value) > 253 || !identifierRE.MatchString(value) {
		return fmt.Errorf("%s must be a DNS-1123 compatible PRI identifier", path)
	}
	return nil
}

func validateOptionalIdentifier(path, value string) error {
	if value == "" {
		return nil
	}
	return validateIdentifier(path, value)
}

func validateIdentifierList(path string, values []string) error {
	for i, value := range values {
		if err := validateIdentifier(fmt.Sprintf("%s[%d]", path, i), value); err != nil {
			return err
		}
	}
	return nil
}

func validateRequiredURI(path, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", path)
	}
	return validateOptionalURI(path, value)
}

func validateOptionalURI(path, value string) error {
	if value == "" {
		return nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" {
		return fmt.Errorf("%s must be an absolute URI", path)
	}
	return nil
}

func validateOptionalTimestamp(path, value string) error {
	if value == "" {
		return nil
	}
	if _, err := time.Parse(time.RFC3339, value); err != nil {
		return fmt.Errorf("%s must be RFC 3339: %w", path, err)
	}
	return nil
}

func oneOf(value string, allowed ...string) bool {
	for _, item := range allowed {
		if value == item {
			return true
		}
	}
	return false
}

// EncodeYAMLDocuments writes a YAML multi-document stream.
func EncodeYAMLDocuments(w io.Writer, docs []any) error {
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	for _, doc := range docs {
		if err := Validate(doc); err != nil {
			_ = enc.Close()
			return err
		}
		if err := enc.Encode(doc); err != nil {
			_ = enc.Close()
			return err
		}
	}
	return enc.Close()
}

// EncodeJSONDocuments writes the documents as a JSON array.
func EncodeJSONDocuments(w io.Writer, docs []any) error {
	for _, doc := range docs {
		if err := Validate(doc); err != nil {
			return err
		}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(docs)
}
