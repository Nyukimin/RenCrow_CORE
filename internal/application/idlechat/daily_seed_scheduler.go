package idlechat

import (
	"log"
	"strings"
	"time"
)

const dailySeedRefreshHourJST = 4

func nextDailySeedRefreshAt(now time.Time) time.Time {
	nowJST := now.In(jst)
	next := time.Date(nowJST.Year(), nowJST.Month(), nowJST.Day(), dailySeedRefreshHourJST, 0, 0, 0, jst)
	if nowJST.After(next) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

func (o *IdleChatOrchestrator) startDailySeedRefreshScheduler(sourceConfig NewsSourceConfig) {
	log.Printf("[IdleChat] Daily seed scheduler enabled: skill=%s schedule=04:00 timezone=JST tools=go_http,rss,atom,wikipedia_api,reddit_atom,x_recent_search,web_gather.fetch,web_search_unknown_terms_only,shiro_worker_llm sources=%s",
		dailySourceBriefSkillID, dailySeedSourceLogValue(defaultNewsSeedSources))

	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		for {
			next := nextDailySeedRefreshAt(time.Now())
			timer := time.NewTimer(time.Until(next))
			select {
			case <-o.ctx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				return
			case <-timer.C:
				go func() {
					if err := refreshDailySeeds(sourceConfig, true); err != nil {
						log.Printf("[IdleChat] Scheduled daily seeds refresh failed: %v", err)
						return
					}
					o.enrichCurrentDailySeeds()
				}()
			}
		}
	}()
}

func dailySeedSourceLogValue(sources []NewsSeedSource) string {
	values := make([]string, 0, len(sources))
	for _, source := range sources {
		values = append(values, strings.TrimSpace(source.Name)+"="+strings.TrimSpace(source.URL))
	}
	return strings.Join(values, ",")
}
