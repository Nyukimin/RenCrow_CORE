#!/bin/bash
# IdleChat テスト用起動スクリプト

set -e

echo "=== RenCrow IdleChat Test ==="

# 既存プロセスを停止
echo "1. Stopping existing rencrow processes..."
pkill -9 -f "/home/nyukimi/.local/bin/rencrow" 2>/dev/null || true
pkill -9 -f "rencrow-test" 2>/dev/null || true
sleep 3

# DuckDBをロックしているプロセスを強制終了
LOCK_PID=$(lsof /home/nyukimi/.rencrow/memory.duckdb 2>/dev/null | tail -n1 | awk '{print $2}')
if [ -n "$LOCK_PID" ] && [ "$LOCK_PID" != "PID" ]; then
    echo "   Killing DuckDB lock holder: PID $LOCK_PID"
    kill -9 $LOCK_PID 2>/dev/null || true
    sleep 2
fi

# DuckDB ロックを確認
if lsof /home/nyukimi/.rencrow/memory.duckdb 2>/dev/null; then
    echo "Warning: DuckDB still locked. Waiting..."
    sleep 3
fi

# 環境変数読み込み
if [ -f ~/.rencrow/.env ]; then
    source ~/.rencrow/.env
fi

# ログファイルをクリア
echo "" > /tmp/rencrow-test.log

echo "2. Starting rencrow-test..."
./rencrow-test > /tmp/rencrow-test.log 2>&1 &
PID=$!
echo "   Started PID: $PID"

# 起動待機
sleep 3

# ログ表示
echo ""
echo "=== Initial Log Output ==="
tail -30 /tmp/rencrow-test.log

echo ""
echo "=== Running ==="
echo "Log file: /tmp/rencrow-test.log"
echo "To monitor: tail -f /tmp/rencrow-test.log | grep -E 'IdleChat|Strategy|Topic:'"
echo "To stop: pkill -f rencrow-test"
