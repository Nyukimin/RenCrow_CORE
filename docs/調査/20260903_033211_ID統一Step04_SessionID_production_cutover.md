# ID統一 Step 04 SessionID production cutover receipt

- 実施日時: 2026-09-03 UTC
- Source branch: `identity/03-dci`
- Runtime source revision: `649697260911f591c6f700dbac109bbec20b12de`
- Canonical source: `docs/architecture/identity/IDENTITY_CANONICAL.md` Step 04
- Active config: `/home/nyukimi/.rencrow/config/core.yaml`
- Active Session root: `/srv/rencrow/db/core/memory/sessions`

## CLI / Boundary / LLM classification

- `CLI`: Session source inventory、deterministic mapping、dry-run/apply receipt、hash、build、systemd operation。
- `Boundary`: fixed `rencrow.service`、active config、writer stop、fresh output、same-filesystem rename、
  binary atomic install、rollback、readiness、実Actor route。
- `LLM`: migrationとstate変更には使用していない。配備後E2EのMio/Shiro応答だけが通常runtimeとしてLLMを通った。

## Source implementation

| Unit | Commit | Result |
| --- | --- | --- |
| Canonical Session value、`logical_date`、`ChannelAddress` | `f2f49730ca620b3c0415c347780620f3c10e6a0c` | pushed |
| UUIDv7生成、explicit lookup、ingress integration | `2c056aa350a0947a9b48779a6e7cc61768d0e282` | pushed |
| writer-stopped migration CLIとstrict receipt | `efd14ba46ee1056bdbfea27e189530f356288df0` | pushed and used for cutover |
| canonical-only runtime、legacy constructor/read/write拒否 | `649697260911f591c6f700dbac109bbec20b12de` | pushed and deployed |

`make test`、関連race test、`make vet`、architecture test、`git diff --check`はcutover前に成功した。

## Writer-stopped migration

停止前にactive config、unit、binary、PID、socketを確認した。`rencrow.service`をruntime maskして停止し、
`MainPID=0`、`inactive/dead`、`:18790` listener zeroを確認してからactive rootを入力にした。

| Evidence | Value |
| --- | --- |
| Source files | 21 |
| Legacy Sessions | 15 |
| Canonical Sessions before | 0 |
| Non-session files | 6 |
| Existing history | 487 |
| Writer-stopped source SHA-256 | `52ee59ada773411c53f4f532149ef48f40dd971836dda999fce50b03c9e3ad5f` |
| Mapping SHA-256 | `1f09c41c48de8cd159121a356be5a012fad56b30404ca349d0418ac4a3221b52` |
| Canonical output SHA-256 | `c96e1bc3f6053096f36ea17fb4578f52de84dc161128b25f7eaf97ea8bf1e1f0` |
| Canonical Sessions after | 15 |
| Legacy Sessions remaining | 0 |
| History after materialization | 487 |

Dry-run receipt SHA-256は`5e4601a3c6b96458e6466806583a711abefa68e53132a01b03dee30dc6c167a0`、
apply receipt SHA-256は`e17582e54776c8929da740f8ec3dfec8fb620567efaa97f7845a130f7742111b`で、
いずれもowner-only mode `0600`である。fresh staged rootを独立dry-runした結果もcanonical 15、legacy 0、
history 487、input/output hash一致だった。

旧Session rootは
`/srv/rencrow/db/core/memory/sessions.before-step04-20260903T033211Z`、旧binaryは
`/home/nyukimi/.local/bin/rencrow.before-step04-20260903T033211Z`へ削除せず保持した。

## Runtime identity and readiness

- Old installed binary SHA-256: `b029f80500ac0150930e157b1ba8876c3daea4f716f5181a70f5ba05ab866473`
- New staged／installed／process executable SHA-256:
  `5de8ef470a3bce975fc19a4cac376671d5eb9020bcbb62cdd0808a429fac59d0`
- Runtime stamp: commit `64969726`, built `2026-09-03T03:31:51+0000`
- `rencrow.service`: active/running、PID `3079109`、NRestarts `0`
- cgroup: `/user.slice/user-1000.slice/user@1000.service/app.slice/rencrow.service`
- listener: `*:18790`、owner PID `3079109`
- `/health/ready`: `ready=true`

最初の30秒pollではstartup中でlistenerが未生成だった。process、cgroup、NRestarts、journal、L1 DB ownerを
確認し、fixed owner timeout 300秒内で同じgenerationを監視した結果、rollbackなしでreadinessへ到達した。

## Canonical Actor E2E

`X-RenCrow-Client: RenCrow_CMD`、`cmd-chat` profileで実稼働`POST /viewer/send`をMioへ送信した。

- job: `20260903-033751-b09cbec0`
- trace: `trc_01a06558-2838-777c-b9e6-c8a6e10d316d`
- user message: `msg_01a06558-2838-7772-9bea-97aea21f45ec`
- created Session: `ses_01a06558-2842-76ab-b6a8-796d3917b08a`
- route: `OPS`
- result: MioからShiroへdelegateし、Shiro response/report、Mioからuserへのresponse、
  `ProcessMessage COMPLETE`、Viewer `async complete`まで同一job/traceで到達した。

保存Sessionは`logical_date=2026-09-03`、`ChannelAddress={viewer, viewer-user}`、history 1、mode `0600`で、
top-level legacy `channel`／`chat_id`は存在しない。E2E後のactive rootは22 files、canonical 16、legacy 0、
non-session 6、history 488だった。

## Gate 7 closure

production receiptとrollback cohort確定後、cutover専用`rencrow-session-migrate` commandと
`sessionmigration` packageをsourceから削除した。architecture testはlegacy SessionID builder、
legacy constructor、runtime migration sourceの再導入をfailさせる。active runtimeにdual read、dual write、
legacy JSON field fallbackはない。
