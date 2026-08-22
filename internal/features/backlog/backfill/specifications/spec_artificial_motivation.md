# RenCrow 「賢くなる」とArtificial Motivation 設計判断

起点: 2026-08-18

## 中核定義
RenCrowが賢くなる = 経験から変わる力。
知識量が増えるだけでなく、経験後に将来の判断・行動選択が変化することを重視する。

## 区別
### Instruction
例: 「60点のコードを100点にして」。
外部からの探索・最適化指示でありMotivationではない。

### Externally Imposed Objective / Terminal Condition
例: 「100ドルを増やし、0なら消滅」。
意思決定問題・utilityを変え、自己保存的な行動を誘発し得るが、恐怖や内発的Motivationの証明ではない。この例は思考実験であり正式仕様そのものではない。

### Artificial Motivation
過去経験から形成される持続的・内部的状態で、同じ指示が繰り返されなくても将来のGoal/Action Selectionを変えるもの。

成立条件:
- Persistence
- Internality
- Experience-dependence
- Actual effect on action selection

## Drive候補
growth / curiosity / mastery / success / utility / efficiency / coherence。
感情語のrole-playではなく、実際に選択を変えるdriveとして扱う。

## Loop
Experience
→ Reflection
→ Evaluation
→ Motivation
→ Goal / Action Selection
→ Action
→ Result
→ Experience

## 未固定
Drive間競合、更新率・減衰率、永続化schema、Memory/Persona/Goal責務境界、評価方法は後続設計課題。
