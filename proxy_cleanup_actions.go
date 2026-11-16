package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

func resinCleanupID(job ResinLeaseDelete) string {
	sum := sha256.Sum256([]byte(resinLeaseDeleteKey(job.AdminURL, job.PlatformID, job.Account)))
	return hex.EncodeToString(sum[:])
}

func (p *Plugin) actionResinCleanup(params map[string]any, action string) (any, int, error) {
	id := strings.TrimSpace(asString(params["id"]))
	if id == "" {
		id = strings.TrimSpace(asString(params["cleanup_id"]))
	}
	if action == "list" {
		page := max(1, intFromParams(params, "page", 1))
		size := intFromParams(params, "page_size", 50)
		if size < 1 || size > 500 {
			size = 50
		}
		start := int(^uint(0) >> 1)
		if page-1 <= start/size {
			start = (page - 1) * size
		}
		status := strings.TrimSpace(asString(params["status"]))
		items := make([]map[string]any, 0, size)
		total := 0
		store := p.stateStore()
		store.mu.RLock()
		for i := len(store.state.ResinLeaseDeletes) - 1; i >= 0; i-- {
			job := store.state.ResinLeaseDeletes[i]
			jobStatus := firstNonEmptyString(job.Status, "pending")
			if job.Paused {
				jobStatus = "paused"
			}
			if status != "" && status != "all" && status != jobStatus {
				continue
			}
			if total >= start && total-start < size {
				items = append(items, map[string]any{"id": resinCleanupID(job), "auth_id": job.AuthID, "account": job.Account, "admin_url": job.AdminURL, "platform_id": job.PlatformID, "status": jobStatus, "paused": job.Paused, "attempts": job.Attempts, "next_attempt_at": job.NextAttemptAt, "last_error": job.LastError, "created_at": job.CreatedAt})
			}
			total++
		}
		store.mu.RUnlock()
		return map[string]any{"items": items, "total": total, "page": page, "page_size": size}, 200, nil
	}
	if action != "retry" && action != "pause" {
		return nil, 400, errors.New("unknown cleanup action")
	}
	count := 0
	paused := boolFromParams(params, "paused")
	err := p.stateStore().Update(func(state *State) error {
		for i := range state.ResinLeaseDeletes {
			job := &state.ResinLeaseDeletes[i]
			if id != "" && resinCleanupID(*job) != id {
				continue
			}
			if action == "pause" {
				job.Paused = paused
			} else {
				job.Status = "pending"
				job.NextAttemptAt = time.Now().UTC()
				job.LastError = ""
			}
			count++
		}
		if id != "" && count == 0 {
			return proxyError("resin_cleanup_not_found", 404, "cleanup job not found")
		}
		return nil
	})
	if err != nil {
		return nil, proxyErrorStatus(err), err
	}
	p.wakeResinLeaseDeleteLoop()
	return map[string]any{"count": count, "paused": paused}, 200, nil
}
