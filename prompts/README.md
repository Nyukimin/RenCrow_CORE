# prompts/ - LLM SystemPrompt

このdirectoryは、COREに同梱するfallback／互換promptと、IdleChat固有の補正promptを
管理します。現在のAgent identity、Persona、Execution Roleの契約は
[`docs/03_キャラクター・エージェント仕様.md`](../docs/03_キャラクター・エージェント仕様.md)
を参照してください。

## 読込方法

`config.yaml`の`prompts_dir`を省略した場合は`./prompts`を使用します。

```yaml
prompts_dir: "./prompts"
```

COREはrepo側のfallback promptを読み、その後に`workspace_dir`のpromptで上書きします。
character bundleは`workspace_dir/prompts/characters/<character>/manifest.txt`の順序で
結合します。repo側の`prompts/characters`は読みません。

現在のcharacter bundleはMio、Shiro、Kuro、Midoriを対象とします。KuroはHeavy、
MidoriはWildのExecution Roleへ接続されますが、Heavy／Wild自体はcharacterでは
ありません。

## 主なファイル

| path | 用途 |
| --- | --- |
| `mio.md` | Mio会話用のfallback prompt |
| `worker.md` | Shiro Worker用のfallback prompt |
| `coder.md` | Coder1/2/3/4共通のproposal形式 |
| `coder/codex_like.md` | Coder loop用prompt |
| `classifier.md` | 内部route classifier |
| `idle_chat/mio.md` | MioのIdleChat補正 |
| `idle_chat/shiro.md` | ShiroのIdleChat補正 |
| `idle_chat/aka.md` | Coder AkaのIdleChat補正 |
| `idle_chat/ao.md` | Coder AoのIdleChat補正 |
| `idle_chat/kin.md` | Coder KinのIdleChat補正 |
| `idle_chat/gin.md` | Coder GinのIdleChat補正 |
| `idle_chat/dialogue_*.md` | IdleChat dialogue生成 |
| `idle_chat/topic_*.md` | IdleChat topic生成・判定 |

Aka、Ao、Kin、Ginは、それぞれCoder1、Coder2、Coder3、Coder4のidentityとして
4体セットで現役です。4体すべてを`idle_chat.participants`に指定でき、専用補正promptも
読み込まれます。

## character bundle

標準形は次のとおりです。

```text
workspace_dir/
└── prompts/
    └── characters/
        ├── mio/
        ├── shiro/
        ├── kuro/
        └── midori/
```

各directoryの`manifest.txt`に、読み込むMarkdownファイルを順番に列挙します。
portableなrouting、handoff、Tool選択姿勢は`workspace_dir/control/`のversioned setを
使用し、character固有promptへ重複して持たせません。

## 注意事項

- UTF-8で保存する。
- 前後の空白は読込時にtrimされる。
- 空ファイルまたは欠落ファイルはfallbackとして扱われる。
- prompt本文へsecret、endpoint、物理Model、権限付与を書かない。
