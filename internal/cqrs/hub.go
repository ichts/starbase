// Package cqrs holds the hub that wakes the render streams after the
// writer's commits.
package cqrs

import (
	"strings"
	"sync"
)

// Hub tracks open render streams and wakes them after commits.
//
// Every subscription owns a channel with capacity one. Notifications are
// non-blocking sends, so a slow stream coalesces any number of wake-ups into
// a single re-render of the latest state (frame dropping).
type Hub struct {
	mu   sync.Mutex
	subs map[*Sub]struct{}
}

type Sub struct {
	SID   string
	TabID string
	Path  string // page the stream renders, for presence counts
	C     chan struct{}
}

func NewHub() *Hub {
	return &Hub{subs: map[*Sub]struct{}{}}
}

// Subscribe registers a stream. Other streams on the same path are woken,
// so presence counts ("N watching") stay live.
func (h *Hub) Subscribe(sid, tabID, path string) *Sub {
	s := &Sub{SID: sid, TabID: tabID, Path: path, C: make(chan struct{}, 1)}
	h.mu.Lock()
	h.subs[s] = struct{}{}
	h.wakePath(path, s)
	h.mu.Unlock()
	return s
}

func (h *Hub) Unsubscribe(s *Sub) {
	h.mu.Lock()
	delete(h.subs, s)
	h.wakePath(s.Path, nil)
	h.mu.Unlock()
}

// wakePath wakes the streams on path except skip. Callers hold h.mu.
func (h *Hub) wakePath(path string, skip *Sub) {
	for o := range h.subs {
		if o != skip && o.Path == path {
			select {
			case o.C <- struct{}{}:
			default:
			}
		}
	}
}

// Count returns the number of open streams rendering path.
func (h *Hub) Count(path string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for s := range h.subs {
		if s.Path == path {
			n++
		}
	}
	return n
}

// Changed wakes the streams that the writer's committed topics name
// (commands.TopicAll, SessionTopic, TabTopic). It is the writer's OnCommit.
func (h *Hub) Changed(topics []string) {
	all := false
	sids, tabs := map[string]bool{}, map[string]bool{}
	for _, t := range topics {
		if sid, ok := strings.CutPrefix(t, "sid:"); ok {
			sids[sid] = true
		} else if tab, ok := strings.CutPrefix(t, "tab:"); ok {
			tabs[tab] = true
		} else if t == "all" {
			all = true
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		if all || sids[s.SID] || tabs[s.TabID] {
			select {
			case s.C <- struct{}{}:
			default:
			}
		}
	}
}

// Len returns the number of open streams.
func (h *Hub) Len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}
