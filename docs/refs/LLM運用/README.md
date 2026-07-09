# LLM 運用

このディレクトリは、RenCrow / RenCrow_LLM の LLM 運用仕様をまとめる。

現行運用の確認は、まず [`最新情報/README.md`](最新情報/README.md) を参照する。

## 分類

### 最新情報

- [`最新情報/README.md`](最新情報/README.md)

現時点の採用構成、公開 model 名、主要 endpoint、参照すべき仕様の入口をまとめる。

### サーバとクライアント

- [`サーバとクライアント/OpenAI互換API仕様.md`](サーバとクライアント/OpenAI互換API仕様.md)
- [`サーバとクライアント/管理API_起動再起動仕様.md`](サーバとクライアント/管理API_起動再起動仕様.md)
- [`サーバとクライアント/LLM切り替え仕様.md`](サーバとクライアント/LLM切り替え仕様.md)
- [`サーバとクライアント/MLX運用デーモン仕様.md`](サーバとクライアント/MLX運用デーモン仕様.md)
- [`サーバとクライアント/LLMメモリ監視API仕様.md`](サーバとクライアント/LLMメモリ監視API仕様.md)
- [`サーバとクライアント/Viewer_LLM_Ops_Status仕様.md`](サーバとクライアント/Viewer_LLM_Ops_Status仕様.md)

OpenAI 互換 API、管理 API、起動・停止・再起動、Viewer 連携、メモリ監視に関する仕様を置く。

### LLM

- [`LLM/LLM仕様.md`](LLM/LLM仕様.md)
- [`LLM/PromptBundle仕様.md`](LLM/PromptBundle仕様.md)
- [`LLM/思考プロンプト自動適用システム仕様.md`](LLM/思考プロンプト自動適用システム仕様.md)

LLM role、モデル割り当て、Prompt Bundle、KV キャッシュ向け固定 prefix、思考プロンプト自動適用の仕様を置く。

## 旧資料

古い運用前提、重複文書、追加依頼書は削除済みであり、実装判断では参照しない。必要な内容はこのディレクトリの現行仕様へ統合する。
