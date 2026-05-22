// Package conformance contains shared result types for Kapro plugin
// conformance suites.
package conformance

import "fmt"

// Result records one conformance check outcome.
type Result struct {
	Name    string
	Passed  bool
	Message string
}

// Pass returns a successful conformance result.
func Pass(name string) Result {
	return Result{Name: name, Passed: true}
}

// Fail returns a failed conformance result.
func Fail(name, format string, args ...any) Result {
	return Result{Name: name, Passed: false, Message: fmt.Sprintf(format, args...)}
}

// Report records all outcomes from one conformance suite run.
type Report struct {
	Suite   string
	Results []Result
}

// Passed returns true when every check passed.
func (r Report) Passed() bool {
	if len(r.Results) == 0 {
		return false
	}
	for _, result := range r.Results {
		if !result.Passed {
			return false
		}
	}
	return true
}

// Failed returns failed checks in their original order.
func (r Report) Failed() []Result {
	var failed []Result
	for _, result := range r.Results {
		if !result.Passed {
			failed = append(failed, result)
		}
	}
	return failed
}
