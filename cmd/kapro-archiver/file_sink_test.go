package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"kapro.io/kapro/pkg/events"
)

func TestFileSinkWritesOriginalEnvelopeAndMetadata(t *testing.T) {
	dir := t.TempDir()
	sink, err := NewFileSink(dir)
	if err != nil {
		t.Fatalf("NewFileSink: %v", err)
	}
	body := []byte(`{"specversion":"1.0","id":"event-1","source":"/apis/kapro.io/v1alpha2/promotions/demo","type":"kapro.io/promotion.succeeded","subject":"demo","time":"2026-05-22T10:00:00Z","datacontenttype":"application/json","data":{"promotion":"demo","phase":"Succeeded"}}`)
	envelope := events.Envelope{
		SpecVersion:     "1.0",
		ID:              "event-1",
		Source:          "/apis/kapro.io/v1alpha2/promotions/demo",
		Type:            events.EventType("kapro.io/promotion.succeeded"),
		Subject:         "demo",
		Time:            "2026-05-22T10:00:00Z",
		DataContentType: "application/json",
	}
	record := ArchiveRecord{
		Envelope: envelope,
		Body:     body,
		Metadata: ArchiveMetadata{
			ID:              envelope.ID,
			Source:          envelope.Source,
			Type:            string(envelope.Type),
			Subject:         envelope.Subject,
			Time:            envelope.Time,
			DataContentType: envelope.DataContentType,
			ReceivedAt:      time.Date(2026, 5, 22, 10, 0, 1, 0, time.UTC).Format(time.RFC3339Nano),
			BodySHA256:      "known",
			DedupeKey:       dedupeKey(envelope, time.Date(2026, 5, 22, 10, 0, 1, 0, time.UTC)),
		},
	}

	if err := sink.Write(context.Background(), record); err != nil {
		t.Fatalf("Write: %v", err)
	}
	eventPath := filepath.Join(dir, filepath.FromSlash(record.Metadata.DedupeKey), "event.json")
	gotBody, err := os.ReadFile(eventPath)
	if err != nil {
		t.Fatalf("read event: %v", err)
	}
	if string(gotBody) != string(body) {
		t.Fatalf("event body changed\n got: %s\nwant: %s", gotBody, body)
	}

	metadataPath := filepath.Join(dir, filepath.FromSlash(record.Metadata.DedupeKey), "metadata.json")
	gotMetadataBody, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	var gotMetadata ArchiveMetadata
	if err := json.Unmarshal(gotMetadataBody, &gotMetadata); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if gotMetadata.DedupeKey != record.Metadata.DedupeKey {
		t.Fatalf("metadata dedupe key = %q, want %q", gotMetadata.DedupeKey, record.Metadata.DedupeKey)
	}
	if gotMetadata.ID != "event-1" || gotMetadata.Source != envelope.Source || gotMetadata.Type != string(envelope.Type) {
		t.Fatalf("metadata = %#v", gotMetadata)
	}
}

func TestFileSinkTreatsDuplicateDedupeKeyAsAlreadyArchived(t *testing.T) {
	dir := t.TempDir()
	sink, err := NewFileSink(dir)
	if err != nil {
		t.Fatalf("NewFileSink: %v", err)
	}
	envelope := events.Envelope{
		SpecVersion:     "1.0",
		ID:              "event-1",
		Source:          "/apis/kapro.io/v1alpha2/promotions/demo",
		Type:            events.EventType("kapro.io/promotion.succeeded"),
		Time:            "2026-05-22T10:00:00Z",
		DataContentType: "application/json",
	}
	record := ArchiveRecord{
		Envelope: envelope,
		Body:     []byte(`{"id":"event-1"}`),
		Metadata: ArchiveMetadata{
			ID:         envelope.ID,
			Source:     envelope.Source,
			Type:       string(envelope.Type),
			Time:       envelope.Time,
			BodySHA256: "first",
			DedupeKey:  dedupeKey(envelope, time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC)),
		},
	}
	if err := sink.Write(context.Background(), record); err != nil {
		t.Fatalf("first Write: %v", err)
	}
	duplicate := record
	duplicate.Body = []byte(`{"id":"event-1","changed":true}`)
	duplicate.Metadata.BodySHA256 = "second"
	if err := sink.Write(context.Background(), duplicate); err != nil {
		t.Fatalf("duplicate Write: %v", err)
	}

	eventPath := filepath.Join(dir, filepath.FromSlash(record.Metadata.DedupeKey), "event.json")
	gotBody, err := os.ReadFile(eventPath)
	if err != nil {
		t.Fatalf("read event: %v", err)
	}
	if string(gotBody) != string(record.Body) {
		t.Fatalf("duplicate rewrote event\n got: %s\nwant: %s", gotBody, record.Body)
	}
}
