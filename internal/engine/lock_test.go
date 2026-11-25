package engine

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// A job that has to wait for a site says what it is waiting for, instead of
// leaving its log empty (the UI then shows "Starting…" with no end).
func TestWaitSiteSaysWhatHoldsTheSite(t *testing.T) {
	e := testEngine(t)
	unlock := e.lockSite("demo")
	var mu sync.Mutex
	var lines []string
	logf := func(f string, a ...any) { mu.Lock(); lines = append(lines, fmt.Sprintf(f, a...)); mu.Unlock() }
	got := make(chan struct{})
	go func() {
		u := e.waitSite("demo", logf)
		u()
		close(got)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(lines)
		mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case <-got:
		t.Fatal("waitSite took a held lock")
	default:
	}
	unlock()
	<-got
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "Waiting for ") {
		t.Fatalf("log = %q", lines)
	}
	if !strings.Contains(lines[0], "TestWaitSiteSaysWhatHoldsTheSite") {
		t.Fatalf("holder not named: %q", lines[0])
	}
	// Free again: no waiting line.
	lines = nil
	e.waitSite("demo", logf)()
	if len(lines) != 0 {
		t.Fatalf("log = %q", lines)
	}
}
