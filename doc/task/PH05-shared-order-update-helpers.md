# PH05 - Shared Order Update Helpers

Language: [English](#english) | [繁體中文](#繁體中文)

## English

Goal: reduce repeated order lifecycle validation and authorization logic across memory and PostgreSQL stores.

### Checklist

- [x] Create `canUpdateOrderDetails(order domain.Order, claims auth.Claims) error`.
- [x] Create `canCancelOrder(order domain.Order, claims auth.Claims) error`.
- [x] Create `canRejectOrder(order domain.Order, claims auth.Claims) error`.
- [x] Create `applyOptionalQuantity(order *domain.Order, quantity int) error`.
- [x] Create `applyOptionalDueDate(order *domain.Order, dueDate string, currentDate time.Time) error`.
- [x] Create `resetRejectedState(order *domain.Order)`.
- [x] Apply helpers to memory `UpdateOrderDueDate`.
- [x] Apply helpers to memory `RejectOrders`.
- [x] Apply helpers to memory `ResubmitOrder`.
- [x] Apply helpers to memory `CancelOrders`.
- [x] Mirror compatible helpers in `internal/api/postgres_store.go`.
- [x] Keep database-specific locking and SQL behavior in PostgreSQL methods.
- [x] Confirm successful mutations still bump line revision.
- [x] Confirm successful mutations still write audit records.
- [x] Run order lifecycle and authorization tests.
- [x] Update `doc/progress.md` when this phase is complete.

## 繁體中文

目標：降低 memory store 與 PostgreSQL store 之間重複的訂單生命週期 validation 與 authorization logic。

### Checklist

- [x] 建立 `canUpdateOrderDetails(order domain.Order, claims auth.Claims) error`。
- [x] 建立 `canCancelOrder(order domain.Order, claims auth.Claims) error`。
- [x] 建立 `canRejectOrder(order domain.Order, claims auth.Claims) error`。
- [x] 建立 `applyOptionalQuantity(order *domain.Order, quantity int) error`。
- [x] 建立 `applyOptionalDueDate(order *domain.Order, dueDate string, currentDate time.Time) error`。
- [x] 建立 `resetRejectedState(order *domain.Order)`。
- [x] 將 helper 套用到 memory `UpdateOrderDueDate`。
- [x] 將 helper 套用到 memory `RejectOrders`。
- [x] 將 helper 套用到 memory `ResubmitOrder`。
- [x] 將 helper 套用到 memory `CancelOrders`。
- [x] 在 `internal/api/postgres_store.go` mirror compatible helpers。
- [x] PostgreSQL methods 保留 database-specific locking 與 SQL 行為。
- [x] 確認成功 mutation 仍會 bump line revision。
- [x] 確認成功 mutation 仍會寫入 audit record。
- [x] 執行 order lifecycle 與 authorization tests。
- [x] 完成本階段後更新 `doc/progress.md`。
