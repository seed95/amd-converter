package main

import (
	"context"
	"log"
	"sync"
	"time"
)

const failedRetry = 30 * time.Second

type entry struct {
	quote    Quote
	hasGood  bool
	fetched  time.Time
	failedAt time.Time
}

// Store caches each source independently. A source that fails keeps serving
// its last good value (marked stale) and is retried after failedRetry, so one
// flaky site never takes the whole app down.
type Store struct {
	mu      sync.Mutex
	ttl     time.Duration
	sources []Source
	entries map[string]*entry
	now     func() time.Time
}

func NewStore(sources []Source, ttl time.Duration) *Store {
	st := &Store{ttl: ttl, sources: sources, entries: map[string]*entry{}, now: time.Now}
	for _, s := range sources {
		st.entries[s.ID] = &entry{}
	}
	return st
}

func (st *Store) due(e *entry) bool {
	now := st.now()
	if !e.failedAt.IsZero() && now.Sub(e.failedAt) < failedRetry {
		return false
	}
	return !e.hasGood || now.Sub(e.fetched) >= st.ttl || !e.failedAt.IsZero()
}

// Quotes returns one Quote per source, refreshing the ones that are due.
func (st *Store) Quotes(ctx context.Context) []Quote {
	st.mu.Lock()
	defer st.mu.Unlock()

	var wg sync.WaitGroup
	for _, s := range st.sources {
		e := st.entries[s.ID]
		if !st.due(e) {
			continue
		}
		wg.Add(1)
		go func(s Source, e *entry) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			q, err := s.Fetch(cctx)
			if err != nil {
				log.Printf("source %s failed: %v", s.ID, err)
				e.failedAt = st.now()
				e.quote.Error = err.Error()
				return
			}
			e.quote, e.hasGood = q, true
			e.fetched, e.failedAt = st.now(), time.Time{}
		}(s, e)
	}
	wg.Wait()

	out := make([]Quote, 0, len(st.sources))
	for _, s := range st.sources {
		e := st.entries[s.ID]
		q := e.quote
		if q.ID == "" {
			q.ID, q.Name = s.ID, s.Name
		}
		if !e.hasGood {
			q.Mid, q.Buy, q.Sell = nil, nil, nil
		} else if q.Error != "" {
			q.Stale = true
		}
		out = append(out, q)
	}
	return out
}

// ---------------------------------------------------------------------------
// Conversion
// ---------------------------------------------------------------------------

// Line is one converted result row.
type Line struct {
	SourceID string  `json:"source_id"`
	Label    string  `json:"label"`
	Rate     float64 `json:"rate"` // AMD per 1 USD
	USD      float64 `json:"usd"`
	Toman    float64 `json:"toman,omitempty"`
}

// Convert turns an AMD amount into USD (and Toman when usdToman > 0) for every
// available rate. Buy/sell sources produce two lines.
func Convert(amd, usdToman float64, quotes []Quote) []Line {
	var lines []Line
	add := func(q Quote, label string, rate *float64) {
		if rate == nil || *rate <= 0 {
			return
		}
		usd := amd / *rate
		l := Line{SourceID: q.ID, Label: label, Rate: *rate, USD: usd}
		if usdToman > 0 {
			l.Toman = usd * usdToman
		}
		lines = append(lines, l)
	}
	for _, q := range quotes {
		add(q, q.Name, q.Mid)
		add(q, q.Name+", you buy dollars", q.Sell)
		add(q, q.Name+", you sell dollars", q.Buy)
	}
	return lines
}
