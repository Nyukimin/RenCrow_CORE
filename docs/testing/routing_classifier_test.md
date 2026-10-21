# Routing classifier experiment

This CLI is an isolated evaluation harness. It does not change normal CORE startup, routing policy, production conversation data, or the existing classifier.

## Current gate

The current `route.go` CODE4 comment and `rules/routing-policy.md` both define fast prototyping and experimental coding. `inspect` records `SOURCE_ALIGNED_REVIEW_REQUIRED`; source agreement does not set `semantic_review_status` to `verified`, and the extracted `route_policy.json` remains inspection-only. If these two sources later conflict or are missing, `inspect` records `POLICY_UNDEFINED`. The baseline separately records `classifier_parser_accepted_labels` and `repository_classifier_prompt_labels`: the parser accepts all 11 routes, while the preserved legacy prompt lists `CHAT`, `PLAN`, `ANALYZE`, `OPS`, `RESEARCH`, `CODE`, `CODE1`, `CODE2`, and `CODE3` (it omits `CODE4` and `WILD`). CODE4 is reachable from the explicit `/code4` command and has no dedicated RuleDictionary rule. Natural-language classifier selection is recorded as `UNDEFINED`; parser token acceptance and prompt omission do not prove automatic unreachability. Preparation still requires an explicitly reviewed policy set to `verified`, its matching prompt file, an exact SDK version, and an exact model commit; inspect never grants verification or starts acquisition.

The active prompt loaded by the running application is not observable from repository files because workspace prompt overrides are supported. The baseline records the repository prompt SHA separately and leaves `loaded_prompt_sha256` null. Optional `inspect --core-config <path>` resolves the effective `cfg.Prompts.Classifier` through CORE's normal config loader and records its prompt/config hashes plus canonical manifest ID under `legacy_baseline`; it records neither prompt text nor API key value. This is config-only evidence: `loaded_prompt_sha256` remains null, `legacy_provider_invoked` remains false, and the physical model remains unknown, so it cannot establish a live legacy comparison or clear the report gate. Omitting `--core-config` keeps the legacy baseline `UNOBSERVED` and unknown.

The Laya SDK and model are not downloaded when route and policy sources conflict (`POLICY_UNDEFINED`), when source status is not recognized, or when explicit preparation inputs are missing. A `SOURCE_ALIGNED_REVIEW_REQUIRED` result is only a source comparison; it does not count as semantic review. `prepare` requires an explicitly reviewed policy, the prompt file whose hash that policy names, an exact SDK version, and an exact Hub commit in `candidate.expected_model_revision`. It creates a private venv and model directory, writes a hash-locked `requirements.lock`, hashes every downloaded model file, and emits `prepared_manifest.json` plus `resolved-config.yaml`. It never changes the input config or global Python installation. `adapter_contract.json` stays `UNVERIFIED` until the real SDK adapter is exercised and the independent runtime and isolation evidence required by the experiment is available.

## CLI contract

`testdata/routing_classifier/config.yaml.example` contains every §9 section with `mode: off`, placeholder paths and revisions, and no token value. `inspect` can read it; the placeholders are not a prepared SDK or model environment.

Each command writes only to a new output directory. The directory is mode `0700` and files are mode `0600` where the OS supports those permissions. Existing output paths are rejected.

```sh
go run ./cmd/routing-classifier-eval inspect \
  --config /private/classifier-test.yaml \
  --repo /path/to/RenCrow_CORE \
  --out /private/routing-eval/baseline

go run ./cmd/routing-classifier-eval prepare \
  --config /private/classifier-test.yaml \
  --policy /private/reviewed/route_policy.json \
  --legacy-prompt /path/to/RenCrow_CORE/prompts/classifier.md \
  --sdk-version 0.3.28 \
  --base-python python3 \
  --repo /path/to/RenCrow_CORE \
  --out /private/routing-eval/prepared

go run ./cmd/routing-classifier-eval smoke \
  --config /private/routing-eval/prepared/resolved-config.yaml \
  --fixtures testdata/routing_classifier/smoke.jsonl \
  --repo /path/to/RenCrow_CORE \
  --out /private/routing-eval/smoke

go run ./cmd/routing-classifier-eval replay \
  --config /private/routing-eval/prepared/resolved-config.yaml \
  --dataset /private/routing-eval/corpus.jsonl \
  --split test --variants legacy,candidate,rules-only \
  --repo /path/to/RenCrow_CORE \
  --out /private/routing-eval/run-001

go run ./cmd/routing-classifier-eval e2e \
  --config /private/routing-eval/prepared/resolved-config.yaml \
  --dataset /private/routing-eval/chat-e2e.jsonl --isolated \
  --repo /path/to/RenCrow_CORE \
  --out /private/routing-eval/e2e-001

go run ./cmd/routing-classifier-eval report \
  --run /private/routing-eval/run-001 \
  --e2e /private/routing-eval/e2e-001 \
  --out /private/routing-eval/report
```

With current source status, `prepare` remains blocked until explicit review and exact pins are supplied; missing `--policy`, `--legacy-prompt`, and `--sdk-version` returns `PREPARE_PINS_REQUIRED` before creating a venv or making a network request. A source conflict returns `POLICY_UNDEFINED`. Model revision is read from the config and must be a lowercase 40- or 64-character Hub commit. The policy must set `semantic_review_status: verified`, contain the exact current route order and extracted policy criteria, and name the current source paths. The supplied prompt file bytes must match `legacy_prompt_sha256` in that policy. The repository prompt hash and supplied-file hash are recorded separately; the active runtime prompt remains `NOT_OBSERVED` because workspace overrides are supported. The SDK is installed as `laya==<exact version>` from the official PyPI index into `<out>/venv`; the multilingual repository `convaiinnovations/laya-multilingual` is downloaded at the exact configured commit into `<out>/model`. The resulting package lock contains exact versions and PyPI artifact SHA-256 values. No SDK method or model inference is called during preparation.

`smoke` first requires current route sources to be reviewable, a sibling `prepared_manifest.json` and verified `route_policy.json`, matching local model hashes, the SDK lock, a mode-off resolved config, and a configured bearer token. With `POLICY_UNDEFINED` or missing/mismatched prepared artifacts it writes `NOT_RUN` and makes no HTTP request. When prepared, it accepts only `http://127.0.0.1:<port>`, disables proxy use and redirects, strictly validates `/readyz`, checks that `/v1/classify` rejects a missing bearer token, then sends the authenticated synthetic Japanese, English, mixed, long-input, and fixed-policy choice cases. It does not follow or contact external endpoints. The 10,000-character Japanese long case must explicitly abstain with `INPUT_TOO_LONG`. A classified result for the fixed policy case confirms complete route probabilities; it does not prove M07 choice-description overflow handling. M07 is recorded only when the actual prepared policy produces `POLICY_TOO_LONG`; otherwise `choice_budget_boundary_status` remains `NOT_OBSERVED` and M07 remains independently unverified.

The server's strict `/readyz` schema reports `offline_configuration_verified: true` only after the child confirms its configured offline mode, and currently reports `network_isolation_status: "UNVERIFIED"`. That child attestation permits smoke to run and save each multilingual and boundary HTTP result. It is not proof that external communication is blocked: M01 remains `UNVERIFIED`, smoke remains `INCOMPLETE` with `OFFLINE_ISOLATION_UNVERIFIED`, and `adapter_contract.json` must remain `UNVERIFIED`. The smoke status records `offline_configuration_verified`, `network_isolation_status`, and `choice_budget_boundary_status` separately; the legacy `external_network_allowed` field is omitted because permission to reach external hosts is unknown. Absent an actual `POLICY_TOO_LONG` response, M07 remains `NOT_OBSERVED`. `smoke_results.jsonl` omits message text and records prepared hashes, HTTP statuses, model metadata, timings, and per-case outcomes. Fake HTTP server unit tests validate only the client contract and result handling; they do not verify the SDK, child attestation provenance, or egress isolation and cannot mark M01, M07, or the adapter contract verified.

`replay` always validates the complete corpus for duplicate IDs and cross-split group leakage, then deterministically shuffles the selected split from the configured seed. It reconstructs `turn_fixture.session_id` and `turn_fixture.viewer_recipient` as bounded normalized strings; unknown fixture keys, controls, and non-string values are rejected. Conversation history, old classifier outputs, attachments, forced routes, and channel identity are not reconstructed. `rules-only` executes the existing `Mio.DecideAction` and `NewRuleDictionary` in memory with a no-op classifier probe to observe whether Mio reaches the classifier stage. It has no model, provider, database, tool, or network dependency and emits only case IDs, label metadata, routes, stage flags, and timings; message text is never written to predictions. A rules-only-only run may be `COMPLETED` even while the CODE4 policy is undefined because this route does not use the candidate model. That status means only that the requested rules-only observations completed.

Candidate replay first runs the same Mio decision path for each case and records `rule_matched` and `classifier_reached`. Only cases that actually reach Mio's classifier stage are sent to the candidate; explicit commands and Mio rule/catalog matches are recorded without model calls and excluded from candidate/legacy classifier-quality denominators. Candidate and future legacy rows retain the same per-case reachability flags for paired comparison. Candidate replay requires the same verified `route_policy.json`, `prepared_manifest.json`, exact local model tree and SDK lock as smoke, plus a mode-off resolved config and bearer token. It is designed to contact only the configured `http://127.0.0.1:<port>` endpoint, disable proxy and redirects, validate the current `/readyz` schema, and send authenticated `/v1/classify` requests serially. No independent network-isolation proof is available, so after local prepared-artifact preflight it stops before any HTTP call with `OFFLINE_ISOLATION_UNVERIFIED`. The child's `offline_configuration_verified` value cannot substitute for `network_isolation_status=VERIFIED`. Candidate predictions, warmups, and latency measurements therefore remain blocked. Replay evidence may still copy the exact prepared manifest, route policy, SDK lock, and resolved config to explain the block. Replay leaves `adapter_contract.json` `UNVERIFIED`. A route/policy conflict blocks candidate replay with `POLICY_UNDEFINED`; under current aligned sources it reaches subsequent prepared-artifact and network-isolation gates instead.

`legacy` and `hybrid-replay` remain explicitly `UNAVAILABLE` in the CLI until the live provider, loaded runtime prompt, model identity, middleware/timeout/context budget, and isolated legacy baseline are observed. The library hybrid adapter is injectable and tests candidate-first serial fallback, but CLI does not fabricate a provider or candidate acceptance threshold. Unavailable variants are recorded in `variant_statuses`; a mixed request produces a partial run and any predictions from completed variants remain reviewable. `e2e` exits nonzero with `ISOLATION_ADAPTER_UNAVAILABLE` even when `--isolated` is supplied because the real legacy and prepared-candidate compositions are not yet available.

The eval package has an in-process **test-only** Mio harness exercised by `go test ./cmd/routing-classifier-eval -run 'TestRunIsolatedMioTrial|TestE2E'`. Each trial creates a mode-`0700` directory under the OS temp directory, opens a fresh L1 SQLite store and `RealConversationEngine`, and removes the directory after the trial. It calls the real `Mio.DecideAction`; only a final `CHAT` calls `Mio.Chat`. The harness requires a completed L1 turn receipt before publishing the final text to its in-memory test sink. TTFT starts at the first non-whitespace stream callback, and final time is recorded at sink publication. Tool, MCP, KB, web-search-cache, user-memory-write, and persona-write integrations are injected as deny-and-count spies. Worker and Coder are not constructed. The result type has no JSON output and is always marked `test_only_fake_provider` and `adoption_eligible=false`.

The test harness accepts injected classifiers and an injected provider for contract tests only. Its Mio confidence value `1.0` is a dispatch-gate shim with reason `test_adapter_gate_only`; it is not a probability or quality observation. The paired TTFT helper requires the same `origin=real_conversation`, `label_status=verified`, single expected `CHAT`, no-context case to finish on `CHAT` in both `legacy` and `candidate`; a wrong route is excluded rather than counted as faster. This harness does not establish the live provider, loaded prompt, real candidate SDK/model, verified policy, or external-sink isolation needed by the CLI or report gate. Fake-provider test output must never be promoted into `e2e_status.json`, `adapter_contract.json`, or a model-adoption `PASS`.

`report` accepts partial runs for inspection and lists unmeasured variants. It cannot produce model adoption `PASS` from synthetic-only data, an unverified adapter contract, unknown legacy model or loaded prompt identity, missing verified real-conversation support for any route, or incomplete E2E evidence. It also requires paired TTFT and final-response latency, shared-resource contention, and repeated paired-measurement evidence. The current CLI does not collect or validate those measurements, so `metrics.json` reports each as `UNAVAILABLE`, lists the missing issue, and keeps adoption `INCONCLUSIVE` even if classification F1 and call latency look favorable. A `VERIFIED` adapter contract is accepted only with all required checks, matching prepared SDK/model/policy pins, and matching smoke evidence that attests network isolation; a status string alone is insufficient. Current smoke reports `network_isolation_status: "UNVERIFIED"`, so the adapter remains `UNVERIFIED`. Rules-only replay completion does not change this model-adoption gate.

All result directories should contain `baseline_manifest.json`, `experiment_manifest.json`, `adapter_contract.json`, `route_policy.json`, and `policy_review.json`. The prepared-artifact contract also requires sibling `prepared_manifest.json` and `route_policy.json` beside `resolved-config.yaml`. The manifest fields and integrity checks are defined by `classifierlab.PreparedManifest` in `internal/infrastructure/routing/classifierlab/prepared_manifest.go`. Source alignment does not verify policy meaning: a manifest may claim `verified` only after explicit review, and a route/rules conflict must remain `POLICY_UNDEFINED`.

The working-tree evidence separates the tracked `git diff HEAD` hash from the porcelain status hash and lists hashes for untracked Go, Python, Markdown, JSON, and YAML source. The scope is recorded in the manifest; non-source artifacts are not represented by the source hash map. `legacy_model_unknown` remains true until the live provider/model is observed.

## Data

`testdata/routing_classifier/synthetic_corpus.jsonl` contains 88 synthetic rows: 22 each in `dev`, `calibration`, `test`, and `challenge`. Every row is `origin=synthetic`, `label_status=draft`, and explicitly marked as unapproved fixture material. Repeated examples share a group only inside one split. None are real conversations or verified ground truth. `smoke.jsonl` contains Japanese, English, mixed-language, long-input, and fixed-policy fixture shapes. The long fixture uses bounded repeat fields so the request is large enough to exercise explicit over-budget handling while staying below the 64 KiB HTTP body limit. It does not expand route criteria and therefore cannot by itself exercise M07.

Replay input JSONL is bounded to 100,000 rows and 1 MiB per line. Unknown fields, duplicate JSON keys, duplicate case IDs, invalid route names, and group leakage across splits are rejected. Prediction JSONL additionally requires a single manifest ID, unique case/variant/repetition keys, safe categorical error and reason codes, and a fixed complete probability set for classified candidate outputs. Metrics count only repetition zero for quality while retaining all repetitions for latency and candidate error/abstention/fallback counts. `experiment_manifest.json`, `run_manifest.json`, and `replay_status.json` bind each requested variant to its status, reason, case count, measured calls, errors, abstentions, offline configuration attestation, and independent network-isolation status.

## Worker location mapping

The specification's `tools/routing_classifier_test/laya_worker.py` is implemented as the embedded payload `cmd/routing-classifier-test-server/sdk_worker.py`. The Go server embeds and launches that payload only after validating sibling prepared artifacts and the local model tree. No Python dependency is part of standard CORE startup or build. No `tools/routing_classifier_test/requirements.<platform>.lock` has been created because the exact official SDK and dependency versions are not yet pinned and verified.
