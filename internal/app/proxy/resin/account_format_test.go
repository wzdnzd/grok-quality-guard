package resin

import "testing"

func TestAccountFormatExtractsUsernameOnly(t *testing.T) {
	cases := map[string]string{
		"socks5h://{platform}.{account}:{proxy_token}@resin:2260":               "{platform}.{account}",
		"socks5h://CPA.grok-{account}:secret@[2001:db8::1]:2260/path":           "CPA.grok-{account}",
		"socks5h://%7Bplatform%7D.%7Baccount%7D:secret@proxy.example:2260":      "{platform}.{account}",
		"socks5h://literal-user:secret@proxy.example:2260?ignored=yes#fragment": "literal-user",
	}
	for raw, want := range cases {
		got, err := AccountFormat(raw)
		if err != nil {
			t.Fatalf("AccountFormat(%q) failed: %v", raw, err)
		}
		if got != want {
			t.Fatalf("AccountFormat(%q) = %q, want %q", raw, got, want)
		}
	}
}
