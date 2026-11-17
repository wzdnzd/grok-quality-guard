package state

import "strings"

func resinLeaseDeleteKey(adminURL, platformID, account string) string {
	return strings.TrimRight(strings.TrimSpace(adminURL), "/") + "\x00" +
		strings.TrimSpace(platformID) + "\x00" + strings.TrimSpace(account)
}
