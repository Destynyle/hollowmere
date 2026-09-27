// Package limit provides the token bucket used against flooding: by the
// session layer for every command, and by the game for chatty commands
// such as private messages.
package limit

import "time"

// Bucket refills Rate tokens per second up to Burst; each action costs one.
// It is not safe for concurrent use: the owner serialises calls.
type Bucket struct {
	Rate   float64
	Burst  float64
	tokens float64
	last   time.Time
}

// New returns a full bucket.
func New(rate, burst float64) *Bucket {
	return &Bucket{Rate: rate, Burst: burst, tokens: burst, last: time.Now()}
}

// Allow spends one token if there is one.
func (b *Bucket) Allow(now time.Time) bool {
	b.tokens += now.Sub(b.last).Seconds() * b.Rate
	if b.tokens > b.Burst {
		b.tokens = b.Burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
