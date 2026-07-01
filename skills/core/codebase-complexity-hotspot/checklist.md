# Codebase Complexity Hotspot Checklist

## Scan

- [ ] scan scope が明示されている
- [ ] generated / vendored / build artifact を除外した
- [ ] hot path / request path / render path を区別した
- [ ] Evidence に file path と行番号を含めた

## Report

- [ ] hotspot type を分類した
- [ ] before / after complexity を推定した
- [ ] risk level を分類した
- [ ] required tests を列挙した
- [ ] benchmark 未実施なら断言を避けた

## Safety

- [ ] コードを変更していない
- [ ] 修正が必要な場合は別 Goal に分けた
- [ ] high risk を自動適用扱いにしていない
- [ ] 複数 hotspot を 1 PR に混ぜていない
