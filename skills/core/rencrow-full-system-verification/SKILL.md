---
name: rencrow-full-system-verification
description: "システム全体を検証して、RenCrowの全componentと横断surfaceを、Coverage Policy、owner check、固定Plan、実Actor I/O、receiptまで読み取り専用で監査する。部分checkやhealth代表確認ではなく、全phaseの機械可読trackerを必要とする時に使う。"
---

# RenCrow Full-System Verification

## Outcome

「システム全体を検証して」を、代表的なhealth確認ではなく、catalogに存在する全componentと横断surfaceの完全なread-only監査として実行する。

```text
ecosystem.yaml
  -> EcoSystem Coverage Policy
  -> every owner v2 check manifest
  -> five frozen phase Plans
  -> Tools execute-set
  -> owner CLI receipts
  -> deterministic aggregate-set tracker
  -> user report
```

Skill本文はcomponent、port、endpoint、check数を正本として複製しない。実行時の正本は次である。

- catalog: `/home/nyukimi/RenCrow/ecosystem.yaml`
- coverage: `/home/nyukimi/RenCrow/config/full-system-coverage.json`
- CORE checks: `RenCrow_CORE/config/checks/core.json`
- other checks: 各componentの`config/checks/runtime.json`
- planner/composer/aggregator: `RenCrow_Tools/tools/quality/full_system_verification`

manifestにcomponentが追加されたのにCoverage Policyまたはowner manifestがなければ、実行開始前にfail closedでpreflight errorにする。owner receiptを作らず、Skillの固定表へ手で追加して補完しない。

必須componentでもcanonical runtimeが未実装なら、Coverage Policyの
`temporarily_excluded_components`に`reason=required_component_unimplemented`、
`reinclude_when=canonical_runtime_implemented`が正規宣言されている場合だけ、現行のowner manifest実行、
binding、receipt母数から一時除外できる。component宣言と将来のcoverage requirementsは保持し、
trackerへ理由と再参加条件を出す。未知component、optional component、自由記述理由はfail closedとし、
実装後は同じ変更単位で除外を削除する。現在のASSISTANTはこの契約で扱う。

## Authorization boundary

デフォルトはread-onlyである。検査のためにrestart、stop、rebuild、deploy、install、fix、restore promotion、commit、push、外部公開、credential変更を行わない。必要なら該当checkを`blocked`とし、監査とは別の明示依頼を求める。

正規routeが失敗してもdirect backend、fake server、test double、別model、module省略経路を作らない。認証済みuserまたはCORE-managed Agentだけを実Actorとして扱う。

## Classification

- `CLI`: manifest、source/artifact、process/socket、health、DB、backup、resource取得。
- `Boundary`: Coverage Policy、認証、policy、canonical route、Check Plan、owner receipt、aggregate-set判定。
- `LLM`: user-visible文章や会話品質など、決定規則だけで評価できない残余のみ。

LLMの自然言語をcheck成功、認証、state、外部I/Oのreceiptにしない。

## Procedure

### 1. Preflight

1. workspace rootと各child repositoryを別Git repositoryとして扱う。
2. catalog validatorとSkill contract validatorを実行する。
3. `ecosystem.yaml`のcomponent集合とCoverage Policyのcomponent集合が完全一致し、実行対象componentのowner manifest集合が一致することを確認する。一時除外は上記の構造化宣言だけを許す。
4. dirty worktreeは観測だけして保存する。監査のためにstash/resetしない。

### 2. Compose every required phase

Coverage Policyの`required_phases`を列挙し、各phaseについて同じUTC評価時刻でcomposeする。

```bash
go -C /home/nyukimi/RenCrow/RenCrow_Tools/tools/quality/full_system_verification \
  run ./cmd/rencrow-full-system-verification compose \
  --ecosystem /home/nyukimi/RenCrow/ecosystem.yaml \
  --coverage-policy /home/nyukimi/RenCrow/config/full-system-coverage.json \
  --workspace-root /home/nyukimi/RenCrow \
  --phase <phase> \
  --now <RFC3339-UTC> \
  --pretty
```

全phaseで次を確認する。

- Plan statusが`ready`。
- component coverageとcross-system coverageの`missing`が0。
- required phaseにowner checkが1件以上ある。
- `excluded`が0。
- `deferred`は別phaseの`wrong_phase`だけ。
- request、Plan、composition revisionを変更不可のEvidenceとして保存する。

どれか一つでも満たさなければ全checkを始めず`blocked`にする。

Coverage Policyの現在のrequired phaseは5つであるため、5つのcompose結果を同一のbounded
composition directoryへ、`startup.json`、`runtime.json`、`deploy.json`、`backup.json`、
`diagnostic.json`として保存してから次へ進む。component、check、endpointの集合はcatalog、policy、
owner manifestから毎回動的に得る。Skill本文へ固定値を追加して不足を補ってはならない。

### 3. Execute the frozen set through Tools

5つのfrozen compositionを作成した後、owner checkを個別に手実行せず、Toolsのdeterministic common
executorを一度だけ呼び出す。

```bash
go -C /home/nyukimi/RenCrow/RenCrow_Tools/tools/quality/full_system_verification \
  run ./cmd/rencrow-full-system-verification execute-set \
  --composition-dir /path/to/compositions \
  --owner-bin-dir /path/to/owner-verifiers \
  --workspace-root /home/nyukimi/RenCrow \
  --evidence-dir /path/to/evidence \
  --receipt-dir /path/to/receipts \
  --pretty
```

`--composition-dir`には5つのfrozen JSONだけを置く。`--owner-bin-dir`はbindingから導出される
固定owner verifier binary、`--workspace-root`はfrozen `manifest_ref`の解決根、`--evidence-dir`は
owner CLIへ渡すbounded Evidence root、`--receipt-dir`はphase別receiptの出力rootである。composition、
manifest、owner binaryのregular/non-symlink境界はrunnerがpreflightし、`evidence-dir`とEvidence参照の
境界はowner CLIでも検査する。

`execute-set`は`execution_bindings`からowner binaryと共通引数（`--manifest`、`--check-id`、
`--observed-at`、`--evidence-dir`）だけを導出し、shell、endpoint解決、owner-specific flag、
restart、fixを行わない。owner binaryまたは参照manifestがmissingならpreflight errorで停止し、
Toolsはowner receiptをfabricateしない。`blocked`と`unverified`を発行できるのはowner CLIだけであり、
Tools、CORE、LLMはそのstatusのreceiptを代作しない。

owner CLIがcommand_idを実装していない、正規認証scopeがない、安全なread-only実行ができない場合は、
代替経路を作らずそのcheckの`blocked`または`unverified` receiptをowner境界から得る。LLMがreceiptを
捏造しない。

receipt schemaは`rencrow.check-receipt.v1`で、最低限次を持つ。

```text
check_id, guarantee_id, owner,
status, observed_at, route_or_target,
evidence_refs, failure_boundary
```

statusは`passed / failed / blocked / deferred / unverified / not_applicable`だけを使う。`passed`はsourceからuser-visible resultとreceiptまで、そのcheckが宣言する保証全体を満たした場合だけ使う。

`passed`と`not_applicable`は空でない`evidence_refs`を必須とする。`failed`、`blocked`、
`unverified`は`evidence_refs`を空にできるが、その場合は空でない`failure_boundary`を必須とする。
空の`evidence_refs`と空の`failure_boundary`の組み合わせは無効であり、Evidenceを返す場合も
bounded directory内の参照だけを使う。

`--observed-at`はfrozen compositionの要求時刻としてowner validatorへそのまま伝播する。`passed`を
成立させるEvidenceはRFC3339 UTCの`observed_at`を必ず持ち、receiptに記録された時刻との間で次の
inclusive windowを満たす。

```text
receipt.observed_at - 5m <= evidence.observed_at <= receipt.observed_at
```

下限・上限ちょうどは有効である。stale、future、欠落、非UTCのEvidenceは`passed`にならない。
意味のあるfile mtimeは補助Evidenceになり得るが、明示的な`observed_at`の代替にはしない。live
HTTP/systemd、canonical route、実Actor、media、deploy/runtime identityのEvidenceにも同じwindowを
適用し、現在時刻で上書きして古いEvidenceを再利用してはならない。

### 4. Aggregate all phases

`execute-set`がphase別receiptとaggregate trackerを出力する。出力は全required phaseのfrozen
compositionとowner receiptを`aggregate-set`へ渡した結果として扱い、必要な場合だけ同じ不変入力で
次の明示的な再検証を行う。

```bash
go -C /home/nyukimi/RenCrow/RenCrow_Tools/tools/quality/full_system_verification \
  run ./cmd/rencrow-full-system-verification aggregate-set \
  --input <aggregate-set-input.json> \
  --pretty
```

aggregate-setは、全phase、同一request集合、bindings、coverage、revision、included checkごとの一意receiptを検証する。単一phaseの`phase_clear`を全体正常と読み替えない。

`all_clear=true`は全必須checkが`passed`または根拠付き`not_applicable`で、欠落、重複、要求時刻に
対してfreshでないEvidence、failed、blocked、deferred、unverified、unexpected exclusionが0の場合
だけ成立する。

### 5. Report

決定的なaggregate-set JSONを正本trackerとし、報告には次を含める。

- 評価時刻、host/runtime identity、catalog revision。
- component数、check数、phase別included数。
- Plan/composition/aggregate revisions。
- status別件数と全check tracker。
- 一時除外component、その理由、再参加条件。
- canonical route、実Actor、browser、media、security/Tailscale/LAN、DB/storage、backup/restore、startup/resources、publicationの結果。
- failed/blocked/deferred/unverifiedのowner、failure boundary、次の安全な操作。
- `all_clear: true|false`。

未確認が一つでもあれば「完了」「100%」「問題なし」と報告しない。health、listener、unit test、代表機能だけを全体成功へ昇格しない。
