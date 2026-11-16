package proxy

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"
	"sync"
	"testing"
)

type zeroReader struct{}

func (zeroReader) Read(buffer []byte) (int, error) {
	clear(buffer)
	return len(buffer), nil
}

func relationTemplate(rows [][]string) Template {
	return Template{
		Protocol: ProtocolHTTP, Host: "proxy.example", Port: 8080,
		UsernamePattern: "{供应商}-{some-city}", HasPassword: true, Password: "pass",
		Placeholders: []Placeholder{
			{Name: "供应商", Kind: PlaceholderEnum, Enum: &EnumSpec{Separator: "|"}, GroupID: "custom"},
			{Name: "some-city", Kind: PlaceholderEnum, Enum: &EnumSpec{Separator: "|"}, GroupID: "custom"},
		},
		ValueGroups: []ValueGroup{{ID: "custom", Columns: []string{"供应商", "some-city"}, Rows: rows}},
	}
}

func TestTemplateEncodingPreservesLiteralBracesAndCredentialValues(t *testing.T) {
	template, err := ParseTemplateURL("HTTP://u%40literal-%7Bfixed%7D-{供应商}-{供应商}:p%7Bbraces%7D%40%3A%25@EXAMPLE.COM:8080/")
	if err != nil {
		t.Fatal(err)
	}
	value := "v@:/%{nested}+汉字"
	template.Placeholders = []Placeholder{{Name: "供应商", Kind: PlaceholderEnum, Enum: &EnumSpec{Separator: "|", Input: value}}}
	compiled, err := CompileTemplate(template)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := compiled.RenderWithReader(zeroReader{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := rendered.Endpoint.Username, "u@literal-{fixed}-"+value+"-"+value; got != want {
		t.Fatalf("username = %q, want %q", got, want)
	}
	if got, want := rendered.Endpoint.Password, "p{braces}@:%"; got != want {
		t.Fatalf("password = %q, want %q", got, want)
	}
	reparsed, err := ParseEndpoint(rendered.URL, ParseOptions{})
	if err != nil || reparsed != rendered.Endpoint || reparsed.Host != "example.com" {
		t.Fatalf("rendered URL did not round-trip: endpoint=%#v error=%v", reparsed, err)
	}
	if names := template.UsernamePlaceholders(); len(names) != 2 || names[0] != "供应商" || names[1] != "供应商" {
		t.Fatalf("encoded braces were recognized as variables: %v", names)
	}
	if compiled.MaximumUsernameBytes() != len(rendered.Endpoint.Username) {
		t.Fatal("maximum length counted encoded bytes instead of decoded credential bytes")
	}
}

func TestTemplateFieldInputIsLiteral(t *testing.T) {
	template := Template{
		Protocol: ProtocolHTTP, Host: "proxy.example", Port: 8080,
		UsernamePattern: "%40-{name}", Password: "{literal}@:/%", HasPassword: true,
		Placeholders: []Placeholder{{Name: "name", Kind: PlaceholderEnum, Enum: &EnumSpec{Separator: "|", Input: "%7Bname%7D"}}},
	}
	rendered, err := template.RenderWithReader(zeroReader{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rendered.Endpoint.Username != "%40-%7Bname%7D" || rendered.Endpoint.Password != template.Password {
		t.Fatalf("field values were decoded or expanded: %#v", rendered.Endpoint)
	}
}

func TestTemplateStaticAuthenticationForms(t *testing.T) {
	for _, raw := range []string{
		"http://proxy.example:8080/",
		"https://alice@proxy.example:443",
		"socks5://alice:@proxy.example:1080",
	} {
		t.Run(raw, func(t *testing.T) {
			template, err := ParseTemplateURL(raw)
			if err != nil {
				t.Fatal(err)
			}
			rendered, err := template.RenderWithReader(zeroReader{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			expected, err := ParseEndpoint(raw, ParseOptions{})
			if err != nil || rendered.Endpoint != expected {
				t.Fatalf("authentication form changed: got=%#v expected=%#v error=%v", rendered.Endpoint, expected, err)
			}
		})
	}
}

func TestEndpointValidationAndCanonicalIDNA(t *testing.T) {
	endpoint, err := ParseEndpoint("HTTP://u:p@例子.测试:443/", ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.CanonicalURL() != "http://u:p@xn--fsqu00a.xn--0zwm56d:443" {
		t.Fatalf("unexpected IDNA canonical URL: %s", endpoint.CanonicalURL())
	}
	bad := []string{
		"http://alice:private@[fe80::1%25eth0]:8080",
		"http://alice:private@bad_host.example:8080",
		"http://alice:private@bad..example:8080",
		"http://alice:private@-bad.example:8080",
		"http://alice:private@proxy.example:8080?",
		"http://alice:private@proxy.example:8080#",
		"http://alice%0A:private@proxy.example:8080",
		"http://alice:private%00@proxy.example:8080",
		"http://alice:private@proxy.example:8080/%20",
		"http://alice:private%GG@proxy.example:8080",
	}
	for _, raw := range bad {
		if _, err := ParseEndpoint(raw, ParseOptions{}); err == nil {
			t.Fatalf("invalid endpoint accepted: %q", raw)
		} else if strings.Contains(err.Error(), "private") {
			t.Fatalf("endpoint error disclosed credentials: %v", err)
		}
	}
	fieldBad := []Endpoint{
		{Protocol: Protocol("ftp"), Host: "proxy.example", Port: 8080},
		{Protocol: ProtocolHTTP, Host: "host/path", Port: 8080},
		{Protocol: ProtocolHTTP, Host: "proxy.example", Port: 8080, Password: "hidden", HasPassword: true},
		{Protocol: ProtocolHTTP, Host: "proxy.example", Port: 8080, Username: "ignored"},
		{Protocol: ProtocolHTTP, Host: "proxy.example", Port: 8080, Username: "alice", HasUser: true, Password: "ignored"},
	}
	for _, endpoint := range fieldBad {
		if err := endpoint.Validate(); err == nil {
			t.Fatalf("invalid field endpoint accepted: %#v", endpoint)
		}
	}
	if MaskProxyURL("http://host:8080:user:private") != "[redacted]" {
		t.Fatal("invalid colon input was not redacted")
	}
}

func TestEndpointCredentialProtocolByteLimits(t *testing.T) {
	for _, protocol := range []Protocol{ProtocolHTTP, ProtocolHTTPS, ProtocolSOCKS5, ProtocolSOCKS5H} {
		limit := credentialLimit(protocol)
		for _, field := range []string{"username", "password"} {
			endpoint := Endpoint{Protocol: protocol, Host: "proxy.example", Port: 8080, Username: "alice", Password: "pass", HasUser: true, HasPassword: true}
			if field == "username" {
				endpoint.Username = strings.Repeat("a", limit)
			} else {
				endpoint.Password = strings.Repeat("a", limit)
			}
			if err := endpoint.Validate(); err != nil {
				t.Fatalf("%s %s at byte limit was rejected: %v", protocol, field, err)
			}
			if field == "username" {
				endpoint.Username += "汉"
			} else {
				endpoint.Password += "汉"
			}
			if err := endpoint.Validate(); err == nil {
				t.Fatalf("%s %s exceeded its byte limit", protocol, field)
			}
		}
	}
}

func TestColonFormatIPv6AndDecodeOnce(t *testing.T) {
	endpoint, err := ParseEndpoint("socks5h://[2001:db8::1]:1080:u%40x:p%253A%40", ParseOptions{AllowColonFormat: true})
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.Host != "2001:db8::1" || endpoint.Username != "u@x" || endpoint.Password != "p%3A@" {
		t.Fatalf("unexpected address-first endpoint: %#v", endpoint)
	}
}

func TestTemplateMalformedInputsReturnErrorsWithoutPanic(t *testing.T) {
	base := relationTemplate([][]string{{"alpha", "one"}})
	cases := []Template{}
	add := func(change func(*Template)) {
		value := CloneTemplate(base)
		change(&value)
		cases = append(cases, value)
	}
	add(func(value *Template) { value.ValueGroups[0].Rows[0] = []string{"alpha"} })
	add(func(value *Template) { value.Placeholders[0].Enum = nil })
	add(func(value *Template) {
		value.Placeholders[0].Kind = PlaceholderRandom
		value.Placeholders[0].Random = nil
	})
	add(func(value *Template) { value.Placeholders[0].Random = &RandomSpec{Length: -1} })
	add(func(value *Template) { value.UsernamePattern = "{undefined}" })
	add(func(value *Template) { value.UsernamePattern = "{供应商" })
	add(func(value *Template) { value.ValueGroups[0].Columns = []string{"供应商", "供应商"} })
	add(func(value *Template) { value.ValueGroups = nil })
	add(func(value *Template) { value.ValueGroups[0].Columns = []string{"供应商", "unknown"} })
	add(func(value *Template) { value.Protocol = Protocol("ftp") })
	add(func(value *Template) { value.Host = "invalid/host" })
	add(func(value *Template) { value.Placeholders[0].Enum.Input = "two-sources" })
	for index, template := range cases {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("malformed template panicked: %v", recovered)
				}
			}()
			if _, err := template.RenderWithReader(bytes.NewReader(nil), nil); err == nil {
				t.Fatal("malformed template was rendered")
			}
			template.MaximumUsernameBytes()
			report := ValidateTemplate(template)
			if report.Valid || len(report.Errors) != 1 || report.Errors[0].Path == "" {
				t.Fatalf("validation error lacks a field location: %#v", report)
			}
		})
	}
}

func TestTemplateAllRowsValidatedBeyondPreviewLimit(t *testing.T) {
	rows := make([][]string, 121)
	for index := range rows {
		rows[index] = []string{"alpha", fmt.Sprint(index)}
	}
	rows[120][1] = "bad\x01value"
	report := ValidateTemplate(relationTemplate(rows))
	if report.Valid || report.Errors[0].Path != "value_groups[0].rows[120][1]" {
		t.Fatalf("invalid late row was missed or not located: %#v", report)
	}
}

func TestTemplateEnumsRejectEmptyAndNormalizeDuplicates(t *testing.T) {
	template := Template{
		Protocol: ProtocolHTTP, Host: "proxy.example", Port: 8080, UsernamePattern: "{name}",
		Placeholders: []Placeholder{{Name: "name", Kind: PlaceholderEnum, Enum: &EnumSpec{Separator: "|", Input: " US |US|us "}}},
	}
	compiled, err := CompileTemplate(template)
	if err != nil {
		t.Fatal(err)
	}
	if compiled.CombinationCount() != 2 || len(compiled.Warnings()) != 1 {
		t.Fatal("duplicate enum values were not normalized")
	}
	if got := compiled.NormalizedTemplate().Placeholders[0].Enum.Input; got != "US|us" {
		t.Fatalf("case or trimmed values changed: %q", got)
	}
	for _, input := range []string{"a||b", "|a", "a|", ""} {
		bad := CloneTemplate(template)
		bad.Placeholders[0].Enum.Input = input
		if err := bad.Validate(); err == nil {
			t.Fatalf("empty enum candidate was discarded from %q", input)
		}
	}
	template.Placeholders[0].Enum.Separator = strings.Repeat("界", 9)
	if err := template.Validate(); err == nil {
		t.Fatal("separator exceeded the Unicode character limit")
	}
}

func TestTemplateRelationNormalizationAndConditionalSampling(t *testing.T) {
	template := relationTemplate([][]string{{" alpha ", " one "}, {"alpha", "one"}, {"beta", "two"}, {"beta", "three"}})
	compiled, err := CompileTemplate(template)
	if err != nil {
		t.Fatal(err)
	}
	if compiled.GroupRowCount() != 3 || compiled.CombinationCount() != 3 || len(compiled.Warnings()) != 1 {
		t.Fatal("duplicate relation rows were not normalized")
	}
	// A fixed child filters the parent's options before choosing a parent.
	rendered, err := compiled.RenderWithReader(zeroReader{}, map[string]string{"some-city": "two"})
	if err != nil || rendered.Values["供应商"] != "beta" || rendered.Values["some-city"] != "two" {
		t.Fatalf("non-prefix assignments did not constrain parents: %#v, %v", rendered, err)
	}
	if _, err := compiled.RenderWithReader(zeroReader{}, map[string]string{"供应商": "alpha", "some-city": "two"}); err == nil {
		t.Fatal("illegal fixed relation was silently changed")
	}
	if _, err := compiled.RenderWithReader(zeroReader{}, map[string]string{"unknown": "one"}); err == nil {
		t.Fatal("unknown assignment was silently accepted")
	}
}

func TestTemplateParentChoiceDoesNotUseLeafWeights(t *testing.T) {
	rows := make([][]string, 101)
	for index := 0; index < 100; index++ {
		rows[index] = []string{"alpha", fmt.Sprint(index)}
	}
	rows[100] = []string{"beta", "only"}
	compiled, err := CompileTemplate(relationTemplate(rows))
	if err != nil {
		t.Fatal(err)
	}
	// The first random selection has two choices, even though there are 101 rows.
	rendered, err := compiled.RenderWithReader(bytes.NewReader([]byte{1}), nil)
	if err != nil || rendered.Values["供应商"] != "beta" {
		t.Fatalf("parent choices were weighted by leaf rows: %#v %v", rendered.Values, err)
	}
}

func TestTemplateLengthUsesLegalGroupRowsAndRepeatedVariables(t *testing.T) {
	template := relationTemplate([][]string{{strings.Repeat("a", 120), "b"}, {"a", strings.Repeat("b", 240)}})
	template.Protocol = ProtocolSOCKS5
	template.UsernamePattern = "{供应商}{供应商}{some-city}"
	compiled, err := CompileTemplate(template)
	if err != nil {
		t.Fatal(err)
	}
	if compiled.MaximumUsernameBytes() != 242 {
		t.Fatalf("maximum combined lengths from incompatible rows: %d", compiled.MaximumUsernameBytes())
	}
	template.ValueGroups[0].Rows = append(template.ValueGroups[0].Rows, []string{strings.Repeat("汉", 43), "x"})
	if err := template.Validate(); err == nil {
		t.Fatal("repeated multibyte variable exceeded the SOCKS5 byte limit")
	}
}

func TestCompiledTemplateIsImmutableAndConcurrent(t *testing.T) {
	template := relationTemplate([][]string{{"alpha", "one"}, {"beta", "two"}})
	compiled, err := CompileTemplate(template)
	if err != nil {
		t.Fatal(err)
	}
	template.ValueGroups[0].Rows[0][0] = "changed"
	template.Placeholders[0].Enum.Separator = "changed"
	snapshot := compiled.NormalizedTemplate()
	snapshot.ValueGroups[0].Rows[0][0] = "also-changed"
	var wait sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for attempt := 0; attempt < 100; attempt++ {
				rendered, err := compiled.RenderWithReader(zeroReader{}, nil)
				if err != nil || rendered.Endpoint.Username != "alpha-one" {
					t.Errorf("immutable compiled template changed: %#v, %v", rendered, err)
					return
				}
			}
		}()
	}
	wait.Wait()
}

func TestTemplateRenderAvoidingFindsFiniteAlternative(t *testing.T) {
	template := relationTemplate([][]string{{"alpha", "one"}, {"beta", "two"}})
	compiled, err := CompileTemplate(template)
	if err != nil {
		t.Fatal(err)
	}
	old, err := compiled.RenderWithReader(zeroReader{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	next, err := compiled.RenderAvoiding(zeroReader{}, old.Endpoint.Fingerprint())
	if err != nil || next.Endpoint.Fingerprint() == old.Endpoint.Fingerprint() {
		t.Fatalf("finite alternative was missed: %#v %v", next, err)
	}
	template.ValueGroups[0].Rows = [][]string{{"alpha", "one"}}
	compiled, err = CompileTemplate(template)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := compiled.RenderAvoiding(zeroReader{}, old.Endpoint.Fingerprint()); err == nil {
		t.Fatal("single fixed endpoint reported a successful rotation")
	} else {
		var issue *ValidationError
		if !errors.As(err, &issue) || issue.Code != "rotation_unavailable" {
			t.Fatalf("unexpected rotation error: %v", err)
		}
	}
}

func TestTemplateCombinationCountDoesNotOverflow(t *testing.T) {
	template := Template{Protocol: ProtocolHTTP, Host: "proxy.example", Port: 8080}
	for index := 0; index < 20; index++ {
		name := fmt.Sprintf("v%d", index)
		template.UsernamePattern += "{" + name + "}"
		template.Placeholders = append(template.Placeholders, Placeholder{Name: name, Kind: PlaceholderEnum, Enum: &EnumSpec{Separator: "|", Input: "0|1|2|3|4|5|6|7|8|9"}})
	}
	compiled, err := CompileTemplate(template)
	if err != nil {
		t.Fatal(err)
	}
	if compiled.CombinationCount() != math.MaxInt64 {
		t.Fatal("large finite combination count overflowed")
	}
	if !compiled.CombinationCountCapped() || compiled.CombinationCountExact() != "100000000000000000000" {
		t.Fatal("large finite combination count was silently truncated")
	}
	report := ValidateTemplate(template)
	if report.Summary["combination_count_capped"] != true || report.Summary["combination_count_exact"] != "100000000000000000000" {
		t.Fatal("validation summary presented a capped count as exact")
	}
}

func TestTemplateRenderedURLIsAStandardURL(t *testing.T) {
	template := relationTemplate([][]string{{"x@y", "p:q/r%+"}})
	rendered, err := template.RenderWithReader(zeroReader{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(rendered.URL)
	if err != nil || parsed.User.Username() != "x@y-p:q/r%+" || parsed.Host != "proxy.example:8080" {
		t.Fatalf("credential separators changed URL structure: %q %v", rendered.URL, err)
	}
}

func BenchmarkCompileRelations(b *testing.B) {
	for _, count := range []int{100, 1000, 10000} {
		rows := make([][]string, count)
		for index := range rows {
			rows[index] = []string{fmt.Sprint(index / 10), fmt.Sprint(index)}
		}
		template := relationTemplate(rows)
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := CompileTemplate(template); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkCompiledRenderRelations(b *testing.B) {
	for _, count := range []int{100, 1000, 10000} {
		rows := make([][]string, count)
		for index := range rows {
			rows[index] = []string{fmt.Sprint(index / 10), fmt.Sprint(index)}
		}
		compiled, err := CompileTemplate(relationTemplate(rows))
		if err != nil {
			b.Fatal(err)
		}
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := compiled.RenderWithReader(zeroReader{}, nil); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
