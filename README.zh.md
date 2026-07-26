# RenCrow_CORE（中文）

[日本語](README.md) | **中文**

RenCrow_CORE 是 RenCrow 的核心编排运行时，负责角色化对话、多智能体路由、记忆与
Recall、任务执行、审批、持续任务以及 Debug Viewer 的状态投影。

RenCrow_CORE 不内置其他模块的实现主体。LLM、STT、TTS、Vision、游戏、跨模块工具、
个人或家庭通知，以及面向外部用户的 Web UI，分别由独立的 RenCrow 模块负责。

## 快速开始

需要 Go 1.25 或更高版本。

```bash
cp config/config.yaml.example config.yaml
make build
RENCROW_CONFIG=./config.yaml ./build/rencrow
```

配置使用 YAML。API key、token 和其他密钥应通过 `${ENV_VAR}` 从环境变量展开，
不得写入仓库。

## 当前规范

以下文档是当前 `main` 的规范入口：

- [当前规范索引](docs/README.md)
- [系统概要](docs/01_システム概要.md)
- [配置参考](docs/05_設定リファレンス.md)
- [Public API](docs/06_Public_API仕様.md)
- [实现状态和路线图](docs/08_実装状況・ロードマップ.md)

面向外部用户的 Chat 和 IdleChat 页面由 RenCrow_PORTAL 提供。RenCrow_CORE 的
`/viewer` 仅用于调试和运行状态确认，不提供旧的
`/viewer?mode=view|live|lab` 页面。

详细规范目前以日文维护；翻译与日文规范冲突时，以
[docs/README.md](docs/README.md) 列出的当前规范为准。
