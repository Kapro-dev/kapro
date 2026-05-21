package events_test

import (
	"encoding/json"
	"testing"

	"kapro.io/kapro/pkg/events"
)

// TestStageWaveGateFieldsRenderInData verifies the new Wave/Stage/Gate/Target
// fields land in CloudEvents data. Subscribers depend on these for
// filtering by fleet topology.
func TestStageWaveGateFieldsRenderInData(t *testing.T) {
	body, env, err := events.Render(events.Event{
		Type:          events.EventPromotionStageGatePassed,
		PromotionName: "checkout",
		FleetRef:      "checkout-fleet",
		Phase:         "Progressing",
		Version:       "v1.2.3",
		AttemptName:   "checkout-att-1",
		Wave:          "default",
		Stage:         "canary",
		Gate:          "metrics",
		Target:        "fi-prod",
		Reason:        "gate passed",
		Message:       "Datadog SLO ok",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if env.Data.Wave != "default" || env.Data.Stage != "canary" ||
		env.Data.Gate != "metrics" || env.Data.Target != "fi-prod" {
		t.Fatalf("data missing scope fields: %+v", env.Data)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	data := got["data"].(map[string]any)
	for _, key := range []string{"wave", "stage", "gate", "target"} {
		if data[key] == nil {
			t.Fatalf("data.%s missing in JSON envelope: %v", key, data)
		}
	}
}
