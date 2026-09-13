// SPDX-License-Identifier: Apache-2.0
package relay

import (
	"bytes"
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/akenhq/aken/protocol"
)

type queue []protocol.Envelope

type liveSession struct {
	mu                  sync.Mutex
	joined              *protocol.JoinInfo
	jobs, results       queue
	nextJob, nextResult uint64
	lastJob, lastResult *protocol.Envelope
	changed             chan struct{}
	gone                bool
	expiresAt           time.Time
}

type registry struct {
	mu       sync.Mutex
	sessions map[protocol.SessionID]*liveSession
}

func (r *registry) get(id protocol.SessionID) *liveSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessions[id]
}

func (r *registry) dropLocked(id protocol.SessionID) {
	if live := r.sessions[id]; live != nil {
		live.mu.Lock()
		live.gone = true
		live.jobs, live.results = nil, nil
		live.lastJob, live.lastResult = nil, nil
		live.notify()
		live.mu.Unlock()
		delete(r.sessions, id)
	}
}

func (live *liveSession) notify() {
	close(live.changed)
	live.changed = make(chan struct{})
}

// Sweep wakes polls even when no request accesses an expired session.
func (s *Handler) Sweep() {
	s.live.mu.Lock()
	defer s.live.mu.Unlock()
	now := s.now()
	for id, live := range s.live.sessions {
		if !now.Before(live.expiresAt) {
			_ = s.store.DeleteSession(context.Background(), id)
			s.live.dropLocked(id)
		}
	}
}

func (live *liveSession) post(e protocol.Envelope, results bool, maxQueue int) (int, string) {
	live.mu.Lock()
	defer live.mu.Unlock()
	if live.gone {
		return 404, "not_found"
	}
	if live.joined == nil {
		return 409, "not_joined"
	}
	pending, next, last := &live.jobs, &live.nextJob, &live.lastJob
	if results {
		pending, next, last = &live.results, &live.nextResult, &live.lastResult
	}
	if *last != nil && e.Seq == (*last).Seq && e.Version == (*last).Version && e.SessionID == (*last).SessionID && e.Class == (*last).Class && bytes.Equal(e.Payload, (*last).Payload) {
		return 202, ""
	}
	if e.Seq != *next {
		return 409, "bad_sequence"
	}
	if len(*pending) >= maxQueue {
		return 429, "queue_full"
	}
	*pending = append(*pending, e)
	*last = &e
	*next++
	live.notify()
	return 202, ""
}

func (s *Handler) poll(w http.ResponseWriter, r *http.Request, id protocol.SessionID, live *liveSession, kind string, wait time.Duration) {
	timeout := time.NewTimer(wait)
	defer timeout.Stop()
	expiry := time.NewTimer(max(0, live.expiresAt.Sub(s.now())))
	defer expiry.Stop()
	timedOut := false
	for {
		live.mu.Lock()
		if live.gone {
			live.mu.Unlock()
			writeError(w, 404, "not_found", "not found")
			return
		}
		if !s.now().Before(live.expiresAt) {
			live.mu.Unlock()
			s.live.mu.Lock()
			// An old poll must not delete a replacement session with the same ID.
			if s.live.sessions[id] == live {
				_ = s.store.DeleteSession(r.Context(), id)
				s.live.dropLocked(id)
			}
			s.live.mu.Unlock()
			writeError(w, 404, "not_found", "not found")
			return
		}
		if kind == "join" && live.joined != nil {
			joined := *live.joined
			live.mu.Unlock()
			writeJSON(w, 200, joined)
			return
		}
		if kind != "join" {
			pending := &live.jobs
			if kind == "results" {
				pending = &live.results
			}
			if len(*pending) != 0 {
				messages := *pending
				*pending = nil
				live.notify()
				live.mu.Unlock()
				writeJSON(w, 200, protocol.Messages{Messages: messages})
				return
			}
		}
		changed := live.changed
		live.mu.Unlock()
		if timedOut || wait == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-timeout.C:
			timedOut = true
		case <-expiry.C:
		case <-changed:
		}
	}
}
