package i18n

import (
	"embed"
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const DefaultLocale = "zh-CN"

var SupportedLocales = []string{"zh-CN", "zh-TW", "en"}

//go:embed locales/*.json
var localeFiles embed.FS

var (
	loadOnce  sync.Once
	catalogs  map[string]map[string]string
	resources []byte
	loadErr   error
)

func load() {
	catalogs = make(map[string]map[string]string, len(SupportedLocales))
	bundle := make(map[string]json.RawMessage, len(SupportedLocales))
	for _, locale := range SupportedLocales {
		raw, err := localeFiles.ReadFile("locales/" + locale + ".json")
		if err != nil {
			if loadErr == nil {
				loadErr = err
			}
			continue
		}
		var catalog map[string]string
		if err := json.Unmarshal(raw, &catalog); err != nil {
			if loadErr == nil {
				loadErr = err
			}
			continue
		}
		catalogs[locale] = catalog
		bundle[locale] = json.RawMessage(raw)
	}
	resources, loadErr = json.Marshal(bundle)
}

// NormalizeLocale maps browser or API language tags to one of the supported
// locales. Traditional Chinese variants are mapped to zh-TW; every other
// Chinese variant maps to Simplified Chinese.
func NormalizeLocale(value string) string {
	tag := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(value, "_", "-")))
	if tag == "" {
		return ""
	}
	if strings.HasPrefix(tag, "zh-hant") || strings.HasPrefix(tag, "zh-tw") || strings.HasPrefix(tag, "zh-hk") || strings.HasPrefix(tag, "zh-mo") {
		return "zh-TW"
	}
	if strings.HasPrefix(tag, "zh") {
		return "zh-CN"
	}
	if strings.HasPrefix(tag, "en") {
		return "en"
	}
	return ""
}

// Negotiate selects the best supported locale from an Accept-Language value.
func Negotiate(header string) string {
	type preference struct {
		tag   string
		q     float64
		order int
	}
	preferences := make([]preference, 0, 8)
	for index, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		tag, quality := part, 1.0
		if separator := strings.IndexByte(part, ';'); separator >= 0 {
			tag = strings.TrimSpace(part[:separator])
			for _, parameter := range strings.Split(part[separator+1:], ";") {
				keyValue := strings.SplitN(strings.TrimSpace(parameter), "=", 2)
				if len(keyValue) == 2 && strings.EqualFold(strings.TrimSpace(keyValue[0]), "q") {
					if parsed, err := strconv.ParseFloat(strings.TrimSpace(keyValue[1]), 64); err == nil {
						quality = parsed
					}
				}
			}
		}
		if quality <= 0 || tag == "*" {
			continue
		}
		preferences = append(preferences, preference{tag: tag, q: quality, order: index})
	}
	sort.SliceStable(preferences, func(i, j int) bool {
		if preferences[i].q == preferences[j].q {
			return preferences[i].order < preferences[j].order
		}
		return preferences[i].q > preferences[j].q
	})
	for _, preference := range preferences {
		if locale := NormalizeLocale(preference.tag); locale != "" {
			return locale
		}
	}
	return DefaultLocale
}

// Catalog returns the loaded resource catalog for a locale.
func Catalog(locale string) map[string]string {
	loadOnce.Do(load)
	normalized := NormalizeLocale(locale)
	if normalized == "" {
		normalized = DefaultLocale
	}
	return catalogs[normalized]
}

// T translates a key and performs simple named-placeholder interpolation.
func T(locale, key string, values map[string]any) string {
	catalog := Catalog(locale)
	message, ok := catalog[key]
	if !ok {
		return key
	}
	message = formatPlural(message, values)
	for name, value := range values {
		message = strings.ReplaceAll(message, "{"+name+"}", formatValue(value))
	}
	return message
}

// Has reports whether a key exists in the locale catalog.
func Has(locale, key string) bool {
	_, ok := Catalog(locale)[key]
	return ok
}

// ResourcesJSON returns the locale bundle used by the embedded dashboard.
func ResourcesJSON() []byte {
	loadOnce.Do(load)
	return append([]byte(nil), resources...)
}

// LoadError reports an embedded locale loading error, if any.
func LoadError() error {
	loadOnce.Do(load)
	return loadErr
}

func formatValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	default:
		raw, err := json.Marshal(typed)
		if err == nil && (strings.HasPrefix(string(raw), "{") || strings.HasPrefix(string(raw), "[")) {
			return string(raw)
		}
		return strings.Trim(string(raw), "\"")
	}
}

func formatPlural(template string, values map[string]any) string {
	const prefix = "{count:plural|"
	for {
		start := strings.Index(template, prefix)
		if start < 0 {
			return template
		}
		end := strings.IndexByte(template[start:], '}')
		if end < 0 {
			return template
		}
		end += start
		rules := template[start+len(prefix) : end]
		count := int64(0)
		if value, ok := values["count"]; ok {
			switch typed := value.(type) {
			case int:
				count = int64(typed)
			case int64:
				count = typed
			case float64:
				count = int64(typed)
			case json.Number:
				count, _ = typed.Int64()
			}
		}
		category := "other"
		if count == 1 {
			category = "one"
		}
		selected := ""
		for _, branch := range strings.Split(rules, "|") {
			keyValue := strings.SplitN(branch, "=", 2)
			if len(keyValue) != 2 {
				continue
			}
			if strings.TrimSpace(keyValue[0]) == category {
				selected = keyValue[1]
				break
			}
			if strings.TrimSpace(keyValue[0]) == "other" {
				selected = keyValue[1]
			}
		}
		selected = strings.ReplaceAll(selected, "#", formatValue(count))
		template = template[:start] + selected + template[end+1:]
	}
}

var (
	sourceOnce  sync.Once
	sourceExact map[string]string
	sourceRules []sourceRule
)

type sourceRule struct {
	regex *regexp.Regexp
	names []string
	key   string
}

func compileSourceIndex() {
	sourceExact = make(map[string]string)
	sourceRules = nil
	placeholder := regexp.MustCompile(`\{(\w+)\}`)
	for key, value := range Catalog("zh-CN") {
		source := strings.TrimSpace(value)
		if source == "" || strings.Contains(source, ":plural") {
			continue
		}
		if !strings.Contains(source, "{") {
			if _, exists := sourceExact[source]; !exists {
				sourceExact[source] = key
			}
			continue
		}
		names := make([]string, 0, 4)
		var pattern strings.Builder
		pattern.WriteString("^")
		cursor := 0
		matches := placeholder.FindAllStringSubmatchIndex(source, -1)
		for _, match := range matches {
			pattern.WriteString(regexp.QuoteMeta(source[cursor:match[0]]))
			pattern.WriteString("(.+?)")
			names = append(names, source[match[2]:match[3]])
			cursor = match[1]
		}
		pattern.WriteString(regexp.QuoteMeta(source[cursor:]))
		pattern.WriteString("$")
		sourceRules = append(sourceRules, sourceRule{regex: regexp.MustCompile(pattern.String()), names: names, key: key})
	}
	sort.SliceStable(sourceRules, func(i, j int) bool {
		return len(sourceRules[i].regex.String()) > len(sourceRules[j].regex.String())
	})
}

// TranslateSource is a compatibility bridge for persisted or externally
// supplied Simplified Chinese values. New code should use semantic keys.
func TranslateSource(locale, text string) string {
	normalized := NormalizeLocale(locale)
	if normalized == "" || normalized == DefaultLocale || strings.TrimSpace(text) == "" {
		return text
	}
	sourceOnce.Do(compileSourceIndex)
	trimmed := strings.TrimSpace(text)
	if key, ok := sourceExact[trimmed]; ok {
		leading := text[:len(text)-len(strings.TrimLeft(text, " \t\r\n"))]
		trailing := text[len(strings.TrimRight(text, " \t\r\n")):]
		return leading + T(normalized, key, nil) + trailing
	}
	for _, rule := range sourceRules {
		match := rule.regex.FindStringSubmatch(trimmed)
		if match == nil {
			continue
		}
		values := make(map[string]any, len(rule.names))
		for index, name := range rule.names {
			values[name] = match[index+1]
		}
		return T(normalized, rule.key, values)
	}
	return text
}
