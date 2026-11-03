package scheduler

import (
	"testing"
	"time"
)

func TestOrderPriorityThenAge(t *testing.T) {
	now := time.Now()
	items := []Item{
		{ID: "old-low", Priority: 0, CreatedAt: now.Add(-10 * time.Minute)},
		{ID: "new-high", Priority: 2, CreatedAt: now},
		{ID: "old-high", Priority: 2, CreatedAt: now.Add(-time.Minute)},
		{ID: "urgent", Priority: 3, CreatedAt: now},
	}
	Order(items, now, 30*time.Minute)
	want := []string{"urgent", "old-high", "new-high", "old-low"}
	for i, w := range want {
		if items[i].ID != w {
			t.Fatalf("pos %d = %s, want %s (%v)", i, items[i].ID, w, items)
		}
	}
}

func TestAgingPreventsStarvation(t *testing.T) {
	now := time.Now()
	starving := Item{ID: "low", Priority: 0, CreatedAt: now.Add(-61 * time.Minute)}
	if p := Effective(starving, now, 30*time.Minute); p != 2 {
		t.Fatalf("effective = %d, want 2 after an hour", p)
	}
	if p := Effective(Item{Priority: 1, CreatedAt: now.Add(-10 * time.Hour)}, now, 30*time.Minute); p != 3 {
		t.Fatalf("aging must cap at urgent, got %d", p)
	}
	items := []Item{{ID: "fresh-normal", Priority: 1, CreatedAt: now}, starving}
	Order(items, now, 30*time.Minute)
	if items[0].ID != "low" {
		t.Fatal("aged low item should outrank a fresh normal one")
	}
	if Effective(starving, now, 0) != 0 {
		t.Fatal("no aging when step is 0")
	}
}

func TestSlots(t *testing.T) {
	s := NewSlots(2, 1)
	a1 := Item{ID: "1", Agent: "a", Handler: "worker"}
	a2 := Item{ID: "2", Agent: "a", Handler: "worker"}
	b := Item{ID: "3", Agent: "b", Handler: "worker"}
	c := Item{ID: "4", Agent: "c", Handler: "worker"}
	cmd := Item{ID: "5", Agent: "a", Handler: "command"}
	if !s.CanStart(a1) {
		t.Fatal()
	}
	s.Take(a1, "r1")
	if s.CanStart(a2) {
		t.Fatal("second worker for the same agent must wait (R-CON-1)")
	}
	if !s.AgentBusy("a") || s.AgentBusy("b") {
		t.Fatal("busy flags")
	}
	s.Take(b, "r2")
	if s.CanStart(c) {
		t.Fatal("global cap exceeded")
	}
	if !s.CanStart(cmd) {
		t.Fatal("command pool is separate")
	}
	s.Take(cmd, "r3")
	if s.CanStart(cmd) {
		t.Fatal("command pool cap")
	}
	if w, c := s.Counts(); w != 2 || c != 1 {
		t.Fatalf("counts %d %d", w, c)
	}
	s.Release("worker", "a")
	s.Release("command", "a")
	s.Release("command", "a") // extra release is harmless
	s.Release("worker", "zzz")
	if !s.CanStart(a2) || !s.CanStart(cmd) {
		t.Fatal("release")
	}
}
