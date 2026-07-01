#!/usr/bin/env python3
import requests
import json

payload = {
    "events": [{
        "type": "message",
        "message": {"type": "text", "id": "123", "text": "テスト"},
        "timestamp": 1646400000000,
        "source": {"type": "user", "userId": "U123"},
        "replyToken": "test-token",
        "mode": "active"
    }],
    "destination": "D123"
}

body = json.dumps(payload).encode('utf-8')
response = requests.post(
    "http://localhost:18790/webhook",
    data=body,
    headers={"Content-Type": "application/json", "X-Line-Signature": "dummy"}
)
print(f"Status: {response.status_code}")
