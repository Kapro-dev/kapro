package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	"kapro.io/kapro/pkg/events"
)

const (
	defaultMaxBodyBytes = int64(1 << 20)
	defaultListenAddr   = ":8080"
	defaultFileDir      = "/var/lib/kapro-archiver/events"
)

var safePathToken = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

type ArchiveSink interface {
	Write(ctx context.Context, record ArchiveRecord) error
	Close() error
}

type ArchiveRecord struct {
	Envelope events.Envelope `json:"envelope"`
	Body     []byte          `json:"-"`
	Metadata ArchiveMetadata `json:"metadata"`
}

type ArchiveMetadata struct {
	ID                 string `json:"id"`
	Source             string `json:"source"`
	Type               string `json:"type"`
	Subject            string `json:"subject,omitempty"`
	Time               string `json:"time"`
	DataContentType    string `json:"datacontenttype,omitempty"`
	ReceivedAt         string `json:"receivedAt"`
	RequestContentType string `json:"requestContentType,omitempty"`
	ContentLength      int64  `json:"contentLength,omitempty"`
	UserAgent          string `json:"userAgent,omitempty"`
	RemoteAddr         string `json:"remoteAddr,omitempty"`
	BodySHA256         string `json:"bodySha256"`
	DedupeKey          string `json:"dedupeKey"`
}

func newArchiveRecord(envelope events.Envelope, body []byte, r *http.Request, receivedAt time.Time) ArchiveRecord {
	sum := sha256.Sum256(body)
	dedupeKey := dedupeKey(envelope, receivedAt)
	return ArchiveRecord{
		Envelope: envelope,
		Body:     append([]byte(nil), body...),
		Metadata: ArchiveMetadata{
			ID:                 envelope.ID,
			Source:             envelope.Source,
			Type:               string(envelope.Type),
			Subject:            envelope.Subject,
			Time:               envelope.Time,
			DataContentType:    envelope.DataContentType,
			ReceivedAt:         receivedAt.UTC().Format(time.RFC3339Nano),
			RequestContentType: r.Header.Get("Content-Type"),
			ContentLength:      r.ContentLength,
			UserAgent:          r.UserAgent(),
			RemoteAddr:         remoteHost(r.RemoteAddr),
			BodySHA256:         hex.EncodeToString(sum[:]),
			DedupeKey:          dedupeKey,
		},
	}
}

func (m ArchiveMetadata) JSON() ([]byte, error) {
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(body, '\n'), nil
}

func dedupeKey(envelope events.Envelope, fallback time.Time) string {
	eventTime := fallback.UTC()
	if parsed, err := time.Parse(time.RFC3339Nano, envelope.Time); err == nil {
		eventTime = parsed.UTC()
	}
	sourceHash := sha256.Sum256([]byte(envelope.Source))
	return path.Join(
		sanitizeToken(string(envelope.Type)),
		eventTime.Format("2006"),
		eventTime.Format("01"),
		eventTime.Format("02"),
		hex.EncodeToString(sourceHash[:])[:16],
		sanitizeToken(envelope.ID),
	)
}

func sanitizeToken(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	value = safePathToken.ReplaceAllString(value, "_")
	value = strings.Trim(value, "._-")
	if value == "" {
		return "unknown"
	}
	if len(value) > 180 {
		return value[:180]
	}
	return value
}

func validateEnvelope(envelope events.Envelope) error {
	if strings.TrimSpace(envelope.SpecVersion) != "1.0" {
		return errors.New("unsupported cloudevents specversion")
	}
	if strings.TrimSpace(envelope.ID) == "" {
		return errors.New("missing cloudevents id")
	}
	if strings.TrimSpace(envelope.Source) == "" {
		return errors.New("missing cloudevents source")
	}
	if envelope.Type == "" {
		return errors.New("missing cloudevents type")
	}
	if strings.TrimSpace(envelope.Time) == "" {
		return errors.New("missing cloudevents time")
	}
	if _, err := time.Parse(time.RFC3339Nano, envelope.Time); err != nil {
		return fmt.Errorf("invalid cloudevents time: %w", err)
	}
	if envelope.DataContentType != "" && !isJSONContentType(envelope.DataContentType) {
		return fmt.Errorf("unsupported cloudevents datacontenttype %q", envelope.DataContentType)
	}
	return nil
}

func isStructuredCloudEventsContentType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return false
	}
	return mediaType == "application/cloudevents+json" || mediaType == "application/json"
}

func isJSONContentType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return false
	}
	return mediaType == "application/json"
}

func remoteHost(remoteAddr string) string {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return remoteAddr
}
