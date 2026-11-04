package daemon

import (
	"github.com/rgolangh/agent-mission-control/internal/adapter"
	"testing"
	"time"
)

func TestCacheWarningsApplyToAnyAdapter(t *testing.T) {
	s := NewStore()
	s.SetCachePolicy(30*time.Minute, 25*time.Minute)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, name := range []string{"codex", "other"} {
		s.Apply(adapter.SourceEvent{Adapter: name, SourceID: "session", Status: adapter.StatusDone, Timestamp: now, TokenUsage: &adapter.TokenUsage{Input: 1000, CachedInput: 800, ObservedAt: now}})
	}
	if got := s.CacheWarnings(now.Add(24 * time.Minute)); len(got) != 0 {
		t.Fatalf("early warnings: %+v", got)
	}
	got := s.CacheWarnings(now.Add(25 * time.Minute))
	if len(got) != 2 || got[0].Overdue || got[1].Overdue {
		t.Fatalf("warnings: %+v", got)
	}
	if got = s.CacheWarnings(now.Add(26 * time.Minute)); len(got) != 0 {
		t.Fatalf("duplicate warnings: %+v", got)
	}
	s.Apply(adapter.SourceEvent{Adapter: "other", SourceID: "session", TokenUsage: &adapter.TokenUsage{CachedInput: 500, ObservedAt: now.Add(26 * time.Minute)}})
	got = s.CacheWarnings(now.Add(57 * time.Minute))
	if len(got) != 1 || !got[0].Overdue {
		t.Fatalf("overdue warning: %+v", got)
	}
}
