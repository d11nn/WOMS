# Vibe Coding Prompt For Code Quality Fix

Language: [English](#english) | [繁體中文](#繁體中文)

## English

Use this prompt to run the WOMS code quality fix work with one master agent and one or more slave agents.

### Mission

Fix every code quality issue listed in `FIX.md` by following `detail.md`, `doc/progress.md`, and the phase task files under `doc/task/`. Preserve current behavior, deterministic scheduling, localized zh-TW copy, UTF-8 encoding, API response shapes, audit behavior, and existing local changes.

### Required Inputs

- `FIX.md`: source list of code quality findings.
- `detail.md`: full implementation plan and acceptance criteria.
- `doc/progress.md`: total progress tracker owned by the master agent.
- `doc/task/<PH>.md`: phase-level implementation checklist owned by the assigned slave agent while that phase is active.
- `AGENTS.md`: repository rules for branch, Git, documentation, testing, encoding, and deployment discipline.

### Global Rules For All Agents

- Do not work directly on `main`.
- Before creating or rebasing a feature branch, run `git fetch origin` and confirm the branch is based on the latest `origin/main`.
- Use feature branch naming `feat/xxxx-xxxx`.
- Preserve user changes. Do not revert unrelated local modifications.
- Keep all source code, SQL, frontend files, and Markdown in UTF-8.
- Treat mojibake markers such as `敺`, `蝔`, `銝`, `撌`, and `�` as defects to investigate, not text to preserve blindly.
- Use TDD or focused verification for scheduling, authorization, state transitions, Redis locks, Kafka job flow, KEDA behavior, and other risky logic.
- Keep refactors behavior-preserving unless a task explicitly requires a behavior change.
- Go code must be `gofmt` compatible and pass `go test ./...` before final completion.
- Run configured frontend checks from `package.json` when JavaScript or web behavior is touched.
- Update README files only if implementation or deployment behavior changes. Quality-only refactors normally do not require README updates.
- Do not commit secrets, `.env`, local volumes, build output, cache files, or private IDE settings.

### Master Agent Responsibilities

The master agent owns planning, sequencing, conflict control, and final acceptance.

1. Read `FIX.md`, `detail.md`, `doc/progress.md`, and every file in `doc/task/`.
2. Confirm branch and worktree status before assigning work.
3. Keep `doc/progress.md` as the single source of truth for total progress.
4. Assign one phase file at a time unless two phases clearly do not touch the same files.
5. Before assigning parallel slave work, check likely file overlap. Avoid concurrent edits to the same file.
6. Tell each slave agent exactly which `doc/task/<PH>.md` file to implement.
7. Require each slave to update its own phase checklist before reporting completion.
8. After a slave reports completion, review the diff, run or verify relevant tests, and then update the matching checkbox in `doc/progress.md`.
9. Track stale analyzer findings separately. A finding may be marked complete only when it is fixed or explicitly documented as stale against the current revision.
10. Before final completion, run the completion gate from `doc/progress.md` and Phase 13.

### Slave Agent Responsibilities

Each slave agent owns exactly one assigned phase at a time.

1. Read `AGENTS.md`, `FIX.md`, `detail.md`, `doc/progress.md`, and the assigned `doc/task/<PH>.md`.
2. Implement only the assigned phase unless the master explicitly expands scope.
3. Before editing, inspect current code and tests around the assigned files.
4. Keep edits small and behavior-preserving.
5. Update the checklist in the assigned `doc/task/<PH>.md` as items are completed.
6. Run focused tests for the touched area.
7. If a required full test is too slow or unavailable, record exactly what was run and why broader verification was deferred.
8. Do not update `doc/progress.md` directly unless the master instructs you to do so.
9. Notify the master when the phase is ready for review with:
   - phase name and file path,
   - files changed,
   - checklist items completed,
   - tests or verification run,
   - known risks, blocked items, or stale findings.

### Phase Order

Default to this order unless the master identifies a safe reason to split or reorder:

1. `doc/task/PH01-baseline-and-issue-reconciliation.md`
2. `doc/task/PH02-low-risk-formatting-and-literal-cleanup.md`
3. `doc/task/PH03-api-startup-refactor.md`
4. `doc/task/PH04-scheduler-algorithm-refactor.md`
5. `doc/task/PH05-shared-order-update-helpers.md`
6. `doc/task/PH06-schedule-preview-and-job-refactor.md`
7. `doc/task/PH07-calendar-refactor.md`
8. `doc/task/PH08-production-flow-refactor.md`
9. `doc/task/PH09-user-deletion-and-hpa-demo-refactor.md`
10. `doc/task/PH10-test-helper-refactor.md`
11. `doc/task/PH11-parameter-list-cleanup.md`
12. `doc/task/PH12-frontend-preview-action-refactor.md`
13. `doc/task/PH13-analyzer-pass-and-final-verification.md`

### Coordination Protocol

Use this status format between slave and master:

```text
Phase: PHxx - phase title
Task file: doc/task/PHxx-name.md
Status: ready for review | blocked | needs master decision
Changed files:
- path/to/file
Completed checklist:
- item
Verification:
- command or manual check
Notes:
- stale finding, risk, or blocker
```

Master review response format:

```text
Phase: PHxx - phase title
Decision: accepted | needs changes | blocked
Progress update:
- doc/progress.md checkbox updated: yes/no
Required follow-up:
- next action
```

### Final Completion Gate

The master agent may close the work only after all of these are true:

- Every `FIX.md` finding is fixed or documented as stale against the current revision.
- Every phase checklist in `doc/task/` is complete or has an explicit master-approved exception.
- Every phase checkbox and completion gate checkbox in `doc/progress.md` is updated.
- `go test ./...` passes.
- Configured frontend checks pass or are documented as unavailable.
- Any available quality or Sonar scanner command has been run or documented as unavailable.
- `git diff --check` passes.
- A mojibake search for `敺`, `蝔`, `銝`, `撌`, and `�` has been performed.
- No secrets, build output, cache files, local volumes, or private IDE settings are staged.
- Existing user-facing behavior and API response shapes remain unchanged.

## 繁體中文

使用本 prompt 以一位 master agent 與一位或多位 slave agent 執行 WOMS code quality 修正工作。

### 任務目標

依照 `detail.md`、`doc/progress.md` 與 `doc/task/` 底下各 phase task file，修正 `FIX.md` 列出的所有 code quality issue。必須保留目前行為、deterministic scheduling、繁中在地化文案、UTF-8 編碼、API response shape、audit 行為，以及既有 local changes。

### 必要輸入

- `FIX.md`：code quality finding 來源清單。
- `detail.md`：完整實作計畫與 acceptance criteria。
- `doc/progress.md`：由 master agent 維護的總進度追蹤檔。
- `doc/task/<PH>.md`：各 phase checklist，phase 執行中由被指派的 slave agent 維護。
- `AGENTS.md`：repository 的 branch、Git、文件、測試、編碼與部署規則。

### 所有 Agent 的共通規則

- 不得直接在 `main` 開發。
- 建立或 rebase feature branch 前，先執行 `git fetch origin`，並確認 branch 基於最新 `origin/main`。
- Feature branch 使用 `feat/xxxx-xxxx` 命名。
- 保留使用者既有變更，不得 revert 無關 local modification。
- 所有 source code、SQL、frontend files 與 Markdown 都必須維持 UTF-8。
- 遇到 `敺`、`蝔`、`銝`、`撌`、`�` 等 mojibake 指標時，應視為需要追查的缺陷，不可直接當成正常文字保留。
- 對 scheduling、authorization、state transition、Redis lock、Kafka job flow、KEDA 行為與其他高風險邏輯，採 TDD 或明確 focused verification。
- 除非 task 明確要求行為改變，否則所有 refactor 都必須保持行為不變。
- Go code 必須可 `gofmt`，最終完成前必須通過 `go test ./...`。
- 若碰到 JavaScript 或 web 行為，需執行 `package.json` 中已設定的 frontend checks。
- 只有 implementation 或 deployment behavior 改變時才更新 README；單純 quality refactor 通常不需要更新 README。
- 不得提交 secrets、`.env`、local volumes、build output、cache files 或 private IDE settings。

### Master Agent 職責

Master agent 負責規劃、排序、衝突控管與最終驗收。

1. 閱讀 `FIX.md`、`detail.md`、`doc/progress.md` 與 `doc/task/` 中所有 task files。
2. 指派工作前確認 branch 與 worktree 狀態。
3. 將 `doc/progress.md` 維持為總進度的 single source of truth。
4. 預設一次只指派一個 phase；除非兩個 phase 明確不會修改相同檔案，才可平行執行。
5. 指派平行 slave work 前，先確認可能的檔案重疊，避免同一檔案被同時修改。
6. 明確告知每個 slave agent 要實作哪一個 `doc/task/<PH>.md`。
7. 要求 slave 回報完成前先更新自己的 phase checklist。
8. Slave 回報後，review diff、執行或確認相關測試，再更新 `doc/progress.md` 對應 checkbox。
9. 另外追蹤 stale analyzer finding。只有在 finding 已修正，或已依目前 revision 明確記錄為 stale 時，才能視為完成。
10. 最終完成前，執行 `doc/progress.md` 與 PH13 的 completion gate。

### Slave Agent 職責

每個 slave agent 一次只負責一個被指派的 phase。

1. 閱讀 `AGENTS.md`、`FIX.md`、`detail.md`、`doc/progress.md` 與被指派的 `doc/task/<PH>.md`。
2. 只實作被指派的 phase，除非 master 明確擴大 scope。
3. 修改前先檢查相關程式碼與測試。
4. 保持 edits 小而且行為不變。
5. 完成項目時，更新被指派的 `doc/task/<PH>.md` checklist。
6. 對 touched area 執行 focused tests。
7. 若必要的 full test 太慢或目前不可用，明確記錄已執行內容，以及為何延後更廣泛驗證。
8. 除非 master 指示，否則不要直接更新 `doc/progress.md`。
9. Phase 可 review 時通知 master，內容包含：
   - phase name 與 file path，
   - changed files，
   - completed checklist items，
   - 已執行的 tests 或 verification，
   - known risks、blocked items 或 stale findings。

### Phase 順序

除非 master 判斷可安全拆分或重排，預設依下列順序執行：

1. `doc/task/PH01-baseline-and-issue-reconciliation.md`
2. `doc/task/PH02-low-risk-formatting-and-literal-cleanup.md`
3. `doc/task/PH03-api-startup-refactor.md`
4. `doc/task/PH04-scheduler-algorithm-refactor.md`
5. `doc/task/PH05-shared-order-update-helpers.md`
6. `doc/task/PH06-schedule-preview-and-job-refactor.md`
7. `doc/task/PH07-calendar-refactor.md`
8. `doc/task/PH08-production-flow-refactor.md`
9. `doc/task/PH09-user-deletion-and-hpa-demo-refactor.md`
10. `doc/task/PH10-test-helper-refactor.md`
11. `doc/task/PH11-parameter-list-cleanup.md`
12. `doc/task/PH12-frontend-preview-action-refactor.md`
13. `doc/task/PH13-analyzer-pass-and-final-verification.md`

### 協作協議

Slave 對 master 使用下列 status 格式：

```text
Phase: PHxx - phase title
Task file: doc/task/PHxx-name.md
Status: ready for review | blocked | needs master decision
Changed files:
- path/to/file
Completed checklist:
- item
Verification:
- command or manual check
Notes:
- stale finding, risk, or blocker
```

Master review response 使用下列格式：

```text
Phase: PHxx - phase title
Decision: accepted | needs changes | blocked
Progress update:
- doc/progress.md checkbox updated: yes/no
Required follow-up:
- next action
```

### 最終完成門檻

Master agent 只有在下列條件都成立時，才能結束本次工作：

- `FIX.md` 每個 finding 都已修正，或已依目前 revision 記錄為 stale。
- `doc/task/` 中每個 phase checklist 都已完成，或有 master 核准的明確例外。
- `doc/progress.md` 中每個 phase checkbox 與 completion gate checkbox 都已更新。
- `go test ./...` 通過。
- 已設定的 frontend checks 通過，或記錄為目前不可用。
- 已執行可用的 quality 或 Sonar scanner command，或記錄為目前不可用。
- `git diff --check` 通過。
- 已搜尋 `敺`、`蝔`、`銝`、`撌`、`�` 等 mojibake 指標。
- 沒有 staged secrets、build output、cache files、local volumes 或 private IDE settings。
- 既有 user-facing behavior 與 API response shape 保持不變。
