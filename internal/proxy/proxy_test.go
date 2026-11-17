package proxy

import (
	"bytes"
	"errors"
	"regexp"
	"strings"
	"testing"
)

func TestParseEndpointProtocolsAndForms(t *testing.T) {
	tests := []struct {
		raw      string
		protocol Protocol
		user     string
		password string
		hasUser  bool
		hasPass  bool
	}{
		{"http://alice:secret@proxy.example:8080", ProtocolHTTP, "alice", "secret", true, true},
		{"https://alice@proxy.example:8443", ProtocolHTTPS, "alice", "", true, false},
		{"socks5://proxy.example:1080", ProtocolSOCKS5, "", "", false, false},
		{"socks5h://alice:secret@[2001:db8::1]:1080", ProtocolSOCKS5H, "alice", "secret", true, true},
		{"http://alice:p%40ss@proxy.example:8080/", ProtocolHTTP, "alice", "p@ss", true, true},
	}
	for _, test := range tests {
		endpoint, errParse := ParseEndpoint(test.raw)
		if errParse != nil {
			t.Fatalf("ParseEndpoint(%q): %v", test.raw, errParse)
		}
		if endpoint.Protocol != test.protocol || endpoint.Username != test.user || endpoint.Password != test.password || endpoint.HasUser != test.hasUser || endpoint.HasPassword != test.hasPass {
			t.Fatalf("ParseEndpoint(%q) = %#v", test.raw, endpoint)
		}
	}
}
func TestParseEndpointRejectsMissingSchemeAndInvalidValues(t *testing.T) {
	if _, errParse := ParseEndpoint("alice:secret@proxy.example:8080"); !errors.Is(errParse, ErrMissingScheme) {
		t.Fatalf("expected ErrMissingScheme, got %v", errParse)
	}
	invalid := []string{
		"ftp://proxy.example:21",
		"http://proxy.example",
		"http://proxy.example:0",
		"http://proxy.example:65536",
		"http://:password@proxy.example:8080",
		"http://proxy.example:8080/path",
		"http://proxy.example:8080?query=1",
	}
	for _, raw := range invalid {
		if _, errParse := ParseEndpoint(raw); errParse == nil {
			t.Fatalf("expected ParseEndpoint(%q) to fail", raw)
		}
	}
}
func cascadeTemplate(t *testing.T) Template {
	t.Helper()
	template, errParse := ParseTemplateURL("socks5h://vendor-{vendor}-plan-{plan}-profile-{profile}:secret@proxy.example:1080")
	if errParse != nil {
		t.Fatal(errParse)
	}
	template.Name = "cascade"
	template.Placeholders = []Placeholder{
		{Name: "vendor", Kind: PlaceholderEnum, Enum: &EnumSpec{Separator: "|"}, GroupID: "plans"},
		{Name: "plan", Kind: PlaceholderEnum, Enum: &EnumSpec{Separator: "|"}, GroupID: "plans"},
		{Name: "profile", Kind: PlaceholderEnum, Enum: &EnumSpec{Separator: "|"}, GroupID: "plans"},
	}
	template.ValueGroups = []ValueGroup{{
		ID: "plans", Name: "vendor plans", Columns: []string{"vendor", "plan", "profile"},
		Rows: [][]string{
			{"alpha", "basic", "gold"},
			{"alpha", "pro", "silver"},
			{"beta", "basic", "bronze"},
			{"中文", "企业", "钻石"},
		},
	}}
	if errValidate := template.Validate(); errValidate != nil {
		t.Fatalf("validate cascade template: %v", errValidate)
	}
	return template
}
func TestTemplateUsesArbitraryUserDefinedCascadeRows(t *testing.T) {
	template := cascadeTemplate(t)
	legal := map[string]bool{"alpha\x00basic\x00gold": true, "alpha\x00pro\x00silver": true, "beta\x00basic\x00bronze": true, "中文\x00企业\x00钻石": true}
	for attempt := 0; attempt < 100; attempt++ {
		rendered, errRender := template.RenderWithReader(nil, nil)
		if errRender != nil {
			t.Fatal(errRender)
		}
		key := rendered.Values["vendor"] + "\x00" + rendered.Values["plan"] + "\x00" + rendered.Values["profile"]
		if !legal[key] {
			t.Fatalf("rendered illegal user-defined combination %q", key)
		}
	}
	if _, errRender := template.RenderWithReader(nil, map[string]string{"vendor": "beta", "plan": "pro", "profile": "silver"}); errRender == nil {
		t.Fatal("expected illegal fixed combination to fail")
	}
}
func TestTemplateEnumSeparatorAndRandomAlphabet(t *testing.T) {
	template, errParse := ParseTemplateURL("http://x-{region}-{sessionUpper}-{sessionLower}:pass@proxy.example:8080")
	if errParse != nil {
		t.Fatal(errParse)
	}
	template.Name = "enum-random"
	template.Placeholders = []Placeholder{
		{Name: "region", Kind: PlaceholderEnum, Enum: &EnumSpec{Separator: "|", Input: "us|cn|eu"}},
		{Name: "sessionUpper", Kind: PlaceholderRandom, Random: &RandomSpec{Length: 32, CaseSensitive: true}},
		{Name: "sessionLower", Kind: PlaceholderRandom, Random: &RandomSpec{Length: 32, CaseSensitive: false}},
	}
	if errValidate := template.Validate(); errValidate != nil {
		t.Fatal(errValidate)
	}
	for attempt := 0; attempt < 30; attempt++ {
		rendered, errRender := template.RenderWithReader(nil, nil)
		if errRender != nil {
			t.Fatal(errRender)
		}
		if rendered.Values["region"] != "us" && rendered.Values["region"] != "cn" && rendered.Values["region"] != "eu" {
			t.Fatalf("unexpected enum value %q", rendered.Values["region"])
		}
		if len(rendered.Values["sessionUpper"]) != 32 || len(rendered.Values["sessionLower"]) != 32 {
			t.Fatalf("unexpected random lengths: %#v", rendered.Values)
		}
		if !regexp.MustCompile(`^[a-z0-9]+$`).MatchString(rendered.Values["sessionLower"]) {
			t.Fatalf("lowercase random contains invalid chars: %q", rendered.Values["sessionLower"])
		}
		if !regexp.MustCompile(`^[A-Za-z0-9]+$`).MatchString(rendered.Values["sessionUpper"]) {
			t.Fatalf("case-sensitive random contains invalid chars: %q", rendered.Values["sessionUpper"])
		}
	}
}
func TestTemplateRejectsPlaceholdersOutsideUsername(t *testing.T) {
	if _, errParse := ParseTemplateURL("http://alice:{password}@proxy.example:8080"); errParse == nil {
		t.Fatal("expected password placeholder to be rejected")
	}
}
func TestTemplateSupportsUnicodeAndCommonPlaceholderNameCharacters(t *testing.T) {
	template, errParse := ParseTemplateURL("http://x-{some.city}-{地区}:pass@proxy.example:8080")
	if errParse != nil {
		t.Fatal(errParse)
	}
	template.Name = "unicode"
	template.Placeholders = []Placeholder{
		{Name: "some.city", Kind: PlaceholderEnum, Enum: &EnumSpec{Separator: ",", Input: "ny,ca"}},
		{Name: "地区", Kind: PlaceholderEnum, Enum: &EnumSpec{Separator: ",", Input: "华东,华南"}},
	}
	if errValidate := template.Validate(); errValidate != nil {
		t.Fatal(errValidate)
	}
	rendered, errRender := template.RenderWithReader(bytes.NewReader(make([]byte, 128)), nil)
	if errRender != nil {
		t.Fatal(errRender)
	}
	if !strings.Contains(rendered.URL, "%E5%8C%97") && !strings.Contains(rendered.URL, "%E5%8D%8E") {
		t.Fatalf("expected escaped Unicode username in %q", rendered.URL)
	}
}
func TestMaximumUsernameBytes(t *testing.T) {
	template, errParse := ParseTemplateURL("http://prefix-{name}:pass@proxy.example:8080")
	if errParse != nil {
		t.Fatal(errParse)
	}
	template.Name = "length"
	template.Placeholders = []Placeholder{{Name: "name", Kind: PlaceholderEnum, Enum: &EnumSpec{Separator: ",", Input: "a,中文"}}}
	if got, want := template.MaximumUsernameBytes(), len("prefix-")+len("中文"); got != want {
		t.Fatalf("MaximumUsernameBytes = %d, want %d", got, want)
	}
	if errValidate := template.Validate(); errValidate != nil {
		t.Fatal(errValidate)
	}
}

func TestAddressFirstFormatFallsBackAfterStandardURL(t *testing.T) {
	raw := "http://proxy.example:8080:alice:secret"
	endpoint, err := ParseEndpoint(raw)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.Protocol != ProtocolHTTP || endpoint.Host != "proxy.example" || endpoint.Port != 8080 || endpoint.Username != "alice" || endpoint.Password != "secret" {
		t.Fatalf("unexpected address-first endpoint: %#v", endpoint)
	}
}

func TestAddressFirstIPv6AndDecodeOnce(t *testing.T) {
	endpoint, err := ParseEndpoint("socks5h://[2001:db8::1]:1080:u%40x:p%253A%40")
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.Host != "2001:db8::1" || endpoint.Username != "u@x" || endpoint.Password != "p%3A@" {
		t.Fatalf("unexpected address-first endpoint: %#v", endpoint)
	}
}

func TestAddressFirstStandardURLTakesPrecedence(t *testing.T) {
	endpoint, err := ParseEndpoint("http://alice:secret@proxy.example:8080")
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.Host != "proxy.example" || endpoint.Username != "alice" || endpoint.Password != "secret" {
		t.Fatalf("standard URL was not parsed first: %#v", endpoint)
	}
	for _, raw := range []string{
		"http://proxy.example:8080:alice:secret/path",
		"http://proxy.example:8080:alice:secret?x=1",
		"http://proxy.example:8080::secret",
		"http://[2001:db8::1:1080:alice:secret",
	} {
		if _, err := ParseEndpoint(raw); err == nil {
			t.Fatalf("expected address-first input to fail: %q", raw)
		}
	}
}
func TestHostPortAtCredentialsFormatFallsBackAfterStandardURL(t *testing.T) {
	endpoint, err := ParseEndpoint("http://proxy.example:8080@alice:secret")
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.Protocol != ProtocolHTTP || endpoint.Host != "proxy.example" || endpoint.Port != 8080 || endpoint.Username != "alice" || endpoint.Password != "secret" {
		t.Fatalf("unexpected host:port@user:pass endpoint: %#v", endpoint)
	}
}

func TestHostPortAtCredentialsIPv6AndDecodeOnce(t *testing.T) {
	endpoint, err := ParseEndpoint("socks5h://[2001:db8::1]:1080@u%40x:p%253A%40")
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.Host != "2001:db8::1" || endpoint.Username != "u@x" || endpoint.Password != "p%3A@" {
		t.Fatalf("unexpected host:port@user:pass endpoint: %#v", endpoint)
	}
}

func TestHostPortAtCredentialsRejectsAmbiguousShapes(t *testing.T) {
	for _, raw := range []string{
		"http://proxy.example:8080@alice",
		"http://proxy.example@alice:secret",
		"http://proxy.example:8080@:secret",
		"http://proxy.example:8080@alice:secret/path",
		"http://proxy.example:8080@alice:secret?x=1",
		"http://2001:db8::1:1080@alice:secret",
	} {
		if _, err := ParseEndpoint(raw); err == nil {
			t.Fatalf("expected host:port@user:pass input to fail: %q", raw)
		}
	}
}
