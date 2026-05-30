# PH10 - Test Helper Refactor

Language: [English](#english) | [繁體中文](#繁體中文)

## English

Goal: reduce test helper complexity while preserving assertions and edge-case coverage.

### Checklist

- [x] Locate `internal/api/server_test.go` test at or near `FIX.md` line 327.
- [x] Split setup logic from request execution for that test.
- [x] Split response decoding from assertions for that test.
- [x] Locate `internal/api/server_test.go` test at or near `FIX.md` line 1787.
- [x] Split setup logic from request execution for that test.
- [x] Split response decoding from assertions for that test.
- [x] Locate `internal/api/server_test.go` test at or near `FIX.md` line 1996.
- [x] Split setup logic from request execution for that test.
- [x] Split response decoding from assertions for that test.
- [x] Keep test names behavior-focused.
- [x] Extract metric family lookup from `gatherLabeledCounterValue`.
- [x] Extract metric label matching from `gatherLabeledCounterValue`.
- [x] Extract counter value reading from `gatherLabeledCounterValue`.
- [x] Consider renaming `gatherGuageValue` to `gatherGaugeValue` if all call sites can be updated in the same patch.
- [x] Run affected API package tests.
- [x] Run affected metrics package tests.
- [x] Confirm no edge-case assertions were removed.
- [x] Update `doc/progress.md` when this phase is complete.

## 繁體中文

目標：降低 test helper 複雜度，同時保留 assertion 與 edge-case coverage。

### Checklist

- [x] 找出 `internal/api/server_test.go` 中接近 `FIX.md` line 327 的測試。
- [x] 將該測試的 setup logic 與 request execution 拆開。
- [x] 將該測試的 response decoding 與 assertions 拆開。
- [x] 找出 `internal/api/server_test.go` 中接近 `FIX.md` line 1787 的測試。
- [x] 將該測試的 setup logic 與 request execution 拆開。
- [x] 將該測試的 response decoding 與 assertions 拆開。
- [x] 找出 `internal/api/server_test.go` 中接近 `FIX.md` line 1996 的測試。
- [x] 將該測試的 setup logic 與 request execution 拆開。
- [x] 將該測試的 response decoding 與 assertions 拆開。
- [x] 保持 test names 以 behavior-focused 命名。
- [x] 從 `gatherLabeledCounterValue` 抽出 metric family lookup。
- [x] 從 `gatherLabeledCounterValue` 抽出 metric label matching。
- [x] 從 `gatherLabeledCounterValue` 抽出 counter value reading。
- [x] 若所有 call site 可同 patch 更新，考慮將 `gatherGuageValue` 修正為 `gatherGaugeValue`。
- [x] 執行受影響 API package tests。
- [x] 執行受影響 metrics package tests。
- [x] 確認沒有移除 edge-case assertion。
- [x] 完成本階段後更新 `doc/progress.md`。
