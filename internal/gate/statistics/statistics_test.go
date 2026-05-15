package statistics

import "testing"

func TestOneSampleDetectsLargeDifference(t *testing.T) {
	result := OneSample([]float64{1, 1, 1, 1, 1}, 0.1)
	if result.PValue > 0.05 {
		t.Fatalf("expected significant result, got p=%f", result.PValue)
	}
	if result.Confidence < 0.95 {
		t.Fatalf("expected high confidence, got %f", result.Confidence)
	}
}

func TestChangePointDetectsShift(t *testing.T) {
	result := ChangePoint([]float64{1, 1, 1, 1, 5, 5, 5, 5})
	if result.PValue > 0.05 {
		t.Fatalf("expected significant change point, got p=%f", result.PValue)
	}
	if result.EffectSize <= 0 {
		t.Fatalf("expected positive effect size, got %f", result.EffectSize)
	}
}

func TestScoreClampsToRange(t *testing.T) {
	if got := Score(1, 0.5, "lte"); got != 50 {
		t.Fatalf("expected score 50, got %f", got)
	}
	if got := Score(0.1, 0.5, "lte"); got != 100 {
		t.Fatalf("expected score 100, got %f", got)
	}
}
