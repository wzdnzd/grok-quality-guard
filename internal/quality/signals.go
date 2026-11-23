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
	// A reasoning stream that already delivered at least two visible chunks or
	// a short visible window has settled: evidence is emitted before the
	// answer, so waiting longer cannot change a missing-thinking verdict.
	defaultQualitySettleSpanMS = int64(40)
	defaultQualitySettleChunks = 2
	// Silence followed by one short burst is the signature of a buffered dump.
	defaultQualityBurstGapMS = int64(1500)
	// Evidence this far ahead of the first visible chunk is ordered thinking.
	defaultQualityEvidenceLeadMS = int64(50)
	// Account history is only trusted after a few samples, and a single
	// inferred miss is excused on accounts that usually carry evidence.
	defaultQualityPriorMinSamples   = int64(3)
	defaultQualityPriorEvidenceRate = 0.5
	maxQualityVisibleGaps           = 128
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
	VisibleGapMaxMS   int64
	VisibleGapMedMS   int64
	ProgressEvents    int
	Terminal          bool
	HoldExpired       bool
	SemanticOutput    bool
	Streaming         bool
	ReasoningRequired bool
	// ReasoningExplicit is true when the client itself asked for reasoning
	// instead of the capability table inferring it from the model name.
	ReasoningExplicit bool
	// CapabilityKnown marks EvidenceCapable as an intentional decision.
	// Unknown capability keeps the historical strict behavior.
	CapabilityKnown bool
	// EvidenceCapable reports whether this request path can expose reasoning
	// evidence at all.
	EvidenceCapable bool
	// EvidenceAtMS and FirstVisibleMS are measured from the attempt start and
	// order thinking against the visible answer.
	EvidenceAtMS     int64
	FirstVisibleMS   int64
	ReasoningStartMS int64
	// Account history used to excuse a single inferred miss.
	AccountEvidenceRate    float64
	AccountEvidenceSamples int64
	AccountThinkingStrikes int
	FirstTokenMS           int64
	FirstTokenKnown        bool
	DurationMS             int64
	GenerationMS           int64
	TPS                    float64
	VisibleText            string
	ChunkCount             int
	ByteCount              int64
	EventCount             int
	ParsedEvents           int
	UnknownEvents          int
	ParseFailures          int
}

type QualityVerdict struct {
	Action  string
	Class   string
	Reason  string
	Signals Signals
}
