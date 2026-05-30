# PH06 - Schedule Preview And Job Refactor

Language: [English](#english) | [繁體中文](#繁體中文)

## English

Goal: split schedule preview and job execution flows into smaller units without changing conflict or lock behavior.

### Checklist

- [x] Split memory `planLocked` with `resolveScheduleLineLocked`.
- [x] Split memory `planLocked` with `parseScheduleDatesLocked`.
- [x] Split memory `planLocked` with `draftOrderInputsLocked`.
- [x] Split memory `planLocked` with `selectedOrderInputsLocked`.
- [x] Split memory `planLocked` with `resolutionOrderIDSetLocked`.
- [x] Split memory `planLocked` with `existingAllocationInputsLocked`.
- [x] Split memory `planLocked` with `runSchedulePlan`.
- [x] Split PostgreSQL `previewFromDB` into line resolution.
- [x] Split PostgreSQL `previewFromDB` into date parsing.
- [x] Split PostgreSQL `previewFromDB` into input loading.
- [x] Split PostgreSQL `previewFromDB` into existing allocation loading.
- [x] Split PostgreSQL `previewFromDB` into plan execution.
- [x] Split PostgreSQL `previewFromDB` into draft filtering.
- [x] Split PostgreSQL `previewFromDB` into preview record creation.
- [x] Keep JSON persistence in `PreviewSchedule`.
- [x] Extract preview validation helper for schedule jobs.
- [x] Extract stale revision validation helper.
- [x] Extract job creation helper.
- [x] Extract job fail, run, and complete state transition helpers.
- [x] Preserve line lock behavior.
- [x] Preserve queued job deletion behavior.
- [x] Run schedule preview and job tests.
- [x] Verify stale preview revision, manual force audit, line lock handling, and conflict persistence.
- [x] Update `doc/progress.md` when this phase is complete.

## 繁體中文

目標：將 schedule preview 與 job execution flow 拆成較小單元，且不改變 conflict 或 lock 行為。

### Checklist

- [x] 用 `resolveScheduleLineLocked` 拆分 memory `planLocked`。
- [x] 用 `parseScheduleDatesLocked` 拆分 memory `planLocked`。
- [x] 用 `draftOrderInputsLocked` 拆分 memory `planLocked`。
- [x] 用 `selectedOrderInputsLocked` 拆分 memory `planLocked`。
- [x] 用 `resolutionOrderIDSetLocked` 拆分 memory `planLocked`。
- [x] 用 `existingAllocationInputsLocked` 拆分 memory `planLocked`。
- [x] 用 `runSchedulePlan` 拆分 memory `planLocked`。
- [x] 將 PostgreSQL `previewFromDB` 拆出 line resolution。
- [x] 將 PostgreSQL `previewFromDB` 拆出 date parsing。
- [x] 將 PostgreSQL `previewFromDB` 拆出 input loading。
- [x] 將 PostgreSQL `previewFromDB` 拆出 existing allocation loading。
- [x] 將 PostgreSQL `previewFromDB` 拆出 plan execution。
- [x] 將 PostgreSQL `previewFromDB` 拆出 draft filtering。
- [x] 將 PostgreSQL `previewFromDB` 拆出 preview record creation。
- [x] 將 JSON persistence 保留在 `PreviewSchedule`。
- [x] 抽出 schedule job preview validation helper。
- [x] 抽出 stale revision validation helper。
- [x] 抽出 job creation helper。
- [x] 抽出 job fail、run、complete state transition helper。
- [x] 保留 line lock 行為。
- [x] 保留 queued job deletion 行為。
- [x] 執行 schedule preview 與 job tests。
- [x] 驗證 stale preview revision、manual force audit、line lock handling 與 conflict persistence。
- [x] 完成本階段後更新 `doc/progress.md`。
