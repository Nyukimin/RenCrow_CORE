package viewer

import (
	"os"
	"strings"
	"testing"
)

func TestViewerStaticContractCoderIdentityMapping(t *testing.T) {
	data, err := os.ReadFile("assets/js/viewer.js")
	if err != nil {
		t.Fatalf("read viewer.js: %v", err)
	}
	js := string(data)
	required := []string{
		`coder1: {c:'#fb923c', l:'あか',  en:'Aka'`,
		`coder2: {c:'#818cf8', l:'あお',  en:'Ao'`,
		`coder3: {c:'#facc15', l:'きん',  en:'Kin'`,
		`coder4: {c:'#a78bfa', l:'ぎん',  en:'Gin'`,
		`if (raw === 'aka') return 'coder1';`,
		`if (raw === 'ao') return 'coder2';`,
		`if (raw === 'kin') return 'coder3';`,
		`if (raw === 'gin') return 'coder4';`,
	}
	for _, needle := range required {
		if !strings.Contains(js, needle) {
			t.Fatalf("viewer.js missing Coder identity contract %q", needle)
		}
	}
}

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
		`id="dciOwnerTokenInput"`:        "local DCI owner token input",
		`id="debugLatencySummary"`:       "LLM/TTS/STT/network latency summary",
		`id="debugSttTrace"`:             "STT trace log",
		`id="sourceRegistryBody"`:        "Source Registry panel",
		`id="sourceRegistryStagingBody"`: "Source Registry staging review panel",
		`id="memoryLayerBody"`:           "Memory layer panel",
		`id="micBtn"`:                    "normal chat voice input control",
		`id="idlePlaybackPlay"`:          "IdleChat playback control separated from mic input",
		`id="idlePlaybackNext"`:          "IdleChat next-topic playback control",
		`id="idlePlaybackPrevious"`:      "IdleChat previous-topic playback control",
		`id="audioBtn"`:                  "browser audio enable control",
		`id="sourceRegistrySaveBtn"`:     "Source Registry save control",
	}
	for needle, purpose := range required {
		if !strings.Contains(html, needle) {
			t.Fatalf("viewer.html missing %s (%s)", needle, purpose)
		}
	}

	micIndex := strings.Index(html, `id="micBtn"`)
	idleIndex := strings.Index(html, `id="idlePlaybackPlay"`)
	headerEnd := strings.Index(html, `</header>`)
	lipsyncIndex := strings.Index(html, `class="lipsync-stage"`)
	if micIndex < 0 || idleIndex < 0 {
		t.Fatal("mic and IdleChat playback controls must both be present")
	}
	if micIndex > idleIndex {
		t.Fatal("normal chat mic control should be in the normal input controls before IdleChat controls")
	}
	if headerEnd < 0 || lipsyncIndex < 0 || lipsyncIndex > headerEnd {
		t.Fatal("Mio/Shiro lipsync mini icons must be placed inside the top header band")
	}
}

func TestViewerStaticContractDCIUsesCanonicalOwnerReferences(t *testing.T) {
	htmlData, err := os.ReadFile("viewer.html")
	if err != nil {
		t.Fatalf("read viewer.html: %v", err)
	}
	html := string(htmlData)
	for _, required := range []string{
		`id="dciOwnerTokenInput"`,
		"Local-only owner operation",
		"memory only",
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("viewer.html missing DCI owner contract %q", required)
		}
	}
	jsData, err := os.ReadFile("assets/js/tabs/ops.js")
	if err != nil {
		t.Fatalf("read ops.js: %v", err)
	}
	js := string(jsData)
	start := strings.Index(js, "function dciField")
	end := strings.Index(js, "function sandboxField")
	if start < 0 || end <= start {
		t.Fatal("ops.js missing bounded DCI renderer")
	}
	dci := js[start:end]
	for _, required := range []string{
		"action_id",
		"trace_id",
		"evidence_id",
		"created_by_event_id",
		"dciOwnerBearerToken",
		"X-RenCrow-Client",
		"X-RenCrow-Interaction-Profile",
	} {
		if !strings.Contains(js, required) {
			t.Fatalf("ops.js missing DCI owner/canonical contract %q", required)
		}
	}
	for _, retired := range []string{"EventID", "Actor", "dciField(trace, 'event_id'", "pack.event_id", "result.Pack", "result.Trace"} {
		if strings.Contains(dci, retired) {
			t.Fatalf("ops.js DCI renderer retains retired field %q", retired)
		}
	}
	if strings.Contains(js, "localStorage") || strings.Contains(js, "sessionStorage") {
		t.Fatal("ops.js must not persist the DCI owner token in browser storage")
	}
}

func TestViewerStaticContractRendersOnlyTrustedGeneratedPNGInMidoriChat(t *testing.T) {
	timelineData, err := os.ReadFile("assets/js/tabs/timeline.js")
	if err != nil {
		t.Fatalf("read timeline.js: %v", err)
	}
	timeline := string(timelineData)
	for _, needle := range []string{
		`function renderTrustedGeneratedImages`,
		`/viewer/image/result?id=`,
		`generated-chat-image`,
		`ev.type === 'agent.response' && (ev.to || '').toLowerCase() === 'user'`,
	} {
		if !strings.Contains(timeline, needle) {
			t.Fatalf("timeline.js missing trusted Midori image contract %q", needle)
		}
	}
	for _, forbidden := range []string{
		`<img src="' + url`,
		`<img src="${url}`,
	} {
		if strings.Contains(timeline, forbidden) {
			t.Fatalf("timeline.js must not interpolate an arbitrary image URL: %q", forbidden)
		}
	}

	cssData, err := os.ReadFile("assets/css/viewer.css")
	if err != nil {
		t.Fatalf("read viewer.css: %v", err)
	}
	css := string(cssData)
	for _, needle := range []string{
		`.generated-chat-image`,
		`max-width:100%`,
		`height:auto`,
	} {
		if !strings.Contains(css, needle) {
			t.Fatalf("viewer.css missing generated image layout contract %q", needle)
		}
	}
}

func TestViewerStaticContractChatRecipientLabelFollowsRoleSelection(t *testing.T) {
	htmlData, err := os.ReadFile("viewer.html")
	if err != nil {
		t.Fatalf("read viewer.html: %v", err)
	}
	html := string(htmlData)
	for _, needle := range []string{`id="chatRecipientTitle"`, `id="inp"`} {
		if !strings.Contains(html, needle) {
			t.Fatalf("viewer.html missing selected chat recipient display contract %q", needle)
		}
	}

	rolesData, err := os.ReadFile("assets/js/tabs/roles.js")
	if err != nil {
		t.Fatalf("read roles.js: %v", err)
	}
	roles := string(rolesData)
	for _, needle := range []string{
		`function renderSelectedViewerChatRecipient`,
		`chatRecipientTitle`,
		`midori: 'Midori'`,
		`label + ' にメッセージを送る...'`,
	} {
		if !strings.Contains(roles, needle) {
			t.Fatalf("roles.js missing selected chat recipient display contract %q", needle)
		}
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
		`data-tab="movie-db-movies"`:             "movie list tab",
		`data-tab="movie-db-people"`:             "people list tab",
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
		`id="collectionPhaseSummary"`,
		`id="collectionAllItemsCount"`,
		`id="collectionAllItemsBody"`,
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
		"function renderCollectionLedgerRows(items)",
		"renderCollectionLedgerRows(items)",
		"collection.category_counts",
		"collection.source_read_status_counts",
		"collection.processing_status_counts",
		"collection.sources",
		"collection.enrichment_status",
		"collection.skill_id",
		"item.source_read_status",
		"item.processing_status",
		"item.processing_error",
		"item.translated_body",
		"item.summary",
		"item.term_notes",
		"item.perspective",
	} {
		if !strings.Contains(collectionJS, needle) {
			t.Fatalf("collection.js contract missing %q", needle)
		}
	}
	ledgerStart := strings.Index(collectionJS, "function renderCollectionLedgerRows(items)")
	if ledgerStart < 0 {
		t.Fatal("collection ledger renderer must remain a separate pure projection")
	}
	ledgerEnd := strings.Index(collectionJS[ledgerStart:], "function renderCollectionData()")
	if ledgerEnd < 0 {
		t.Fatal("collection ledger renderer must remain a separate pure projection")
	}
	ledgerRenderer := collectionJS[ledgerStart : ledgerStart+ledgerEnd]
	if !strings.Contains(ledgerRenderer, "return items.map((item, index)") || strings.Contains(ledgerRenderer, "visibleItems") {
		t.Fatal("collection ledger must render every collected item independently from detail filters")
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

func TestViewerStaticContractIdleChatEpisodeInventoryIsSelectableAndShowsFailures(t *testing.T) {
	htmlData, err := os.ReadFile("viewer.html")
	if err != nil {
		t.Fatalf("read viewer.html: %v", err)
	}
	jsData, err := os.ReadFile("assets/js/tabs/idlechat.js")
	if err != nil {
		t.Fatalf("read idlechat.js: %v", err)
	}
	cssData, err := os.ReadFile("assets/css/viewer.css")
	if err != nil {
		t.Fatalf("read viewer.css: %v", err)
	}

	html := string(htmlData)
	for _, needle := range []string{
		`data-idle-view="stock" type="button" role="tab" aria-controls="idleViewStock">Topic Stock</button>`,
		`data-idle-view="episodes" type="button" role="tab" aria-controls="idleViewEpisodes">Story Stock</button>`,
		`id="idleViewEpisodes"`,
		`<h3>物語ストック</h3>`,
		`class="idle-episode-list-head">物語専用リスト</h4>`,
		`id="idleEpisodeOverview"`,
		`id="idleEpisodeSelect"`,
		`id="idleEpisodeRefresh"`,
		`id="idleEpisodeList" class="idle-episode-list" aria-label="物語ストック一覧"`,
		`id="idleEpisodeDetail"`,
	} {
		if !strings.Contains(html, needle) {
			t.Fatalf("IdleChat episode selection UI missing %q", needle)
		}
	}

	js := string(jsData)
	for _, needle := range []string{
		"function refreshIdleEpisodes()",
		"function renderIdleEpisodes()",
		"function renderIdleEpisodeDetail()",
		"/viewer/idlechat/episodes",
		"first_invalid_turn",
		"validation.errors",
		"is-invalid",
		"is-repair-suffix",
		"needs_repair",
		"failed",
		"episode.story_title",
		"stock.untitled_ready",
	} {
		if !strings.Contains(js, needle) {
			t.Fatalf("IdleChat episode failure render contract missing %q", needle)
		}
	}

	css := string(cssData)
	for _, needle := range []string{
		".idle-episode-browser",
		".idle-episode-turn.is-invalid",
		".idle-episode-turn.is-repair-suffix",
	} {
		if !strings.Contains(css, needle) {
			t.Fatalf("IdleChat episode failure style missing %q", needle)
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
	if !strings.Contains(html, "viewer.css?v=20260805-idle-topic-playback") {
		t.Fatal("IdleChat stock layout must invalidate the Viewer CSS cache")
	}
}

func TestViewerStaticContractMovieDatabaseTabSwitchMapping(t *testing.T) {
	data, err := os.ReadFile("assets/js/viewer.js")
	if err != nil {
		t.Fatalf("read viewer.js: %v", err)
	}
	js := string(data)
	for _, route := range []string{"movie-db-movies", "movie-db-people"} {
		if !strings.Contains(js, `'`+route+`': document.getElementById('panel-movie-db')`) {
			t.Fatalf("viewer.js missing %s panel switch mapping", route)
		}
	}
	if !strings.Contains(js, `const activePanel = panels[tab]`) {
		t.Fatal("shared movie panel routes must activate the selected panel once")
	}
	if !strings.Contains(js, `movieDbSetMode(tab === 'movie-db-people' ? 'people' : 'movies')`) {
		t.Fatal("movie list routes must select the matching catalog mode")
	}
}

func TestViewerStaticContractMemoryDatabaseAccordionUsesLeftNavigationColumn(t *testing.T) {
	htmlData, err := os.ReadFile("viewer.html")
	if err != nil {
		t.Fatalf("read viewer.html: %v", err)
	}
	jsData, err := os.ReadFile("assets/js/viewer.js")
	if err != nil {
		t.Fatalf("read viewer.js: %v", err)
	}
	cssData, err := os.ReadFile("assets/css/viewer.css")
	if err != nil {
		t.Fatalf("read viewer.css: %v", err)
	}
	html := string(htmlData)
	js := string(jsData)
	css := string(cssData)

	navStart := strings.Index(html, `<nav class="tabs"`)
	navEnd := strings.Index(html[navStart:], `</nav>`)
	if navStart < 0 || navEnd < 0 {
		t.Fatal("Viewer left navigation is missing")
	}
	nav := html[navStart : navStart+navEnd]
	for needle, purpose := range map[string]string{
		`id="memoryNavToggle"`:                        "Memory accordion trigger",
		`aria-expanded="false"`:                       "collapsed initial state",
		`aria-controls="memoryDbNav"`:                 "accessible accordion relationship",
		`id="memoryDbNav"`:                            "database entries in the same navigation column",
		`class="memory-db-nav" hidden`:                "collapsed database list",
		`data-tab="memory" data-memory-db-tab="true"`: "L1 database Viewer entry",
		`data-tab="memory-archive"`:                   "conversation archive Viewer entry",
		`data-tab="knowledge-memory"`:                 "Knowledge Memory Viewer entry",
		`data-tab="glossary-db"`:                      "glossary Viewer entry",
		`data-tab="movie-db-movies"`:                  "movie list Viewer entry",
		`data-tab="movie-db-people"`:                  "people list Viewer entry",
		`data-tab="tool-registry"`:                    "Tool Registry Viewer entry",
	} {
		if !strings.Contains(nav, needle) {
			t.Fatalf("left navigation missing %s (%s)", needle, purpose)
		}
	}
	for _, label := range []string{"映画リスト", "人物リスト"} {
		if !strings.Contains(nav, `>`+label+`</button>`) {
			t.Fatalf("left navigation missing %q", label)
		}
	}
	if strings.Contains(html, `memory-db-secondary`) {
		t.Fatal("database navigation must not introduce a second navigation column")
	}
	for _, needle := range []string{
		`id="panel-memory-archive"`,
		`id="panel-knowledge-memory"`,
		`id="panel-glossary-db"`,
		`id="panel-tool-registry"`,
		`<optgroup label="Memory">`,
	} {
		if !strings.Contains(html, needle) {
			t.Fatalf("Viewer missing database navigation contract %q", needle)
		}
	}
	for _, needle := range []string{
		`const memoryNavToggle = document.getElementById('memoryNavToggle')`,
		`const memoryDbNav = document.getElementById('memoryDbNav')`,
		`'knowledge-memory'`,
		`function setMemoryDatabaseNavigationExpanded(expanded)`,
		`memoryDbTabs.has(tab)`,
		`tab === 'knowledge-memory' && typeof refreshKnowledgeMemoryLedger === 'function'`,
	} {
		if !strings.Contains(js, needle) {
			t.Fatalf("viewer.js missing Memory accordion behavior %q", needle)
		}
	}
	for _, needle := range []string{
		`.memory-nav-toggle`,
		`.memory-db-nav`,
		`.memory-db-tab`,
	} {
		if !strings.Contains(css, needle) {
			t.Fatalf("viewer.css missing Memory accordion styling %q", needle)
		}
	}
}

func TestViewerStaticContractDatabasePanelsDoNotMixConversationStores(t *testing.T) {
	htmlData, err := os.ReadFile("viewer.html")
	if err != nil {
		t.Fatalf("read viewer.html: %v", err)
	}
	memoryJSData, err := os.ReadFile("assets/js/tabs/memory.js")
	if err != nil {
		t.Fatalf("read memory.js: %v", err)
	}
	html := string(htmlData)
	memoryJS := string(memoryJSData)

	panel := func(id string) string {
		start := strings.Index(html, `<section id="panel-`+id+`"`)
		if start < 0 {
			t.Fatalf("missing panel %s", id)
		}
		end := strings.Index(html[start:], `</section>`)
		if end < 0 {
			t.Fatalf("panel %s is not closed", id)
		}
		return html[start : start+end]
	}

	l1 := panel("memory")
	archive := panel("memory-archive")
	knowledge := panel("knowledge-memory")
	for _, forbidden := range []string{"Knowledge Memory Ledger", `id="knowledgeMemoryBody"`, `id="knowledgeMemoryDetail"`, `id="memoryL2Count"`, `Archive L2`, `id="memoryArchiveBody"`} {
		if strings.Contains(l1, forbidden) {
			t.Fatalf("Conversation L1 panel mixes Archive/Knowledge content %q", forbidden)
		}
	}
	if strings.Contains(archive, `id="knowledgeMemoryBody"`) || strings.Contains(archive, `id="knowledgeMemoryDetail"`) {
		t.Fatal("Conversation Archive panel must not contain Knowledge Memory controls")
	}
	if strings.Contains(knowledge, `id="memoryArchiveBody"`) || strings.Contains(knowledge, `id="memoryArchiveSession"`) {
		t.Fatal("Knowledge Memory panel must not contain Conversation Archive controls")
	}

	renderStart := strings.Index(memoryJS, "function renderMemoryLayers")
	renderEnd := strings.Index(memoryJS, "function refreshMemoryLayers")
	if renderStart < 0 || renderEnd <= renderStart {
		t.Fatal("memory layer render functions are missing")
	}
	render := memoryJS[renderStart:renderEnd]
	if strings.Contains(render, "l2") || strings.Contains(render, "L2") || strings.Contains(render, "memoryL2Count") {
		t.Fatal("Conversation L1 renderer must not render Archive L2")
	}
	snapshotStart := strings.Index(memoryJS, "function refreshMemorySnapshot")
	if snapshotStart < 0 {
		t.Fatal("memory snapshot refresh function is missing")
	}
	snapshotEnd := strings.Index(memoryJS[snapshotStart:], "\nfunction postMemoryAction")
	if snapshotEnd <= 0 {
		t.Fatal("memory snapshot refresh boundary is missing")
	}
	snapshot := memoryJS[snapshotStart : snapshotStart+snapshotEnd]
	if strings.Contains(snapshot, "refreshKnowledgeMemoryLedger();") {
		t.Fatal("Conversation L1 refresh must not fetch Knowledge Memory")
	}
}

func TestViewerStaticContractMobileMemoryDatabaseOptgroupMatchesDesktop(t *testing.T) {
	data, err := os.ReadFile("viewer.html")
	if err != nil {
		t.Fatalf("read viewer.html: %v", err)
	}
	html := string(data)
	optgroupStart := strings.Index(html, `<optgroup label="Memory">`)
	optgroupEnd := strings.Index(html[optgroupStart:], `</optgroup>`)
	if optgroupStart < 0 || optgroupEnd < 0 {
		t.Fatal("mobile Memory optgroup is missing")
	}
	optgroup := html[optgroupStart : optgroupStart+optgroupEnd]
	for _, value := range []string{"memory", "memory-archive", "knowledge-memory", "glossary-db", "movie-db-movies", "movie-db-people", "tool-registry"} {
		if !strings.Contains(optgroup, `value="`+value+`"`) {
			t.Fatalf("mobile Memory optgroup missing %q", value)
		}
	}
	if strings.Contains(optgroup, `value="news-pack"`) || strings.Contains(optgroup, `value="collection"`) {
		t.Fatal("mobile Memory optgroup must not absorb non-database tabs")
	}
}

func TestViewerStaticContractMovieAssessmentGrid(t *testing.T) {
	htmlData, err := os.ReadFile("viewer.html")
	if err != nil {
		t.Fatalf("read viewer.html: %v", err)
	}
	jsData, err := os.ReadFile("assets/js/tabs/movie-db.js")
	if err != nil {
		t.Fatalf("read movie-db.js: %v", err)
	}
	html := string(htmlData)
	js := string(jsData)

	for needle, purpose := range map[string]string{
		`data-tab="movie-db-movies" data-movie-db-mode="movies"`: "left movie list selector",
		`data-tab="movie-db-people" data-movie-db-mode="people"`: "left people list selector",
		`id="movieDbRows" class="movie-db-rows"`:                 "single list surface",
	} {
		if !strings.Contains(html, needle) {
			t.Fatalf("viewer.html missing %s (%s)", needle, purpose)
		}
	}
	for _, needle := range []string{
		`return '<tr><th>人物</th><th>みた</th><th>すき</th></tr>'`,
		`return '<tr><th>映画</th><th>みた</th><th>すき</th></tr>'`,
		`movieDbAssessmentToggleHTML(item, 'familiarity', 'seen', 'みた')`,
		`movieDbAssessmentToggleHTML(item, 'familiarity', 'known', 'みた')`,
		`movieDbAssessmentToggleHTML(item, 'sentiment', 'like', 'すき')`,
		"movie-db-assessment-toggle",
		"dimension: dimension",
		"value: value",
		`const value = control.checked ? String(control.dataset.value || '') : '';`,
	} {
		if !strings.Contains(js, needle) {
			t.Fatalf("movie assessment grid contract missing %q", needle)
		}
	}
	for _, forbidden := range []string{"見てない", "嫌い", "知ってる", "知らない", `params.set('role', '出演')`} {
		if strings.Contains(js, forbidden) {
			t.Fatalf("movie assessment list retains forbidden control/filter %q", forbidden)
		}
	}
	for _, forbidden := range []string{`id="movieDbModeMovies"`, `id="movieDbModePeople"`} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("movie screen retains hidden in-panel selector %q", forbidden)
		}
	}
	for _, forbidden := range []string{
		`class="movie-db-side"`,
		`class="movie-db-cards"`,
		`class="movie-db-toolbar"`,
		`class="movie-db-list-head"`,
		`class="movie-db-detail"`,
	} {
		if strings.Contains(html, forbidden) || strings.Contains(js, forbidden) {
			t.Fatalf("single-row list retains secondary surface %q", forbidden)
		}
	}
	for _, needle := range []string{`limit: 50`, `movieDbLoadNextPage`, `rows.addEventListener('scroll'`, `'<table class="movie-db-table"><tbody>'`} {
		if !strings.Contains(js, needle) {
			t.Fatalf("single list infinite-scroll contract missing %q", needle)
		}
	}
}

func TestViewerStaticContractMovieD0D1CardsAndResolverCandidates(t *testing.T) {
	htmlData, err := os.ReadFile("viewer.html")
	if err != nil {
		t.Fatalf("read viewer.html: %v", err)
	}
	jsData, err := os.ReadFile("assets/js/tabs/movie-db.js")
	if err != nil {
		t.Fatalf("read movie-db.js: %v", err)
	}
	html := string(htmlData)
	js := string(jsData)
	for _, needle := range []string{
		`movieDbRefreshCards`,
		`/viewer/movie-catalog?action=cards`,
		`D0`,
		`D1`,
		`item.label`,
		`data-url`,
	} {
		if !strings.Contains(html, needle) && !strings.Contains(js, needle) {
			t.Fatalf("movie D0/D1 card contract missing %q", needle)
		}
	}
	for _, forbidden := range []string{
		`eiga.com/search`,
		`映画.comで検索`,
	} {
		if strings.Contains(html, forbidden) || strings.Contains(js, forbidden) {
			t.Fatalf("movie Viewer must not generate search links: %q", forbidden)
		}
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
	htmlData, err := os.ReadFile("viewer.html")
	if err != nil {
		t.Fatalf("read viewer.html: %v", err)
	}
	html := string(htmlData)
	for _, needle := range []string{
		`id="investmentDependencyCard"`,
		`id="investmentPortfolioCard"`,
		`id="investmentCapabilityCard"`,
		`RenCrow_TRADE owner projection`,
	} {
		if !strings.Contains(html, needle) {
			t.Fatalf("Investment Viewer missing owner projection contract %q", needle)
		}
	}
	for _, forbidden := range []string{
		`id="investmentSnapshotBody"`,
		`id="investmentSourceBody"`,
		`id="investmentFeatureBody"`,
		`id="investmentEventBody"`,
		`DB path`,
	} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("Investment Viewer must not expose retired local database contract %q", forbidden)
		}
	}
}

func TestViewerStaticContractInvestmentUsesTradeOwnerProjection(t *testing.T) {
	data, err := os.ReadFile("assets/js/tabs/investment.js")
	if err != nil {
		t.Fatalf("read investment.js: %v", err)
	}
	js := string(data)
	if !strings.Contains(js, "fetch('/viewer/trade/status") {
		t.Fatal("Investment tab must read the canonical RenCrow_TRADE status projection")
	}
	for _, forbidden := range []string{
		"/viewer/investment/status",
		"/viewer/investment/notify",
		"db_path",
		"investmentSnapshotBody",
		"investmentSourceBody",
		"investmentFeatureBody",
		"investmentEventBody",
	} {
		if strings.Contains(js, forbidden) {
			t.Fatalf("Investment tab must not depend on retired local investment data %q", forbidden)
		}
	}
}

func TestViewerStaticContractRetiredInvestmentRoutesAreNotRegistered(t *testing.T) {
	data, err := os.ReadFile("../../features/viewer/registrar.go")
	if err != nil {
		t.Fatalf("read viewer registrar: %v", err)
	}
	registrar := string(data)
	for _, forbidden := range []string{
		"/viewer/investment/status",
		"/viewer/investment/notify",
		"InvestmentStatus",
		"InvestmentNotify",
	} {
		if strings.Contains(registrar, forbidden) {
			t.Fatalf("Viewer registrar still exposes retired investment route wiring %q", forbidden)
		}
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
		t.Fatal("Now Playing offset must stay clear of the composer")
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

func TestOpsCanonicalEventProjectionUsesEnvelopeAndPayloadFields(t *testing.T) {
	data, err := os.ReadFile("assets/js/tabs/ops.js")
	if err != nil {
		t.Fatalf("read ops.js: %v", err)
	}
	js := string(data)
	for _, required := range []string{"function canonicalEventField", "run_reference", "run.lead_agent_paused", "run.resume_queued", "command.invoked"} {
		if !strings.Contains(js, required) {
			t.Fatalf("ops.js missing canonical Event projection contract %q", required)
		}
	}
	for _, retired := range []string{"'lead_agent_paused'", "'lead_agent_resumed'", "run.lead_agent_resumed", "'command_invoked'", "payload_summary"} {
		if strings.Contains(js, retired) {
			t.Fatalf("ops.js retains legacy Event field/type %q", retired)
		}
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
	if !strings.Contains(page, "ops.js?v=20260806-prompt-logs-panel") {
		t.Fatal("viewer.html missing Prompt Debug Ops cache buster")
	}
	if !strings.Contains(page, "games.js?v=20260729-agent-launch") {
		t.Fatal("viewer.html missing Games tab cache buster")
	}
	if !strings.Contains(page, "viewer.js?v=20260806-prompt-logs-panel") {
		t.Fatal("viewer.html missing Prompt Debug viewer cache buster")
	}
}

func TestViewerStaticContractPromptDebugLogPresentation(t *testing.T) {
	page, err := os.ReadFile("viewer.html")
	if err != nil {
		t.Fatal(err)
	}
	promptJS, err := os.ReadFile("assets/js/tabs/prompt-logs.js")
	if err != nil {
		t.Fatal(err)
	}
	promptCSS, err := os.ReadFile("assets/css/tabs/prompt-logs.css")
	if err != nil {
		t.Fatal(err)
	}
	opsJS, err := os.ReadFile("assets/js/tabs/ops.js")
	if err != nil {
		t.Fatal(err)
	}
	opsCSS, err := os.ReadFile("assets/css/tabs/ops.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		`<option value="prompt-logs">Character Latest Prompt</option>`,
		`data-tab="prompt-logs"`,
		`id="panel-prompt-logs"`,
		"promptDebugRefreshBtn",
		"promptDebugCharacterList",
		"promptDebugInternalList",
		"Character Latest Prompt",
		"Internal / Worker Raw Details",
		"prompt-legend-block prompt-block-00",
		"prompt-legend-age age-recent",
		"prompt-legend-level level-error",
		"<th>Level</th>",
	} {
		if !strings.Contains(string(page), required) {
			t.Fatalf("viewer.html missing Prompt Debug log contract: %s", required)
		}
	}
	if strings.Count(string(page), `id="promptDebugCharacterList"`) != 1 {
		t.Fatal("viewer.html must contain exactly one independent Character Latest Prompt list")
	}
	for _, required := range []string{
		`prompt-logs.css?v=20260806-prompt-logs-panel`,
		`prompt-logs.js?v=20260806-prompt-logs-panel`,
	} {
		if !strings.Contains(string(page), required) {
			t.Fatalf("viewer.html missing Prompt Debug independent asset: %s", required)
		}
	}
	viewerJS, err := os.ReadFile("assets/js/viewer.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"/viewer/prompt-debug?limit=40",
		`'prompt-logs': document.getElementById('panel-prompt-logs')`,
		"shouldRefreshPromptLogsPanel",
		"promptDebugRenderSignature",
	} {
		if !strings.Contains(string(viewerJS), required) {
			t.Fatalf("viewer.js missing Prompt Debug tab wiring: %s", required)
		}
	}
	for _, required := range []string{
		"function renderPromptDebug()",
		"function promptDebugProjectionSignature()",
		"function capturePromptDebugViewState()",
		"function restorePromptDebugViewState(viewState)",
		"function promptDebugExchangeCard",
		"function promptDebugEmptyCharacterCard",
		"function promptDebugAgeClass",
		"prompt-debug-block",
		"送信Payload全文",
	} {
		if !strings.Contains(string(promptJS), required) {
			t.Fatalf("prompt-logs.js missing Prompt Debug log contract: %s", required)
		}
	}
	for _, required := range []string{
		".prompt-debug-block-00",
		".prompt-debug-block-10",
		".prompt-debug-block-20",
	} {
		if !strings.Contains(string(promptCSS), required) {
			t.Fatalf("prompt-logs.css missing Prompt Debug log color contract: %s", required)
		}
	}
	for _, required := range []string{
		".ops-log-row.age-hour",
		".ops-log-row.level-error",
		".ops-log-level.level-error",
	} {
		if !strings.Contains(string(opsCSS), required) {
			t.Fatalf("ops.css missing Ops event log color contract: %s", required)
		}
	}
	if strings.Contains(string(opsJS), "renderPromptDebug") {
		t.Fatal("ops.js must not own the independent Prompt Debug renderer")
	}
}

func TestViewerStaticContractGamesAgentOwnedLaunchDesk(t *testing.T) {
	htmlData, err := os.ReadFile("viewer.html")
	if err != nil {
		t.Fatalf("read viewer.html: %v", err)
	}
	gamesData, err := os.ReadFile("assets/js/tabs/games.js")
	if err != nil {
		t.Fatalf("read games.js: %v", err)
	}
	cssData, err := os.ReadFile("assets/css/tabs/desk.css")
	if err != nil {
		t.Fatalf("read desk.css: %v", err)
	}

	html := string(htmlData)
	games := string(gamesData)
	css := strings.ReplaceAll(string(cssData), "\r\n", "\n")

	for needle, purpose := range map[string]string{
		`id="gamesLaunchForm"`:    "Agent-owned launch form",
		`id="gamesLaunchGame"`:    "game_id selector",
		`id="gamesPersonaMio"`:    "Mio participant control",
		`id="gamesPersonaShiro"`:  "Shiro participant control",
		`id="gamesPersonaKuro"`:   "Kuro participant control",
		`id="gamesPersonaMidori"`: "Midori participant control",
		`id="gamesLaunchReason"`:  "optional launch reason",
		`id="gamesLaunchTurns"`:   "optional turn limit",
		`id="gamesLaunchMode"`:    "optional game mode",
		`id="gamesLaunchBtn"`:     "launch action",
		`id="gamesLaunchResult"`:  "launch success or upstream error feedback",
		`aria-label="参加ペルソナ"`:     "participant group label",
	} {
		if !strings.Contains(html, needle) {
			t.Fatalf("viewer.html missing %s (%s)", needle, purpose)
		}
	}

	for _, needle := range []string{
		"function submitGamesLaunch",
		"fetch('/viewer/games/launch'",
		"method: 'POST'",
		"'Content-Type': 'application/json'",
		"personas: personas",
		"/viewer/games/observer?session=",
		"response.session_id",
		"gamesLaunchResult",
		"if (!response.ok || data.ok === false)",
		"data.message || data.error",
		"renderGamesLaunchResult('error'",
	} {
		if !strings.Contains(games, needle) {
			t.Fatalf("games.js missing Agent-owned launch contract %q", needle)
		}
	}

	for _, forbidden := range []string{
		"launchPersonaLimits",
		"personaLimits",
	} {
		if strings.Contains(games, forbidden) {
			t.Fatalf("games.js must leave title-specific persona limits to RenCrow_GAMES: found %q", forbidden)
		}
	}

	for _, needle := range []string{
		".games-launch-form {",
		".games-launch-control {",
		"min-height: 40px;",
		".games-launch-personas {",
		".games-launch-result {",
		"overflow-wrap: anywhere;",
		"@media (max-width: 900px), (max-aspect-ratio: 21/20)",
	} {
		if !strings.Contains(css, needle) {
			t.Fatalf("desk.css missing Games launch desk contract %q", needle)
		}
	}
}

func TestViewerStaticContractAtlasProjectionAndOwnerDecisionGUI(t *testing.T) {
	htmlData, err := os.ReadFile("viewer.html")
	if err != nil {
		t.Fatalf("read viewer.html: %v", err)
	}
	jsData, err := os.ReadFile("assets/js/tabs/atlas.js")
	if err != nil {
		t.Fatalf("read atlas.js: %v", err)
	}
	viewerData, err := os.ReadFile("assets/js/viewer.js")
	if err != nil {
		t.Fatalf("read viewer.js: %v", err)
	}
	cssData, err := os.ReadFile("assets/css/viewer.css")
	if err != nil {
		t.Fatalf("read viewer.css: %v", err)
	}

	html := string(htmlData)
	atlas := string(jsData)
	viewer := string(viewerData)
	css := string(cssData)
	for _, needle := range []string{
		`data-tab="atlas"`,
		`<option value="atlas">Atlas</option>`,
		`id="panel-atlas"`,
		`id="atlasRoot"`,
		`id="atlasSummaryCards"`,
		`data-atlas-tab="current"`,
		`data-atlas-tab="radar"`,
		`data-atlas-tab="backlog"`,
		`data-atlas-tab="pipeline"`,
		`data-atlas-tab="evidence"`,
		`data-atlas-tab="modules"`,
		`/viewer/assets/js/tabs/atlas.js`,
	} {
		if !strings.Contains(html, needle) {
			t.Fatalf("viewer.html missing Atlas Viewer contract %q", needle)
		}
	}
	for _, needle := range []string{
		`fetch('/viewer/atlas'`,
		`cache: 'no-store'`,
		`function atlasRenderItemDetail`,
		`function atlasOpenItemDetail`,
		`atlasRenderItemDetail('current')`,
		`atlasRenderItemDetail('radar')`,
		`atlasRenderItemDetail('backlog')`,
		`data-atlas-item-id`,
		`'/viewer/atlas/items/' + encodeURIComponent(itemID)`,
		`'/viewer/atlas/specifications/' + encodeURIComponent(specID)`,
		`specifications`,
		`content_available`,
		`content_sha256`,
		`captured_at`,
		`Purpose`,
		`Problem`,
		`Idea`,
		`Background`,
		`Expected Effect`,
		`Relations`,
		`Lifecycle Owner`,
		`Target Modules`,
		`Consumer Modules`,
		`Affected Modules`,
		`Specification`,
		`Sources`,
		`Source Strength`,
		`Implementation Status`,
		`Evidence`,
		`atlasEscape(artifact.content`,
		`catalog`,
		`features`,
		`current`,
		`radar`,
		`backlog`,
		`queue`,
		`evidence`,
		`modules`,
		`function refreshAtlas`,
		`function atlasRenderPipeline`,
		`function atlasRenderEvidence`,
		`function atlasRenderModules`,
		`function atlasRenderOwnerDecision`,
		`function atlasRenderIntakeForm`,
		`function atlasRenderMaturationMetrics`,
		`function atlasRenderRevalidationRecord`,
		`function atlasOwnerPost`,
		`method: 'POST'`,
		`'Authorization': 'Bearer ' + atlasOwnerToken`,
		`'X-RenCrow-Client': 'RenCrow_CMD'`,
		`'X-RenCrow-Interaction-Profile': 'cmd-control'`,
		`'/v1/atlas/intake'`,
		`base + 'candidate'`,
		`base + 'revalidate'`,
		`base + 'enrich'`,
		`base + 'adopt'`,
		`<option value="PROMOTE">`,
		`<option value="HOLD">`,
		`<option value="DROP">`,
		`Maturation metrics`,
		`maturation_metrics`,
		`Receipt`,
		`Risk: 新しい責務・維持コスト`,
		`Risk: 責務境界の誤統合`,
	} {
		if !strings.Contains(atlas, needle) {
			t.Fatalf("atlas.js missing projection or owner GUI contract %q", needle)
		}
	}
	for _, forbidden := range []string{
		`localStorage`,
		`sessionStorage`,
		`file://`,
		`file_path`,
		`spec_path`,
		`<option value="MERGE">`,
		`/viewer/backlog', {method: 'POST'`,
	} {
		if strings.Contains(atlas, forbidden) {
			t.Fatalf("atlas.js violates owner GUI boundary: found %q", forbidden)
		}
	}
	for _, needle := range []string{
		`atlas: document.getElementById('panel-atlas')`,
		`tab === 'atlas'`,
		`typeof atlasRender === 'function'`,
	} {
		if !strings.Contains(viewer, needle) {
			t.Fatalf("viewer.js missing Atlas tab hook %q", needle)
		}
	}
	for _, needle := range []string{
		`.atlas-summary-grid`,
		`.atlas-detail`,
		`.atlas-detail-grid`,
		`.atlas-spec-body`,
		`.atlas-active-unit`,
		`.atlas-stage-list`,
		`.atlas-evidence-timeline`,
		`.atlas-module-grid`,
		`@media (max-width:640px)`,
		`overflow-wrap:anywhere`,
	} {
		if !strings.Contains(css, needle) {
			t.Fatalf("viewer.css missing Atlas layout contract %q", needle)
		}
	}
}

func TestViewerAtlasCapturesOwnerInputBeforeBusyRender(t *testing.T) {
	jsData, err := os.ReadFile("assets/js/tabs/atlas.js")
	if err != nil {
		t.Fatalf("read atlas.js: %v", err)
	}
	source := string(jsData)
	functionStart := strings.Index(source, "async function atlasRunOwnerAction(action, root)")
	if functionStart < 0 {
		t.Fatal("atlasRunOwnerAction is missing")
	}
	functionBody := source[functionStart:]
	capture := strings.Index(functionBody, "const input = {")
	render := strings.Index(functionBody, "atlasRender();")
	if capture < 0 || render < 0 || capture > render {
		t.Fatal("owner input must be captured before atlasRender replaces the form controls")
	}
}
