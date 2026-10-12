package cqrs_test

import (
	"testing"

	"starbase/internal/cqrs"
)

func woken(s *cqrs.Sub) bool {
	select {
	case <-s.C:
		return true
	default:
		return false
	}
}

func TestChangedWakesByTopic(t *testing.T) {
	hub := cqrs.NewHub()
	mine := hub.Subscribe("s1", "t1", "/")
	other := hub.Subscribe("s2", "t2", "/other")
	hub.Changed([]string{"sid:s1"})
	if !woken(mine) || woken(other) {
		t.Fatal("a session's topic must wake that session's streams only")
	}
	hub.Changed([]string{"tab:t2"})
	if woken(mine) || !woken(other) {
		t.Fatal("a tab's topic must wake that tab's stream only")
	}
	hub.Changed([]string{"all"})
	if !woken(mine) || !woken(other) {
		t.Fatal("all must wake every stream")
	}
	hub.Changed(nil)
	if woken(mine) || woken(other) {
		t.Fatal("a commit that touched nothing woke a stream")
	}
}

func TestChangedCoalesces(t *testing.T) {
	hub := cqrs.NewHub()
	s := hub.Subscribe("s", "t", "/")
	for range 10 {
		hub.Changed([]string{"all"})
	}
	<-s.C
	if woken(s) {
		t.Fatal("expected a single pending wake-up")
	}
}

func TestPresence(t *testing.T) {
	hub := cqrs.NewHub()
	a := hub.Subscribe("s1", "t1", "/showcase")
	if hub.Count("/showcase") != 1 || hub.Count("/") != 0 {
		t.Fatal("counts")
	}
	b := hub.Subscribe("s2", "t2", "/showcase")
	if !woken(a) {
		t.Fatal("existing viewer not woken on join")
	}
	hub.Unsubscribe(b)
	if !woken(a) {
		t.Fatal("existing viewer not woken on leave")
	}
	if hub.Count("/showcase") != 1 {
		t.Fatal("count after leave")
	}
}
