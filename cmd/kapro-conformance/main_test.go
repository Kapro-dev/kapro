package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"strings"
	"testing"
)

func TestRunHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run([]string{"help"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("run help: %v", err)
	}
	if !strings.Contains(stdout.String(), "kapro-conformance all") {
		t.Fatalf("help output did not include all suite: %s", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunSuiteHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run([]string{"actuator", "--help"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("run actuator --help: %v", err)
	}
	if !strings.Contains(stdout.String(), "-endpoint string") {
		t.Fatalf("suite help output did not include endpoint flag: %s", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunNoArgsShowsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run(nil, &stdout, &stderr)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("run without args error = %v, want flag.ErrHelp", err)
	}
	if !strings.Contains(stderr.String(), "kapro-conformance all") {
		t.Fatalf("stderr did not include usage: %s", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

func TestRunAllJSON(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run([]string{"all", "-o", "json"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("run all json: %v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	var reports []suiteReport
	if err := json.Unmarshal(stdout.Bytes(), &reports); err != nil {
		t.Fatalf("unmarshal reports: %v\n%s", err, stdout.String())
	}
	if len(reports) != 4 {
		t.Fatalf("len(reports) = %d, want 4", len(reports))
	}
	for _, report := range reports {
		if !report.Passed {
			t.Fatalf("report %s did not pass: %+v", report.Suite, report)
		}
		if report.Total == 0 {
			t.Fatalf("report %s had no checks", report.Suite)
		}
	}
}

func TestRunProviderText(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run([]string{"provider"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("run provider: %v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "KSP provider conformance against reference") {
		t.Fatalf("provider output missing suite header: %s", out)
	}
	if !strings.Contains(out, "PASS") {
		t.Fatalf("provider output missing pass lines: %s", out)
	}
}

func TestRunActuatorRequiresEndpoint(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run([]string{"actuator"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "--endpoint is required") {
		t.Fatalf("run actuator error = %v, want endpoint requirement", err)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q, want empty", stdout.String(), stderr.String())
	}
}

func TestRunRejectsUnknownOutput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run([]string{"all", "-o", "yaml"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "--output must be json") {
		t.Fatalf("run all -o yaml error = %v, want output validation", err)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q, want empty", stdout.String(), stderr.String())
	}
}
