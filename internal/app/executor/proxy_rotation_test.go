package executor

import "testing"

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
