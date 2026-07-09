# Config Migration v3 to v4

This migration removes the old Ollama model split fields from `ollama`.

## What Changes

- `ollama.chat_model` is replaced by `ollama.model`.
- `ollama.worker_model` is removed.
- Runtime compatibility mapping was removed. Old fields are ignored by the loader and no longer select the active Ollama model.

## One-Time Migration

Run:

```bash
./scripts/migrate_config_v3_to_v4.sh /path/to/config.yaml
```

The script creates `/path/to/config.yaml.bak` before changing the file.

If `ollama.model` is missing and `ollama.chat_model` exists, the script writes `ollama.model` with the old `chat_model` value, then removes `chat_model` and `worker_model`.

## Manual Check

After migration, confirm the Ollama block uses only the v4 model field:

```yaml
ollama:
  base_url: "http://localhost:11434"
  model: "picoclaw-v1"
  max_context: 8192
```

Then verify config loading:

```bash
go test ./internal/adapter/config/... -v
```
