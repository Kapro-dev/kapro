// Package lint statically checks Kapro YAML manifests for schema
// errors and best-practice violations. It is the engine behind
// `kapro lint <file>` but is split out so plugin authors and CI
// pipelines can call it directly without spinning up a CLI.
//
// The package never reads the filesystem and never talks to a
// Kubernetes cluster — all I/O happens in the cmd/kapro package.
// This keeps tests fast and the public API small.
package lint

import (
	"bytes"
	"fmt"
	"strings"

	"sigs.k8s.io/yaml"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
)

// Severity ranks an Issue. ERROR fails the lint; WARN is advisory
// unless the user opted into --strict.
type Severity string

const (
	SeverityError Severity = "ERROR"
	SeverityWarn  Severity = "WARN"
)

// Issue is one finding from a lint run. File and DocIndex identify
// where the issue lives in multi-doc YAML streams; Path is the field
// path inside the object (e.g. `spec.scope.targets[1]`).
type Issue struct {
	Severity Severity `json:"severity"`
	File     string   `json:"file,omitempty"`
	DocIndex int      `json:"docIndex"`
	Kind     string   `json:"kind,omitempty"`
	Name     string   `json:"name,omitempty"`
	Path     string   `json:"path,omitempty"`
	Message  string   `json:"message"`
}

func (i Issue) String() string {
	prefix := string(i.Severity)
	loc := i.File
	if loc == "" {
		loc = "<stdin>"
	}
	if i.DocIndex > 0 {
		loc = fmt.Sprintf("%s[doc=%d]", loc, i.DocIndex)
	}
	tail := i.Message
	if i.Path != "" {
		tail = i.Path + ": " + tail
	}
	if i.Kind != "" {
		return fmt.Sprintf("%-5s %s  %s/%s  %s", prefix, loc, i.Kind, i.Name, tail)
	}
	return fmt.Sprintf("%-5s %s  %s", prefix, loc, tail)
}

// LintFile decodes one YAML document stream (possibly multi-doc) and
// returns the union of all issues. file is informational — used only
// to label the returned Issues.
func LintFile(file string, data []byte) []Issue {
	var issues []Issue
	docs := splitYAMLDocs(data)
	for i, doc := range docs {
		if len(bytes.TrimSpace(doc)) == 0 {
			continue
		}
		docIssues := lintOneDoc(doc)
		for j := range docIssues {
			docIssues[j].File = file
			docIssues[j].DocIndex = i
		}
		issues = append(issues, docIssues...)
	}
	return issues
}

// lintOneDoc dispatches a single YAML document to the kind-specific
// linter. Non-Kapro manifests and Kapro kinds the linter has no rules
// for yet are skipped silently (returns nil) so that running
// `kapro lint **/*.yaml` over a mixed tree only surfaces real issues.
// A parse failure or a Kapro-kind doc with the wrong apiVersion still
// produces an issue.
func lintOneDoc(data []byte) []Issue {
	var meta struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   struct {
			Name string `json:"name"`
		} `json:"metadata"`
	}
	if err := yaml.Unmarshal(data, &meta); err != nil {
		return []Issue{{
			Severity: SeverityError,
			Message:  fmt.Sprintf("YAML parse failed: %v", err),
		}}
	}
	// A doc that decoded to zero metadata is either comment-only,
	// `null`, or an empty map — none of which the linter should flag.
	// splitYAMLDocs already drops whitespace-only docs; this handles
	// the harder cases that survive past it.
	if meta.Kind == "" && meta.APIVersion == "" && meta.Metadata.Name == "" {
		return nil
	}
	if meta.Kind == "" {
		return []Issue{{
			Severity: SeverityError,
			Message:  "missing kind",
		}}
	}
	// Non-Kapro manifests (Deployment, Service, ConfigMap, …) are out
	// of scope. Skip silently so mixed-content lint runs are clean.
	if !strings.HasPrefix(meta.APIVersion, "kapro.io/") {
		return nil
	}
	// Same apiVersion family but wrong version is still a Kapro
	// manifest the user probably wants flagged.
	if meta.APIVersion != "kapro.io/v1alpha1" {
		return []Issue{{
			Severity: SeverityWarn,
			Kind:     meta.Kind,
			Name:     meta.Metadata.Name,
			Path:     "apiVersion",
			Message:  fmt.Sprintf("expected kapro.io/v1alpha1, got %q", meta.APIVersion),
		}}
	}

	switch meta.Kind {
	case "DeliveryUnit":
		var du kaprov1alpha1.DeliveryUnit
		if err := yaml.Unmarshal(data, &du); err != nil {
			return parseFail(meta.Kind, meta.Metadata.Name, err)
		}
		return tagIssues(LintDeliveryUnit(&du), meta.Kind, du.Name)
	case "Kapro":
		var k kaprov1alpha1.Fleet
		if err := yaml.Unmarshal(data, &k); err != nil {
			return parseFail(meta.Kind, meta.Metadata.Name, err)
		}
		return tagIssues(LintKapro(&k), meta.Kind, k.Name)
	case "Promotion":
		var p kaprov1alpha1.Promotion
		if err := yaml.Unmarshal(data, &p); err != nil {
			return parseFail(meta.Kind, meta.Metadata.Name, err)
		}
		return tagIssues(LintPromotion(&p), meta.Kind, p.Name)
	case "Plan":
		var pp kaprov1alpha1.Plan
		if err := yaml.Unmarshal(data, &pp); err != nil {
			return parseFail(meta.Kind, meta.Metadata.Name, err)
		}
		return tagIssues(LintPromotionPlan(&pp), meta.Kind, pp.Name)
	default:
		// Other Kapro kinds (Substrate, Cluster, Approval,
		// PromotionRun, Trigger, etc.) are out of scope for
		// this version of the linter. Skip silently — running
		// `kapro lint examples/**/*.yaml` should not flag manifests
		// just because we have no rules for them yet.
		return nil
	}
}

func parseFail(kind, name string, err error) []Issue {
	return []Issue{{
		Severity: SeverityError,
		Kind:     kind,
		Name:     name,
		Message:  fmt.Sprintf("decode into %s: %v", kind, err),
	}}
}

func tagIssues(issues []Issue, kind, name string) []Issue {
	for i := range issues {
		if issues[i].Kind == "" {
			issues[i].Kind = kind
		}
		if issues[i].Name == "" {
			issues[i].Name = name
		}
	}
	return issues
}

// LintDeliveryUnit checks the canonical app/workload authoring object for
// mistakes that otherwise lead to controller-derived Source/Trigger objects
// failing later.
func LintDeliveryUnit(du *kaprov1alpha1.DeliveryUnit) []Issue {
	var out []Issue
	if du.Name == "" {
		out = append(out, errAt("metadata.name", "DeliveryUnit requires a name"))
	}
	if len(du.Spec.Source.Units) == 0 {
		out = append(out, errAt("spec.source.units",
			"at least one source unit is required"))
	}
	seenUnits := map[string]int{}
	for i, unit := range du.Spec.Source.Units {
		if unit.Name == "" {
			out = append(out, errAt(fmt.Sprintf("spec.source.units[%d].name", i),
				"unit name must not be empty"))
			continue
		}
		if first, ok := seenUnits[unit.Name]; ok {
			out = append(out, errAt(fmt.Sprintf("spec.source.units[%d].name", i),
				fmt.Sprintf("duplicate unit %q also appears at spec.source.units[%d]", unit.Name, first)))
			continue
		}
		seenUnits[unit.Name] = i
	}

	seenTriggers := map[string]int{}
	for i, trigger := range du.Spec.Triggers {
		name := strings.TrimSpace(trigger.Name)
		if name == "" {
			name = "default"
		}
		if first, ok := seenTriggers[name]; ok {
			out = append(out, errAt(fmt.Sprintf("spec.triggers[%d].name", i),
				fmt.Sprintf("duplicate derived Trigger suffix %q also appears at spec.triggers[%d]", name, first)))
		} else {
			seenTriggers[name] = i
		}
		if strings.TrimSpace(trigger.FleetRef) == "" && strings.TrimSpace(du.Spec.DefaultFleetRef) == "" {
			out = append(out, errAt(fmt.Sprintf("spec.triggers[%d].fleet", i),
				"trigger requires fleet or spec.defaultFleet"))
		}
		if trigger.Source.Type == "" {
			out = append(out, errAt(fmt.Sprintf("spec.triggers[%d].source.type", i),
				"trigger source type is required"))
			continue
		}
		if trigger.Source.Type != "oci" {
			out = append(out, errAt(fmt.Sprintf("spec.triggers[%d].source.type", i),
				fmt.Sprintf("unsupported trigger source type %q", trigger.Source.Type)))
			continue
		}
		if trigger.Source.OCI == nil {
			out = append(out, errAt(fmt.Sprintf("spec.triggers[%d].source.oci", i),
				"source.oci is required when source.type=oci"))
			continue
		}
		if trigger.Source.OCI.Repository == "" {
			out = append(out, errAt(fmt.Sprintf("spec.triggers[%d].source.oci.repository", i),
				"OCI trigger repository is required"))
		}
		if trigger.Source.OCI.TagPattern == "" {
			out = append(out, errAt(fmt.Sprintf("spec.triggers[%d].source.oci.tagPattern", i),
				"OCI trigger tagPattern is required"))
		}
	}
	return out
}

// LintKapro checks a Fleet custom resource for required fields and
// common foot-guns. It does not validate cluster connectivity.
func LintKapro(k *kaprov1alpha1.Fleet) []Issue {
	var out []Issue
	if k.Name == "" {
		out = append(out, errAt("metadata.name", "Fleet requires a name"))
	}
	// FleetSpec.Source is a legacy compatibility hook. Target-set Fleets are
	// valid with neither source nor sourceRef because DeliveryUnit owns source
	// intent in the public-preview authoring path.
	inlineSourceSet := k.Spec.Source != nil && len(k.Spec.Source.Units) > 0
	if k.Spec.SourceRef != "" && inlineSourceSet {
		out = append(out, errAt("spec.source / spec.sourceRef",
			"only one of spec.source or spec.sourceRef may be set"))
	}
	if k.Spec.Delivery.SubstrateName() == "" {
		out = append(out, errAt("spec.delivery.ref",
			"delivery substrate is required (e.g. flux, argo)"))
	}
	if len(k.Spec.Clusters) == 0 {
		out = append(out, warnAt("spec.clusters",
			"no clusters configured; the Fleet will not roll anything until a Cluster matches"))
	}
	return out
}

// LintPromotion checks a Promotion for required fields and shape problems.
func LintPromotion(p *kaprov1alpha1.Promotion) []Issue {
	var out []Issue
	if p.Name == "" {
		out = append(out, errAt("metadata.name", "Promotion requires a name"))
	}
	if p.Spec.FleetRef == "" {
		out = append(out, errAt("spec.fleet", "fleet is required"))
	}
	if p.Spec.DeliveryUnitRef == "" {
		out = append(out, errAt("spec.unit", "unit is required"))
	}
	if p.Spec.Version == "" && len(p.Spec.Versions) == 0 {
		out = append(out, errAt("spec.version / spec.versions",
			"either spec.version or at least one spec.versions[unit] is required"))
	}
	if p.Spec.Timeout == "" {
		out = append(out, warnAt("spec.timeout",
			"no timeout set; a stuck PromotionRun will reconcile indefinitely (recommended: 30m–4h)"))
	}
	if p.Spec.Scope != nil {
		seen := map[string]bool{}
		for i, t := range p.Spec.Scope.Targets {
			if t == "" {
				out = append(out, errAt(fmt.Sprintf("spec.scope.targets[%d]", i),
					"target name must not be empty"))
				continue
			}
			if seen[t] {
				out = append(out, warnAt(fmt.Sprintf("spec.scope.targets[%d]", i),
					fmt.Sprintf("duplicate target %q", t)))
			}
			seen[t] = true
		}
	}
	for i, plan := range p.Spec.Plans {
		if plan.Plan == "" {
			out = append(out, errAt(fmt.Sprintf("spec.plans[%d].plan", i),
				"Plan reference must not be empty"))
		}
		if plan.Name == "" {
			out = append(out, warnAt(fmt.Sprintf("spec.plans[%d].name", i),
				"logical name is empty; the controller will autogenerate one"))
		}
	}
	return out
}

// LintPromotionPlan checks a Plan DAG for the most common
// schema and structural violations (duplicate stage names, dangling
// dependsOn references, cycles, manual gates without approvers, etc.).
func LintPromotionPlan(pp *kaprov1alpha1.Plan) []Issue {
	var out []Issue
	if pp.Name == "" {
		out = append(out, errAt("metadata.name", "Plan requires a name"))
	}
	if len(pp.Spec.Stages) == 0 {
		out = append(out, errAt("spec.stages", "at least one stage is required"))
		return out
	}

	names := map[string]int{}
	for i, s := range pp.Spec.Stages {
		if s.Name == "" {
			out = append(out, errAt(fmt.Sprintf("spec.stages[%d].name", i),
				"stage name must not be empty"))
			continue
		}
		if prev, dup := names[s.Name]; dup {
			out = append(out, errAt(fmt.Sprintf("spec.stages[%d].name", i),
				fmt.Sprintf("duplicate stage name %q (first defined at spec.stages[%d])", s.Name, prev)))
		}
		names[s.Name] = i
	}

	for i, s := range pp.Spec.Stages {
		for j, dep := range s.DependsOn {
			if _, ok := names[dep.Stage]; !ok {
				out = append(out, errAt(
					fmt.Sprintf("spec.stages[%d].dependsOn[%d].stage", i, j),
					fmt.Sprintf("stage %q depends on unknown stage %q", s.Name, dep.Stage)))
			}
			if dep.Stage == s.Name {
				out = append(out, errAt(
					fmt.Sprintf("spec.stages[%d].dependsOn[%d].stage", i, j),
					fmt.Sprintf("stage %q cannot depend on itself", s.Name)))
			}
		}
		if s.Gate != nil {
			if s.Gate.Mode == kaprov1alpha1.GateModeManual {
				if s.Gate.Approval == nil || !s.Gate.Approval.Required {
					// Materially breaks the user's stated intent ("wait
					// for a human") — the stage will silently auto-advance.
					// Upgrade from advisory to ERROR so CI catches it.
					out = append(out, errAt(
						fmt.Sprintf("spec.stages[%d].gate.approval.required", i),
						fmt.Sprintf("stage %q gate.mode=manual but approval.required is false; the rollout will NOT wait for a human", s.Name)))
				} else if len(s.Gate.Approval.Approvers) == 0 {
					out = append(out, warnAt(
						fmt.Sprintf("spec.stages[%d].gate.approval.approvers", i),
						fmt.Sprintf("stage %q gate.mode=manual but no approvers listed; anyone with cluster access can approve", s.Name)))
				}
			}
			for k, m := range s.Gate.Gate.Metrics {
				if m.Preset == "" && m.Threshold == nil {
					out = append(out, warnAt(
						fmt.Sprintf("spec.stages[%d].gate.gate.metrics[%d].threshold", i, k),
						fmt.Sprintf("metric gate in stage %q has no preset and no threshold; the gate will always pass", s.Name)))
				}
				if m.Preset == "" && m.Query == "" {
					out = append(out, errAt(
						fmt.Sprintf("spec.stages[%d].gate.gate.metrics[%d].query", i, k),
						fmt.Sprintf("metric gate in stage %q has no preset and no inline query", s.Name)))
				}
			}
		}
		if s.Strategy != nil && s.Strategy.MaxParallel < 0 {
			out = append(out, errAt(
				fmt.Sprintf("spec.stages[%d].strategy.maxParallel", i),
				fmt.Sprintf("maxParallel must be >= 0 (got %d)", s.Strategy.MaxParallel)))
		}
	}

	out = append(out, detectCycles(pp)...)
	return out
}

// detectCycles runs a DFS over the stage DAG and reports the first
// cycle found, if any. The admission webhook also blocks cycles, but
// catching them at lint time is much cheaper.
func detectCycles(pp *kaprov1alpha1.Plan) []Issue {
	const (
		unseen = 0
		open   = 1
		closed = 2
	)
	stages := map[string]kaprov1alpha1.Stage{}
	state := map[string]int{}
	for _, s := range pp.Spec.Stages {
		if s.Name != "" {
			stages[s.Name] = s
		}
	}
	var (
		dfs   func(name string, path []string) []string
		cycle []string
	)
	dfs = func(name string, path []string) []string {
		switch state[name] {
		case closed:
			return nil
		case open:
			// Cycle found — trim path to the cycle start.
			for i, n := range path {
				if n == name {
					return append(append([]string{}, path[i:]...), name)
				}
			}
			return append(path, name)
		}
		state[name] = open
		path = append(path, name)
		for _, dep := range stages[name].DependsOn {
			if c := dfs(dep.Stage, path); c != nil {
				return c
			}
		}
		state[name] = closed
		return nil
	}
	for name := range stages {
		if state[name] == unseen {
			if c := dfs(name, nil); c != nil {
				cycle = c
				break
			}
		}
	}
	if cycle == nil {
		return nil
	}
	return []Issue{{
		Severity: SeverityError,
		Path:     "spec.stages",
		Message:  fmt.Sprintf("dependency cycle: %s", strings.Join(cycle, " → ")),
	}}
}

// splitYAMLDocs separates a multi-doc YAML stream into individual
// document slices. It does NOT do YAML parsing — just `^---$` splits
// — so syntactically broken documents still reach the parser and
// surface as proper "YAML parse failed" issues with correct doc
// indexes.
func splitYAMLDocs(data []byte) [][]byte {
	var docs [][]byte
	var cur []byte
	for _, line := range bytes.Split(data, []byte("\n")) {
		if bytes.Equal(bytes.TrimRight(line, " \t\r"), []byte("---")) {
			docs = append(docs, cur)
			cur = nil
			continue
		}
		cur = append(cur, line...)
		cur = append(cur, '\n')
	}
	docs = append(docs, cur)
	return docs
}

func errAt(path, msg string) Issue  { return Issue{Severity: SeverityError, Path: path, Message: msg} }
func warnAt(path, msg string) Issue { return Issue{Severity: SeverityWarn, Path: path, Message: msg} }

// HasErrors reports whether any issue in the slice has Severity=Error,
// optionally treating warnings as errors when strict is true.
func HasErrors(issues []Issue, strict bool) bool {
	for _, i := range issues {
		if i.Severity == SeverityError {
			return true
		}
		if strict && i.Severity == SeverityWarn {
			return true
		}
	}
	return false
}
