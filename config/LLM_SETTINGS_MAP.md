# LLM設定ファイル索引

このファイルは実行時設定の正本ではなく、関連ファイルへの補助索引です。設定契約は
[`docs/05_設定リファレンス.md`](../docs/05_設定リファレンス.md)、AgentとExecution Roleの
契約は[`docs/03_キャラクター・エージェント仕様.md`](../docs/03_キャラクター・エージェント仕様.md)
を参照してください。

## Runtime config

- `rencrow`は`RENCROW_CONFIG`の指定先、未指定時は作業ディレクトリの`./config.yaml`を読みます。
- `config/config.yaml.example`は公開テンプレートです。repository内に実行時設定の正本は置きません。
- 初期設定は`cp config/config.yaml.example config.yaml`で作り、secretは環境変数から展開します。
- provider、endpoint、model、保存先はdeploymentごとの値であり、この索引へ固定しません。

## Prompt

- `prompts/`はCORE同梱のfallback／互換promptとIdleChat補正です。
- `workspace_dir/prompts/characters/`のbundleが存在する場合は、character promptを上書きします。
- portableなcharacter bundleとshared controlの宣言元は独立した`RenCrow_Workspace`です。
- 読込順と責務は[`prompts/README.md`](../prompts/README.md)を参照してください。

## Code references

- `internal/adapter/config/config.go`: config load、環境変数展開、validation
- `internal/adapter/config/config_types.go`: config schema
- `internal/adapter/config/prompts.go`: fallback promptとworkspace override
- `internal/adapter/config/agentcontrol/`: portable shared controlの検証

Ollama用の`config/Modelfile.chat`はlegacy／開発用providerの補助ファイルです。productionの
Agent実行target、GPU、backend processはRenCrow_LLMが所有し、COREの設定索引へ重複定義しません。
