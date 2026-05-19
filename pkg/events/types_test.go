package events_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"kapro.io/kapro/pkg/events"
)

// TestVocabularyStable is the canary against accidental rename/removal of
// EventType constants. Subscribers depend on these literal strings. If
// this test fails, you are about to break a public contract.
func TestVocabularyStable(t *testing.T) {
	want := []struct {
		name string
		ev   events.EventType
	}{
		{"EventPromotionCreated", "kapro.io/promotion.created"},
		{"EventPromotionProgressing", "kapro.io/promotion.progressing"},
		{"EventPromotionPaused", "kapro.io/promotion.paused"},
		{"EventPromotionResumed", "kapro.io/promotion.resumed"},
		{"EventPromotionRestarting", "kapro.io/promotion.restarting"},
		{"EventPromotionSucceeded", "kapro.io/promotion.succeeded"},
		{"EventPromotionFailed", "kapro.io/promotion.failed"},
		{"EventPromotionRollingBack", "kapro.io/promotion.rollingBack"},
		{"EventPromotionTerminating", "kapro.io/promotion.terminating"},
		{"EventPromotionAttemptStamped", "kapro.io/promotion.attempt.stamped"},
		{"EventPromotionAttemptSuperseded", "kapro.io/promotion.attempt.superseded"},
		{"EventPromotionWaveEntered", "kapro.io/promotion.wave.entered"},
		{"EventPromotionWaveCompleted", "kapro.io/promotion.wave.completed"},
		{"EventPromotionStageEntered", "kapro.io/promotion.stage.entered"},
		{"EventPromotionStageCompleted", "kapro.io/promotion.stage.completed"},
		{"EventPromotionStageGateWaiting", "kapro.io/promotion.stage.gate.waiting"},
		{"EventPromotionStageGatePassed", "kapro.io/promotion.stage.gate.passed"},
		{"EventPromotionStageGateFailed", "kapro.io/promotion.stage.gate.failed"},
	}
	for _, w := range want {
		got, ok := lookupByName(w.name)
		if !ok {
			t.Fatalf("%s not exported; the constant must not be renamed", w.name)
		}
		if got != w.ev {
			t.Fatalf("%s = %q, want %q (subscribers depend on this literal)", w.name, got, w.ev)
		}
	}
}

// TestAllEventTypesIncludesAllConstants ensures AllEventTypes() returns
// every documented constant. Adding a new EventType without adding it to
// the manifest is a documentation bug we want to catch in CI.
func TestAllEventTypesIncludesAllConstants(t *testing.T) {
	all := events.AllEventTypes()
	mustContain := []events.EventType{
		events.EventPromotionCreated,
		events.EventPromotionProgressing,
		events.EventPromotionPaused,
		events.EventPromotionResumed,
		events.EventPromotionRestarting,
		events.EventPromotionSucceeded,
		events.EventPromotionFailed,
		events.EventPromotionRollingBack,
		events.EventPromotionTerminating,
		events.EventPromotionAttemptStamped,
		events.EventPromotionAttemptSuperseded,
		events.EventPromotionWaveEntered,
		events.EventPromotionWaveCompleted,
		events.EventPromotionStageEntered,
		events.EventPromotionStageCompleted,
		events.EventPromotionStageGateWaiting,
		events.EventPromotionStageGatePassed,
		events.EventPromotionStageGateFailed,
	}
	if len(all) != len(mustContain) {
		t.Fatalf("AllEventTypes() returned %d types, want %d", len(all), len(mustContain))
	}
	for _, want := range mustContain {
		found := false
		for _, got := range all {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("AllEventTypes() missing %q", want)
		}
	}
}

// TestRenderProducesValidCloudEventsV1 verifies the JSON shape downstream
// subscribers will see: required CloudEvents v1.0 fields, correct
// content-type, and the Kapro EventData payload as `data`.
func TestRenderProducesValidCloudEventsV1(t *testing.T) {
	frozen := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	ev := events.Event{
		Type:          events.EventPromotionSucceeded,
		PromotionName: "checkout",
		PromotionUID:  "uid-abc",
		KaproRef:      "checkout-fleet",
		Phase:         "Succeeded",
		PreviousPhase: "Progressing",
		Version:       "v1.2.3",
		AttemptName:   "checkout-att-1",
		Reason:        "AttemptSucceeded",
		Message:       "all clusters converged",
		Time:          frozen,
	}
	body, env, err := events.Render(ev)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	// Verify the returned Envelope struct.
	if env.SpecVersion != "1.0" {
		t.Fatalf("specversion = %q, want 1.0", env.SpecVersion)
	}
	if env.Type != events.EventPromotionSucceeded {
		t.Fatalf("type = %q, want %q", env.Type, events.EventPromotionSucceeded)
	}
	if env.Source != "/apis/kapro.io/v1alpha1/promotions/checkout" {
		t.Fatalf("source = %q", env.Source)
	}
	if env.Subject != "checkout" {
		t.Fatalf("subject = %q, want checkout", env.Subject)
	}
	if env.DataContentType != "application/json" {
		t.Fatalf("datacontenttype = %q", env.DataContentType)
	}
	if len(env.ID) != 32 {
		t.Fatalf("id = %q (len=%d), want 32-char hex", env.ID, len(env.ID))
	}
	if env.Data.Promotion != "checkout" || env.Data.Phase != "Succeeded" ||
		env.Data.PreviousPhase != "Progressing" || env.Data.AttemptName != "checkout-att-1" {
		t.Fatalf("data = %+v", env.Data)
	}

	// And the raw JSON.
	var decoded events.Envelope
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if !strings.Contains(string(body), `"specversion":"1.0"`) {
		t.Fatalf("body missing specversion: %s", string(body))
	}
	if !strings.Contains(string(body), `"type":"kapro.io/promotion.succeeded"`) {
		t.Fatalf("body missing type: %s", string(body))
	}
}

// lookupByName resolves the named constant via reflection-free explicit
// mapping. Reflection would defeat the purpose of the canary — the test
// must catch renames at compile time too.
func lookupByName(name string) (events.EventType, bool) {
	switch name {
	case "EventPromotionCreated":
		return events.EventPromotionCreated, true
	case "EventPromotionProgressing":
		return events.EventPromotionProgressing, true
	case "EventPromotionPaused":
		return events.EventPromotionPaused, true
	case "EventPromotionResumed":
		return events.EventPromotionResumed, true
	case "EventPromotionRestarting":
		return events.EventPromotionRestarting, true
	case "EventPromotionSucceeded":
		return events.EventPromotionSucceeded, true
	case "EventPromotionFailed":
		return events.EventPromotionFailed, true
	case "EventPromotionRollingBack":
		return events.EventPromotionRollingBack, true
	case "EventPromotionTerminating":
		return events.EventPromotionTerminating, true
	case "EventPromotionAttemptStamped":
		return events.EventPromotionAttemptStamped, true
	case "EventPromotionAttemptSuperseded":
		return events.EventPromotionAttemptSuperseded, true
	case "EventPromotionWaveEntered":
		return events.EventPromotionWaveEntered, true
	case "EventPromotionWaveCompleted":
		return events.EventPromotionWaveCompleted, true
	case "EventPromotionStageEntered":
		return events.EventPromotionStageEntered, true
	case "EventPromotionStageCompleted":
		return events.EventPromotionStageCompleted, true
	case "EventPromotionStageGateWaiting":
		return events.EventPromotionStageGateWaiting, true
	case "EventPromotionStageGatePassed":
		return events.EventPromotionStageGatePassed, true
	case "EventPromotionStageGateFailed":
		return events.EventPromotionStageGateFailed, true
	}
	return "", false
}
