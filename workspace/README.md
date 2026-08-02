# workspace/ - Runtime workspace入口

このdirectoryは`workspace_dir`の既定配置と互換入口です。CORE repositoryが管理する
portable character bundleの正本ではありません。共有可能なcharacter promptと
`control/`の実行時正本は各deploymentの`workspace_dir`で、productionの正規pathは
`~/.rencrow/workspace`です。独立した`RenCrow_Workspace` repositoryは、そのportableな
非secret要素を保存するbackup／復旧用snapshotです。

## COREが読み込むもの

| path | 読み込み側 | 用途 |
| --- | --- | --- |
| `prompts/characters/<character>/manifest.txt` | `LoadPrompts()` | character bundleによるprompt上書き |
| `control/agents.yaml`等4ファイル | agent control loader | role、routing、handoff、Tool選択姿勢。MioにはChat時に契約indexを注入 |
| `persona/mio.md` | context builder | CHAT用のMio persona |
| `SOUL.md`、`PrimerMessage.md` | context builder | CHAT用bootstrap context |
| `AGENT.md`、`IDENTITY.md`、`USER.md` | context builder | 共通bootstrap context |
| `skills/*/SKILL.md` | skills loader | 利用可能Skillの概要 |

存在しないファイルはcontext builderが読み飛ばします。旧`CHAT_PERSONA.md`は現在の
bootstrap pathではなく、Mio personaは`persona/mio.md`です。

## Repositoryとruntimeの境界

- CORE repositoryの`prompts/`はfallback／互換promptとIdleChat補正です。
- `workspace_dir`のcharacter bundleはfallback promptを上書きします。
- COREは`workspace_dir`を読み込み、RenCrow_Workspace repositoryのcheckoutへ直接接続しません。
- このdirectoryでGit管理するのはREADMEと明示的に採用した互換Skillだけです。
- `.gitignore`対象のローカルworkspace内容を、portable正本や配布物として扱いません。
- OperationMemory、DB、logs、sessions等のruntime stateはrepositoryへ保存しません。

製品契約は[`docs/03_キャラクター・エージェント仕様.md`](../docs/03_キャラクター・エージェント仕様.md)、
設定契約は[`docs/05_設定リファレンス.md`](../docs/05_設定リファレンス.md)を参照してください。
