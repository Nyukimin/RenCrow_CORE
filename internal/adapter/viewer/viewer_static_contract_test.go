package viewer

import (
	"os"
	"strings"
	"testing"
)

func TestViewerStaticContractSeparatesDisplayAudioLipsyncAndLogs(t *testing.T) {
	data, err := os.ReadFile("viewer.html")
	if err != nil {
		t.Fatalf("read viewer.html: %v", err)
	}
	html := string(data)

	required := map[string]string{
		`id="chat"`:                      "normal chat timeline display",
		`id="idleLiveLog"`:               "IdleChat live display",
		`id="idleSummaryReview"`:         "IdleChat summary/review display",
		`id="ttsNowPlaying"`:             "TTS playback status",
		`id="lipSyncMio"`:                "Mio lipsync state",
		`id="lipSyncShiro"`:              "Shiro lipsync state",
		`id="opsFeedBody"`:               "ops/event log",
		`id="toolHarnessBody"`:           "Tool Harness mediation event log",
		`id="dciTraceBody"`:              "DCI search trace log",
		`id="debugLatencySummary"`:       "LLM/TTS/STT/network latency summary",
		`id="debugSttTrace"`:             "STT trace log",
		`id="sourceRegistryBody"`:        "Source Registry panel",
		`id="sourceRegistryStagingBody"`: "Source Registry staging review panel",
		`id="memoryLayerBody"`:           "Memory layer panel",
		`id="micBtn"`:                    "normal chat voice input control",
		`id="idleStart"`:                 "IdleChat control separated from mic input",
		`id="audioBtn"`:                  "browser audio enable control",
		`id="liveAudioBtn"`:              "live audio enable control",
		`id="sourceRegistrySaveBtn"`:     "Source Registry save control",
	}
	for needle, purpose := range required {
		if !strings.Contains(html, needle) {
			t.Fatalf("viewer.html missing %s (%s)", needle, purpose)
		}
	}

	micIndex := strings.Index(html, `id="micBtn"`)
	idleIndex := strings.Index(html, `id="idleStart"`)
	headerEnd := strings.Index(html, `</header>`)
	lipsyncIndex := strings.Index(html, `class="lipsync-stage"`)
	if micIndex < 0 || idleIndex < 0 {
		t.Fatal("mic and IdleChat controls must both be present")
	}
	if micIndex > idleIndex {
		t.Fatal("normal chat mic control should be in the normal input controls before IdleChat controls")
	}
	if headerEnd < 0 || lipsyncIndex < 0 || lipsyncIndex > headerEnd {
		t.Fatal("Mio/Shiro lipsync mini icons must be placed inside the top header band")
	}
}

func TestViewerStaticContractDailyDeskTabs(t *testing.T) {
	data, err := os.ReadFile("viewer.html")
	if err != nil {
		t.Fatalf("read viewer.html: %v", err)
	}
	html := string(data)

	required := map[string]string{
		`data-tab="home"`:                        "Home tab",
		`data-tab="develop"`:                     "Develop tab",
		`data-tab="instructions"`:                "Instructions tab",
		`data-tab="reports"`:                     "Reports tab",
		`data-tab="movie-db"`:                    "Movie Database tab",
		`data-tab="investment"`:                  "Investment tab",
		`data-tab="games"`:                       "Games tab",
		`id="panel-home" class="panel active"`:   "Home is the initial active panel",
		`id="panel-develop"`:                     "Develop panel",
		`id="panel-instructions"`:                "Instructions panel",
		`id="panel-reports"`:                     "Reports panel",
		`id="panel-movie-db"`:                    "Movie Database panel",
		`id="panel-investment"`:                  "Investment panel",
		`id="panel-games"`:                       "Games panel",
		`id="gamesBridgeStatusCard"`:             "Games bridge status card",
		`id="investmentRefreshBtn"`:              "Investment refresh action",
		`id="movieDbFetchKind"`:                  "Movie Database fetch kind selector",
		`id="movieDbFetchQuery"`:                 "Movie Database fetch query input",
		`id="movieDbFetchBtn"`:                   "Movie Database fetch action",
		`/viewer/assets/css/tabs/desk.css`:       "Daily Desk CSS",
		`/viewer/assets/js/tabs/home.js`:         "Home tab JavaScript",
		`/viewer/assets/js/tabs/develop.js`:      "Develop tab JavaScript",
		`/viewer/assets/js/tabs/instructions.js`: "Instructions tab JavaScript",
		`/viewer/assets/js/tabs/reports.js`:      "Reports tab JavaScript",
		`/viewer/assets/js/tabs/movie-db.js`:     "Movie Database tab JavaScript",
		`/viewer/assets/js/tabs/investment.js`:   "Investment tab JavaScript",
		`/viewer/assets/js/tabs/games.js`:        "Games tab JavaScript",
	}
	for needle, purpose := range required {
		if !strings.Contains(html, needle) {
			t.Fatalf("viewer.html missing %s (%s)", needle, purpose)
		}
	}

	if strings.Contains(html, `id="panel-overview" class="panel active"`) {
		t.Fatal("overview must not remain the initial active panel after Daily Desk addition")
	}
}

func TestViewerStaticContractInformationCollectionTab(t *testing.T) {
	htmlData, err := os.ReadFile("viewer.html")
	if err != nil {
		t.Fatalf("read viewer.html: %v", err)
	}
	jsData, err := os.ReadFile("assets/js/viewer.js")
	if err != nil {
		t.Fatalf("read viewer.js: %v", err)
	}
	collectionData, err := os.ReadFile("assets/js/tabs/collection.js")
	if err != nil {
		t.Fatalf("read collection.js: %v", err)
	}

	html := string(htmlData)
	for _, needle := range []string{
		`data-tab="collection"`,
		`id="panel-collection"`,
		`id="collectionStatus"`,
		`id="collectionCategoryFilter"`,
		`id="collectionSourceFilter"`,
		`id="collectionItems"`,
		`id="collectionSources"`,
		`/viewer/assets/css/tabs/collection.css`,
		`/viewer/assets/js/tabs/collection.js`,
	} {
		if !strings.Contains(html, needle) {
			t.Fatalf("collection Viewer contract missing %q", needle)
		}
	}
	if !strings.Contains(string(jsData), "collection: document.getElementById('panel-collection')") ||
		!strings.Contains(string(jsData), "refreshCollectionData") {
		t.Fatal("viewer tab switch must register and refresh Collection")
	}
	collectionJS := string(collectionData)
	for _, needle := range []string{
		"/viewer/idlechat/collection",
		"function refreshCollectionData()",
		"function renderCollectionData()",
		"collection.category_counts",
		"collection.sources",
		"collection.enrichment_status",
		"collection.skill_id",
		"item.translated_body",
		"item.summary",
		"item.term_notes",
		"item.perspective",
	} {
		if !strings.Contains(collectionJS, needle) {
			t.Fatalf("collection.js contract missing %q", needle)
		}
	}
	translationIndex := strings.Index(collectionJS, "<strong>原文翻訳</strong>")
	termNotesIndex := strings.Index(collectionJS, "<strong>用語補足</strong>")
	summaryIndex := strings.Index(collectionJS, "<strong>サマリ</strong>")
	perspectiveIndex := strings.Index(collectionJS, "<strong>Shiroの見解</strong>")
	if translationIndex < 0 || summaryIndex < 0 || perspectiveIndex < 0 || termNotesIndex < 0 || !(translationIndex < summaryIndex && summaryIndex < perspectiveIndex && perspectiveIndex < termNotesIndex) {
		t.Fatalf("collection output order must be 原文翻訳 -> サマリ -> Shiroの見解 -> 用語補足")
	}
}

func TestViewerStaticContractChatAndIdleChatDeskRedesign(t *testing.T) {
	data, err := os.ReadFile("viewer.html")
	if err != nil {
		t.Fatalf("read viewer.html: %v", err)
	}
	html := string(data)

	required := map[string]string{
		`id="panel-timeline" class="panel chat-desk-panel"`: "Chat panel keeps tab contract",
		`class="chat-desk-shell"`:                           "Chat uses desk shell",
		`class="chat-character-pane"`:                       "Chat has large character pane",
		`class="chat-main-pane"`:                            "Chat has main conversation pane",
		`class="chat-intent-strip"`:                         "Chat shows compact routing/action guidance",
		`id="chat"`:                                         "Chat message render target remains stable",
		`id="panel-idlechat" class="panel idle-desk-panel"`: "IdleChat panel uses redesigned shell",
		`class="idle-desk-shell"`:                           "IdleChat uses desk shell",
		`class="idle-character-pane"`:                       "IdleChat has character/status pane",
		`class="idle-mode-board"`:                           "IdleChat mode controls are first-class controls",
		`id="idleLiveLog"`:                                  "IdleChat live render target remains stable",
		`id="idleSummaryReview"`:                            "IdleChat summary review remains stable",
		`data-idle-view="stock"`:                            "IdleChat stock subview is selectable",
		`id="idleForecastStock"`:                            "IdleChat forecast stock is readable",
		`id="idlechatBody"`:                                 "IdleChat history body remains stable",
	}
	for needle, purpose := range required {
		if !strings.Contains(html, needle) {
			t.Fatalf("viewer.html missing %s (%s)", needle, purpose)
		}
	}

	if strings.Contains(html, `<section id="panel-idlechat" class="panel">
    <div class="grid">`) {
		t.Fatal("IdleChat must not use the old generic grid-first layout")
	}
}

func TestViewerStaticContractIdleChatRendersForecastStockSnapshot(t *testing.T) {
	data, err := os.ReadFile("assets/js/tabs/idlechat.js")
	if err != nil {
		t.Fatalf("read idlechat.js: %v", err)
	}
	js := string(data)
	for _, needle := range []string{
		"function renderIdleForecastStock()",
		"state.idleChat.forecastStock = d.forecast_stock || null",
		"forecastStock.domains",
	} {
		if !strings.Contains(js, needle) {
			t.Fatalf("IdleChat stock Viewer contract missing %q", needle)
		}
	}
}

func TestViewerStaticContractIdleChatStockUsesReadableInspectionLayout(t *testing.T) {
	cssData, err := os.ReadFile("assets/css/viewer.css")
	if err != nil {
		t.Fatalf("read viewer.css: %v", err)
	}
	jsData, err := os.ReadFile("assets/js/tabs/idlechat.js")
	if err != nil {
		t.Fatalf("read idlechat.js: %v", err)
	}
	htmlData, err := os.ReadFile("viewer.html")
	if err != nil {
		t.Fatalf("read viewer.html: %v", err)
	}

	css := string(cssData)
	js := string(jsData)
	html := string(htmlData)
	for _, needle := range []string{
		".idle-desk-shell.stock-view",
		".idle-stock-overview",
		".idle-stock-diagnostics",
		".idle-stock-topic-grid",
	} {
		if !strings.Contains(css, needle) {
			t.Fatalf("IdleChat stock readable layout CSS missing %q", needle)
		}
	}
	for _, needle := range []string{
		"classList.toggle('stock-view', next === 'stock')",
		`<details class="idle-stock-diagnostics"`,
		`class="idle-stock-topic-grid"`,
	} {
		if !strings.Contains(js, needle) {
			t.Fatalf("IdleChat stock readable layout render contract missing %q", needle)
		}
	}
	if !strings.Contains(html, "viewer.css?v=20260718-idle-stock-reader") {
		t.Fatal("IdleChat stock layout must invalidate the Viewer CSS cache")
	}
}

func TestViewerStaticContractLabLiveModeHidesTopGuidancePanels(t *testing.T) {
	data, err := os.ReadFile("assets/css/viewer.css")
	if err != nil {
		t.Fatalf("read viewer.css: %v", err)
	}
	css := string(data)
	required := []string{
		`body.lab-mode.live-mode .chat-intent-strip{display:none !important}`,
		`body.lab-mode.live-mode .lab-datetime-panel{`,
		`background:transparent !important;`,
		`border:0 !important;`,
		`box-shadow:none !important;`,
		`backdrop-filter:none !important;`,
	}
	for _, needle := range required {
		if !strings.Contains(css, needle) {
			t.Fatalf("Lab live mode top guidance cleanup missing %q", needle)
		}
	}
}

func TestViewerStaticContractLabModeIncludesAllChatRecipients(t *testing.T) {
	htmlData, err := os.ReadFile("viewer.html")
	if err != nil {
		t.Fatalf("read viewer.html: %v", err)
	}
	jsData, err := os.ReadFile("assets/js/viewer.js")
	if err != nil {
		t.Fatalf("read viewer.js: %v", err)
	}
	html := string(htmlData)
	js := string(jsData)
	if !strings.Contains(html, `data-lab-switch="mio"`) {
		t.Fatal("viewer.html missing independent Mio lab switch")
	}
	if !strings.Contains(html, `id="labModePartnerChip"`) {
		t.Fatal("viewer.html missing lab partner chip")
	}
	if !strings.Contains(html, `id="labPartnerOptions"`) {
		t.Fatal("viewer.html missing lab partner options")
	}
	if strings.Contains(html, `data-lab-switch="kuro"`) || strings.Contains(html, `data-lab-switch="midori"`) {
		t.Fatal("viewer.html must not render Kuro/Midori as horizontal lab switches")
	}
	for _, actor := range []string{"shiro", "kuro", "midori"} {
		if !strings.Contains(html, `data-lab-partner-option="`+actor+`"`) {
			t.Fatalf("viewer.html missing lab partner option for %s", actor)
		}
	}
	if !strings.Contains(js, `const LAB_CHAT_PARTNERS = ['shiro', 'kuro', 'midori'];`) {
		t.Fatal("viewer.js missing picker lab chat partner list")
	}
	for _, needle := range []string{
		`data-lab-partner-toggle`,
		`data-lab-partner-option`,
		`setLabPartnerMenuOpen`,
		`syncLabPartnerPicker`,
	} {
		if !strings.Contains(js, needle) {
			t.Fatalf("viewer.js missing lab partner picker wiring %q", needle)
		}
	}
}

func TestViewerStaticContractMovieDatabaseTabSwitchMapping(t *testing.T) {
	data, err := os.ReadFile("assets/js/viewer.js")
	if err != nil {
		t.Fatalf("read viewer.js: %v", err)
	}
	js := string(data)
	if !strings.Contains(js, `'movie-db': document.getElementById('panel-movie-db')`) {
		t.Fatal("viewer.js missing Movie Database panel switch mapping")
	}
}

func TestViewerStaticContractInvestmentTabSwitchMapping(t *testing.T) {
	data, err := os.ReadFile("assets/js/viewer.js")
	if err != nil {
		t.Fatalf("read viewer.js: %v", err)
	}
	js := string(data)
	if !strings.Contains(js, `investment: document.getElementById('panel-investment')`) {
		t.Fatal("viewer.js missing Investment panel switch mapping")
	}
	if !strings.Contains(js, "refreshInvestmentData()") {
		t.Fatal("viewer.js missing Investment tab refresh wiring")
	}
}

func TestViewerStaticContractChatScrollUsesConversationContainer(t *testing.T) {
	data, err := os.ReadFile("assets/js/viewer.js")
	if err != nil {
		t.Fatalf("read viewer.js: %v", err)
	}
	js := string(data)
	if !strings.Contains(js, "const target = chat || mainEl") {
		t.Fatal("Chat timeline auto-follow must scroll the chat container, not the whole main viewport")
	}
	if !strings.Contains(js, "if (mainEl) mainEl.scrollTop = 0") {
		t.Fatal("Tab switching must reset the main viewport to avoid clipped Viewer panels")
	}
}

func TestViewerStaticContractNowPlayingFloatsAboveComposer(t *testing.T) {
	data, err := os.ReadFile("assets/css/viewer.css")
	if err != nil {
		t.Fatalf("read viewer.css: %v", err)
	}
	css := string(data)

	required := []string{
		`.tts-now-playing{`,
		`bottom:calc(148px + var(--safe-bottom));z-index:41;pointer-events:none;`,
		`@media (orientation: landscape) and (max-height: 520px){`,
		`.tts-now-playing{max-width:min(76vw,760px);font-size:13px;bottom:calc(180px + var(--safe-bottom))}`,
		`@media (max-width: 640px){`,
		`.tts-now-playing{bottom:calc(180px + var(--safe-bottom))}`,
	}
	for _, needle := range required {
		if !strings.Contains(css, needle) {
			t.Fatalf("viewer.css missing %q", needle)
		}
	}
	if strings.Contains(css, `bottom:calc(96px + var(--safe-bottom));z-index:35;pointer-events:none;`) {
		t.Fatal("legacy Now Playing offset must not remain after the composer overlap fix")
	}
}

func TestViewerResponsiveBreakpointsUseViewportShape(t *testing.T) {
	viewerData, err := os.ReadFile("assets/css/viewer.css")
	if err != nil {
		t.Fatalf("read viewer.css: %v", err)
	}
	deskData, err := os.ReadFile("assets/css/tabs/desk.css")
	if err != nil {
		t.Fatalf("read desk.css: %v", err)
	}
	viewerCSS := string(viewerData)
	deskCSS := strings.ReplaceAll(string(deskData), "\r\n", "\n")
	narrowQuery := `@media (max-width: 900px), (max-aspect-ratio: 21/20)`

	if !strings.Contains(viewerCSS, narrowQuery) {
		t.Fatalf("viewer shell narrow breakpoint must use width plus aspect ratio: missing %q", narrowQuery)
	}
	if !strings.Contains(deskCSS, narrowQuery) {
		t.Fatalf("Daily Desk narrow breakpoint must match viewer shell: missing %q", narrowQuery)
	}
	if strings.Contains(deskCSS, `@media (max-width: 980px)`) {
		t.Fatal("Daily Desk must not keep the old 980px-only breakpoint that created desktop shell with narrow content")
	}
	if !strings.Contains(deskCSS, `.desk-card-list.home-focus {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }`) {
		t.Fatal("Daily Desk landscape compact mode must keep Home cards readable before the full wide layout")
	}
}

func TestViewerStaticContractHobbyGraphOpsOverview(t *testing.T) {
	viewerJS, err := os.ReadFile("assets/js/viewer.js")
	if err != nil {
		t.Fatalf("read viewer.js: %v", err)
	}
	opsJS, err := os.ReadFile("assets/js/tabs/ops.js")
	if err != nil {
		t.Fatalf("read ops.js: %v", err)
	}
	if !strings.Contains(string(viewerJS), "function refreshHobbyGraphOverviewData()") {
		t.Fatal("viewer.js missing Hobby Graph overview refresh function")
	}
	if !strings.Contains(string(viewerJS), "/viewer/hobby-graph?action=overview&limit=5") {
		t.Fatal("viewer.js missing Hobby Graph overview endpoint fetch")
	}
	if !strings.Contains(string(opsJS), "function hobbyGraphOpsCard()") {
		t.Fatal("ops.js missing Hobby Graph Ops card")
	}
	if !strings.Contains(string(opsJS), "hobbyGraphOpsCard()") {
		t.Fatal("ops.js missing Hobby Graph Ops card registration")
	}
}

func TestViewerStaticContractGameBridgeOpsCard(t *testing.T) {
	viewerJS, err := os.ReadFile("assets/js/viewer.js")
	if err != nil {
		t.Fatalf("read viewer.js: %v", err)
	}
	opsJS, err := os.ReadFile("assets/js/tabs/ops.js")
	if err != nil {
		t.Fatalf("read ops.js: %v", err)
	}
	gamesJS, err := os.ReadFile("assets/js/tabs/games.js")
	if err != nil {
		t.Fatalf("read games.js: %v", err)
	}
	html, err := os.ReadFile("viewer.html")
	if err != nil {
		t.Fatalf("read viewer.html: %v", err)
	}
	viewer := string(viewerJS)
	ops := string(opsJS)
	games := string(gamesJS)
	page := string(html)

	for _, required := range []string{
		"function refreshGameBridgeData()",
		"/viewer/games/status",
		"/viewer/games/sessions?limit=5",
		"/viewer/games/events?limit=5",
	} {
		if !strings.Contains(viewer, required) {
			t.Fatalf("viewer.js missing Game Bridge refresh contract: %s", required)
		}
	}
	for _, required := range []string{
		"function gameBridgeOpsCard()",
		"title: 'Game Bridge'",
		"candidate-only: not confirmed",
		"gameBridgeOpsCard()",
	} {
		if !strings.Contains(ops, required) {
			t.Fatalf("ops.js missing Game Bridge Ops card contract: %s", required)
		}
	}
	for _, required := range []string{
		"function renderGamesDesk()",
		"gamesBridgeState()",
		"gamesBridgeStatusCard",
		"gamesLatestSessionCard",
		"gamesEventsCard",
		"candidate-only: not confirmed",
	} {
		if !strings.Contains(games, required) {
			t.Fatalf("games.js missing Game Bridge Games tab contract: %s", required)
		}
	}
	for _, required := range []string{
		"games: document.getElementById('panel-games')",
		"tab === 'games'",
		"renderGamesDesk",
	} {
		if !strings.Contains(viewer, required) {
			t.Fatalf("viewer.js missing Games tab wiring: %s", required)
		}
	}
	if !strings.Contains(page, "ops.js?v=20260702-game-bridge-card") {
		t.Fatal("viewer.html missing Game Bridge Ops cache buster")
	}
	if !strings.Contains(page, "games.js?v=20260702-games-tab") {
		t.Fatal("viewer.html missing Games tab cache buster")
	}
	if !strings.Contains(page, "viewer.js?v=20260702-games-tab") {
		t.Fatal("viewer.html missing Game Bridge viewer cache buster")
	}
}
