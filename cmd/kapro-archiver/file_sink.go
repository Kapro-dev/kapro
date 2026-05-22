package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type FileSink struct {
	dir string
}

func NewFileSink(dir string) (*FileSink, error) {
	if dir == "" {
		dir = defaultFileDir
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create archive dir: %w", err)
	}
	return &FileSink{dir: dir}, nil
}

func (s *FileSink) Write(ctx context.Context, record ArchiveRecord) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	dir := filepath.Join(s.dir, filepath.FromSlash(record.Metadata.DedupeKey))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create event archive dir: %w", err)
	}

	// Treat the archive as complete only when BOTH event.json and
	// metadata.json exist. If a prior attempt crashed after writing the
	// event but before metadata, fall through and create metadata
	// idempotently. O_CREATE|O_EXCL preserves the first-received metadata
	// (including ReceivedAt) on retries.
	eventPath := filepath.Join(dir, "event.json")
	f, err := os.OpenFile(eventPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	switch {
	case err == nil:
		if _, werr := f.Write(record.Body); werr != nil {
			_ = f.Close()
			_ = os.Remove(eventPath)
			return fmt.Errorf("write event archive: %w", werr)
		}
		if cerr := f.Close(); cerr != nil {
			_ = os.Remove(eventPath)
			return fmt.Errorf("close event archive: %w", cerr)
		}
	case errors.Is(err, os.ErrExist):
		// event.json already exists — proceed to ensure metadata.json exists.
	default:
		return fmt.Errorf("create event archive: %w", err)
	}

	metadata, err := record.Metadata.JSON()
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}
	metadataPath := filepath.Join(dir, "metadata.json")
	mf, err := os.OpenFile(metadataPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil
		}
		return fmt.Errorf("create metadata archive: %w", err)
	}
	if _, err := mf.Write(metadata); err != nil {
		_ = mf.Close()
		_ = os.Remove(metadataPath)
		return fmt.Errorf("write metadata archive: %w", err)
	}
	if err := mf.Close(); err != nil {
		_ = os.Remove(metadataPath)
		return fmt.Errorf("close metadata archive: %w", err)
	}
	return nil
}

func (s *FileSink) Close() error {
	return nil
}
