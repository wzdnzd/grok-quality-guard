package state

import "strings"

// ResinLeaseDeleteKey is the durable identity of a remote Resin lease cleanup.
func ResinLeaseDeleteKey(adminURL, platformID, account string) string {
	return strings.TrimRight(strings.TrimSpace(adminURL), "/") + "\x00" +
		strings.TrimSpace(platformID) + "\x00" + strings.TrimSpace(account)
}

func resinLeaseDeleteKey(adminURL, platformID, account string) string {
	return ResinLeaseDeleteKey(adminURL, platformID, account)
}
