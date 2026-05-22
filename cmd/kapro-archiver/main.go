package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
)

type archiverConfig struct {
	Addr         string
	Sink         string
	MaxBodyBytes int64
	FileDir      string
	S3           S3SinkOptions
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := loadConfig()
	if err != nil {
		logger.Error("load config", "error", err)
		os.Exit(2)
	}
	sink, err := buildSink(ctx, cfg)
	if err != nil {
		logger.Error("configure archive sink", "error", err)
		os.Exit(2)
	}
	defer func() {
		if err := sink.Close(); err != nil {
			logger.Error("close archive sink", "error", err)
		}
	}()

	logger.Info("starting kapro-archiver", "addr", cfg.Addr, "sink", cfg.Sink)
	if err := NewServer(cfg.Addr, sink, cfg.MaxBodyBytes, logger).Run(ctx); err != nil {
		logger.Error("run archiver", "error", err)
		os.Exit(1)
	}
}

func loadConfig() (archiverConfig, error) {
	maxBody, err := parseInt64Env("KAPRO_ARCHIVER_MAX_BODY_BYTES", defaultMaxBodyBytes)
	if err != nil {
		return archiverConfig{}, err
	}
	cfg := archiverConfig{
		Addr:         envOrDefault("KAPRO_ARCHIVER_ADDR", defaultListenAddr),
		Sink:         strings.ToLower(envOrDefault("KAPRO_ARCHIVER_SINK", "file")),
		MaxBodyBytes: maxBody,
		FileDir:      envOrDefault("KAPRO_ARCHIVE_DIR", defaultFileDir),
		S3: S3SinkOptions{
			Bucket:   os.Getenv("KAPRO_ARCHIVE_S3_BUCKET"),
			Prefix:   os.Getenv("KAPRO_ARCHIVE_S3_PREFIX"),
			Region:   os.Getenv("AWS_REGION"),
			Endpoint: os.Getenv("KAPRO_ARCHIVE_S3_ENDPOINT"),
		},
	}
	if region := os.Getenv("KAPRO_ARCHIVE_S3_REGION"); region != "" {
		cfg.S3.Region = region
	}
	return cfg, nil
}

func buildSink(ctx context.Context, cfg archiverConfig) (ArchiveSink, error) {
	switch cfg.Sink {
	case "file":
		return NewFileSink(cfg.FileDir)
	case "s3":
		return NewS3Sink(ctx, cfg.S3)
	default:
		return nil, fmt.Errorf("unsupported KAPRO_ARCHIVER_SINK %q (supported: file, s3)", cfg.Sink)
	}
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func parseInt64Env(name string, fallback int64) (int64, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", name, err)
	}
	if parsed <= 0 {
		return 0, errors.New(name + " must be greater than zero")
	}
	return parsed, nil
}
