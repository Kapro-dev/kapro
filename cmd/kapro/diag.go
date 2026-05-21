package main

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	"kapro.io/kapro/internal/cli"
)

// promotionDiag is the JSON payload shape for `kapro diag -o json`.
// It is intentionally a thin envelope around the live v1alpha1 /
// corev1 types — scripts that key on `.promotion.spec.version` or
// `.events[0].reason` get the full Kubernetes object shape they
// already know. The envelope keys (`promotion`, `promotionRuns`,
// `promotionTargets`, `events`, `blockedOn`, `suggestedNextActions`)
// are the stable contract; the embedded types track their own
// versioning policy (api/v1alpha2 stability via ADRs).
type promotionDiag struct {
	Promotion *kaprov1alpha2.Promotion        `json:"promotion"`
	Runs      []kaprov1alpha2.PromotionRun    `json:"promotionRuns"`
	Targets   []kaprov1alpha2.Target `json:"promotionTargets"`
	Events    []corev1.Event                  `json:"events"`
	BlockedOn []string                        `json:"blockedOn,omitempty"`
	Next      []string                        `json:"suggestedNextActions,omitempty"`
}

func newDiagCmd() *cobra.Command {
	var (
		kubeconfig  string
		eventsLimit int
	)
	cmd := &cobra.Command{
		Use:   "diag <promotion-name>",
		Short: "Diagnose a Promotion's current state",
		Long: `Show everything you need to answer "what is this Promotion doing right now?"

Renders a single screen with: phase + age, conditions, attempt history,
active run target progress, blocked-on hints (gates, approvals), and the
most recent Kubernetes Events.

Examples:
  kapro diag checkout-v1.2.3
  kapro diag checkout-v1.2.3 -o json
  kapro diag checkout-v1.2.3 --events 25`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDiag(cmd.Context(), args[0], kubeconfig, eventsLimit)
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	cmd.Flags().IntVar(&eventsLimit, "events", 10, "Max recent Events to show")
	return cmd
}

func runDiag(ctx context.Context, name, kubeconfigPath string, eventsLimit int) error {
	c, err := buildClient(kubeconfigPath)
	if err != nil {
		return err
	}
	return runDiagWithClient(ctx, c, name, eventsLimit)
}

// runDiagWithClient is the testable core. It accepts a controller-runtime
// client so tests can wire a fake.
func runDiagWithClient(ctx context.Context, c client.Client, name string, eventsLimit int) error {
	diag, err := collectDiag(ctx, c, name, eventsLimit)
	if err != nil {
		return err
	}

	if cli.IsJSON() {
		return cli.JSON(diag)
	}
	renderDiag(diag)
	return nil
}

func collectDiag(ctx context.Context, c client.Client, name string, eventsLimit int) (*promotionDiag, error) {
	var promo kaprov1alpha2.Promotion
	if err := c.Get(ctx, client.ObjectKey{Name: name}, &promo); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("promotion %q not found", name)
		}
		return nil, fmt.Errorf("get promotion: %w", err)
	}

	var runs kaprov1alpha2.PromotionRunList
	if err := c.List(ctx, &runs, client.MatchingLabels{"kapro.io/promotion": name}); err != nil {
		return nil, fmt.Errorf("list promotionruns: %w", err)
	}
	sort.Slice(runs.Items, func(i, j int) bool {
		return runs.Items[i].CreationTimestamp.After(runs.Items[j].CreationTimestamp.Time)
	})

	// Pick the "most relevant" PromotionRun to show target state for:
	// the active attempt if there is one, otherwise the most recent
	// historical attempt. Without this fallback a terminal-phase
	// Promotion (Failed or Succeeded with no in-flight attempt)
	// renders no target context at all.
	relevantRunName := ""
	if promo.Status.ActiveAttemptRef != nil {
		relevantRunName = promo.Status.ActiveAttemptRef.Name
	} else if len(promo.Status.Attempts) > 0 {
		relevantRunName = promo.Status.Attempts[0].Name
	} else if len(runs.Items) > 0 {
		// Status not yet populated; fall back to whatever the label-
		// scoped run list produced.
		relevantRunName = runs.Items[0].Name
	}
	var targets []kaprov1alpha2.Target
	if relevantRunName != "" {
		t, err := listPromotionTargetsForPromotionRun(ctx, c, relevantRunName)
		if err != nil {
			return nil, err
		}
		targets = t
	}

	// Event collection is best-effort and intentionally scoped. The
	// previous behaviour was c.List(events) cluster-wide, which can
	// be slow / memory-heavy / RBAC-rejected on large clusters.
	// Instead, do one targeted list per object we actually care
	// about, using the API server's built-in involvedObject.name
	// field selector. Failures are non-fatal — diag is useful even
	// without events.
	filtered := collectScopedEvents(ctx, c, &promo, runs.Items, targets)
	sort.Slice(filtered, func(i, j int) bool {
		return eventTime(filtered[i]).After(eventTime(filtered[j]))
	})
	if eventsLimit > 0 && len(filtered) > eventsLimit {
		filtered = filtered[:eventsLimit]
	}

	d := &promotionDiag{
		Promotion: &promo,
		Runs:      runs.Items,
		Targets:   targets,
		Events:    filtered,
	}
	d.BlockedOn = computeBlockedOn(&promo, targets)
	d.Next = computeNextActions(&promo, targets)
	return d, nil
}

// collectScopedEvents does one List per (Kind, Name) we care about,
// using the Event resource's built-in involvedObject.name field
// selector. The API server returns only matching events, so this
// stays fast on large clusters and works under tight Event-read RBAC
// (the equivalent of `kubectl get events --field-selector=...`).
//
// Each list is best-effort: failures are accumulated into a single
// warning log but never block the rest of diag — events are
// context, not load-bearing for the rollout state diagnosis.
func collectScopedEvents(ctx context.Context, c client.Client, p *kaprov1alpha2.Promotion,
	runs []kaprov1alpha2.PromotionRun, targets []kaprov1alpha2.Target) []corev1.Event {

	names := []string{p.Name}
	for _, r := range runs {
		names = append(names, r.Name)
	}
	for _, t := range targets {
		names = append(names, t.Name)
	}

	var out []corev1.Event
	for _, n := range names {
		if n == "" {
			continue
		}
		var list corev1.EventList
		sel := fields.OneTermEqualSelector("involvedObject.name", n)
		err := c.List(ctx, &list, &client.ListOptions{FieldSelector: sel})
		if err != nil {
			// Field-selector listing is unavailable (e.g. fake client
			// without an index). Fall back to one unscoped list and
			// filter client-side. Best-effort by design.
			return fallbackUnscopedEvents(ctx, c, p, runs, targets)
		}
		out = append(out, list.Items...)
	}
	return out
}

// fallbackUnscopedEvents preserves the original cluster-wide list
// behaviour for environments where the field-selector path errors
// (notably the controller-runtime fake client without a registered
// index). It is intentionally only reachable from collectScopedEvents
// — production clients use the scoped path.
func fallbackUnscopedEvents(ctx context.Context, c client.Client, p *kaprov1alpha2.Promotion,
	runs []kaprov1alpha2.PromotionRun, targets []kaprov1alpha2.Target) []corev1.Event {
	var list corev1.EventList
	if err := c.List(ctx, &list); err != nil {
		return nil
	}
	return filterPromotionEvents(list.Items, p, runs, targets)
}

func filterPromotionEvents(all []corev1.Event, p *kaprov1alpha2.Promotion,
	runs []kaprov1alpha2.PromotionRun, targets []kaprov1alpha2.Target) []corev1.Event {

	wanted := map[string]bool{}
	add := func(kind, name string) {
		if name != "" {
			wanted[kind+"/"+name] = true
		}
	}
	add("Promotion", p.Name)
	for _, r := range runs {
		add("PromotionRun", r.Name)
	}
	for _, t := range targets {
		add("PromotionTarget", t.Name)
	}

	out := make([]corev1.Event, 0, len(all))
	for _, e := range all {
		if wanted[e.InvolvedObject.Kind+"/"+e.InvolvedObject.Name] {
			out = append(out, e)
		}
	}
	return out
}

func eventTime(e corev1.Event) time.Time {
	if !e.LastTimestamp.IsZero() {
		return e.LastTimestamp.Time
	}
	if !e.EventTime.IsZero() {
		return e.EventTime.Time
	}
	return e.CreationTimestamp.Time
}

func computeBlockedOn(p *kaprov1alpha2.Promotion, targets []kaprov1alpha2.Target) []string {
	var blocked []string
	if p.Spec.Suspended {
		blocked = append(blocked, "Promotion is suspended (spec.suspended=true)")
	}
	for _, t := range targets {
		switch t.Status.Phase {
		case kaprov1alpha2.TargetPhaseWaitingApproval:
			blocked = append(blocked, fmt.Sprintf("Target %q waiting for approval (stage=%s)",
				t.Spec.Target, t.Spec.Stage))
		case kaprov1alpha2.TargetPhaseFailed:
			msg := t.Status.Message
			if msg == "" {
				msg = "no message"
			}
			blocked = append(blocked, fmt.Sprintf("Target %q failed (stage=%s): %s",
				t.Spec.Target, t.Spec.Stage, msg))
		}
	}
	return blocked
}

func computeNextActions(p *kaprov1alpha2.Promotion, targets []kaprov1alpha2.Target) []string {
	var next []string
	active := p.Status.ActiveAttemptRef
	for _, t := range targets {
		if t.Status.Phase == kaprov1alpha2.TargetPhaseWaitingApproval && active != nil {
			next = append(next, fmt.Sprintf("kapro approve %s/%s", active.Name, t.Spec.Target))
		}
	}
	if p.Spec.Suspended {
		next = append(next, fmt.Sprintf("kubectl patch promotion %s --type=merge -p '{\"spec\":{\"suspended\":false}}'", p.Name))
	}
	if p.Status.Phase == kaprov1alpha2.PromotionPhaseFailed {
		next = append(next, fmt.Sprintf("kubectl describe promotion %s   # inspect failure conditions", p.Name))
		// Only suggest a rollback command when we have a concrete
		// PromotionRun name to roll back. Emitting "kapro rollback
		// <run> --to ..." with a placeholder is worse than silent —
		// users copy-paste it and hit "<run> not found".
		if run := attemptName(p); run != "" {
			next = append(next, fmt.Sprintf("kapro rollback %s --to <previous-digest>", run))
		}
	}
	return next
}

// attemptName returns the most recent concrete PromotionRun name for
// this Promotion, or "" if no attempt has been stamped yet.
func attemptName(p *kaprov1alpha2.Promotion) string {
	if p.Status.ActiveAttemptRef != nil {
		return p.Status.ActiveAttemptRef.Name
	}
	if len(p.Status.Attempts) > 0 {
		return p.Status.Attempts[0].Name
	}
	return ""
}

func renderDiag(d *promotionDiag) {
	p := d.Promotion
	cli.Header(fmt.Sprintf("promotion/%s", p.Name))

	cli.KV("Kapro", p.Spec.FleetRef)
	if p.Spec.Version != "" {
		cli.KV("Version", p.Spec.Version)
	}
	if len(p.Spec.Versions) > 0 {
		cli.KV("Versions", formatPromotionRunVersions(p.Spec.Versions))
	}
	cli.KV("Phase", styledPromotionPhase(p.Status.Phase))
	if p.Status.ActiveAttemptRef != nil {
		cli.KV("Active Run", p.Status.ActiveAttemptRef.Name)
	} else {
		cli.KV("Active Run", "(none)")
	}
	if p.Spec.Suspended {
		cli.KV("Suspended", "true")
	}
	cli.KV("Attempts", fmt.Sprintf("%d", len(p.Status.Attempts)))
	cli.KV("Age", cli.Age(p.CreationTimestamp.Time))

	renderConditions(p.Status.Conditions)
	renderAttempts(p.Status.Attempts)
	renderTargets(d.Targets)
	renderBlockedOn(d.BlockedOn)
	renderEvents(d.Events)
	renderNext(d.Next)
}

func renderConditions(conds []metav1.Condition) {
	if len(conds) == 0 {
		return
	}
	cli.Header("Conditions")
	tbl := cli.NewTable("TYPE", "STATUS", "REASON", "MESSAGE", "AGE")
	for _, c := range conds {
		tbl.AddRow(c.Type, string(c.Status), c.Reason,
			truncate(c.Message, 60), cli.Age(c.LastTransitionTime.Time))
	}
	tbl.Render()
}

func renderAttempts(attempts []kaprov1alpha2.PromotionAttemptRef) {
	if len(attempts) == 0 {
		return
	}
	cli.Header("Attempt history (newest first)")
	tbl := cli.NewTable("RUN", "VERSION", "PHASE", "STARTED", "FINISHED", "NOTE")
	for _, a := range attempts {
		started := "-"
		if a.StartedAt != nil {
			started = cli.Age(a.StartedAt.Time) + " ago"
		}
		finished := "-"
		if a.CompletedAt != nil {
			finished = cli.Age(a.CompletedAt.Time) + " ago"
		}
		tbl.AddRow(a.Name, truncate(a.Version, 22), string(a.Phase),
			started, finished, a.SupersededReason)
	}
	tbl.Render()
}

func renderTargets(targets []kaprov1alpha2.Target) {
	if len(targets) == 0 {
		return
	}
	cli.Header("Active run targets")
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].Spec.Stage != targets[j].Spec.Stage {
			return targets[i].Spec.Stage < targets[j].Spec.Stage
		}
		return targets[i].Spec.Target < targets[j].Spec.Target
	})
	tbl := cli.NewTable("TARGET", "STAGE", "PLAN", "PHASE", "VERSION", "AGE")
	for _, t := range targets {
		tbl.AddRow(
			t.Spec.Target, t.Spec.Stage, t.Spec.Plan,
			string(t.Status.Phase), truncate(t.Spec.Version, 22),
			cli.Age(t.CreationTimestamp.Time),
		)
	}
	tbl.Render()
}

func renderBlockedOn(items []string) {
	if len(items) == 0 {
		return
	}
	cli.Header("Blocked on")
	for _, s := range items {
		cli.Warn(s)
	}
}

func renderEvents(events []corev1.Event) {
	if len(events) == 0 {
		return
	}
	cli.Header("Recent events")
	tbl := cli.NewTable("AGE", "TYPE", "REASON", "OBJECT", "MESSAGE")
	for _, e := range events {
		obj := e.InvolvedObject.Kind + "/" + e.InvolvedObject.Name
		tbl.AddRow(cli.Age(eventTime(e)), e.Type, e.Reason, obj, truncate(e.Message, 60))
	}
	tbl.Render()
}

func renderNext(items []string) {
	if len(items) == 0 {
		return
	}
	cli.Header("Suggested next actions")
	for _, s := range items {
		cli.Info(s)
	}
}

func styledPromotionPhase(phase kaprov1alpha2.PromotionPhase) string {
	s := string(phase)
	switch phase {
	case kaprov1alpha2.PromotionPhaseSucceeded:
		return cli.Theme.PhaseComplete.Render(s)
	case kaprov1alpha2.PromotionPhaseProgressing, kaprov1alpha2.PromotionPhaseRestarting:
		return cli.Theme.PhaseProgressing.Render(s)
	case kaprov1alpha2.PromotionPhaseFailed:
		return cli.Theme.PhaseFailed.Render(s)
	case kaprov1alpha2.PromotionPhasePaused:
		return cli.Theme.PhaseWaiting.Render(s)
	case kaprov1alpha2.PromotionPhasePending, kaprov1alpha2.PromotionPhaseTerminating,
		kaprov1alpha2.PromotionPhaseRollingBack:
		return cli.Theme.PhasePending.Render(s)
	}
	if s == "" {
		return cli.Theme.Muted.Render("(unset)")
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}
