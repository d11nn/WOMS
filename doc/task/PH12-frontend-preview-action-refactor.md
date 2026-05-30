# PH12 - Frontend Preview Action Refactor

Language: [English](#english) | [繁體中文](#繁體中文)

## English

Goal: reduce complexity in the preview click handler while preserving UI behavior.

### Checklist

- [x] Locate the complex preview click handler in `web/app.js`.
- [x] Create a dispatch map keyed by `data-preview-action`.
- [x] Extract handler for return workstation.
- [x] Extract handler for retry tomorrow.
- [x] Extract handler for retry suggested start.
- [x] Extract handler for update conflict due date.
- [x] Extract handler for unselect conflict order.
- [x] Extract handler for reject preview orders.
- [x] Extract handler for preview conflict solution.
- [x] Extract handler for retry manual force.
- [x] Keep validation messages unchanged.
- [x] Keep selected order state updates unchanged.
- [x] Keep conflict acknowledgement behavior unchanged.
- [x] Keep manual-force behavior unchanged.
- [x] Keep consistent error reporting through a shared top-level `try/catch` or equivalent pattern.
- [x] Run configured JavaScript checks.
- [x] Manually test preview actions in the browser if a dev server is available.
- [x] Update `doc/progress.md` when this phase is complete.

## 繁體中文

目標：降低 preview click handler 複雜度，同時保留 UI 行為。

### Checklist

- [x] 找出 `web/app.js` 中複雜的 preview click handler。
- [x] 建立以 `data-preview-action` 為 key 的 dispatch map。
- [x] 抽出 return workstation handler。
- [x] 抽出 retry tomorrow handler。
- [x] 抽出 retry suggested start handler。
- [x] 抽出 update conflict due date handler。
- [x] 抽出 unselect conflict order handler。
- [x] 抽出 reject preview orders handler。
- [x] 抽出 preview conflict solution handler。
- [x] 抽出 retry manual force handler。
- [x] 保持 validation messages 不變。
- [x] 保持 selected order state updates 不變。
- [x] 保持 conflict acknowledgement 行為不變。
- [x] 保持 manual-force 行為不變。
- [x] 透過共用 top-level `try/catch` 或等價模式保持一致 error reporting。
- [x] 執行已設定的 JavaScript 檢查。
- [x] 若 dev server 可用，在 browser 手動測試 preview actions。
- [x] 完成本階段後更新 `doc/progress.md`。
