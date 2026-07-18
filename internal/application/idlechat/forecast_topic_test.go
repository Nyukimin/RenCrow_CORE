package idlechat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
)

type failingForecastProvider struct {
	err error
}

func (p failingForecastProvider) Generate(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error) {
	return llm.GenerateResponse{}, p.err
}

func (p failingForecastProvider) Name() string {
	return "failing-forecast"
}

type queuedForecastProvider struct {
	responses []string
	errs      []error
	requests  int
	name      string
}

func (p *queuedForecastProvider) Generate(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.requests++
	if len(p.errs) > 0 {
		err := p.errs[0]
		p.errs = p.errs[1:]
		if err != nil {
			return llm.GenerateResponse{}, err
		}
	}
	if len(p.responses) == 0 {
		return llm.GenerateResponse{Content: "ok"}, nil
	}
	out := p.responses[0]
	p.responses = p.responses[1:]
	return llm.GenerateResponse{Content: out}, nil
}

func (p *queuedForecastProvider) Name() string {
	if p.name != "" {
		return p.name
	}
	return "queued-forecast"
}

func TestNormalizeForecastDisplayTopicFallsBackWhenEmpty(t *testing.T) {
	domain := ForecastDomain{Name: "技術"}
	got := normalizeForecastDisplayTopic(domain, "")
	if got != "技術の3年後を考える" {
		t.Fatalf("fallback topic = %q", got)
	}
}

func TestBuildForecastLLMTopicNeverStartsWithEmptyTopic(t *testing.T) {
	domain := ForecastDomain{Name: "社会"}
	got := buildForecastLLMTopic(domain, "   ", nil)
	if !strings.Contains(got, "【社会 未来展望】社会の3年後を考える") {
		t.Fatalf("LLM topic did not include fallback display topic:\n%s", got)
	}
}

func TestInitForecastTopicStockBootstrapsOneTopicPerDomainWhenEmpty(t *testing.T) {
	o := NewIdleChatOrchestrator(
		failingForecastProvider{err: errors.New("provider should not be called directly")},
		session.NewCentralMemory(),
		[]string{"mio", "shiro"},
		5,
		10,
		0.7,
		nil,
		"",
	)
	var calls atomic.Int32
	o.forecastTopicGenerator = func(domain ForecastDomain) (string, []string, *forecastTopicFailure) {
		calls.Add(1)
		return domain.Name + "の初期お題", []string{"seed"}, nil
	}
	path := filepath.Join(t.TempDir(), "forecast_topic_stock.json")

	o.InitForecastTopicStock(path)
	waitForForecastStock(t, o, func(snapshot ForecastTopicStockSnapshot) bool {
		return snapshot.Total == len(forecastDomains) && !snapshot.Filling
	})

	snapshot := o.ForecastTopicStockSnapshot()
	if got, want := calls.Load(), int32(len(forecastDomains)); got != want {
		t.Fatalf("startup bootstrap calls = %d, want %d", got, want)
	}
	for _, domain := range snapshot.Domains {
		if domain.Count != 1 {
			t.Fatalf("startup bootstrap domain %s count = %d, want 1", domain.Name, domain.Count)
		}
	}
	if snapshot.LastTrigger != "startup" {
		t.Fatalf("last trigger = %q, want startup", snapshot.LastTrigger)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("bootstrap stock was not persisted: %v", err)
	}
}

func TestInitForecastTopicStockReusesPersistedStockWithoutBootstrap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forecast_topic_stock.json")
	data, err := json.Marshal(stockFile{Stock: map[string][]PreparedTopic{
		"AI技術": {{Domain: forecastDomains[0], Topic: "保存済みのお題", Created: time.Now().UTC()}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")
	var calls atomic.Int32
	o.forecastTopicGenerator = func(domain ForecastDomain) (string, []string, *forecastTopicFailure) {
		calls.Add(1)
		return domain.Name + "の再生成", nil, nil
	}

	o.InitForecastTopicStock(path)
	time.Sleep(50 * time.Millisecond)

	if got := calls.Load(); got != 0 {
		t.Fatalf("persisted stock must suppress startup bootstrap, calls=%d", got)
	}
	if got := o.ForecastTopicStockSnapshot().Total; got != 1 {
		t.Fatalf("persisted stock total = %d, want 1", got)
	}
}

func TestRefillForecastTopicStockIfIdleAddsOneAndDefersWhenBusy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forecast_topic_stock.json")
	data, err := json.Marshal(stockFile{Stock: map[string][]PreparedTopic{
		"AI技術": {{Domain: forecastDomains[0], Topic: "保存済みのお題", Created: time.Now().UTC()}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")
	var calls atomic.Int32
	o.forecastTopicGenerator = func(domain ForecastDomain) (string, []string, *forecastTopicFailure) {
		n := calls.Add(1)
		return domain.Name + "の補充お題" + string(rune('0'+n)), nil, nil
	}
	o.InitForecastTopicStock(path)

	if !o.RefillForecastTopicStockIfIdle("heartbeat") {
		t.Fatal("heartbeat refill was not started")
	}
	waitForForecastStock(t, o, func(snapshot ForecastTopicStockSnapshot) bool {
		return snapshot.Total == 2 && !snapshot.Filling
	})
	if got := o.ForecastTopicStockSnapshot().LastTrigger; got != "heartbeat" {
		t.Fatalf("last trigger = %q, want heartbeat", got)
	}

	o.SetExternalLLMBusyFunc(func() bool { return true })
	if o.RefillForecastTopicStockIfIdle("idle") {
		t.Fatal("refill must be deferred while an external LLM is busy")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("busy refill called generator: calls=%d", got)
	}
}

func TestRefillForecastTopicStockIfIdleIsSingleFlightAcrossTriggers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forecast_topic_stock.json")
	data, err := json.Marshal(stockFile{Stock: map[string][]PreparedTopic{
		"AI技術": {{Domain: forecastDomains[0], Topic: "保存済みのお題", Created: time.Now().UTC()}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	o.forecastTopicGenerator = func(domain ForecastDomain) (string, []string, *forecastTopicFailure) {
		calls.Add(1)
		close(started)
		<-release
		return domain.Name + "の補充お題", nil, nil
	}
	o.InitForecastTopicStock(path)

	if !o.RefillForecastTopicStockIfIdle("idle") {
		t.Fatal("first refill was not started")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first refill did not reach generator")
	}
	if o.RefillForecastTopicStockIfIdle("heartbeat") {
		t.Fatal("concurrent heartbeat must not start a second stock generation")
	}
	close(release)
	waitForForecastStock(t, o, func(snapshot ForecastTopicStockSnapshot) bool { return !snapshot.Filling })
	if got := calls.Load(); got != 1 {
		t.Fatalf("single-flight generator calls = %d, want 1", got)
	}
}

func TestForecastTopicStockLoadDiscardsInvalidDuplicateAndUnknownRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forecast_topic_stock.json")
	data, err := json.Marshal(stockFile{Stock: map[string][]PreparedTopic{
		"AI技術": {
			{Topic: "有効なお題", Created: time.Now().UTC()},
			{Topic: "  有効なお題  ", Created: time.Now().UTC()},
			{Topic: "   ", Created: time.Now().UTC()},
		},
		"未知": {{Topic: "未知ドメイン", Created: time.Now().UTC()}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	snapshot := newForecastTopicStock(path).snapshot()
	if snapshot.Total != 1 {
		t.Fatalf("validated stock total = %d, want 1: %+v", snapshot.Total, snapshot)
	}
	if len(snapshot.Domains) != len(forecastDomains) {
		t.Fatalf("snapshot domains = %d, want %d", len(snapshot.Domains), len(forecastDomains))
	}
}

func waitForForecastStock(t *testing.T, o *IdleChatOrchestrator, ready func(ForecastTopicStockSnapshot) bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if snapshot := o.ForecastTopicStockSnapshot(); ready(snapshot) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("forecast stock condition timed out: %+v", o.ForecastTopicStockSnapshot())
}

func TestFetchNewsHeadlinesFromNonOKIncludesResponseBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nhk rss unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	_, err := fetchNewsHeadlinesFrom(srv.URL, 3)
	if err == nil {
		t.Fatal("expected error")
	}
	if got := err.Error(); !strings.Contains(got, "nhk rss returned status 503: nhk rss unavailable") {
		t.Fatalf("error did not include upstream body: %q", got)
	}
}

func TestFetchGoogleNewsRSSNonOKIncludesResponseBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "google news throttled", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	_, err := fetchGoogleNewsRSS(srv.URL, 3)
	if err == nil {
		t.Fatal("expected error")
	}
	if got := err.Error(); !strings.Contains(got, "google news rss status 429: google news throttled") {
		t.Fatalf("error did not include upstream body: %q", got)
	}
}

func TestBuildGoogleNewsRSSSearchURLEncodesJapaneseKeyword(t *testing.T) {
	got := buildGoogleNewsRSSSearchURL("はしか感染拡大")
	if strings.Contains(got, "はしか") {
		t.Fatalf("URL should percent-encode Japanese keyword: %s", got)
	}
	if !strings.Contains(got, "q=%E3%81%AF%E3%81%97%E3%81%8B%E6%84%9F%E6%9F%93%E6%8B%A1%E5%A4%A7") {
		t.Fatalf("URL did not contain encoded query: %s", got)
	}
	if !strings.Contains(got, "hl=ja&gl=JP&ceid=JP:ja") {
		t.Fatalf("URL lost locale parameters: %s", got)
	}
}

func TestForecastLLMErrorCodeClassifiesQuotaAndRateLimit(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{name: "quota", err: errors.New("openai error: insufficient_quota"), want: "insufficient_quota"},
		{name: "429", err: errors.New("provider returned status 429"), want: "rate_limited"},
		{name: "timeout", err: errors.New("request timeout"), want: "timeout"},
		{name: "generic", err: errors.New("boom"), want: "provider_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := forecastLLMErrorCode(tc.err); got != tc.want {
				t.Fatalf("forecastLLMErrorCode() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGenerateForecastTopicReturnsFailureInsteadOfFallbackTopic(t *testing.T) {
	o := NewIdleChatOrchestrator(
		failingForecastProvider{err: errors.New("openai error: insufficient_quota")},
		session.NewCentralMemory(),
		[]string{"mio", "shiro"},
		5,
		10,
		0.7,
		nil,
		"",
	)
	o.SetForecastProviderWithLabel(failingForecastProvider{err: errors.New("openai error: insufficient_quota")}, "Coder2 openai (gpt-4o-mini)")

	topic, failure := o.generateForecastTopic(ForecastDomain{Name: "AI技術"}, []string{"生成AI規制の新指針"})
	if topic != "" {
		t.Fatalf("generateForecastTopic returned fallback topic instead of failure: %q", topic)
	}
	if failure == nil {
		t.Fatal("expected failure")
	}
	if failure.ErrorCode != "insufficient_quota" {
		t.Fatalf("unexpected error_code: %+v", failure)
	}
	display := formatForecastTopicError(ForecastDomain{Name: "AI技術"}, failure)
	for _, want := range []string{"FORECAST_TOPIC_GENERATION_FAILED", "error_code=insufficient_quota", "phase=topic", "domain=AI技術", "provider=Coder2 openai (gpt-4o-mini)"} {
		if !strings.Contains(display, want) {
			t.Fatalf("display error missing %q: %s", want, display)
		}
	}
}

func TestGenerateForecastTopicUsesInterestingJudge(t *testing.T) {
	provider := &queuedForecastProvider{
		responses: []string{
			topicCandidatesJSON("AI技術が、個人の記憶整理をどう変えるか", "変化の分岐"),
			topicJudgeJSON("AI技術が、個人の記憶整理をどう変えるか"),
		},
		name: "forecast-local",
	}
	o := NewIdleChatOrchestrator(
		provider,
		session.NewCentralMemory(),
		[]string{"mio", "shiro"},
		5,
		10,
		0.7,
		nil,
		"",
	)
	o.SetForecastProviderWithLabel(provider, "Coder1 local_openai (Worker)")

	topic, failure := o.generateForecastTopic(ForecastDomain{Name: "AI技術"}, []string{"生成AI規制の新指針"})
	if failure != nil {
		t.Fatalf("unexpected failure: %+v", failure)
	}
	if topic != "AI技術が、個人の記憶整理をどう変えるか" {
		t.Fatalf("topic = %q", topic)
	}
	if provider.requests != 2 {
		t.Fatalf("provider requests = %d, want candidates + judge", provider.requests)
	}
}

func TestGenerateForecastTopicUsesDedicatedShiroProvider(t *testing.T) {
	coder := &queuedForecastProvider{
		responses: []string{
			topicCandidatesJSON("Coderが生成したお題", "Coder route"),
			topicJudgeJSON("Coderが生成したお題"),
		},
		name: "coder",
	}
	shiro := &queuedForecastProvider{
		responses: []string{
			topicCandidatesJSON("AI技術が、地域医療の担い手をどう変えるか", "変化の分岐"),
			topicJudgeJSON("AI技術が、地域医療の担い手をどう変えるか"),
		},
		name: "worker",
	}
	o := NewIdleChatOrchestrator(
		coder,
		session.NewCentralMemory(),
		[]string{"mio", "shiro"},
		5,
		10,
		0.7,
		nil,
		"",
	)
	o.SetForecastProviderWithLabel(coder, "Coder1 local_openai (Coder1)")
	o.SetForecastTopicProviderWithLabel(shiro, "Shiro")

	topic, failure := o.generateForecastTopic(ForecastDomain{Name: "AI技術"}, []string{"生成AI規制の新指針"})
	if failure != nil {
		t.Fatalf("unexpected failure: %+v", failure)
	}
	if topic != "AI技術が、地域医療の担い手をどう変えるか" {
		t.Fatalf("topic = %q", topic)
	}
	if shiro.requests != 2 {
		t.Fatalf("Shiro provider requests = %d, want candidates + judge", shiro.requests)
	}
	if coder.requests != 0 {
		t.Fatalf("Coder provider must not generate stock topics, requests = %d", coder.requests)
	}
}

func TestForecastTopicGenerationConfigCapsDedicatedShiroCandidates(t *testing.T) {
	base := TopicGenerationConfig{CandidatesPerAttempt: 5}
	shiro := forecastTopicGenerationConfigForProvider(base, true)
	if shiro.CandidatesPerAttempt != 3 {
		t.Fatalf("Shiro forecast candidates = %d, want 3", shiro.CandidatesPerAttempt)
	}
	legacy := forecastTopicGenerationConfigForProvider(base, false)
	if legacy.CandidatesPerAttempt != 5 {
		t.Fatalf("legacy forecast candidates = %d, want configured 5", legacy.CandidatesPerAttempt)
	}
}

func TestExtractForecastKeywordReturnsFailureWithoutDomainFallback(t *testing.T) {
	o := NewIdleChatOrchestrator(
		failingForecastProvider{err: errors.New("should not be called")},
		session.NewCentralMemory(),
		[]string{"mio", "shiro"},
		5,
		10,
		0.7,
		nil,
		"",
	)

	keyword, failure := o.extractForecastKeyword(ForecastDomain{Name: "医療"}, nil)
	if keyword != "" {
		t.Fatalf("extractForecastKeyword returned fallback keyword instead of failure: %q", keyword)
	}
	if failure == nil || failure.ErrorCode != "no_seed_headlines" {
		t.Fatalf("unexpected failure: %+v", failure)
	}
}

func TestForecastLLMReturnsPrimaryErrorWithoutExternalRetry(t *testing.T) {
	primary := &queuedForecastProvider{
		errs: []error{errors.New("primary failed")},
		name: "primary",
	}
	external := &queuedForecastProvider{
		responses: []string{"外部LLMの一回だけの結果"},
		name:      "external",
	}
	o := NewIdleChatOrchestrator(
		primary,
		session.NewCentralMemory(),
		[]string{"mio", "shiro"},
		5,
		10,
		0.7,
		nil,
		"",
	)
	o.SetForecastProviderWithLabel(primary, "Coder1 local_openai (Worker)")

	resp, label, err := o.generateForecastLLM("topic", "AI技術", llm.GenerateRequest{
		Messages: []llm.Message{{Role: "user", Content: "topic"}},
	})
	if err == nil {
		t.Fatal("expected primary error")
	}
	if resp.Content != "" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if label != "Coder1 local_openai (Worker)" {
		t.Fatalf("unexpected provider label: %q", label)
	}
	if primary.requests != 1 {
		t.Fatalf("primary requests = %d, want 1", primary.requests)
	}
	if external.requests != 0 {
		t.Fatalf("external requests = %d, want 0", external.requests)
	}
}

func TestForecastLLMExplicitExternalProviderIsPrimary(t *testing.T) {
	external := &queuedForecastProvider{
		responses: []string{"明示された外部LLMの結果"},
		name:      "external",
	}
	o := NewIdleChatOrchestrator(
		external,
		session.NewCentralMemory(),
		[]string{"mio", "shiro"},
		5,
		10,
		0.7,
		nil,
		"",
	)
	o.SetForecastProviderWithLabel(external, "Coder2 openai (gpt-4o-mini)")

	resp, label, err := o.generateForecastLLM("topic", "AI技術", llm.GenerateRequest{
		Messages: []llm.Message{{Role: "user", Content: "topic"}},
	})
	if err != nil {
		t.Fatalf("generateForecastLLM failed: %v", err)
	}
	if resp.Content != "明示された外部LLMの結果" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if label != "Coder2 openai (gpt-4o-mini)" {
		t.Fatalf("unexpected provider label: %q", label)
	}
	if external.requests != 1 {
		t.Fatalf("external requests = %d, want 1", external.requests)
	}
}
