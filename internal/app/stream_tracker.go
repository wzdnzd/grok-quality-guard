package app

import (
	"strings"
)

func (p *Plugin) trackModelStream(streamID string) {
	if p == nil || strings.TrimSpace(streamID) == "" {
		return
	}
	p.modelStreamsMu.Lock()
	p.modelStreams[streamID] = struct{}{}
	p.modelStreamsMu.Unlock()
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
	}
	p.modelStreamsMu.Unlock()
	for _, streamID := range streamIDs {
		_ = p.closeModelStream(streamID)
	}
}
