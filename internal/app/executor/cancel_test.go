package executor

import (
	"context"
	"testing"
)

func TestRequestCancelRegistryCancelsOncePerRequest(t *testing.T) {
	var service Service
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if !service.RegisterRequestCancel("req-1", cancel) {
		t.Fatal("a request with an id was not registered")
	}
	if !service.CancelRequest("req-1") {
		t.Fatal("registered request was not canceled")
	}
	if ctx.Err() == nil {
		t.Fatal("registering a cancel did not stop the request")
	}
	// The registration is dropped on cancel, so a duplicate completion event
	// must not report a live request.
	if service.CancelRequest("req-1") {
		t.Fatal("canceling twice reported a live request")
	}
}

func TestRequestCancelRegistryKeepsUnknownRequestsSilent(t *testing.T) {
	var service Service
	if service.RegisterRequestCancel("", func() {}) {
		t.Fatal("an empty request id must not be registered")
	}
	if service.CancelRequest("req-missing") {
		t.Fatal("an unknown request must not be reported as canceled")
	}
	if service.CancelRequest("") {
		t.Fatal("an empty request id must not be canceled")
	}
}

func TestRequestCancelReleaseKeepsRequestRunning(t *testing.T) {
	var service Service
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if !service.RegisterRequestCancel("req-2", cancel) {
		t.Fatal("request was not registered")
	}
	service.ReleaseRequestCancel("req-2")
	if ctx.Err() != nil {
		t.Fatal("releasing a finished request canceled it")
	}
	if service.CancelRequest("req-2") {
		t.Fatal("a released request was still canceled")
	}
}

func TestRequestIDFromMetadataIgnoresOtherValues(t *testing.T) {
	if got := requestIDFromMetadata(map[string]any{hostRequestIDMetadataKey: "req-3"}); got != "req-3" {
		t.Fatalf("request id = %q", got)
	}
	if got := requestIDFromMetadata(map[string]any{hostRequestIDMetadataKey: "  "}); got != "" {
		t.Fatalf("blank request id = %q", got)
	}
	if got := requestIDFromMetadata(map[string]any{hostRequestIDMetadataKey: 42}); got != "" {
		t.Fatalf("non-string request id = %q", got)
	}
	if got := requestIDFromMetadata(nil); got != "" {
		t.Fatalf("missing metadata = %q", got)
	}
}

func TestTransparentCancelErrorRecognizesHostTeardown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !isTransparentCancelError(ctx, nil) {
		t.Fatal("a canceled request context was not recognized")
	}
	for _, text := range []string{
		"host plugin callback instance is closed",
		"host callback context closed while opening model stream",
		"host model stream closed during startup",
	} {
		if !isTransparentCancelError(context.Background(), errString(text)) {
			t.Fatalf("%q was not recognized as cancellation", text)
		}
	}
	for _, text := range []string{
		"connection reset by peer",
		"stream 12 is not open",
		"upstream stream idle timeout",
	} {
		if isTransparentCancelError(context.Background(), errString(text)) {
			t.Fatalf("%q was treated as cancellation", text)
		}
	}
}

// errString is a minimal error so the test does not depend on fmt.
type errString string

func (e errString) Error() string { return string(e) }
