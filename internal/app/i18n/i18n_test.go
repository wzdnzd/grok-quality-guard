package i18n

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

func TestNormalizeLocale(t *testing.T) {
	tests := map[string]string{
		"zh-CN":   "zh-CN",
		"zh-Hans": "zh-CN",
		"zh-TW":   "zh-TW",
		"zh-Hant": "zh-TW",
		"zh-HK":   "zh-TW",
		"en-US":   "en",
		"de-DE":   "",
	}
	for input, want := range tests {
		if got := NormalizeLocale(input); got != want {
			t.Fatalf("NormalizeLocale(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNegotiateAcceptLanguage(t *testing.T) {
	tests := map[string]string{
		"en-US,en;q=0.9,zh-CN;q=0.8": "en",
		"zh-TW;q=0.9,en;q=0.8":       "zh-TW",
		"fr-FR,zh-CN;q=0.5":          "zh-CN",
		"fr-FR,de-DE;q=0.8":          DefaultLocale,
		"":                           DefaultLocale,
	}
	for header, want := range tests {
		if got := Negotiate(header); got != want {
			t.Fatalf("Negotiate(%q) = %q, want %q", header, got, want)
		}
	}
}

func TestLocaleCatalogsHaveMatchingEnglishKeys(t *testing.T) {
	chinese := Catalog("zh-CN")
	if len(chinese) == 0 {
		t.Fatal("zh-CN catalog is empty")
	}
	cjkKey := regexp.MustCompile(`[\p{Han}]`)
	englishKey := regexp.MustCompile(`^[a-z][a-z0-9_.-]*$`)
	for _, locale := range SupportedLocales {
		catalog := Catalog(locale)
		if len(catalog) != len(chinese) {
			t.Fatalf("%s has %d keys, want %d", locale, len(catalog), len(chinese))
		}
		for key := range chinese {
			if _, ok := catalog[key]; !ok {
				t.Fatalf("%s is missing key %q", locale, key)
			}
			if cjkKey.MatchString(key) || !englishKey.MatchString(key) {
				t.Fatalf("%s contains non-English key %q", locale, key)
			}
		}
	}
}

func TestResourcesJSONContainsAllLocales(t *testing.T) {
	var resources map[string]json.RawMessage
	if err := json.Unmarshal(ResourcesJSON(), &resources); err != nil {
		t.Fatal(err)
	}
	for _, locale := range SupportedLocales {
		if len(resources[locale]) == 0 {
			t.Fatalf("missing locale %s in dashboard resources", locale)
		}
	}
}

func TestTranslateSourceCompatibility(t *testing.T) {
	if got := TranslateSource("en", "任务不存在"); got != "Task not found" {
		t.Fatalf("TranslateSource en = %q", got)
	}
	if got := TranslateSource("zh-TW", "任务不存在"); got != "任務不存在" {
		t.Fatalf("TranslateSource zh-TW = %q", got)
	}
}

func TestLocaleCatalogsHaveNoEmptyValues(t *testing.T) {
	for _, locale := range SupportedLocales {
		for key, value := range Catalog(locale) {
			if strings.TrimSpace(value) == "" {
				t.Fatalf("%s contains an empty value for %q", locale, key)
			}
		}
	}
}

func TestEnglishCatalogContainsNoHanCharacters(t *testing.T) {
	han := regexp.MustCompile(`\p{Han}`)
	for key, value := range Catalog("en") {
		if han.MatchString(key) || han.MatchString(value) {
			t.Fatalf("en catalog contains CJK text for %q: %q", key, value)
		}
	}
}

func TestLocaleCatalogsPreservePlaceholders(t *testing.T) {
	placeholder := regexp.MustCompile(`\{([a-zA-Z_][a-zA-Z0-9_]*)(?::[^}]*)?\}`)
	extract := func(value string) map[string]struct{} {
		names := make(map[string]struct{})
		for _, match := range placeholder.FindAllStringSubmatch(value, -1) {
			names[match[1]] = struct{}{}
		}
		return names
	}
	source := Catalog("zh-CN")
	for _, locale := range SupportedLocales[1:] {
		catalog := Catalog(locale)
		for key, sourceValue := range source {
			value := catalog[key]
			sourcePlaceholders := extract(sourceValue)
			valuePlaceholders := extract(value)
			if len(sourcePlaceholders) != len(valuePlaceholders) {
				t.Fatalf("%s placeholder count differs for %q: %q vs %q", locale, key, sourceValue, value)
			}
			for name := range sourcePlaceholders {
				if _, ok := valuePlaceholders[name]; !ok {
					t.Fatalf("%s is missing placeholder %q for %q: %q", locale, name, key, value)
				}
			}
		}
	}
}

func TestLocaleFilesHaveNoDuplicateKeys(t *testing.T) {
	for _, locale := range SupportedLocales {
		raw, errRead := localeFiles.ReadFile("locales/" + locale + ".json")
		if errRead != nil {
			t.Fatal(errRead)
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		token, errToken := decoder.Token()
		if errToken != nil || token != json.Delim('{') {
			t.Fatalf("%s is not a JSON object: token=%v err=%v", locale, token, errToken)
		}
		seen := make(map[string]struct{})
		for decoder.More() {
			token, errKey := decoder.Token()
			if errKey != nil {
				t.Fatal(errKey)
			}
			key, ok := token.(string)
			if !ok {
				t.Fatalf("%s contains a non-string key: %v", locale, token)
			}
			if _, exists := seen[key]; exists {
				t.Fatalf("%s contains duplicate key %q", locale, key)
			}
			seen[key] = struct{}{}
			var value json.RawMessage
			if errValue := decoder.Decode(&value); errValue != nil {
				t.Fatal(errValue)
			}
		}
	}
}
