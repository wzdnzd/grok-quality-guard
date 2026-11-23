package executor

import (
	"errors"
	"testing"
)

func TestProxyFailureReachedRotationThreshold(t *testing.T) {
	cases := []struct {
		name      string
		class     string
		strikes   int
		threshold int
		want      bool
	}{
		{name: "first proxy error", class: ClassProxyError, strikes: 0, threshold: 2, want: false},
		{name: "second proxy error", class: ClassProxyError, strikes: 1, threshold: 2, want: true},
		{name: "second probe error", class: ClassProbeError, strikes: 1, threshold: 2, want: true},
		{name: "custom threshold pending", class: ClassProxyError, strikes: 1, threshold: 3, want: false},
		{name: "custom threshold reached", class: ClassProxyError, strikes: 2, threshold: 3, want: true},
		{name: "quality class", class: ClassMissingThinking, strikes: 9, threshold: 2, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := proxyFailureReachedRotationThreshold(tc.class, tc.strikes, tc.threshold); got != tc.want {
				t.Fatalf("proxyFailureReachedRotationThreshold(%q, %d, %d) = %t, want %t", tc.class, tc.strikes, tc.threshold, got, tc.want)
			}
		})
	}
}

func TestTransparentProxyFailurePrefersAccountSwitch(t *testing.T) {
	for _, class := range []string{ClassProxyError, ClassProbeError, ClassMissingThinking} {
		if shouldRotateTransparentFailure(true, class) {
			t.Fatalf("class %q rotated synchronously despite an alternative account", class)
		}
		if !shouldRotateTransparentFailure(false, class) {
			t.Fatalf("class %q did not use synchronous rotation as the last candidate", class)
		}
	}
	if !shouldRotateTransparentFailure(true, ClassBufferedBurst) {
		t.Fatal("quality failure rotation behavior changed")
	}
}

func TestTransparentFailureCanFailoverByStage(t *testing.T) {
	cases := []struct {
		name        string
		replayable  bool
		emitted     bool
		preResponse bool
		class       string
		want        bool
	}{
		{name: "replayable quality failure", replayable: true, class: ClassMissingThinking, want: true},
		{name: "unsafe pre-response probe error", preResponse: true, class: ClassProbeError, want: true},
		{name: "unsafe pre-response proxy error", preResponse: true, class: ClassProxyError, want: true},
		{name: "unsafe post-open probe error", class: ClassProbeError, want: false},
		{name: "non-transport pre-response failure", preResponse: true, class: ClassMissingThinking, want: false},
		{name: "already emitted", replayable: true, emitted: true, preResponse: true, class: ClassProbeError, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := transparentFailureCanFailover(tc.replayable, tc.emitted, tc.preResponse, tc.class); got != tc.want {
				t.Fatalf("transparentFailureCanFailover(...) = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestTransparentTransportFailureUsesNetworkReason(t *testing.T) {
	for _, test := range []struct {
		message string
		class   string
	}{
		{message: "net/http: TLS handshake timeout", class: ClassProbeError},
		{message: "proxyconnect tcp: dial tcp 127.0.0.1:2260: connect: connection refused", class: ClassProxyError},
		{message: "unknown error general SOCKS server failure", class: ClassProxyError},
	} {
		class, reason := classifyTransparentTransportFailure(errors.New(test.message))
		if class != test.class {
			t.Fatalf("%q classified as %q, want %q", test.message, class, test.class)
		}
		if reason != "reason.detail.upstream_network_error" {
			t.Fatalf("%q reason = %q, want upstream_network_error", test.message, reason)
		}
	}
}
