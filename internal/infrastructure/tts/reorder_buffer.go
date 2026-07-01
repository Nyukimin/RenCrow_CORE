package tts

import (
	"sort"
	"time"
)

type audioChunk struct {
	ChunkIndex int
	Text       string
	AudioPath  string
	AudioURL   string
	PauseAfter string
}

type reorderBuffer struct {
	expectedIndex int
	pending       map[int]audioChunk
	lastReceived  time.Time
	gapTimeout    time.Duration
}

func newReorderBuffer(gapTimeout time.Duration) *reorderBuffer {
	if gapTimeout <= 0 {
		gapTimeout = 3 * time.Second
	}
	return &reorderBuffer{
		pending:    make(map[int]audioChunk),
		gapTimeout: gapTimeout,
	}
}

func (b *reorderBuffer) add(ch audioChunk, now time.Time) {
	if ch.ChunkIndex < 0 {
		return
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	b.pending[ch.ChunkIndex] = ch
	b.lastReceived = now
}

func (b *reorderBuffer) drain(now time.Time, allowSkip bool) []audioChunk {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	out := make([]audioChunk, 0, len(b.pending))
	for {
		if ch, ok := b.pending[b.expectedIndex]; ok {
			delete(b.pending, b.expectedIndex)
			out = append(out, ch)
			b.expectedIndex++
			continue
		}
		if len(b.pending) == 0 {
			break
		}
		if allowSkip {
			minIdx := b.minPendingIndex()
			if minIdx < 0 {
				break
			}
			b.expectedIndex = minIdx
			continue
		}
		if !b.lastReceived.IsZero() && now.Sub(b.lastReceived) >= b.gapTimeout {
			b.expectedIndex++
			continue
		}
		break
	}
	return out
}

func (b *reorderBuffer) pendingSorted() []audioChunk {
	if len(b.pending) == 0 {
		return nil
	}
	keys := make([]int, 0, len(b.pending))
	for k := range b.pending {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	out := make([]audioChunk, 0, len(keys))
	for _, k := range keys {
		out = append(out, b.pending[k])
	}
	return out
}

func (b *reorderBuffer) minPendingIndex() int {
	if len(b.pending) == 0 {
		return -1
	}
	min := -1
	for i := range b.pending {
		if min < 0 || i < min {
			min = i
		}
	}
	return min
}
