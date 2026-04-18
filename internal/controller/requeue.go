package controller

import "time"

const (
	requeueFast   = 15 * time.Second // retry after transient error
	requeueNormal = 30 * time.Second // normal polling interval
	requeueSlow   = 2 * time.Minute  // waiting for external condition
)

// parseDurationOrDefault parses s as a duration; returns requeueNormal on empty or parse error.
func parseDurationOrDefault(s string) time.Duration {
	if s == "" {
		return requeueNormal
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return requeueNormal
	}
	return d
}
