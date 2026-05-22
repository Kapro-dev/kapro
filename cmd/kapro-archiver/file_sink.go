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

	eventPath := filepath.Join(dir, "event.json")
	f, err := os.OpenFile(eventPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil
		}
		return fmt.Errorf("create event archive: %w", err)
	}
	if _, err := f.Write(record.Body); err != nil {
		_ = f.Close()
		_ = os.Remove(eventPath)
		return fmt.Errorf("write event archive: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(eventPath)
		return fmt.Errorf("close event archive: %w", err)
	}

	metadata, err := record.Metadata.JSON()
	if err != nil {
		_ = os.Remove(eventPath)
		return fmt.Errorf("marshal metadata: %w", err)
	}
	metadataPath := filepath.Join(dir, "metadata.json")
	if err := os.WriteFile(metadataPath, metadata, 0o644); err != nil {
		_ = os.Remove(eventPath)
		return fmt.Errorf("write metadata archive: %w", err)
	}
	return nil
}

func (s *FileSink) Close() error {
	return nil
}
