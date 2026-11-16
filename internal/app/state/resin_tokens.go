package state

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

func migrateResinAdminTokens(state *State) {
	if state == nil {
		return
	}
	if state.ResinAdminTokens == nil {
		state.ResinAdminTokens = make(map[string]string)
	}
	for index := range state.ResinLeaseDeletes {
		job := &state.ResinLeaseDeletes[index]
		if strings.TrimSpace(job.AdminToken) == "" {
			continue
		}
		sum := sha256.Sum256([]byte(job.AdminToken))
		ref := hex.EncodeToString(sum[:])
		state.ResinAdminTokens[ref] = job.AdminToken
		job.AdminTokenRef = ref
		job.AdminToken = ""
	}
}

func gcResinAdminTokens(state *State) {
	if state == nil || len(state.ResinAdminTokens) == 0 {
		return
	}
	used := make(map[string]struct{}, len(state.ResinLeaseDeletes))
	for _, job := range state.ResinLeaseDeletes {
		if job.AdminTokenRef != "" {
			used[job.AdminTokenRef] = struct{}{}
		}
	}
	for ref := range state.ResinAdminTokens {
		if _, ok := used[ref]; !ok {
			delete(state.ResinAdminTokens, ref)
		}
	}
}
