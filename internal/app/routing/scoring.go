package routing

import (
	"math"
	"sort"
	"strconv"
	"time"

	appstate "grok-quality-guard/internal/app/state"
	"grok-quality-guard/internal/config"
)

const routingNeutralScore = 0.50

func normalizeRoutingPolicy(policy config.Policy) config.Policy {
	routing := policy.Routing
	defaults := config.DefaultRoutingConfig()
	if routing.Strategy.Selection == "" {
		routing.Strategy.Selection = defaults.Strategy.Selection
	}
	if routing.Affinity.TTL.Duration <= 0 {
		routing.Affinity.TTL = defaults.Affinity.TTL
	}
	if routing.Affinity.MaxEntries <= 0 {
		routing.Affinity.MaxEntries = defaults.Affinity.MaxEntries
	}
	if routing.Scoring.MaxAccountsPerBucket <= 0 {
		routing.Scoring.MaxAccountsPerBucket = defaults.Scoring.MaxAccountsPerBucket
	}
	if routing.Scoring.Refresh.MinInterval.Duration <= 0 {
		routing.Scoring.Refresh.MinInterval = defaults.Scoring.Refresh.MinInterval
	}
	if routing.Scoring.Refresh.MaxAge.Duration <= 0 {
		routing.Scoring.Refresh.MaxAge = defaults.Scoring.Refresh.MaxAge
	}
	policy.Routing = routing
	return policy
}

func accountRoutingScore(account Account, stats appstate.RoutingStats, policy config.Policy, now time.Time) float64 {
	policy = normalizeRoutingPolicy(policy)
	scoring := policy.Routing.Scoring
	if !scoring.Enabled {
		return routingNeutralScore
	}

	sources := []struct {
		name   string
		stats  appstate.RoutingSourceStats
		weight float64
	}{
		{name: appstate.RoutingSourceProbe, stats: stats.Probe, weight: scoring.Evidence.Probe},
		{name: appstate.RoutingSourceTransparent, stats: stats.Transparent, weight: scoring.Evidence.Transparent},
		{name: appstate.RoutingSourcePassive, stats: stats.Passive, weight: scoring.Evidence.Passive},
	}
	weightedScore := 0.0
	weightTotal := 0.0
	halfLifeHours := policy.Probe.HealthyIntervalHours
	for _, source := range sources {
		if source.weight <= 0 {
			continue
		}
		halfLife := appstate.RoutingSampleHalfLife(source.name, halfLifeHours)
		for _, window := range []appstate.RoutingSampleWindow{source.stats.Reasoning, source.stats.NonReasoning} {
			if window.Samples == 0 {
				continue
			}
			age := routingWindowAge(window, now)
			recency := 0.05
			if halfLife > 0 {
				recency = math.Max(recency, math.Exp2(-age.Seconds()/halfLife.Seconds()))
			}
			sampleConfidence := math.Sqrt(math.Min(1, float64(window.Samples)/4))
			confidence := recency * sampleConfidence
			// Old evidence fades toward neutral instead of retaining its old
			// quality forever just because it is the only remaining sample.
			windowScore := routingNeutralScore + (routingWindowScore(window, policy)-routingNeutralScore)*confidence
			weight := source.weight * confidence
			weightedScore += weight * windowScore
			weightTotal += weight
		}
	}
	if weightTotal <= 0 {
		return routingNeutralScore
	}
	return clamp01(weightedScore / weightTotal)
}

func routingLatestProbe(stats appstate.RoutingStats) time.Time {
	latestMS := int64(0)
	for _, window := range []appstate.RoutingSampleWindow{stats.Probe.Reasoning, stats.Probe.NonReasoning} {
		if window.LastObservedMS > latestMS {
			latestMS = window.LastObservedMS
		}
	}
	if latestMS <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(latestMS).UTC()
}

func routingWindowAge(window appstate.RoutingSampleWindow, now time.Time) time.Duration {
	if window.LastObservedMS <= 0 {
		return 0
	}
	observedAt := time.UnixMilli(window.LastObservedMS).UTC()
	if !now.After(observedAt) {
		return 0
	}
	return now.Sub(observedAt)
}

func routingWindowScore(window appstate.RoutingSampleWindow, policy config.Policy) float64 {
	weights := policy.Routing.Scoring.Weights
	weightTotal := weights.Success + weights.TTFT + weights.TPS
	if weightTotal <= 0 {
		return routingNeutralScore
	}

	success := routingNeutralScore
	if total := float64(window.SuccessEWMA) + float64(window.FailureEWMA); total > 0 {
		success = float64(window.SuccessEWMA) / total
	}

	ttft := routingNeutralScore
	if window.LogTTFTEWMA > 0 {
		fast := math.Max(100, float64(policy.Quality.MinGenerationMS)*0.5)
		slow := math.Max(fast+1, float64(policy.Quality.MinGenerationMS)*8)
		ttft = decreasingLogScore(math.Exp(float64(window.LogTTFTEWMA)), fast, slow)
	}

	tps := routingNeutralScore
	if window.LogTPSEWMA > 0 {
		good := math.Max(1, policy.Quality.SoftTPS)
		slow := math.Max(0.1, good/20)
		tps = increasingLogScore(math.Exp(float64(window.LogTPSEWMA)), slow, good)
	}

	return clamp01((success*weights.Success + ttft*weights.TTFT + tps*weights.TPS) / weightTotal)
}

func decreasingLogScore(value, good, bad float64) float64 {
	if value <= good {
		return 1
	}
	if value >= bad {
		return 0
	}
	return clamp01((math.Log(bad) - math.Log(value)) / (math.Log(bad) - math.Log(good)))
}

func increasingLogScore(value, bad, good float64) float64 {
	if value >= good {
		return 1
	}
	if value <= bad {
		return 0
	}
	return clamp01((math.Log(value) - math.Log(bad)) / (math.Log(good) - math.Log(bad)))
}

func clamp01(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func buildScoredBuckets(layerKey string, accounts []Account, scores map[string]float64, lastProbe map[string]time.Time, maxAccounts int) []routingSelectionBucket {
	values := append([]Account(nil), accounts...)
	sort.SliceStable(values, func(i, j int) bool {
		leftScore := scores[values[i].ID]
		rightScore := scores[values[j].ID]
		if math.Abs(leftScore-rightScore) > 1e-9 {
			return leftScore > rightScore
		}
		leftObserved := lastProbe[values[i].ID]
		rightObserved := lastProbe[values[j].ID]
		if !leftObserved.Equal(rightObserved) {
			return leftObserved.After(rightObserved)
		}
		return values[i].ID < values[j].ID
	})
	return chunkSelectionBuckets(layerKey, values, maxAccounts)
}

func buildIDBuckets(layerKey string, accounts []Account, maxAccounts int) []routingSelectionBucket {
	values := append([]Account(nil), accounts...)
	sort.Slice(values, func(i, j int) bool { return values[i].ID < values[j].ID })
	return chunkSelectionBuckets(layerKey, values, maxAccounts)
}

func chunkSelectionBuckets(layerKey string, accounts []Account, maxAccounts int) []routingSelectionBucket {
	if len(accounts) == 0 {
		return nil
	}
	if maxAccounts <= 0 || maxAccounts > len(accounts) {
		maxAccounts = len(accounts)
	}
	buckets := make([]routingSelectionBucket, 0, (len(accounts)+maxAccounts-1)/maxAccounts)
	for start := 0; start < len(accounts); start += maxAccounts {
		end := start + maxAccounts
		if end > len(accounts) {
			end = len(accounts)
		}
		buckets = append(buckets, routingSelectionBucket{
			Key:      layerKey + "#" + strconv.Itoa(len(buckets)),
			Accounts: accounts[start:end],
		})
	}
	return buckets
}

func flattenSelectionBuckets(buckets []routingSelectionBucket) []Account {
	total := 0
	for _, bucket := range buckets {
		total += len(bucket.Accounts)
	}
	accounts := make([]Account, 0, total)
	for _, bucket := range buckets {
		accounts = append(accounts, bucket.Accounts...)
	}
	return accounts
}
