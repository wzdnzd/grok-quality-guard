package app

import (
	"context"
	"strings"
	"sync"
	"time"
)

const (
	modelStreamMaxAge       = 2 * time.Hour
	modelStreamSweepPeriod  = 5 * time.Minute
	modelStreamCloseWorkers = 16
)

func (p *Plugin) trackModelStream(streamID string) {
	if p == nil || strings.TrimSpace(streamID) == "" {
		return
	}
	p.mu.RLock()
	if p.quiescing {
		p.mu.RUnlock()
		_ = p.closeModelStreamNow(streamID)
		return
	}
	p.modelStreamsMu.Lock()
	if p.modelStreams == nil {
		p.modelStreams = make(map[string]time.Time)
	}
	p.modelStreams[streamID] = time.Now()
	p.modelStreamsMu.Unlock()
	p.mu.RUnlock()
}

func (p *Plugin) untrackModelStream(streamID string) {
	if p == nil || strings.TrimSpace(streamID) == "" {
		return
	}
	p.modelStreamsMu.Lock()
	delete(p.modelStreams, streamID)
	p.modelStreamsMu.Unlock()
}

func (p *Plugin) closeAllModelStreams() {
	if p == nil {
		return
	}
	p.modelStreamsMu.Lock()
	streamIDs := make([]string, 0, len(p.modelStreams))
	for streamID := range p.modelStreams {
		streamIDs = append(streamIDs, streamID)
		delete(p.modelStreams, streamID)
	}
	p.modelStreamsMu.Unlock()
	p.closeModelStreamsNow(streamIDs)
}

// runModelStreamCleanupLoop closes streams whose close callback never reached
// the plugin. Without this guard a dropped streaming response would leave its
// stream ID resident for the lifetime of the process.
func (p *Plugin) runModelStreamCleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(modelStreamSweepPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			p.sweepStaleModelStreams(now)
		}
	}
}

func (p *Plugin) sweepStaleModelStreams(now time.Time) {
	if p == nil {
		return
	}
	cutoff := now.Add(-modelStreamMaxAge)
	p.modelStreamsMu.Lock()
	streamIDs := make([]string, 0)
	for streamID, trackedAt := range p.modelStreams {
		if !trackedAt.After(cutoff) {
			streamIDs = append(streamIDs, streamID)
			delete(p.modelStreams, streamID)
		}
	}
	p.modelStreamsMu.Unlock()
	p.closeModelStreamsNow(streamIDs)
}

func (p *Plugin) closeModelStreamsNow(streamIDs []string) {
	if p == nil || len(streamIDs) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), hostCallGrace)
	defer cancel()
	workers := min(modelStreamCloseWorkers, len(streamIDs))
	jobs := make(chan string)
	var wait sync.WaitGroup
	wait.Add(workers)
	for worker := 0; worker < workers; worker++ {
		go func() {
			defer wait.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case streamID, ok := <-jobs:
					if !ok {
						return
					}
					_ = p.closeModelStreamNowWithContext(ctx, streamID)
				}
			}
		}()
	}
sendJobs:
	for _, streamID := range streamIDs {
		select {
		case jobs <- streamID:
		case <-ctx.Done():
			break sendJobs
		}
	}
	close(jobs)
	wait.Wait()
}
