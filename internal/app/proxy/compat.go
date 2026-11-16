package proxy

import (
	"strings"

	statepkg "grok-quality-guard/internal/app/state"
	"grok-quality-guard/internal/config"
)

type Event = statepkg.Event
type ProxyConfig = config.ProxyConfig

func appendEvent(state *State, ev Event) {
	statepkg.AppendEvent(state, ev)
}

func resinSnapshotID(settings ResinSettings) string {
	return statepkg.ResinSnapshotID(settings)
}

func normalizeProxyConfig(cfg *ProxyConfig) error {
	return config.NormalizeProxyConfig(cfg)
}

func catalogAccountIDs(accounts []Account) []string {
	ids := make([]string, len(accounts))
	for index, account := range accounts {
		ids[index] = strings.TrimSpace(account.ID)
	}
	return ids
}
