package proxy

import "testing"

func TestTemplateDefinitionsEqualIgnoresPlaceholderNamesAndEnumOrder(t *testing.T) {
	left := equivalenceTemplate(t, "socks5h://rn-region-{country}-sid-{session}-t-5:password@us.arxlabs.io:3010", []Placeholder{
		{Name: "country", Kind: PlaceholderEnum, Enum: &EnumSpec{Separator: ",", Input: "US,CN,SG"}},
		{Name: "session", Kind: PlaceholderRandom, Random: &RandomSpec{Length: 8, CaseSensitive: true}},
	})
	right := equivalenceTemplate(t, "socks5h://rn-region-{pl1}-sid-{pl2}-t-5:password@us.arxlabs.io:3010", []Placeholder{
		{Name: "pl2", Kind: PlaceholderRandom, Random: &RandomSpec{Length: 8, CaseSensitive: true}},
		{Name: "pl1", Kind: PlaceholderEnum, Enum: &EnumSpec{Separator: "|", Values: []string{"SG", "US", "CN"}}},
	})
	if !TemplateDefinitionsEqual(left, right) {
		t.Fatal("renamed placeholders with the same value spaces were not equivalent")
	}

	differentEnum := replaceTemplatePlaceholder(right, "pl1", func(placeholder *Placeholder) {
		placeholder.Enum = &EnumSpec{Separator: ",", Input: "US,CN"}
	})
	if TemplateDefinitionsEqual(left, differentEnum) {
		t.Fatal("templates with different enum value spaces were equivalent")
	}

	differentRandom := replaceTemplatePlaceholder(right, "pl2", func(placeholder *Placeholder) {
		placeholder.Random = &RandomSpec{Length: 9, CaseSensitive: true}
	})
	if TemplateDefinitionsEqual(left, differentRandom) {
		t.Fatal("templates with different random value spaces were equivalent")
	}

	differentLiteral := equivalenceTemplate(t, "socks5h://rn-zone-{pl1}-sid-{pl2}-t-5:password@us.arxlabs.io:3010", right.Placeholders)
	if TemplateDefinitionsEqual(left, differentLiteral) {
		t.Fatal("templates with different username literals were equivalent")
	}
}

func TestTemplateDefinitionsEqualUsesPlaceholderPositions(t *testing.T) {
	left := equivalenceTemplate(t, "socks5h://{first}-{second}-{first}:password@proxy.example:3010", []Placeholder{
		{Name: "first", Kind: PlaceholderEnum, Enum: &EnumSpec{Separator: ",", Input: "US,CN"}},
		{Name: "second", Kind: PlaceholderEnum, Enum: &EnumSpec{Separator: ",", Input: "basic,pro"}},
	})
	right := equivalenceTemplate(t, "socks5h://{one}-{two}-{three}:password@proxy.example:3010", []Placeholder{
		{Name: "one", Kind: PlaceholderEnum, Enum: &EnumSpec{Separator: ",", Input: "US,CN"}},
		{Name: "two", Kind: PlaceholderEnum, Enum: &EnumSpec{Separator: ",", Input: "basic,pro"}},
		{Name: "three", Kind: PlaceholderEnum, Enum: &EnumSpec{Separator: ",", Input: "US,CN"}},
	})
	if TemplateDefinitionsEqual(left, right) {
		t.Fatal("inconsistent placeholder reuse was treated as equivalent")
	}
}

func TestTemplateDefinitionsEqualNormalizesRelationGroups(t *testing.T) {
	left := equivalenceTemplate(t, "socks5h://rn-{country}-{plan}:password@proxy.example:3010", []Placeholder{
		{Name: "country", Kind: PlaceholderEnum, GroupID: "plans", Enum: &EnumSpec{Separator: ","}},
		{Name: "plan", Kind: PlaceholderEnum, GroupID: "plans", Enum: &EnumSpec{Separator: ","}},
	})
	left.ValueGroups = []ValueGroup{{
		ID: "plans", Name: "Plans", Columns: []string{"country", "plan"},
		Rows: [][]string{{"US", "basic"}, {"CN", "pro"}},
	}}
	right := equivalenceTemplate(t, "socks5h://rn-{region}-{tier}:password@proxy.example:3010", []Placeholder{
		{Name: "tier", Kind: PlaceholderEnum, GroupID: "renamed", Enum: &EnumSpec{Separator: "|"}},
		{Name: "region", Kind: PlaceholderEnum, GroupID: "renamed", Enum: &EnumSpec{Separator: "|"}},
	})
	right.ValueGroups = []ValueGroup{{
		ID: "renamed", Name: "Different label", Columns: []string{"tier", "region"},
		Rows: [][]string{{"pro", "CN"}, {"basic", "US"}},
	}}
	if !TemplateDefinitionsEqual(left, right) {
		t.Fatal("equivalent relation groups with renamed placeholders were not equivalent")
	}

	different := right
	different.ValueGroups = []ValueGroup{{
		ID: "renamed", Name: "Different label", Columns: []string{"tier", "region"},
		Rows: [][]string{{"enterprise", "CN"}, {"basic", "US"}},
	}}
	if TemplateDefinitionsEqual(left, different) {
		t.Fatal("templates with different relation rows were equivalent")
	}
}

func TestTemplateDefinitionsEqualTreatsSingleEnumAsLiteral(t *testing.T) {
	left := equivalenceTemplate(t, "socks5h://x-{country}-{session}:password@proxy.example:3010", []Placeholder{
		{Name: "country", Kind: PlaceholderEnum, Enum: &EnumSpec{Separator: ",", Input: "US"}},
		{Name: "session", Kind: PlaceholderRandom, Random: &RandomSpec{Length: 8}},
	})
	right := equivalenceTemplate(t, "socks5h://x-US-{session}:password@proxy.example:3010", []Placeholder{
		{Name: "session", Kind: PlaceholderRandom, Random: &RandomSpec{Length: 8}},
	})
	if !TemplateDefinitionsEqual(left, right) {
		t.Fatal("single-value enum was not equivalent to its literal value")
	}

	multiValue := equivalenceTemplate(t, "socks5h://x-{country}-{session}:password@proxy.example:3010", []Placeholder{
		{Name: "country", Kind: PlaceholderEnum, Enum: &EnumSpec{Separator: ",", Input: "US,CN"}},
		{Name: "session", Kind: PlaceholderRandom, Random: &RandomSpec{Length: 8}},
	})
	if TemplateDefinitionsEqual(multiValue, right) {
		t.Fatal("multi-value enum was treated as a literal")
	}
}

func TestTemplateDefinitionsEqualEquatesAlnumEnumAndRandom(t *testing.T) {
	lower := equivalenceTemplate(t, "socks5h://x-{token}:password@proxy.example:3010", []Placeholder{
		{Name: "token", Kind: PlaceholderEnum, Enum: &EnumSpec{Separator: ",", Values: testAlphabet(false)}},
	})
	randomLower := equivalenceTemplate(t, "socks5h://x-{value}:password@proxy.example:3010", []Placeholder{
		{Name: "value", Kind: PlaceholderRandom, Random: &RandomSpec{Length: 1}},
	})
	if !TemplateDefinitionsEqual(lower, randomLower) {
		t.Fatal("36-character enum was not equivalent to a length-1 case-insensitive random placeholder")
	}

	mixed := equivalenceTemplate(t, "socks5h://x-{token}:password@proxy.example:3010", []Placeholder{
		{Name: "token", Kind: PlaceholderEnum, Enum: &EnumSpec{Separator: "|", Values: testAlphabet(true)}},
	})
	randomMixed := equivalenceTemplate(t, "socks5h://x-{value}:password@proxy.example:3010", []Placeholder{
		{Name: "value", Kind: PlaceholderRandom, Random: &RandomSpec{Length: 1, CaseSensitive: true}},
	})
	if !TemplateDefinitionsEqual(mixed, randomMixed) {
		t.Fatal("62-character enum was not equivalent to a length-1 case-sensitive random placeholder")
	}

	if TemplateDefinitionsEqual(lower, randomMixed) {
		t.Fatal("36-character enum was equivalent to a case-sensitive random placeholder")
	}
	if TemplateDefinitionsEqual(mixed, randomLower) {
		t.Fatal("62-character enum was equivalent to a case-insensitive random placeholder")
	}
}

func equivalenceTemplate(t *testing.T, rawURL string, placeholders []Placeholder) Template {
	t.Helper()
	template, err := ParseTemplateURL(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	template.Placeholders = placeholders
	return template
}

func replaceTemplatePlaceholder(template Template, name string, update func(*Placeholder)) Template {
	clone := CloneTemplate(template)
	for index := range clone.Placeholders {
		if clone.Placeholders[index].Name == name {
			update(&clone.Placeholders[index])
			return clone
		}
	}
	return clone
}

func testAlphabet(caseSensitive bool) []string {
	characters := "0123456789zyxwvutsrqponmlkjihgfedcba"
	if caseSensitive {
		characters += "ZYXWVUTSRQPONMLKJIHGFEDCBA"
	}
	values := make([]string, 0, len(characters))
	for _, character := range characters {
		values = append(values, string(character))
	}
	return values
}
