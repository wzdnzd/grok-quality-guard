package dashboard

import _ "embed"

//go:embed dashboard.html
var content string

// HTML returns the embedded management dashboard document.
func HTML() string {
	return content
}
