package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/internal/cli"
)

func suspendTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("clientgoscheme: %v", err)
	}
	if err := kaprov1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("kapro scheme: %v", err)
	}
	return s
}

func captureSuspendOutput(t *testing.T, fn func()) string {
	t.Helper()
	orig := cli.Out
	defer func() { cli.Out = orig }()
	var buf bytes.Buffer
	cli.Out = &buf
	fn()
	return buf.String()
}

func newFakeClientWithPromo(t *testing.T, promo *kaprov1alpha1.Promotion) client.Client {
	t.Helper()
	objs := []client.Object{}
	if promo != nil {
		objs = append(objs, promo)
	}
	return fake.NewClientBuilder().
		WithScheme(suspendTestScheme(t)).
		WithObjects(objs...).
		Build()
}

func TestSuspend_FlipsTrueAndPatches(t *testing.T) {
	promo := &kaprov1alpha1.Promotion{
		ObjectMeta: metav1.ObjectMeta{Name: "p1"},
		Spec:       kaprov1alpha1.PromotionSpec{KaproRef: "k", Suspended: false},
	}
	c := newFakeClientWithPromo(t, promo)

	out := captureSuspendOutput(t, func() {
		if err := suspendResumeWithClient(context.Background(), c, "p1", true); err != nil {
			t.Fatalf("suspend: %v", err)
		}
	})
	if !strings.Contains(out, "suspended") {
		t.Errorf("output missing 'suspended': %q", out)
	}

	var got kaprov1alpha1.Promotion
	if err := c.Get(context.Background(), client.ObjectKey{Name: "p1"}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.Spec.Suspended {
		t.Fatalf("spec.suspended not flipped to true: %+v", got.Spec)
	}
}

func TestResume_FlipsFalseAndPatches(t *testing.T) {
	promo := &kaprov1alpha1.Promotion{
		ObjectMeta: metav1.ObjectMeta{Name: "p1"},
		Spec:       kaprov1alpha1.PromotionSpec{KaproRef: "k", Suspended: true},
	}
	c := newFakeClientWithPromo(t, promo)

	out := captureSuspendOutput(t, func() {
		if err := suspendResumeWithClient(context.Background(), c, "p1", false); err != nil {
			t.Fatalf("resume: %v", err)
		}
	})
	if !strings.Contains(out, "resumed") {
		t.Errorf("output missing 'resumed': %q", out)
	}

	var got kaprov1alpha1.Promotion
	if err := c.Get(context.Background(), client.ObjectKey{Name: "p1"}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Spec.Suspended {
		t.Fatalf("spec.suspended not flipped to false: %+v", got.Spec)
	}
}

func TestSuspend_NoOpWhenAlreadySuspended(t *testing.T) {
	promo := &kaprov1alpha1.Promotion{
		ObjectMeta: metav1.ObjectMeta{Name: "p1"},
		Spec:       kaprov1alpha1.PromotionSpec{KaproRef: "k", Suspended: true},
	}
	c := newFakeClientWithPromo(t, promo)

	out := captureSuspendOutput(t, func() {
		if err := suspendResumeWithClient(context.Background(), c, "p1", true); err != nil {
			t.Fatalf("suspend no-op: %v", err)
		}
	})
	if !strings.Contains(out, "already") {
		t.Errorf("expected idempotent message, got %q", out)
	}
}

func TestSuspend_NotFound(t *testing.T) {
	c := newFakeClientWithPromo(t, nil)
	err := suspendResumeWithClient(context.Background(), c, "missing", true)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not-found error, got %v", err)
	}
}
