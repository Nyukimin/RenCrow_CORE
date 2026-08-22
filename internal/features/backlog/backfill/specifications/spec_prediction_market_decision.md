# Prediction Market型Multi-Agent協議 設計判断（会話Backfill）

状態: 重要設計判断 / 限定採用
起点: 2026-08-20 Prediction Marketの金融面・Multi-Agent協議面の調査

## 目的
複数Agentの予測・判断を、単純多数決やraw confidenceではなく、独立予測と実績Calibrationを使って統合し、Decision Support品質を上げる。

## MVP
3 Agent段階では本格市場より先に次を行う。
1. Blind Forecast
2. Agent×Domain×Task Type Calibration
3. Proper Scoring。中心はBrier等
4. Calibrated Confidence Weighted Aggregation
5. Evidence Exchange
6. Reforecast
7. Settlement / 結果記録

## 原則
- Raw Confidenceを信用しない。
- Agent独立性と情報多様性を優先する。
- Probabilityを共通言語化する。
- Prediction Market/LMSRを全面採用しない。
- LMSRはShadow Modeで簡易方式と比較し、明確に上回る場合のみ昇格する。
- 3 Agentでは市場機構自体よりCalibration/Proper Scoringの価値が大きい。
- 同一model/同一情報源のAgent cloneで票を水増ししない。

## 位置づけ
RenCrow COREの交換可能なDecision Support protocolとして扱う。PersonaやAgent Identityそのものへ市場機構を埋め込まない。
