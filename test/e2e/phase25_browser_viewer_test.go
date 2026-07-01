//go:build e2e

package e2e_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestE2E_Phase25BrowserViewerSessionContract(t *testing.T) {
	if os.Getenv("PICOCLAW_BROWSER_E2E") != "1" {
		t.Skip("set PICOCLAW_BROWSER_E2E=1 to verify Viewer in a real browser session")
	}
	repoRoot := findRepoRoot(t)
	playwrightBin := filepath.Join(repoRoot, "node_modules", ".bin", "playwright")
	if _, err := os.Stat(playwrightBin); err != nil {
		t.Skipf("playwright binary is not available at %s: %v", playwrightBin, err)
	}

	baseURL := strings.TrimRight(os.Getenv("PICOCLAW_LIVE_BASE_URL"), "/")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:18790"
	}
	message := "Phase25 browser e2e " + time.Now().UTC().Format("20060102T150405Z")

	script := `const { chromium } = require('playwright');
const baseURL = process.env.PICOCLAW_LIVE_BASE_URL || 'http://127.0.0.1:18790';
const message = process.env.PHASE25_BROWSER_MESSAGE;
(async () => {
  const browser = await chromium.launch({ headless: true });
  const page = await browser.newPage({ viewport: { width: 1366, height: 900 } });
  try {
    await page.goto(baseURL + '/viewer', { waitUntil: 'domcontentloaded', timeout: 15000 });
    await page.waitForSelector('#inp', { timeout: 10000 });
    for (const selector of ['#chat', '#opsFeedBody', '#idleLiveLog', '#ttsNowPlaying', '#lipSyncMio', '#lipSyncShiro', '#micBtn', '#idleStart']) {
      await page.waitForSelector(selector, { state: 'attached', timeout: 5000 });
    }
    await page.click('.tab-btn[data-tab="timeline"]');
    await page.waitForSelector('#chat', { state: 'visible', timeout: 10000 });
    const micVisible = await page.locator('#micBtn').isVisible();
    if (!micVisible) {
      throw new Error('normal chat mic must be visible on Chat tab');
    }
    await page.click('.tab-btn[data-tab="idlechat"]');
    await page.waitForSelector('#idleStart', { state: 'visible', timeout: 10000 });
    const idleVisible = await page.locator('#idleStart').isVisible();
    if (!idleVisible) {
      throw new Error('IdleChat start control must be visible on IdleChat tab');
    }
    await page.click('.tab-btn[data-tab="ops"]');
    await page.waitForSelector('#runtimeReadinessCards', { state: 'visible', timeout: 10000 });
    await page.waitForFunction(() => {
      const text = document.querySelector('#runtimeReadinessCards')?.innerText || '';
      return text.includes('LLM Ops') &&
        text.includes('Runtime Health') &&
        text.includes('STT') &&
        text.includes('TTS') &&
        text.includes('Slack') &&
        text.includes('Discord') &&
        text.includes('Telegram') &&
        text.includes('Distributed') &&
        text.includes('Source Registry') &&
        text.includes('Knowledge Memory') &&
        text.includes('Browser Trace API') &&
        text.includes('Sandbox') &&
        text.includes('configured:present') &&
        text.includes('service:missing') &&
        text.includes('chat:missing') &&
        text.includes('worker:missing') &&
        text.includes('blocked:') &&
        text.includes('proxy:present') &&
        text.includes('live:missing') &&
        text.includes('blocked: HTTP 502: upstream unreachable') &&
        text.includes('config:present') &&
        text.includes('credentials:missing') &&
        text.includes('webhook:missing') &&
        text.includes('file:missing') &&
        text.includes('blocked: real external API file event E2E not verified') &&
        text.includes('health:missing') &&
        text.includes('blocked: real microphone STT E2E not verified') &&
        text.includes('live:missing') &&
        text.includes('ready:missing') &&
        text.includes('blocked: browser audio playback/lip sync E2E not verified') &&
        text.includes('ssh-connected:missing') &&
        text.includes('blocked: distributed disabled') &&
        text.includes('blocked: Wild SSH/multi-machine E2E not verified') &&
        text.includes('memory-layers:missing') &&
        text.includes('memory-route:present') &&
        text.includes('source:missing') &&
        text.includes('source-route:present') &&
        text.includes('blocked: conversation L1 disabled') &&
        text.includes('/viewer/knowledge-memory') &&
        text.includes('fetcher:present') &&
        text.includes('review-only: discover and fetcher proposal require evidence') &&
        text.includes('status:present') &&
        text.includes('blocked: sandbox disabled');
    }, { timeout: 10000 });
    await page.waitForFunction(() => {
      const text = document.querySelector('#opsCards')?.innerText || '';
      return text.includes('Tool Harness') &&
        text.includes('provider protocol recovery not verified') &&
        text.includes('DCI Trace') &&
        text.includes('VectorDB/Qdrant E2E not verified');
    }, { timeout: 10000 });
    await page.waitForFunction(() => {
      const text = document.querySelector('#panel-ops')?.innerText || '';
      return text.includes('sandbox promotion gate logs:') &&
        text.includes('needs-review') &&
        text.includes('post-apply evidence') &&
        text.includes('formal apply requires human approval') &&
        text.includes('blocked: no promotion applied');
    }, { timeout: 10000 });
    await page.waitForFunction(() => {
      const text = document.querySelector('#opsCards')?.innerText || '';
      return text.includes('SuperAgent Harness') &&
        text.includes('run queue:') &&
        text.includes('scheduler:disabled') &&
        text.includes('blocked: scheduler disabled');
    }, { timeout: 10000 });
    await page.waitForFunction(() => {
      const text = document.querySelector('#panel-ops')?.innerText || '';
      return text.includes('SuperAgent Terminal Audits') &&
        text.includes('superagent terminal audits:') &&
        text.includes('terminal runs') &&
        text.includes('failed runs') &&
        text.includes('missing evidence:');
    }, { timeout: 10000 });
    await page.waitForFunction(() => {
      const text = document.querySelector('#panel-ops')?.innerText || '';
      return text.includes('SuperAgent Resume Audits') &&
        text.includes('superagent resume audits:') &&
        text.includes('resume queue') &&
        text.includes('manual-ledger') &&
        text.includes('runtime-control') &&
        text.includes('blocked: true long-running resume not verified');
    }, { timeout: 10000 });
    await page.waitForFunction(() => {
      const text = document.querySelector('#panel-ops')?.innerText || '';
      return text.includes('Skill Governance External PR Submit Audits') &&
        text.includes('skill external PR submit audits:') &&
        text.includes('not created') &&
        text.includes('external PR adapter: unconfigured') &&
        text.includes('human approval: required') &&
        text.includes('blocked: no external PR created') &&
        text.includes('created') &&
        text.includes('verified');
    }, { timeout: 10000 });
    await page.waitForFunction(() => {
      const text = document.querySelector('#panel-ops')?.innerText || '';
      return text.includes('Skill Evidence Audits') &&
        text.includes('skill evidence audits:') &&
        text.includes('triggers') &&
        text.includes('contribution gates') &&
        text.includes('coder transcripts') &&
        text.includes('blocked: coder evidence transcript not observed') &&
        text.includes('blocked: passed contribution gate is not external PR evidence');
    }, { timeout: 10000 });
    await page.waitForFunction(() => {
      const text = document.querySelector('#opsCards')?.innerText || '';
      return text.includes('AI Workflow') &&
        text.includes('commands:') &&
        text.includes('context usage:') &&
        text.includes('context-budget:disabled') &&
        text.includes('blocked: context budget disabled');
    }, { timeout: 10000 });
    await page.waitForFunction(() => {
      const text = document.querySelector('#opsCards')?.innerText || '';
      return text.includes('Heavy Runtime') &&
        text.includes('route: ANALYZE') &&
        text.includes('Worker') &&
        text.includes('llm-ops GET /v1/status');
    }, { timeout: 10000 });
    await page.waitForFunction(() => {
      const text = document.querySelector('#panel-ops')?.innerText || '';
      return text.includes('AI Workflow Run Evidence') &&
        text.includes('ai workflow run evidence:') &&
        text.includes('command-context-trace same-run') &&
        text.includes('blocked: scheduler normal completion not verified');
    }, { timeout: 10000 });
    await page.waitForFunction(() => {
      const text = document.querySelector('#opsCards')?.innerText || '';
      return text.includes('Workstreams') &&
        text.includes('waiting goals:') &&
        text.includes('pending-review:') &&
        text.includes('vault updates:') &&
        (text.includes('vault applied:') || text.includes('blocked: no vault apply'));
    }, { timeout: 10000 });
    await page.waitForFunction(() => {
      const text = document.querySelector('#panel-ops')?.innerText || '';
      return text.includes('Workstream Vault Review') &&
        text.includes('workstream vault review:') &&
        text.includes('applied');
    }, { timeout: 10000 });
    await page.waitForFunction(() => {
      const text = document.querySelector('#opsCards')?.innerText || '';
      return text.includes('Complexity Hotspots') &&
        text.includes('reports:') &&
        text.includes('pending-review:') &&
        text.includes('mode: review-only') &&
        text.includes('blocked: no patch applied');
    }, { timeout: 10000 });
    await page.waitForFunction(() => {
      const text = document.querySelector('#panel-ops')?.innerText || '';
      return text.includes('Complexity Review Artifacts') &&
        text.includes('complexity review artifacts:') &&
        text.includes('pending-review') &&
        text.includes('patch applied') &&
        text.includes('blocked: no patch applied');
    }, { timeout: 10000 });
    await page.waitForFunction(() => {
      const text = document.querySelector('#panel-ops')?.innerText || '';
      return text.includes('Runtime Blocked Route Audits') &&
        text.includes('runtime blocked route audits:') &&
        text.includes('source registry unavailable') &&
        text.includes('memory layers unavailable') &&
        text.includes('sandbox store unavailable') &&
        text.includes('upstream unreachable') &&
        text.includes('blocked: Source Registry staging, Memory Layers, Sandbox, and LLM Ops require their runtime dependencies');
    }, { timeout: 10000 });
	    await page.waitForFunction(() => {
	      const text = document.querySelector('#opsCards')?.innerText || '';
	      return text.includes('Knowledge Memory') &&
	        text.includes('daily intake:') &&
	        text.includes('news:') &&
	        text.includes('review-only: promote not verified') &&
	        text.includes('Browser Trace API') &&
	        text.includes('fetcher proposals:') &&
	        text.includes('review-only: no official API adoption');
    }, { timeout: 10000 });
    await page.waitForFunction(() => {
      const text = document.querySelector('#panel-ops')?.innerText || '';
      return text.includes('Revenue External Send Apply Audits') &&
        text.includes('Revenue Channel Drafts') &&
        text.includes('revenue channel drafts:') &&
        text.includes('draft-only') &&
        text.includes('external send requires human approval: yes') &&
        text.includes('revenue external send apply audits:') &&
        text.includes('not sent') &&
        text.includes('external channel adapter: unconfigured') &&
        text.includes('human approval: required') &&
        text.includes('blocked: no external send applied') &&
        text.includes('sent') &&
        text.includes('verified');
    }, { timeout: 10000 });
    await page.click('.tab-btn[data-tab="timeline"]');
    await page.waitForSelector('#chat', { state: 'visible', timeout: 10000 });
    await page.waitForFunction(() => document.querySelectorAll('[data-chat-route]').length === 0, { timeout: 10000 });
    await page.waitForSelector('#inp', { state: 'visible', timeout: 10000 });
    await page.waitForSelector('#sendBtn', { state: 'visible', timeout: 10000 });
    await page.waitForFunction(() => {
      const btn = document.querySelector('#sendBtn');
      const input = document.querySelector('#inp');
      return !!btn && !btn.disabled && !!input && !input.disabled;
    }, { timeout: 10000 });
    await page.fill('#inp', message);
    await page.evaluate(async (expected) => {
      localStorage.removeItem('chatRouteAlias.selected');
      if (typeof syncChatRouteAliasButtons === 'function') syncChatRouteAliasButtons();
      if (typeof sendViewerMessage !== 'function') throw new Error('sendViewerMessage is unavailable');
      await sendViewerMessage(expected);
    }, message);
    await page.waitForFunction((expected) => {
      const chat = document.querySelector('#chat')?.innerText || '';
      const ops = document.querySelector('#opsFeedBody')?.innerText || '';
      return chat.includes(expected) || ops.includes(expected);
    }, message, { timeout: 30000 });
  } finally {
    await browser.close();
  }
})().catch(err => {
  console.error(err);
  process.exit(1);
});`

	scriptPath := filepath.Join(t.TempDir(), "phase25_viewer_browser_e2e.js")
	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		t.Fatalf("write playwright script: %v", err)
	}

	cmd := exec.Command("node", scriptPath)
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(),
		"NODE_PATH="+filepath.Join(repoRoot, "node_modules"),
		"PICOCLAW_LIVE_BASE_URL="+baseURL,
		"PHASE25_BROWSER_MESSAGE="+message,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("browser viewer e2e failed: %v\n%s", err, out)
	}
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	for dir := wd; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found from %s", wd)
		}
	}
}
