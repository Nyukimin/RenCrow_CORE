# RenCrow_CORE Rules

このdirectoryはRenCrow_CORE固有の作業制約と実装補足です。製品仕様の正本は
`docs/README.md`から参照する現行仕様です。

`rules/common/`は既存参照との互換性を保つCORE-local補足であり、RenCrow全module共通の
正本ではありません。別moduleへcopyしません。全project共通の安全性、調査、設計、実装、
test、報告、module分離はworkspace rootの`AGENTS.md`から参照する共通Skillを正本とします。

新しいruleは次の所有境界へ置きます。

- 全project共通: 共通Skillの正本
- RenCrow全体の製品contract: `docs/README.md`以下
- COREの常時作業入口: `AGENTS.md`
- CORE固有のpath／domain／Viewer制約: `rules/`
- 再利用可能な手順: `skills/`

既存`rules/common/`の物理移動は、参照元と適用範囲を測定し、挙動を変えないmigrationとして
別途行います。単にdirectory名を整える目的では移動しません。
