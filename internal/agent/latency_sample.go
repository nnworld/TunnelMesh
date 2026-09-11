package agent

import (
	"sort"
	"sync"
	"time"
)

const latencySampleLimit = 64

// latencySamples keeps a small rolling window so the connection pool can use
// recent P95 signals without retaining per-stream history or target labels.
type latencySamples struct {
	mu     sync.Mutex
	values []time.Duration
}

func (s *latencySamples) Record(duration time.Duration) {
	if duration < 0 {
		duration = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values = append(s.values, duration)
	if len(s.values) > latencySampleLimit {
		s.values = s.values[len(s.values)-latencySampleLimit:]
	}
}

func (s *latencySamples) P95() time.Duration {
	s.mu.Lock()
	values := append([]time.Duration(nil), s.values...)
	s.mu.Unlock()
	if len(values) == 0 {
		return 0
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	index := (len(values)*95 + 99) / 100
	if index >= len(values) {
		index = len(values) - 1
	}
	return values[index]
}
