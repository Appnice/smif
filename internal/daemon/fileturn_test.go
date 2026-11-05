package daemon

import (
	"github.com/appnice/smif/internal/api"
	"strings"
	"testing"
	"time"
)

func TestEnterDelayGrowsWithText(t *testing.T) {
	short, long := enterDelay("jak tam raport z dzisiaj"), enterDelay(strings.Repeat("x", 92))
	if short < 500*time.Millisecond || long <= short || enterDelay(strings.Repeat("x", 5000)) != 2*time.Second {
		t.Errorf("enterDelay: short %v, long %v", short, long)
	}
	if delayMS(strings.Repeat("x", 92)) != "1236" {
		t.Errorf("delayMS = %s", delayMS(strings.Repeat("x", 92)))
	}
}

func TestPromptStateBackground(t *testing.T) {
	var s promptState
	before := time.Now().Add(-time.Minute)
	s.start("a")
	v := api.SessionView{SessionID: "a", Turn: "idle"}
	s.apply(&v, before)
	if v.Turn != "busy" {
		t.Fatalf("while typing: turn %q", v.Turn)
	}
	s.fail("a", "no turn")
	v = api.SessionView{SessionID: "a", Turn: "idle"}
	s.apply(&v, before)
	if v.Turn != "idle" || v.Unsent != "no turn" {
		t.Fatalf("after fail: %+v", v)
	}
	v = api.SessionView{SessionID: "a", Turn: "busy"}
	s.apply(&v, time.Now().Add(time.Second)) // a newer turn clears it
	if v.Unsent != "" {
		t.Fatalf("newer turn kept Unsent %q", v.Unsent)
	}
}
