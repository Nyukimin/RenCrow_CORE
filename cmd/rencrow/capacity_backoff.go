package main

import "time"

// capacityBackoff spaces out the retries of a periodic worker after the Task
// owner refused to admit its Task because execution capacity is full. The delay
// doubles from base on each consecutive refusal and never exceeds max, so the
// worker keeps trying (capacity frees up) without hammering the Task store.
// It is not safe for concurrent use; each worker goroutine owns one.
type capacityBackoff struct {
	base, max time.Duration
	refusals  int
	notBefore time.Time
}

func newCapacityBackoff(base, max time.Duration) *capacityBackoff {
	if base <= 0 {
		base = time.Second
	}
	if max < base {
		max = base
	}
	return &capacityBackoff{base: base, max: max}
}

// refused records one more refusal at now and returns the delay before the
// next attempt.
func (b *capacityBackoff) refused(now time.Time) time.Duration {
	delay := b.base
	for i := 0; i < b.refusals && delay < b.max; i++ {
		delay *= 2
	}
	if delay > b.max {
		delay = b.max
	}
	if delay < b.max {
		// Once the bound is reached the count no longer matters.
		b.refusals++
	}
	b.notBefore = now.Add(delay)
	return delay
}

// succeeded clears the wait and the refusal count.
func (b *capacityBackoff) succeeded() {
	b.refusals = 0
	b.notBefore = time.Time{}
}

// waiting reports whether now is still inside the delay of the last refusal.
func (b *capacityBackoff) waiting(now time.Time) bool {
	return now.Before(b.notBefore)
}
