package dashboard

import (
	"embed"
	"encoding/json"
	"strings"
	"sync"

	"grok-quality-guard/internal/app/i18n"
)

//go:embed dashboard.html parts.json assets/*.css js/*.js
var content embed.FS

const (
	cssPlaceholder  = "/* __DASHBOARD_CSS__ */"
	jsPlaceholder   = "// __DASHBOARD_JS__"
	i18nPlaceholder = `const I18N_RESOURCE_DATA = null; // i18n-resource-data`
)

type dashboardManifest struct {
	CSS []string `json:"css"`
	JS  []string `json:"js"`
}

var (
	htmlOnce        sync.Once
	renderedContent string
)

// HTML returns the embedded management dashboard document with locale resources injected.
func HTML() string {
	htmlOnce.Do(func() {
		document, err := content.ReadFile("dashboard.html")
		if err != nil {
			panic(err)
		}
		manifest, err := loadDashboardManifest()
		if err != nil {
			panic(err)
		}
		rendered := string(document)
		rendered = replaceDashboardPlaceholder(rendered, cssPlaceholder, dashboardParts(manifest.CSS))
		rendered = replaceDashboardPlaceholder(rendered, jsPlaceholder, dashboardParts(manifest.JS))
		resourceJSON := strings.NewReplacer(
			"</", "<\\/",
			"\u2028", "\\u2028",
			"\u2029", "\\u2029",
		).Replace(string(i18n.ResourcesJSON()))
		rendered = replaceDashboardPlaceholder(rendered, i18nPlaceholder, `const I18N_RESOURCE_DATA = `+resourceJSON+`; // i18n-resource-data`)
		renderedContent = rendered
	})
	return renderedContent
}

func loadDashboardManifest() (dashboardManifest, error) {
	data, err := content.ReadFile("parts.json")
	if err != nil {
		return dashboardManifest{}, err
	}
	var manifest dashboardManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return dashboardManifest{}, err
	}
	if len(manifest.CSS) == 0 || len(manifest.JS) == 0 {
		panic("dashboard manifest must list CSS and JS parts")
	}
	return manifest, nil
}

func dashboardParts(files []string) string {
	parts := make([]string, 0, len(files))
	for _, name := range files {
		data, err := content.ReadFile(name)
		if err != nil {
			panic(err)
		}
		parts = append(parts, string(data))
	}
	return strings.Join(parts, "\n")
}

func replaceDashboardPlaceholder(document, placeholder, replacement string) string {
	if !strings.Contains(document, placeholder) {
		panic("dashboard placeholder not found: " + placeholder)
	}
	return strings.Replace(document, placeholder, replacement, 1)
}
