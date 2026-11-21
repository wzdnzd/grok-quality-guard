package state

import (
	"math"
	"strings"
	"time"

	"grok-quality-guard/internal/config"
)

const (
	RoutingSourceProbe       = "probe"
	RoutingSourceTransparent = "transparent"
	RoutingSourcePassive     = "passive"
)

const routingEWMAAlpha = 0.20

// RoutingSampleWindow is the compact, time-decayed EWMA state for one evidence
// source and model class. It intentionally stores no raw sample history.
type RoutingSampleWindow struct {
	Samples        uint32  `json:"samples,omitempty"`
	SuccessEWMA    float32 `json:"success_ewma,omitempty"`
	FailureEWMA    float32 `json:"failure_ewma,omitempty"`
	LogTTFTEWMA    float32 `json:"log_ttft_ewma,omitempty"`
	LogTPSEWMA     float32 `json:"log_tps_ewma,omitempty"`
	LastObservedMS int64   `json:"last_observed_ms,omitempty"`
	LastSuccessMS  int64   `json:"last_success_ms,omitempty"`
	LastFailureMS  int64   `json:"last_failure_ms,omitempty"`
}

type RoutingSourceStats struct {
	Reasoning    RoutingSampleWindow `json:"reasoning,omitzero"`
	NonReasoning RoutingSampleWindow `json:"non_reasoning,omitzero"`
}

type RoutingStats struct {
	Probe       RoutingSourceStats `json:"probe,omitzero"`
	Transparent RoutingSourceStats `json:"transparent,omitzero"`
	Passive     RoutingSourceStats `json:"passive,omitzero"`
}

func (stats *RoutingStats) Source(source string) *RoutingSourceStats {
	if stats == nil {
		return nil
	}
	switch NormalizeRoutingSource(source) {
	case RoutingSourceProbe:
		return &stats.Probe
	case RoutingSourceTransparent:
		return &stats.Transparent
	default:
		return &stats.Passive
	}
}

func NormalizeRoutingSource(source string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case RoutingSourceProbe:
		return RoutingSourceProbe
	case RoutingSourceTransparent:
		return RoutingSourceTransparent
	case RoutingSourcePassive, "response", "stream", "external":
		return RoutingSourcePassive
	default:
		return RoutingSourcePassive
	}
}

func RoutingSampleHalfLife(_ string, healthyIntervalHours int) time.Duration {
	return config.RoutingEWMAHalfLife(healthyIntervalHours)
}

// ObserveRoutingSample updates the source-specific EWMA in real time. The old
// value decays by elapsed time and the new event then receives a fixed 20%
// contribution. This prevents frequently selected accounts from appearing
// fresher merely because they are selected more often.
func ObserveRoutingSample(
	stats *RoutingStats,
	source string,
	reasoning bool,
	success bool,
	ttftMS int64,
	tps float64,
	now time.Time,
	halfLife time.Duration,
) {
	if stats == nil {
		return
	}
	sourceStats := stats.Source(source)
	if sourceStats == nil {
		return
	}
	window := &sourceStats.NonReasoning
	if reasoning {
		window = &sourceStats.Reasoning
	}
	observeRoutingWindow(window, success, ttftMS, tps, now, halfLife)
}

func observeRoutingWindow(window *RoutingSampleWindow, success bool, ttftMS int64, tps float64, now time.Time, halfLife time.Duration) {
	if window == nil {
		return
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	nowMS := now.UnixMilli()
	decay := routingDecay(window.LastObservedMS, nowMS, halfLife)
	retain := decay * (1 - routingEWMAAlpha)
	window.SuccessEWMA = float32(float64(window.SuccessEWMA) * retain)
	window.FailureEWMA = float32(float64(window.FailureEWMA) * retain)
	if success {
		window.SuccessEWMA += routingEWMAAlpha
		window.LastSuccessMS = nowMS
	} else {
		window.FailureEWMA += routingEWMAAlpha
		window.LastFailureMS = nowMS
	}
	if ttftMS > 0 {
		value := math.Log(float64(ttftMS))
		window.LogTTFTEWMA = blendLogWindow(window.LogTTFTEWMA, window.LogTTFTEWMA == 0, value, retain)
	}
	if tps > 0 {
		value := math.Log(tps)
		window.LogTPSEWMA = blendLogWindow(window.LogTPSEWMA, window.LogTPSEWMA == 0, value, retain)
	}
	if window.Samples < math.MaxUint32 {
		window.Samples++
	}
	window.LastObservedMS = nowMS
}

func blendLogWindow(current float32, first bool, sample float64, retain float64) float32 {
	if first {
		return float32(sample)
	}
	return float32(float64(current)*retain + sample*routingEWMAAlpha)
}

func routingDecay(lastMS, nowMS int64, halfLife time.Duration) float64 {
	if lastMS <= 0 || nowMS <= lastMS || halfLife <= 0 {
		return 1
	}
	elapsed := time.Duration(nowMS-lastMS) * time.Millisecond
	return math.Exp2(-elapsed.Seconds() / halfLife.Seconds())
}
