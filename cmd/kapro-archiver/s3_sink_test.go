package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
)

// fakeRoundTripper records every request and returns canned status codes
// (and optional bodies) in order. We hold ints, not *http.Response, so
// bodyclose can't flag the test for unclosed test fixtures — the response
// objects only exist inside putOnce, which already defers Close.
type fakedResponse struct {
	status int
	body   string
}

type fakeRoundTripper struct {
	responses []fakedResponse
	requests  []*http.Request
	bodies    [][]byte
}

func (f *fakeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
		_ = req.Body.Close()
	}
	f.requests = append(f.requests, req)
	f.bodies = append(f.bodies, body)
	if len(f.responses) == 0 {
		return &http.Response{
			StatusCode: 500,
			Body:       io.NopCloser(strings.NewReader("no canned response")),
			Header:     make(http.Header),
		}, nil
	}
	r := f.responses[0]
	f.responses = f.responses[1:]
	return &http.Response{
		StatusCode: r.status,
		Body:       io.NopCloser(strings.NewReader(r.body)),
		Header:     make(http.Header),
	}, nil
}

func ok() fakedResponse                 { return fakedResponse{status: 200} }
func preconditionFailed() fakedResponse { return fakedResponse{status: http.StatusPreconditionFailed} }

func newTestS3Sink(rt http.RoundTripper) *S3Sink {
	return &S3Sink{
		client: &http.Client{Transport: rt, Timeout: time.Second},
		creds: aws.CredentialsProviderFunc(func(_ context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "AKIA", SecretAccessKey: "secret"}, nil
		}),
		bucket:      "test-bucket",
		prefix:      "kapro/events",
		region:      "us-east-1",
		signingName: "s3",
	}
}

func sampleRecord() ArchiveRecord {
	return ArchiveRecord{
		Body: []byte(`{"id":"event-1"}`),
		Metadata: ArchiveMetadata{
			ID:         "event-1",
			Source:     "/apis/kapro.io/v1alpha1/promotions/demo",
			Type:       "kapro.io/promotion.succeeded",
			Time:       "2026-05-22T10:00:00Z",
			BodySHA256: "abc",
			DedupeKey:  "kapro.io/promotion.succeeded/2026/05/22/event-1",
		},
	}
}

func TestS3SinkPutsBothEventAndMetadata(t *testing.T) {
	rt := &fakeRoundTripper{responses: []fakedResponse{ok(), ok()}}
	sink := newTestS3Sink(rt)
	if err := sink.Write(context.Background(), sampleRecord()); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := len(rt.requests); got != 2 {
		t.Fatalf("requests: got %d, want 2 (event + metadata)", got)
	}
	for i, want := range []string{"event.json", "metadata.json"} {
		req := rt.requests[i]
		if req.Method != http.MethodPut {
			t.Errorf("request %d method: got %s, want PUT", i, req.Method)
		}
		if got := req.Header.Get("If-None-Match"); got != "*" {
			t.Errorf("request %d If-None-Match: got %q, want *", i, got)
		}
		if !strings.HasSuffix(req.URL.Path, want) {
			t.Errorf("request %d URL path %q does not end with %q", i, req.URL.Path, want)
		}
		if !strings.Contains(req.URL.Path, "kapro/events/") {
			t.Errorf("request %d URL path %q missing prefix kapro/events/", i, req.URL.Path)
		}
	}
}

// A partial first delivery (event PUT succeeded, metadata PUT failed)
// leaves event.json present. On retry the event PUT returns 412
// PreconditionFailed (If-None-Match: *) and the sink MUST still attempt
// the metadata PUT so the archive can heal.
func TestS3SinkHealsPartialDelivery(t *testing.T) {
	rt := &fakeRoundTripper{responses: []fakedResponse{preconditionFailed(), ok()}}
	sink := newTestS3Sink(rt)
	if err := sink.Write(context.Background(), sampleRecord()); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := len(rt.requests); got != 2 {
		t.Fatalf("requests: got %d, want 2 (event 412 then metadata 200)", got)
	}
	if !strings.HasSuffix(rt.requests[1].URL.Path, "metadata.json") {
		t.Fatalf("second request should be metadata PUT: %s", rt.requests[1].URL.Path)
	}
}

// A fully duplicate delivery returns 412 for both objects; both calls are
// no-ops and Write succeeds.
func TestS3SinkDuplicateDeliveryIsNoOp(t *testing.T) {
	rt := &fakeRoundTripper{responses: []fakedResponse{preconditionFailed(), preconditionFailed()}}
	sink := newTestS3Sink(rt)
	if err := sink.Write(context.Background(), sampleRecord()); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := len(rt.requests); got != 2 {
		t.Fatalf("requests: got %d, want 2", got)
	}
}

func TestS3SinkObjectURLVirtualHost(t *testing.T) {
	sink := newTestS3Sink(&fakeRoundTripper{})
	got := sink.objectURL("kapro/events/foo bar/event.json")
	if !strings.HasPrefix(got, "https://test-bucket.s3.us-east-1.amazonaws.com/") {
		t.Errorf("virtual-host URL prefix wrong: %s", got)
	}
	if !strings.Contains(got, "foo%20bar") {
		t.Errorf("space not escaped in URL: %s", got)
	}
}

func TestS3SinkObjectURLPathStyle(t *testing.T) {
	sink := newTestS3Sink(&fakeRoundTripper{})
	sink.endpoint = "https://minio.local"
	sink.pathStyle = true
	got := sink.objectURL("kapro/events/event-1/event.json")
	if !strings.HasPrefix(got, "https://minio.local/test-bucket/") {
		t.Errorf("path-style URL wrong: %s", got)
	}
}

func TestS3SinkSurfacesNon2xxError(t *testing.T) {
	rt := &fakeRoundTripper{responses: []fakedResponse{{status: 403, body: "AccessDenied"}}}
	sink := newTestS3Sink(rt)
	err := sink.Write(context.Background(), sampleRecord())
	if err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "AccessDenied") {
		t.Fatalf("expected error reporting status 403 and AccessDenied, got: %v", err)
	}
}
