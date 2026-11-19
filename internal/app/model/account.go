package model

import "time"

// Account is the catalog projection used by routing, management and proxy
// binding. It deliberately contains no host-only secrets.
type Account struct {
	ID          string
	OAuthID     string // OAuth subject/sub claim, when exposed by the auth file.
	AuthIndex   string
	Type        string
	Provider    string
	Label       string
	Email       string
	Disabled    bool
	Unavailable bool
	Priority    int
	Path        string
	Size        int64
	ModTime     time.Time
	// FirstSeenAt is runtime-only. It preserves unknown-probe delay/jitter
	// across catalog refreshes without persisting a default AccountState for
	// every account.
	FirstSeenAt time.Time
	// ProxyURLKnown records that the backing auth file was inspected, even
	// when it contains no proxy URL. Proxy credentials are never retained in
	// the catalog; the masked value and hash are enough for list and binding
	// decisions.
	ProxyURLKnown bool
	ProxyHash     string
	ProxyMasked   string
}
