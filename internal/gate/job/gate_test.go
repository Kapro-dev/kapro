package job

import "testing"

func TestSyncLabelValue_TruncatesLongNames(t *testing.T) {
	long := "this-is-a-very-long-sync-name-that-would-exceed-the-kubernetes-label-value-limit-significantly"
	got := syncLabelValue(long)
	if len(got) > 63 {
		t.Fatalf("expected <=63 chars, got %d", len(got))
	}
}
