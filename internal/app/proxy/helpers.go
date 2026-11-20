package proxy

import (
	"encoding/json"
	"strings"
	"time"
)

const (
	catalogRefreshTTL             = 30 * time.Second
	proxyObservationRetention     = 5 * time.Minute
	proxyObservationPruneInterval = time.Minute
	maxProxyObservations          = 20000
)

func proxyObservationTTL(account Account) time.Duration {
	if strings.TrimSpace(account.Path) == "" {
		// Runtime-only auths have no host file signature to invalidate their
		// proxy observation. Retention is their only cheap consistency window;
		// visible-page callers can confirm them again when it expires.
		return proxyObservationRetention
	}
	return catalogRefreshTTL
}

func proxyObservationFreshAt(account Account, observation proxyObservation, now time.Time) bool {
	if observation.CheckedAt.IsZero() || now.Sub(observation.CheckedAt) >= proxyObservationTTL(account) {
		return false
	}
	return account.ModTime.IsZero() || !account.ModTime.After(observation.CheckedAt)
}

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
