package idlechat

import (
	"testing"
	"time"
)

func TestNextDailySeedRefreshAtUsesFourAMJST(t *testing.T) {
	tests := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{
			name: "before four uses same day",
			now:  time.Date(2026, 7, 20, 3, 30, 0, 0, jst),
			want: time.Date(2026, 7, 20, 4, 0, 0, 0, jst),
		},
		{
			name: "at four runs now",
			now:  time.Date(2026, 7, 20, 4, 0, 0, 0, jst),
			want: time.Date(2026, 7, 20, 4, 0, 0, 0, jst),
		},
		{
			name: "utc input is normalized to jst",
			now:  time.Date(2026, 7, 19, 19, 30, 0, 0, time.UTC),
			want: time.Date(2026, 7, 21, 4, 0, 0, 0, jst),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nextDailySeedRefreshAt(tt.now); !got.Equal(tt.want) {
				t.Fatalf("nextDailySeedRefreshAt(%s) = %s, want %s", tt.now, got, tt.want)
			}
		})
	}
}

func TestShouldRefreshDailySeedsAllowsScheduledForcedRefresh(t *testing.T) {
	now := time.Date(2026, 7, 20, 4, 0, 0, 0, jst)
	cache := &DailySeedCache{Date: "2026-07-20", FetchedAt: now.Add(-3 * time.Hour)}

	if shouldRefreshDailySeeds(cache, now, false) {
		t.Fatal("ordinary startup refresh should reuse the current-day cache")
	}
	if !shouldRefreshDailySeeds(cache, now, true) {
		t.Fatal("scheduled 04:00 refresh must bypass the current-day cache")
	}
}

func TestDefaultNewsSeedSourcesCoverBroadAISources(t *testing.T) {
	want := map[string]string{
		"OpenAI News":          "ai_frontier",
		"Google DeepMind":      "ai_frontier",
		"Hugging Face Blog":    "ai_open_source",
		"Microsoft Research":   "ai_research",
		"Google Research":      "ai_research",
		"NVIDIA Generative AI": "ai_infrastructure",
		"arXiv AI Research":    "ai_research",
	}

	got := make(map[string]string, len(defaultNewsSeedSources))
	for _, source := range defaultNewsSeedSources {
		got[source.Name] = source.Category
	}
	for name, category := range want {
		if got[name] != category {
			t.Errorf("source %q category = %q, want %q", name, got[name], category)
		}
	}
}
