package state

const (
	StateUnknown            = "unknown"
	StateHealthy            = "healthy"
	StateSuspect            = "suspect"
	StateDegraded           = "degraded"
	StateCooling            = "cooling"
	StateQuotaExhausted     = "quota_exhausted"
	StateCredentialInvalid  = "credential_invalid"
	StateDisabledByGuard    = "disabled_by_guard"
	StateExternallyDisabled = "externally_disabled"
)

const (
	DisableKindCooldown          = "cooldown"
	DisableKindQuota             = "quota_exhausted"
	DisableKindCredentialInvalid = "credential_invalid"
	DisableKindPermanent         = "permanent"
)

const (
	stateSchemaVersion = 2
	resinSourceID      = "resin"
)
