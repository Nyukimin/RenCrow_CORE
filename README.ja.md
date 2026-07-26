# RenCrow_CORE（日本語）

RenCrow_CORE の日本語READMEは [README.md](README.md) です。

RenCrow_CORE は、人格を持つ会話、複数エージェントへのルーティング、記憶・Recall、
作業実行、承認、継続作業、Debug Viewerによる観測を統合するRenCrowの中核runtimeです。
LLM、STT、TTS、Vision、ゲーム、横断ツール、個人・家族向けPUSH、外部Web UIは、
それぞれ独立したRenCrow moduleが実装本体を所有します。

## クイックスタート

必要条件はGo 1.25以降です。

```bash
cp config/config.yaml.example config.yaml
make build
RENCROW_CONFIG=./config.yaml ./build/rencrow
```

設定はYAMLで管理し、secretは`${ENV_VAR}`形式で環境変数から展開してください。
既定の設定パス、module境界、Public API、実装状況は、次の現行正本を参照します。

- [現行正本の入口](docs/README.md)
- [システム概要](docs/01_システム概要.md)
- [設定リファレンス](docs/05_設定リファレンス.md)
- [Public API仕様](docs/06_Public_API仕様.md)
- [実装状況・ロードマップ](docs/08_実装状況・ロードマップ.md)

外部利用者向けのChat／IdleChat画面はRenCrow_PORTALが所有します。COREの`/viewer`は
Debug Viewer専用で、旧`/viewer?mode=view|live|lab`は提供しません。
