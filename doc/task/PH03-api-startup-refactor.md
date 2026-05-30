# PH03 - API Startup Refactor

Language: [English](#english) | [繁體中文](#繁體中文)

## English

Goal: reduce `cmd/api/main.go` startup complexity while preserving startup behavior.

### Checklist

- [x] Extract store construction into `buildStore(config apiConfig) (api.Store, func(), error)`.
- [x] Extract publisher construction into `buildPublisher(config apiConfig) (api.ScheduleJobPublisher, func(), error)`.
- [x] Extract token session construction into `buildTokenSessions(config apiConfig) (api.TokenSessionStore, func(), error)`.
- [x] Extract PostgreSQL retry wrapper logic.
- [x] Extract Kafka retry wrapper logic.
- [x] Extract Redis retry wrapper logic.
- [x] Keep `main` focused on config parsing, dependency construction, cleanup defers, server construction, and listen startup.
- [x] Add or update tests for memory store selection.
- [x] Add or update tests for demo memory store selection.
- [x] Add or update tests for Redis session validation when address is missing.
- [x] Add retry helper tests through injectable functions where practical.
- [x] Run `go test ./cmd/api ./internal/api`.
  - Result: passed after restoring the demo memory store seed count to the expected 9 orders.
- [x] Update `doc/progress.md` when this phase is complete.
  - Result: updated by the master progress pass.

## 繁體中文

目標：降低 `cmd/api/main.go` 啟動流程複雜度，同時保留既有啟動行為。

### Checklist

- [x] 抽出 store 建構為 `buildStore(config apiConfig) (api.Store, func(), error)`。
- [x] 抽出 publisher 建構為 `buildPublisher(config apiConfig) (api.ScheduleJobPublisher, func(), error)`。
- [x] 抽出 token session 建構為 `buildTokenSessions(config apiConfig) (api.TokenSessionStore, func(), error)`。
- [x] 抽出 PostgreSQL retry wrapper logic。
- [x] 抽出 Kafka retry wrapper logic。
- [x] 抽出 Redis retry wrapper logic。
- [x] 讓 `main` 只負責 config parsing、dependency construction、cleanup defers、server construction 與 listen startup。
- [x] 新增或更新 memory store selection 測試。
- [x] 新增或更新 demo memory store selection 測試。
- [x] 新增或更新 Redis session 缺少 address 時的 validation 測試。
- [x] 可行時透過 injectable function 新增 retry helper 測試。
- [x] 執行 `go test ./cmd/api ./internal/api`。
  - 結果：修復 demo memory store seed count 回到既有測試預期的 9 筆後通過。
- [x] 完成本階段後更新 `doc/progress.md`。
  - 結果：由 master progress pass 更新。
