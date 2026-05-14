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
