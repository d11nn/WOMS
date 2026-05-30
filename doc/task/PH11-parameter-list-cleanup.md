# PH11 - Parameter List Cleanup

Language: [English](#english) | [繁體中文](#繁體中文)

## English

Goal: remove oversized internal function signatures reported by the analyzer.

### Checklist

- [x] Search the current revision for functions with eight or more parameters.
- [x] Compare current results with stale `FIX.md` `cmd/api/main.go` line numbers.
- [x] Identify internal-only functions where option structs improve readability.
- [x] Introduce `schedulePlanContext` or equivalent only where schedule planning call sites become clearer.
- [x] Introduce `calendarWindow` or equivalent only where date window parameters are repeatedly passed together.
- [x] Introduce `productionMutation` or equivalent only where production update parameters are repeatedly passed together.
- [x] Avoid changing public API contracts.
- [x] Update all affected call sites.
- [x] Run `gofmt` on touched Go files.
- [x] Run `go test ./...`.
- [x] Re-run the analyzer if available.
- [x] Confirm parameter-count findings are gone or documented as stale.
- [x] Update `doc/progress.md` when this phase is complete.

## 繁體中文

目標：移除 analyzer 回報的 oversized internal function signature。

### Checklist

- [x] 搜尋目前版本中有八個以上參數的 function。
- [x] 將目前搜尋結果與 `FIX.md` 中過期的 `cmd/api/main.go` line number 對齊。
- [x] 找出適合用 option struct 提升可讀性的 internal-only function。
- [x] 只有在 schedule planning call site 更清楚時才導入 `schedulePlanContext` 或等價 struct。
- [x] 只有在 date window 參數反覆一起傳遞時才導入 `calendarWindow` 或等價 struct。
- [x] 只有在 production update 參數反覆一起傳遞時才導入 `productionMutation` 或等價 struct。
- [x] 避免改變 public API contract。
- [x] 更新所有受影響 call sites。
- [x] 對 touched Go files 執行 `gofmt`。
- [x] 執行 `go test ./...`。
- [x] 若 analyzer 可用，重新執行 analyzer。
- [x] 確認 parameter-count finding 消失或已記錄為 stale。
- [x] 完成本階段後更新 `doc/progress.md`。
