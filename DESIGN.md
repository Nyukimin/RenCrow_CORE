# DESIGN.md - RenCrow Viewer Design System

Status: active design source for RenCrow Viewer UI.

This file defines how RenCrow Viewer should look and feel. `AGENTS.md` defines how agents work in this repository; this file defines the visual language agents must follow when creating or changing Viewer screens.

Scope:

- `internal/adapter/viewer/viewer.html`
- `internal/adapter/viewer/assets/css/**`
- `internal/adapter/viewer/assets/js/**`
- Viewer tabs, panels, cards, logs, chat, Ops, System, Jobs, Memory, Reports, IdleChat, STT/TTS surfaces

Related implementation rules:

- `rules/rules_viewer_ui.md`
- `rules/common/rules_frontend.md`
- `docs/09_Viewer/Viewer仕様.md`

## 1. Visual Theme & Atmosphere

RenCrow Viewer is a night workbench for operating an AI assistant. It should feel calm, readable, technical, and alive.

Core mood:

- Dark workbench, not black-box dashboard.
- Quiet operational surface, not decorative SaaS landing page.
- AI companion presence, not generic admin console.
- Terminal-native clarity with enough warmth for long sessions.
- Summary-first, then drill-down.

Design references by category:

- Warp: dark terminal surface, command blocks, readable operational rhythm.
- Linear: precise hierarchy, low-friction navigation, restrained accents.
- Ollama / OpenCode: local-LLM, developer-native simplicity.
- Sentry: incident clarity, dense diagnostics without losing scanability.
- VoltAgent: terminal-native AI agent energy, emerald accent on dark surfaces.

Viewer must avoid:

- Marketing hero layouts.
- Oversized decorative cards.
- Monitor-wall layouts where every metric competes at once.
- One-note neon or purple gradients.
- Tables/logs as the first thing users see when a summary can be shown.

## 2. Color Palette & Roles

Use a dark neutral base with restrained semantic accents.

| Role | Token | Hex | Usage |
| --- | --- | --- | --- |
| App background | `--rc-bg` | `#090b10` | Page root, live-mode base |
| Workbench surface | `--rc-surface` | `#10141d` | Main panels, primary tab content |
| Elevated surface | `--rc-surface-raised` | `#171c28` | Cards, drawers, focused blocks |
| Soft surface | `--rc-surface-soft` | `#1f2633` | Secondary blocks, code panes, inactive controls |
| Border | `--rc-border` | `#303746` | Panel/card separators |
| Border muted | `--rc-border-muted` | `#202633` | Dense lists, low-emphasis dividers |
| Text | `--rc-text` | `#eef2f8` | Primary text |
| Text muted | `--rc-text-muted` | `#a6afbf` | Supporting text |
| Text dim | `--rc-text-dim` | `#717b8e` | Metadata, timestamps |
| Accent cyan | `--rc-accent-cyan` | `#4dd8ff` | Active focus, current session, STT/TTS live |
| Accent emerald | `--rc-accent-emerald` | `#38e6a6` | Healthy, ready, complete |
| Accent amber | `--rc-accent-amber` | `#ffc857` | Warning, waiting, partial readiness |
| Accent rose | `--rc-accent-rose` | `#ff5f7a` | Error, blocked, destructive |
| Accent violet | `--rc-accent-violet` | `#9b8cff` | AI/coder route accent only |

Color rules:

- Use neutral surfaces for most UI.
- Use one semantic accent per component state.
- Reserve emerald for health/success, amber for waiting/warning, rose for failure.
- Use cyan for focus and live activity.
- Violet is allowed for Coder/AI route hints but must not dominate the page.
- Do not use beige/cream, brown/orange, or purple-blue gradients as the dominant theme.
- Text contrast must remain readable on all dark surfaces.

## 3. Typography Rules

Use compact, readable typography. Do not scale font size with viewport width.

Recommended stack:

```css
font-family: Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
font-family: "JetBrains Mono", "SFMono-Regular", Consolas, "Liberation Mono", monospace;
```

Hierarchy:

| Element | Size | Weight | Notes |
| --- | --- | --- | --- |
| Page title | 20-24px | 700 | One per tab or major surface |
| Section heading | 15-18px | 650 | Clear but compact |
| Card title | 14-16px | 650 | Avoid hero-scale inside panels |
| Body | 13-15px | 400-500 | Default readable text |
| Dense body | 12-13px | 400-500 | Logs, details, tables |
| Metadata | 10-12px | 500 | Never below 10px |
| Code/log | 11-13px | 400 | Monospace, preserve scanability |

Typography rules:

- Letter spacing is `0`, except tiny uppercase labels may use up to `0.06em`.
- Line height: body `1.45-1.6`, dense data `1.3-1.45`.
- Avoid thin type on dark backgrounds.
- Long text should wrap cleanly without overlapping controls.
- Labels must say what the value means; do not rely on color alone.

## 4. Component Stylings

### Buttons

- Use compact rectangular controls, radius `6px` or less.
- Icon buttons should use recognizable icons where available.
- Primary action: filled dark/soft surface with cyan or emerald focus.
- Destructive action: rose border/text, not bright full red unless confirming destructive work.
- Disabled state must be visibly disabled and non-clickable.

### Cards

- Use cards for discrete repeated items, job summaries, message blocks, and modal content.
- Radius: `6px` to `8px`.
- Do not nest cards inside cards.
- Cards should have a stable min-height or grid sizing when values update frequently.

### Status Badges

- Badges are small operational labels, not decorative pills everywhere.
- Use semantic colors:
  - `ready`, `ok`, `passed`: emerald
  - `running`, `live`, `listening`: cyan
  - `waiting`, `partial`, `unknown`: amber
  - `failed`, `blocked`, `timeout`: rose

### Chat Messages

- Assistant responses are readable message blocks with enough line height.
- User input echo should not compete with assistant response.
- Route and token/sec metadata may appear near the response but must not interrupt reading.
- For terminal-like chat UI, show compact metadata lines above the response body.

### Tables And Logs

- Tables are for drill-down, not first-screen summaries.
- Use sticky headers only when helpful.
- Logs must be searchable/filterable or collapsed.
- Long lines should wrap or be clipped with a deliberate expand affordance.

### Forms And Inputs

- Main input surfaces should be obvious and reachable without scrolling.
- Keep input and send controls stable while streaming or updating.
- Use toggles for binary modes, segmented controls for mode groups, sliders/inputs for numbers.

## 5. Layout Principles

RenCrow Viewer is an operational tool. Layout must help the user find the next useful fact quickly.

Default structure:

1. Global navigation or tab rail.
2. Page header with title, current state, and 1-3 primary actions.
3. Summary band with 3-5 key blocks.
4. Main work area.
5. Details, logs, or raw data behind accordion/drawer/secondary panels.

Layout rules:

- First viewport must answer: what is happening, what needs attention, what can I do next.
- Do not require full-page scrolling to find the primary state.
- Keep Ops/System/Jobs summary-first.
- Put raw logs below summaries or behind details.
- Use responsive constraints for boards, toolbars, status cards, and grids to avoid layout shift.
- Prefer full-width sections or constrained inner layouts over floating decorative panels.
- Mobile/narrow layout must work at `390x844`.

Spacing scale:

| Token | Value | Usage |
| --- | --- | --- |
| `--space-1` | 4px | Tight inline gaps |
| `--space-2` | 8px | Button/icon gaps |
| `--space-3` | 12px | Dense card padding |
| `--space-4` | 16px | Standard panel padding |
| `--space-5` | 20px | Section gaps |
| `--space-6` | 24px | Major blocks |

## 6. Depth & Elevation

Depth should be subtle. RenCrow should feel layered, not glossy.

Surface hierarchy:

- Background: no border, deepest color.
- Main panel: slightly lighter surface, low border.
- Card/elevated block: raised surface, clear border.
- Modal/drawer: raised surface plus shadow.
- Live overlays: transparent or minimal when they sit over character/live mode.

Shadow rules:

- Prefer border and contrast over heavy shadows.
- Use soft shadows only for drawers, modals, active popovers.
- Avoid decorative orbs, bokeh blobs, and unrelated gradients.
- Glow is allowed only for live activity/focus and must be restrained.

## 7. Do's And Don'ts

Do:

- Start with a readable summary.
- Limit first-screen summary to 3-5 key blocks.
- Keep user-facing work surfaces calm and direct.
- Use semantic status color consistently.
- Make character presence visible where the screen is conversational.
- Use real data labels and stable empty states.
- Verify narrow viewport and text overflow.
- Check computed CSS for live-mode, overlays, and lipsync when touched.

Don't:

- Make the user scroll a full screen to find state.
- Show every log, queue, raw JSON, and metric at once.
- Hide primary actions below dense diagnostics.
- Use tiny text to solve overcrowding.
- Put UI cards inside other UI cards.
- Use marketing hero pages for tools.
- Let decorative visuals reduce readability.
- Confuse display text, audio chunks, lipsync state, and logs.

## 8. Responsive Behavior

Breakpoints:

| Range | Behavior |
| --- | --- |
| `<= 480px` | Single column, sticky primary input/action, tabs collapse or wrap cleanly |
| `481-820px` | Two-level layout allowed, no side panel required |
| `821-1199px` | Standard workbench layout, summary plus main area |
| `>= 1200px` | Optional side detail panel, never mandatory for primary state |

Responsive rules:

- Mobile must not rely on hover.
- Touch targets should be at least `36px`, primary controls `40px+`.
- Text must not overflow buttons, badges, cards, or status blocks.
- Primary chat input/action must be reachable without scrolling.
- Long operational tables should collapse into cards or horizontal scroll with clear affordance.

## 9. Agent Prompt Guide

When building or redesigning Viewer UI, use this prompt:

```text
Use DESIGN.md as the visual source of truth. Build RenCrow Viewer as a dark night-workbench operational UI: summary-first, readable, compact, terminal-native, and calm. Use neutral dark surfaces, cyan for live/focus, emerald for ready/success, amber for waiting/warning, rose for error/blocked. Do not show raw logs or dense tables as the first thing unless the screen is explicitly a log drill-down. Keep first viewport useful without full-screen scrolling. Verify desktop and 390x844 narrow layout.
```

Minimum review checklist:

- First viewport shows the key state and next action.
- No primary information is hidden behind full-page scrolling.
- Summary-first: 3-5 key blocks before details/logs.
- Text size is readable; no metadata below 10px.
- No card-in-card layout.
- Semantic color usage is consistent.
- Narrow viewport `390x844` is coherent.
- Live-mode/lipsync/overlay CSS is checked if touched.
