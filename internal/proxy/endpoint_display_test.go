package proxy

import "testing"

func TestMaskProxyURLKeepsCredentialShape(t *testing.T) {
	cases := map[string]string{
		"socks5h://user:secret@proxy.example:8080":            "socks5h://us***r:******@proxy.example:8080",
		"https://alice:p%40ss@proxy.example:8443":             "https://al***e:******@proxy.example:8443",
		"socks5h://Default.grok-alice-old1:secret@resin:2260": "socks5h://Def***d1:******@resin:2260",
		"socks5h://proxy.example:1080":                        "socks5h://proxy.example:1080",
	}
	for raw, want := range cases {
		if got := MaskProxyURL(raw); got != want {
			t.Fatalf("MaskProxyURL(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestTemplateURLForRevealKeepsPatternAndEncodesPassword(t *testing.T) {
	template, err := ParseTemplateURL("socks5h://user-{account}:p%40ss@proxy.example:1080")
	if err != nil {
		t.Fatal(err)
	}
	if got := template.URLForDisplay(); got != "socks5h://user-{account}:******@proxy.example:1080" {
		t.Fatalf("display URL = %q", got)
	}
	if got := template.URLForReveal(); got != "socks5h://user-{account}:p%40ss@proxy.example:1080" {
		t.Fatalf("reveal URL = %q", got)
	}
}
