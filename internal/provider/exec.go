package provider

import (
	"os/exec"
)

// execQuiet runs a command and returns nil if it exits 0.
func execQuiet(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	return cmd.Run()
}

// execOutput runs a command and returns stdout.
func execOutput(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}
