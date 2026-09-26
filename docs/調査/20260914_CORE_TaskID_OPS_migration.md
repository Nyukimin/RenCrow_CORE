# CORE TaskID／Shiro OPS 移行

## Failure

Shiro OPSがTaskの旧`job_id`をそのままLLM Gatewayへ送り、Gatewayの`task_id`契約に到達できない状態があった。実行観測の不正IDを空へ正規化すると、検証を回避したまま別の相関値で実行できる危険もあった。

## Problem

旧Task生成器は時刻と短いUUIDを連結し、型付きのUUID version／variant検証を持たなかった。session JSON、Task、実行観測、Gateway metadata、OPS responseの同じ意味が`job_id`と`task_id`へ分散していた。

## Cause

Taskのidentity ownerが`internal/domain/task`へ収束しておらず、保存・provider・Agent境界が文字列を直接受け渡していた。旧値の読み込みと新規生成の境界も区別されていなかった。

## Lesson

COREの新規TaskIDは`tsk_`＋canonical lowercase UUIDv7だけを生成する。UUIDv5は、旧session履歴を再現する決定的移行値だけに限定する。Gatewayへ出すmetadataとOPS responseは`task_id`を使い、旧`job_id`を互換名として送らない。

## Invariant

- `TaskID.Validate`／`ParseTaskID`は`tsk_`付きUUIDv5またはUUIDv7、RFC4122 variant、canonical lowercase表記だけを受理する。
- session JSONは新規保存で`task_id`だけを持つ。読み込み時の旧`history[].job_id`は、`task_id`がなく、旧値に前後空白／NULがなく、他のidentity keyが併記されない場合だけ移行する。
- 旧session値`v`の移行名は`TaskID\0session_history\0job_id\0v`、namespaceは`6570d821-e63e-592d-a51f-8cf4b43cdba5`である。
- 不正な非空TaskIDは新しいTaskへ置き換えず、Agent／provider境界で明示的に失敗させる。空のTaskIDだけがTaskを持たないbackground処理で許可される。
- Scheduler、domain job、execution reportなど別lifecycleの`job_id`はTaskIDへ改名しない。

## Enforcement

`Task`は単一の`TaskID`値を持ち、旧`JobID`型・getter・生成器は削除した。JSON session `toDTO`は保存前に検証し、`fromDTO`の旧値処理は上記migration関数だけを通る。`ExecutionObservation`は不正値を消さず、RenCrow_LLM providerがHTTP送信前に`Validate`してfail closedする。OPSは毎回CORE生成のTaskIDをresponseとShiro実行へ渡す。

## Tests

- `go test ./internal/domain/task ./internal/infrastructure/persistence/session ./internal/domain/llm ./internal/infrastructure/llm/providers/rencrowllm` — exit 0
- `go test ./internal/adapter/modulebridge ./internal/application/complexity ./internal/domain/llm ./internal/infrastructure/llm/middleware ./internal/application/heartbeat` — exit 0
- `go test ./internal/application/orchestrator -count=1` — exit 0
- `go test ./cmd/rencrow -run 'AgentOps|VoiceChat|LocalWorker|LocalAgent|TaskID' -count=1` — exit 0

親の実runtime pilotでも、同じ`tsk_`値がOPS response、Shiro実行、3件のprompt receipt、Gateway worker観測へ到達し、認証なしrequestは401で拒否された。
