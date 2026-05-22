package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
)

type S3Sink struct {
	client      *http.Client
	creds       aws.CredentialsProvider
	bucket      string
	prefix      string
	region      string
	endpoint    string
	pathStyle   bool
	signingName string
}

type S3SinkOptions struct {
	Bucket   string
	Prefix   string
	Region   string
	Endpoint string
}

func NewS3Sink(ctx context.Context, opts S3SinkOptions) (*S3Sink, error) {
	if strings.TrimSpace(opts.Bucket) == "" {
		return nil, errors.New("s3 bucket is required")
	}
	loadOptions := []func(*config.LoadOptions) error{}
	if opts.Region != "" {
		loadOptions = append(loadOptions, config.WithRegion(opts.Region))
	}
	cfg, err := config.LoadDefaultConfig(ctx, loadOptions...)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	region := opts.Region
	if region == "" {
		region = cfg.Region
	}
	if region == "" {
		return nil, errors.New("aws region is required for s3 sink")
	}
	return &S3Sink{
		client:      http.DefaultClient,
		creds:       cfg.Credentials,
		bucket:      opts.Bucket,
		prefix:      strings.Trim(opts.Prefix, "/"),
		region:      region,
		endpoint:    strings.TrimRight(opts.Endpoint, "/"),
		pathStyle:   opts.Endpoint != "",
		signingName: "s3",
	}, nil
}

func (s *S3Sink) Write(ctx context.Context, record ArchiveRecord) error {
	eventKey := s.objectKey(record.Metadata.DedupeKey, "event.json")
	created, err := s.putOnce(ctx, eventKey, "application/cloudevents+json", record.Body)
	if err != nil || !created {
		return err
	}

	metadata, err := record.Metadata.JSON()
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}
	metadataKey := s.objectKey(record.Metadata.DedupeKey, "metadata.json")
	_, err = s.putOnce(ctx, metadataKey, "application/json", metadata)
	return err
}

func (s *S3Sink) Close() error {
	return nil
}

func (s *S3Sink) objectKey(dedupeKey, name string) string {
	key := path.Join(dedupeKey, name)
	if s.prefix == "" {
		return key
	}
	return path.Join(s.prefix, key)
}

func (s *S3Sink) putOnce(ctx context.Context, key, contentType string, body []byte) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, s.objectURL(key), bytes.NewReader(body))
	if err != nil {
		return false, fmt.Errorf("build s3 request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("If-None-Match", "*")
	req.ContentLength = int64(len(body))

	payloadHash := sha256.Sum256(body)
	creds, err := s.creds.Retrieve(ctx)
	if err != nil {
		return false, fmt.Errorf("retrieve aws credentials: %w", err)
	}
	if err := v4.NewSigner().SignHTTP(ctx, creds, req, hex.EncodeToString(payloadHash[:]), s.signingName, s.region, time.Now().UTC()); err != nil {
		return false, fmt.Errorf("sign s3 request: %w", err)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("put s3 object %s: %w", key, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusPreconditionFailed {
		return false, nil
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return true, nil
	}
	errorBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return false, fmt.Errorf("put s3 object %s returned %d: %s", key, resp.StatusCode, strings.TrimSpace(string(errorBody)))
}

func (s *S3Sink) objectURL(key string) string {
	escapedKey := escapeS3Key(key)
	if s.pathStyle {
		return s.endpoint + "/" + path.Join(escapePathSegment(s.bucket), escapedKey)
	}
	return "https://" + s.bucket + ".s3." + s.region + ".amazonaws.com/" + escapedKey
}

func escapeS3Key(key string) string {
	parts := strings.Split(key, "/")
	for i := range parts {
		parts[i] = escapePathSegment(parts[i])
	}
	return strings.Join(parts, "/")
}

func escapePathSegment(value string) string {
	return (&url.URL{Path: value}).EscapedPath()
}
