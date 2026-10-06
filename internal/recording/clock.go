package recording

import "time"

// Clock is the single time source for a session. Every stamp it hands out
// is measured from the same monotonic start, so HTTP and browser records
// order correctly even if the wall clock jumps.
type Clock struct {
	start time.Time
}

// NewClock starts a clock at start, which must come from time.Now so it
// carries a monotonic reading.
func NewClock(start time.Time) *Clock { return &Clock{start: start} }

// Start returns when the session began.
func (c *Clock) Start() time.Time { return c.start }

// Now stamps the current moment.
func (c *Clock) Now() Stamp {
	now := time.Now()
	return Stamp{T: int64(now.Sub(c.start)), Wall: now.UTC()}
}

// Since returns the session time of t, which must carry a monotonic
// reading.
func (c *Clock) Since(t time.Time) int64 { return int64(t.Sub(c.start)) }
