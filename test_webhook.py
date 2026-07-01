#!/usr/bin/env python3
import hmac
import hashlib
import base64
import json
import os
import requests

# 環境変数から読み込み
channel_secret = os.getenv('LINE_CHANNEL_SECRET', '')

# テストペイロード（LINEメッセージイベント）
payload = {
    "events": [
        {
            "type": "message",
            "message": {
                "type": "text",
                "id": "test-message-id",
                "text": "Go言語について教えて"
            },
            "timestamp": 1646400000000,
            "source": {
                "type": "user",
                "userId": "test-user-id"
            },
            "replyToken": "test-reply-token",
            "mode": "active"
        }
    ],
    "destination": "test-destination"
}

# JSONシリアライズ
body = json.dumps(payload, ensure_ascii=False).encode('utf-8')

# 署名計算（HMAC-SHA256 + Base64）
mac = hmac.new(channel_secret.encode('utf-8'), body, hashlib.sha256)
signature = base64.b64encode(mac.digest()).decode('utf-8')

print(f"Body: {body.decode('utf-8')}")
print(f"Signature: {signature}")
print(f"Channel Secret length: {len(channel_secret)}")
print()

# Webhookエンドポイントに送信
url = "http://localhost:18790/webhook"
headers = {
    "Content-Type": "application/json",
    "X-Line-Signature": signature
}

print(f"Sending POST to {url}...")
response = requests.post(url, data=body, headers=headers)

print(f"Status: {response.status_code}")
print(f"Response: {response.text}")
