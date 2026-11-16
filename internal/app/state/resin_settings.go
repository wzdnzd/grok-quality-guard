package state

import "time"

// ResinSettings is the persisted runtime configuration for Resin integration.
type ResinSettings struct {
	Configured                    bool      `json:"configured"`
	Name                          string    `json:"name,omitempty"`
	Enabled                       bool      `json:"enabled"`
	LocalOnly                     bool      `json:"local_only"`
	Platform                      string    `json:"platform"`
	PlatformID                    string    `json:"platform_id"`
	AdminURL                      string    `json:"admin_url"`
	ProxyTemplate                 string    `json:"proxy_template"`
	AutoRotateAfterQualityFailure bool      `json:"auto_rotate_after_quality_failure"`
	AdminToken                    string    `json:"admin_token,omitempty"`
	ProxyToken                    string    `json:"proxy_token,omitempty"`
	ValidatedAt                   time.Time `json:"validated_at,omitempty"`
}
