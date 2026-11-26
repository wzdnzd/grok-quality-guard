package dashboard

import (
	"io/fs"
	"strings"
	"testing"
)

func TestHTMLInjectsLocaleDataIntoExecutableScript(t *testing.T) {
	document := HTML()
	for _, expected := range []string{
		`const I18N_RESOURCE_DATA = {`,
		`"ui.overview":"概览"`,
		`"ui.overview":"概覽"`,
		`"ui.overview":"Overview"`,
	} {
		if !strings.Contains(document, expected) {
			t.Fatalf("dashboard is missing embedded locale data %q", expected)
		}
	}
	if strings.Contains(document, `id="i18n-resources"`) {
		t.Fatal("dashboard still depends on a separate JSON resource script")
	}
	if strings.Contains(document, i18nPlaceholder) {
		t.Fatal("dashboard locale resource placeholder was not replaced")
	}
}

func TestHTMLAssemblesSplitDashboardParts(t *testing.T) {
	document := HTML()
	for _, placeholder := range []string{cssPlaceholder, jsPlaceholder, i18nPlaceholder} {
		if strings.Contains(document, placeholder) {
			t.Fatalf("dashboard still contains unresolved placeholder %q", placeholder)
		}
	}
	for _, expected := range []string{
		`--bg: #fdfdfc;`,
		`.proxy-preview-card {`,
		`const API = '/v0/management/plugins/grok-quality-guard/api';`,
		`function renderProxySources() {`,
		`i18n.applyLocale(i18n.locale);`,
	} {
		if !strings.Contains(document, expected) {
			t.Fatalf("dashboard is missing assembled part %q", expected)
		}
	}
}

func TestDashboardManifestCoversEveryEmbeddedPart(t *testing.T) {
	manifest, err := loadDashboardManifest()
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range []struct {
		pattern string
		files   []string
	}{
		{pattern: "assets/*.css", files: manifest.CSS},
		{pattern: "js/*.js", files: manifest.JS},
	} {
		embedded, err := fs.Glob(content, group.pattern)
		if err != nil {
			t.Fatal(err)
		}
		listed := make(map[string]bool, len(group.files))
		for _, name := range group.files {
			if listed[name] {
				t.Fatalf("dashboard manifest lists %q more than once", name)
			}
			listed[name] = true
			if _, err := content.ReadFile(name); err != nil {
				t.Fatalf("dashboard manifest references missing part %q: %v", name, err)
			}
		}
		if len(embedded) != len(group.files) {
			t.Fatalf("dashboard manifest has %d files for %s, embedded has %d", len(group.files), group.pattern, len(embedded))
		}
		for _, name := range embedded {
			if !listed[name] {
				t.Fatalf("dashboard manifest does not list embedded part %q", name)
			}
		}
	}
}
