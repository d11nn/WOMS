# PH07 - Calendar Refactor

Language: [English](#english) | [繁體中文](#繁體中文)

## English

Goal: split calendar generation into reusable validation, window, allocation, and backlog steps.

### Checklist

- [x] Extract memory line access validation.
- [x] Extract memory month parsing.
- [x] Extract memory calendar window creation.
- [x] Extract memory persisted allocation mapping.
- [x] Extract memory sales pending backlog allocation generation.
- [x] Keep memory calendar response shape unchanged.
- [x] Extract PostgreSQL calendar line access validation if duplicated.
- [x] Extract PostgreSQL month parsing and calendar window creation if duplicated.
- [x] Extract SQL row scanning into `calendarAllocationsFromRows`.
- [x] Extract pending sales backlog lookup into `postgresPendingBacklogCalendarAllocations`.
- [x] Keep PostgreSQL query ordering unchanged.
- [x] Run calendar tests for month boundaries.
- [x] Run calendar tests for adjacent visible days.
- [x] Run calendar tests for other-month exclusion.
- [x] Run calendar tests for sales pending preview allocations.
- [x] Run calendar tests for scheduler line visibility restrictions.
- [x] Update `doc/progress.md` when this phase is complete.

## 繁體中文

目標：將 calendar generation 拆成可重用的 validation、window、allocation 與 backlog 步驟。

### Checklist

- [x] 抽出 memory line access validation。
- [x] 抽出 memory month parsing。
- [x] 抽出 memory calendar window creation。
- [x] 抽出 memory persisted allocation mapping。
- [x] 抽出 memory sales pending backlog allocation generation。
- [x] 保持 memory calendar response shape 不變。
- [x] 若有重複，抽出 PostgreSQL calendar line access validation。
- [x] 若有重複，抽出 PostgreSQL month parsing 與 calendar window creation。
- [x] 將 SQL row scanning 抽成 `calendarAllocationsFromRows`。
- [x] 將 pending sales backlog lookup 抽成 `postgresPendingBacklogCalendarAllocations`。
- [x] 保持 PostgreSQL query ordering 不變。
- [x] 執行 month boundaries calendar tests。
- [x] 執行 adjacent visible days calendar tests。
- [x] 執行 other-month exclusion calendar tests。
- [x] 執行 sales pending preview allocations calendar tests。
- [x] 執行 scheduler line visibility restrictions calendar tests。
- [x] 完成本階段後更新 `doc/progress.md`。
