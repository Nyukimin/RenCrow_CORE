# Browser Actor

Browser Actor is RenCrow's headless Playwright sidecar for browser-like user operations. It is intentionally kept outside the Go runtime. The Go CLI and ToolRunner call this script through JSON stdin/stdout.

## Run

```bash
node tools/browser_actor/run_browser_actor.mjs doctor --json

node tools/browser_actor/run_browser_actor.mjs run --json < request.json
```

stdout is JSON only. stderr is for logs.

## Test

```powershell
.\scripts\test-local.ps1 -Step browser-actor-node
```

## Safety

- Cookie, Authorization, Set-Cookie, password, token-like values are masked before artifact writes.
- Submit-like actions are blocked before execution in Phase 1.
- Test artifacts are written under the repository-local `Tmp/test-runtime/` through the canonical test runner.
- Raw request and response bodies are not saved.
