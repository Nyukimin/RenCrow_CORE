# rules_frontend.md - フロントエンド共通ルール

**作成日**: 2025-12-11  
**目的**: フロントエンド開発における共通ルール  
**対象**: HTML、CSS、JavaScript、TypeScript等のフロントエンド開発全般

---

## 対象言語

このルールセットで対象となる言語：

- **HTML** (.html) - Webページ構造
- **CSS** (.css) - スタイリング
- **JavaScript** (.js) - フロントエンド、Node.js
- **TypeScript** (.ts) - 型安全なJavaScript開発

---

## 1. HTMLコーディングスタイル

### 1.1 基本ルール

- **インデント**: 2スペース
- **属性**: ダブルクォート
- **セマンティックタグを優先**

### 1.2 実装例

```html
<!DOCTYPE html>
<html lang="ja">
<head>
  <meta charset="UTF-8">
  <title>ページタイトル</title>
</head>
<body>
  <header>
    <nav>...</nav>
  </header>
  <main>
    <article>...</article>
  </main>
  <footer>...</footer>
</body>
</html>
```

---

## 2. CSSコーディングスタイル

### 2.1 基本ルール

- **インデント**: 2スペース
- **命名**: BEM記法
- **プロパティ**: アルファベット順（推奨）

### 2.2 BEM記法

```css
/* Block */
.card { }

/* Element */
.card__title { }
.card__body { }

/* Modifier */
.card--highlighted { }
.card__title--large { }
```

### 2.3 CSS変数活用

```css
:root {
  --color-primary: #1da1f2;
  --color-background: #15202b;
  --spacing-unit: 8px;
}

.button {
  background: var(--color-primary);
  padding: var(--spacing-unit);
}
```

---

## 3. JavaScript/TypeScript

### 3.1 命名規則

- **変数・関数**: `camelCase`（例: `getUserData`）
- **クラス**: `PascalCase`（例: `UserManager`）
- **定数**: `UPPER_SNAKE_CASE`（例: `MAX_RETRY_COUNT`）

### 3.2 Linting・フォーマット（必須）

**Lintチェックは必須です。Lintエラーがあるコードはコミットしてはいけません。**

#### ESLint（必須）

```bash
# Lintチェック（必須）
npm run lint

# 自動修正
npm run lint:fix
```

#### Prettier（フォーマット、必須）

```bash
# フォーマットチェック（必須）
npm run format:check  # または prettier --check .

# フォーマット適用
npm run format  # または prettier --write .
```

#### TypeScript型チェック（必須）

```bash
# 型チェック（必須）
npm run type-check  # または tsc --noEmit
```

#### コミット前の必須チェック

```bash
# 1. Lintチェック（必須）
npm run lint

# 2. フォーマットチェック（必須）
npm run format:check

# 3. 型チェック（必須、TypeScript使用時）
npm run type-check

# 4. 自動修正（エラーがある場合）
npm run lint:fix
npm run format
```

### 3.3 コーディング規約

- ESLint/Prettierの設定に従う
- 関数・変数名は説明的な名前を使用
- コメントは日本語で記述
- エラーハンドリングを適切に実装
- **Lintエラーは必ず修正してからコミット**

---

**最終更新**: 2025-12-11  
**バージョン**: 1.0
