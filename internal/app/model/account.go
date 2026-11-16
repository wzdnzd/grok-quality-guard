package model

import "time"

// Account is the catalog projection used by routing, management and proxy
// binding. It deliberately contains no host-only secrets.
type Account struct {
	ID            string
	AuthIndex     string
	Type          string
	Provider      string
	Label         string
	Email         string
	Disabled      bool
	Unavailable   bool
	Priority      int
	Path          string
	Size          int64
	ModTime       time.Time
	ProxyURL      string
	ProxyURLKnown bool
	ProxyHash     string
}
