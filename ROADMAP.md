# RenCrow_CORE Roadmap

RenCrow_COREの実装状態、採用済み・未実装項目、deployment依存項目は、
[docs/08_実装状況・ロードマップ.md](docs/08_実装状況・ロードマップ.md)だけを
現行正本とします。このファイルに別のbacklogや状態表を重複保持しません。

現在のmodule責務は次のとおりです。

- COREはAgent、Persona、Memory、Recall、意味route、Policy Decision、Workstreamを所有する。
- RenCrow_LLM、RenCrow_STT、RenCrow_TTS、RenCrow_Vision、RenCrow_Image、
  RenCrow_GAMES、RenCrow_Toolsは各実装本体を所有する。
- RenCrow_ASSISTANTはpersonal／family Routine、PUSH、端末配信を所有する。
- RenCrow_PORTALは外部利用者向けChat／IdleChat Web UIを所有する。
- RenCrow_CMDは各Public APIを操作するCLI facadeである。
- RenCrow_Workspaceは外部runtime moduleではなく、`~/.rencrow/workspace`のportableな
  非secret snapshot repositoryである。実行時の正本は`~/.rencrow/workspace`に残る。

変更計画を追加する場合は、状態をこのファイルへ複製せず、採用済み契約と実装状態を
`docs/01`から`docs/10`の該当正本へ反映してください。
