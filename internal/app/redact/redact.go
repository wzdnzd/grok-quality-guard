package redact

import (
	"regexp"
	"strings"
)

var (
	userInfoPattern   = regexp.MustCompile(`(?i)\b(https?|socks5h?)://[^/\s@]+@`)
	bearerPattern     = regexp.MustCompile(`(?i)\b(bearer\s+)[A-Za-z0-9._~+/=-]+`)
	headerPattern     = regexp.MustCompile(`(?i)\b(authorization|cookie|set-cookie)\s*:\s*[^\r\n]+`)
	jsonTokenPattern  = regexp.MustCompile(`(?i)("(?:access_token|refresh_token|id_token|admin_token|proxy_token|password|secret)"\s*:\s*")[^"]*(")`)
	queryTokenPattern = regexp.MustCompile(`(?i)([?&](?:token|key|secret|password|api_key)=)[^&\s]+`)
)

// SanitizeError returns bounded text with credentials removed.
func SanitizeError(errValue error) string {
	if errValue == nil {
		return ""
	}
	text := strings.TrimSpace(errValue.Error())
	text = userInfoPattern.ReplaceAllString(text, "${1}://")
	text = bearerPattern.ReplaceAllString(text, "${1}[redacted]")
	text = headerPattern.ReplaceAllString(text, "${1}: [redacted]")
	text = jsonTokenPattern.ReplaceAllString(text, "${1}[redacted]${2}")
	text = queryTokenPattern.ReplaceAllString(text, "${1}[redacted]")
	if len(text) > 512 {
		text = text[:512]
	}
	return text
}
