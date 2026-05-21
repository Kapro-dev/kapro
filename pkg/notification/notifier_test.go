package notification

import "testing"

func TestChannel_MatchesEvent(t *testing.T) {
	tests := []struct {
		name   string
		events []string
		event  string
		want   bool
	}{
		{"empty events matches everything", nil, EventTargetConverged, true},
		{"exact match", []string{EventTargetConverged}, EventTargetConverged, true},
		{"no match", []string{EventTargetConverged}, EventTargetFailed, false},
		{"multiple events match", []string{EventTargetConverged, EventTargetFailed}, EventTargetFailed, true},
		{"approval required", []string{EventApprovalRequired}, EventApprovalRequired, true},
		{"empty type never matches filtered channel", []string{EventTargetConverged}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch := Channel{Events: tt.events}
			if got := ch.MatchesEvent(tt.event); got != tt.want {
				t.Errorf("MatchesEvent(%q) = %v, want %v", tt.event, got, tt.want)
			}
		})
	}
}

func TestBuildCloudEvent_StableID(t *testing.T) {
	event := Event{
		Type:         EventTargetConverged,
		Phase:        "Converged",
		PromotionRun: "rel-1",
		Plan:         "main",
		Stage:        "canary",
		Target:       "cluster-a",
	}

	first := BuildCloudEvent(event, 100, "2026-05-14T10:00:00Z")
	second := BuildCloudEvent(event, 200, "2026-05-14T10:00:01Z")

	want := "promotionrun/rel-1/type/kapro.promotionrun.target.converged/plan/main/stage/canary/target/cluster-a/phase/Converged"
	if first.ID != want {
		t.Fatalf("CloudEvent ID = %q, want %q", first.ID, want)
	}
	if second.ID != first.ID {
		t.Fatalf("CloudEvent ID should be stable across send attempts: first=%q second=%q", first.ID, second.ID)
	}
}
