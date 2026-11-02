package daemon

import "time"

type CacheWarning struct {
	WorkflowID string
	Overdue    bool
}

func (s *Store) SetCachePolicy(lifetime, warningAfter time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cacheLifetime, s.cacheWarningAfter = lifetime, warningAfter
}

func (s *Store) CacheWarnings(now time.Time) []CacheWarning {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cacheWarningAfter <= 0 {
		return nil
	}
	var warnings []CacheWarning
	for _, w := range s.m {
		u := w.TokenUsage
		if w.Stale || u == nil || u.CachedInput <= 0 || u.ObservedAt.IsZero() || w.cacheWarned || now.Sub(u.ObservedAt) < s.cacheWarningAfter {
			continue
		}
		w.cacheWarned = true
		warnings = append(warnings, CacheWarning{WorkflowID: w.ID, Overdue: now.Sub(u.ObservedAt) >= s.cacheLifetime})
	}
	return warnings
}
