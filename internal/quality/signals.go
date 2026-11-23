package quality

const (
	ClassBufferedBurst             = "buffered_burst"
	ClassCipherDrool               = "cipher_drool"
	ClassFakeEncryptedDump         = "fake_encrypted_dump"
	ClassFastReasoningRatio        = "fast_reasoning_ratio_dump"
	ClassMissingThinking           = "missing_thinking"
	ClassHardTPS                   = "hard_tps"
	ClassSoftTPS                   = "soft_tps"
	ClassEmptyStream               = "empty_stream"
	ClassProbeError                = "probe_error"
	ClassProxyError                = "proxy_error"
	ClassCredentialInvalid         = "credential_invalid"
	ClassQuotaExhausted            = "quota_exhausted"
	ClassRateLimited               = "rate_limited"
	ClassIgnored                   = "ignored"
	ClassInsufficientOutput        = "insufficient_output_tokens"
	ClassInsufficientGeneration    = "insufficient_generation_window"
	ClassExpectedMarkerMissing     = "expected_marker_missing"
	ClassNone                      = ""
	QualityActionWait              = "wait"
	QualityActionDeliver           = "deliver"
	QualityActionWithhold          = "withhold"
	qualityProtocolChat            = "chat"
	qualityProtocolResponses       = "responses"
	qualityProtocolAnthropic       = "anthropic"
	qualityReasoningSSEComment     = ": grok2api-reasoning-start"
	qualityReasoningEvidencePrefix = ": grok2api-reasoning-evidence"
	defaultQualityMinOutput        = int64(8)
	defaultQualityMinGenerationMS  = int64(1000)
	defaultQualityMaxVisible       = int64(32)
	defaultQualityMinReasoning     = int64(80)
	defaultQualityFastFlushMS      = int64(1000)
	defaultQualityFakeFlushMS      = int64(2000)
	defaultQualityCipherVisible    = int64(1024)
	defaultQualityMaxVisibleTPS    = int64(1000)
	defaultEncryptedBytes          = int64(256)
	defaultEncryptedBytesPerToken  = int64(4)
	thinkingGuardMinOutputTokens   = int64(64)
)

type Signals struct {
	Protocol          string
	HasThinking       bool
	HasReasoningDelta bool
	ReasoningStarted  bool
	VisibleTokens     int64
	VisibleRunes      int64
	ReasoningTokens   int64
	OutputTokens      int64
	EncryptedBytes    int64
	EncryptedFloor    int64
	UsageReported     bool
	FirstVisible      bool
	VisibleFlushMS    int64
	VisibleSpanMS     int64
	VisibleChunks     int
	ProgressEvents    int
	Terminal          bool
	HoldExpired       bool
	SemanticOutput    bool
	Streaming         bool
	ReasoningRequired bool
	FirstTokenMS      int64
	FirstTokenKnown   bool
	DurationMS        int64
	GenerationMS      int64
	TPS               float64
	VisibleText       string
	ChunkCount        int
	ByteCount         int64
	EventCount        int
	ParsedEvents      int
	UnknownEvents     int
	ParseFailures     int
}

type QualityVerdict struct {
	Action  string
	Class   string
	Reason  string
	Signals Signals
}
