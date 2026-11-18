package dashboard

import (
	_ "embed"
	"strings"
	"sync"

	"grok-quality-guard/internal/app/i18n"
)

//go:embed dashboard.html
var content string

var (
	htmlOnce        sync.Once
	renderedContent string
)

// HTML returns the embedded management dashboard document with locale resources injected.
func HTML() string {
	htmlOnce.Do(func() {
		placeholder := `const I18N_RESOURCE_DATA = null; // i18n-resource-data`
		resourceJSON := strings.NewReplacer(
			"</", "<\\/",
			"\u2028", "\\u2028",
			"\u2029", "\\u2029",
		).Replace(string(i18n.ResourcesJSON()))
		render := `const I18N_RESOURCE_DATA = ` + resourceJSON + `; // i18n-resource-data`
		renderedContent = strings.Replace(content, placeholder, render, 1)
	})
	return renderedContent
}
