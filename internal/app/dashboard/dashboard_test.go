package dashboard

import (
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
	if strings.Contains(document, `const I18N_RESOURCE_DATA = null; // i18n-resource-data`) {
		t.Fatal("dashboard locale resource placeholder was not replaced")
	}
}
