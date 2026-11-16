package proxy

import (
	"encoding/json"
	"strings"
	"time"
)

const catalogRefreshTTL = 30 * time.Second

func catalogAccountMatches(account Account, query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return true
	}
	haystack := strings.ToLower(strings.Join([]string{
		account.ID,
		account.AuthIndex,
		account.Label,
		account.Email,
		account.Provider,
	}, " "))
	return strings.Contains(haystack, query)
}

func marshalRawMap(value map[string]json.RawMessage) (json.RawMessage, error) {
	raw, err := json.Marshal(value)
	return raw, err
}
