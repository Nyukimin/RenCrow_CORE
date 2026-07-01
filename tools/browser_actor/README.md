# Browser Actor

Browser Actor is RenCrow's headless Playwright sidecar for browser-like user operations. It is intentionally kept outside the Go runtime. The Go CLI and ToolRunner call this script through JSON stdin/stdout.

## Run

```bash
node tools/browser_actor/run_browser_actor.mjs doctor --json

node tools/browser_actor/run_browser_actor.mjs run --json < request.json
```

stdout is JSON only. stderr is for logs.

## Test

```bash
node tools/browser_actor/test_browser_actor.mjs
```

## Safety

- Cookie, Authorization, Set-Cookie, password, token-like values are masked before artifact writes.
- Submit-like actions are blocked before execution in Phase 1.
- Artifact paths are restricted to `workspace/browser_runs`, `tmp/browser_runs`, `output/playwright`, or system temp directories for tests.
- Raw request and response bodies are not saved.
