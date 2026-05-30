# PH01 - Baseline And Issue Reconciliation

Language: [English](#english) | [繁體中文](#繁體中文)

## English

Goal: confirm the current repository state, reconcile stale analyzer line numbers, and establish a test baseline before refactoring.

### Checklist

- [x] Run `git status --short` and record pre-existing local modifications.
- [x] Run `git branch --show-current` and confirm work is not being done directly on `main`.
- [x] Run `git fetch origin` before creating or rebasing the feature branch.
- [x] Compare each `FIX.md` finding with the current file locations.
- [x] Confirm `cmd/api/main.go` current length and identify stale reported line numbers.
- [x] Search for moved code related to stale `cmd/api/main.go` findings.
- [x] Decide whether a fresh analyzer report is required.
- [x] Run `go test ./...` and record baseline failures, if any.
- [x] Inspect `package.json` for available frontend checks.
- [x] Run configured frontend checks and record baseline failures, if any.
- [x] Update `doc/progress.md` when this phase is complete.

## 繁體中文

目標：確認目前 repository 狀態、對齊 analyzer line number 是否過期，並在重構前建立測試基準。

### Checklist

- [x] 執行 `git status --short` 並記錄既有 local modification。
- [x] 執行 `git branch --show-current` 並確認沒有直接在 `main` 開發。
- [x] 建立或 rebase feature branch 前執行 `git fetch origin`。
- [x] 將 `FIX.md` 每個 finding 與目前檔案位置對齊。
- [x] 確認 `cmd/api/main.go` 目前長度，找出過期的 line number。
- [x] 搜尋與過期 `cmd/api/main.go` finding 相關的移動後程式碼。
- [x] 判斷是否需要重新產生 analyzer report。
- [x] 執行 `go test ./...`，若有既有失敗需記錄。
- [x] 檢查 `package.json` 中可用的前端檢查。
- [x] 執行已設定的前端檢查，若有既有失敗需記錄。
- [x] 完成本階段後更新 `doc/progress.md`。
