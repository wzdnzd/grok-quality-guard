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
		placeholder := `<script type="application/json" id="i18n-resources">{}</script>`
		replacement := `<script type="application/json" id="i18n-resources">` + string(i18n.ResourcesJSON()) + `</script>`
		renderedContent = strings.Replace(content, placeholder, replacement, 1)
	})
	return renderedContent
}
