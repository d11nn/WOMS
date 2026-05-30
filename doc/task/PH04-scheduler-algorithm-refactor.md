# PH04 - Scheduler Algorithm Refactor

Language: [English](#english) | [繁體中文](#繁體中文)

## English

Goal: split `internal/scheduler/scheduler.go` planning logic into deterministic, testable steps.

### Checklist

- [x] Extract `validateRequest(req Request) error`.
- [x] Extract `sortedOrders(req.Orders) []OrderInput`.
- [x] Extract `buildCapacityLedger(req Request) capacityLedger`.
- [x] Extract `planOrder(req Request, ledger *capacityLedger, order OrderInput, result *Result) error`.
- [x] Extract `availableCapacity(req Request, ledger capacityLedger, day time.Time) int`.
- [x] Extract `recordManualForceConflict(...)`.
- [x] Extract `recordLateCapacityConflict(...)`.
- [x] Introduce a private scheduler state type for usage maps and reported conflicts.
- [x] Preserve sort order: high priority, due date, order ID.
- [x] Preserve date truncation behavior.
- [x] Preserve manual force conflict semantics.
- [x] Run scheduler tests.
- [x] Add focused tests for invalid request, late conflict, manual force affected orders, locked allocation capacity, and deterministic ordering if coverage is missing.
- [x] Update `doc/progress.md` when this phase is complete.

## 繁體中文

目標：將 `internal/scheduler/scheduler.go` planning logic 拆成 deterministic 且可測試的步驟。

### Checklist

- [x] 抽出 `validateRequest(req Request) error`。
- [x] 抽出 `sortedOrders(req.Orders) []OrderInput`。
- [x] 抽出 `buildCapacityLedger(req Request) capacityLedger`。
- [x] 抽出 `planOrder(req Request, ledger *capacityLedger, order OrderInput, result *Result) error`。
- [x] 抽出 `availableCapacity(req Request, ledger capacityLedger, day time.Time) int`。
- [x] 抽出 `recordManualForceConflict(...)`。
- [x] 抽出 `recordLateCapacityConflict(...)`。
- [x] 新增 private scheduler state type 管理 usage map 與 reported conflict。
- [x] 保留排序：high priority、due date、order ID。
- [x] 保留 date truncation 行為。
- [x] 保留 manual force conflict 語意。
- [x] 執行 scheduler tests。
- [x] 若 coverage 不足，新增 invalid request、late conflict、manual force affected orders、locked allocation capacity、deterministic ordering 測試。
- [x] 完成本階段後更新 `doc/progress.md`。
