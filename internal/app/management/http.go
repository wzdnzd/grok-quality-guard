package management

import "net/http"

func actionErrorCode(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "invalid_request"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusConflict:
		return "conflict"
	case http.StatusTooManyRequests:
		return "rate_limited"
	case http.StatusBadGateway, http.StatusServiceUnavailable:
		return "upstream_error"
	default:
		return "internal_error"
	}
}

func jsonHeaders() http.Header {
	return http.Header{
		"Content-Type":  []string{"application/json; charset=utf-8"},
		"Cache-Control": []string{"no-store"},
	}
}

func jsonHeadersFor(locale string) http.Header {
	headers := jsonHeaders()
	headers.Set("Content-Language", locale)
	headers.Add("Vary", "Accept-Language")
	return headers
}

func htmlHeadersFor(locale string) http.Header {
	headers := htmlHeaders()
	headers.Set("Content-Language", locale)
	headers.Add("Vary", "Accept-Language")
	return headers
}

func htmlHeaders() http.Header {
	return http.Header{
		"Content-Type":  []string{"text/html; charset=utf-8"},
		"Cache-Control": []string{"no-store"},
	}
}
