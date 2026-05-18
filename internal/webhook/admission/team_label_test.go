package admission

import "testing"

func TestRequireTeamLabel(t *testing.T) {
	cases := []struct {
		name      string
		labels    map[string]string
		wantError bool
	}{
		{"nil labels", nil, true},
		{"empty map", map[string]string{}, true},
		{"label present empty", map[string]string{LabelKaproTeam: ""}, true},
		{"label present non-empty", map[string]string{LabelKaproTeam: "checkout"}, false},
		{"other labels only", map[string]string{"env": "prod"}, true},
		{"both labels", map[string]string{LabelKaproTeam: "checkout", "env": "prod"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := requireTeamLabel(tc.labels)
			if tc.wantError && err == nil {
				t.Fatalf("expected error for labels %v, got nil", tc.labels)
			}
			if !tc.wantError && err != nil {
				t.Fatalf("expected no error for labels %v, got %v", tc.labels, err)
			}
		})
	}
}
