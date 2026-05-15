package statistics

import "math"

// Summary is the common statistical evidence returned by gate analyses.
type Summary struct {
	Mean        float64
	StdDev      float64
	SampleCount int
}

// TestResult is a deterministic statistical test result suitable for controller
// status. PValue is a normal approximation; it is intentionally conservative
// enough for gate decisions but not a replacement for domain-specific analysis.
type TestResult struct {
	Summary
	PValue     float64
	Confidence float64
	EffectSize float64
}

func Summarize(values []float64) Summary {
	if len(values) == 0 {
		return Summary{}
	}
	mean := Mean(values)
	var variance float64
	for _, value := range values {
		d := value - mean
		variance += d * d
	}
	variance /= float64(len(values))
	return Summary{
		Mean:        mean,
		StdDev:      math.Sqrt(variance),
		SampleCount: len(values),
	}
}

func Mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var total float64
	for _, value := range values {
		total += value
	}
	return total / float64(len(values))
}

// OneSample compares a sampled metric distribution with a threshold.
func OneSample(values []float64, threshold float64) TestResult {
	s := Summarize(values)
	if s.SampleCount == 0 {
		return TestResult{Summary: s, PValue: 1}
	}
	stdErr := s.StdDev / math.Sqrt(float64(s.SampleCount))
	if stdErr == 0 {
		if s.Mean == threshold {
			return TestResult{Summary: s, PValue: 1}
		}
		return TestResult{Summary: s, PValue: 0, Confidence: 1, EffectSize: signedInf(s.Mean - threshold)}
	}
	z := math.Abs(s.Mean-threshold) / stdErr
	pValue := twoTailedNormalP(z)
	effect := 0.0
	if s.StdDev != 0 {
		effect = (s.Mean - threshold) / s.StdDev
	}
	return TestResult{
		Summary:    s,
		PValue:     pValue,
		Confidence: 1 - pValue,
		EffectSize: effect,
	}
}

// ChangePoint compares the first half of a sampled window with the second half.
func ChangePoint(values []float64) TestResult {
	if len(values) < 2 {
		return TestResult{Summary: Summarize(values), PValue: 1}
	}
	mid := len(values) / 2
	before := Summarize(values[:mid])
	after := Summarize(values[mid:])
	if before.SampleCount == 0 || after.SampleCount == 0 {
		return TestResult{Summary: Summarize(values), PValue: 1}
	}
	stdErr := math.Sqrt(square(before.StdDev)/float64(before.SampleCount) + square(after.StdDev)/float64(after.SampleCount))
	delta := after.Mean - before.Mean
	if stdErr == 0 {
		if delta == 0 {
			return TestResult{Summary: after, PValue: 1}
		}
		return TestResult{Summary: after, PValue: 0, Confidence: 1, EffectSize: signedInf(delta)}
	}
	z := math.Abs(delta) / stdErr
	pooled := math.Sqrt((square(before.StdDev) + square(after.StdDev)) / 2)
	effect := 0.0
	if pooled != 0 {
		effect = delta / pooled
	}
	pValue := twoTailedNormalP(z)
	return TestResult{
		Summary:    after,
		PValue:     pValue,
		Confidence: 1 - pValue,
		EffectSize: effect,
	}
}

func Score(value, threshold float64, comparator string) float64 {
	if threshold == 0 {
		if compare(value, threshold, comparator) {
			return 100
		}
		return 0
	}
	var score float64
	switch comparator {
	case "lt", "lte":
		score = 100 * threshold / value
	default:
		score = 100 * value / threshold
	}
	if math.IsNaN(score) || math.IsInf(score, 0) {
		return 0
	}
	if score < 0 {
		return 0
	}
	if score > 100 {
		return 100
	}
	return score
}

func compare(value, threshold float64, comparator string) bool {
	switch comparator {
	case "gte":
		return value >= threshold
	case "lt":
		return value < threshold
	case "lte":
		return value <= threshold
	default:
		return value > threshold
	}
}

func twoTailedNormalP(z float64) float64 {
	return math.Erfc(z / math.Sqrt2)
}

func square(v float64) float64 {
	return v * v
}

func signedInf(v float64) float64 {
	if v < 0 {
		return math.Inf(-1)
	}
	return math.Inf(1)
}
