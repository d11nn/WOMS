# Code Quality Fix Plan

## Objective

Fix every code quality issue listed in `FIX.md` while preserving current behavior, existing local changes, localized user-facing copy, and the deterministic scheduling rules.

## Ground Rules

- Work on a feature branch, not `main`.
- Before creating or rebasing the branch, run `git fetch origin` and confirm the branch is based on the latest `origin/main`.
- Preserve all existing local changes unless the owner explicitly asks to replace them.
- Keep all files UTF-8 encoded.
- Use small, behavior-preserving refactors first; do not mix quality cleanup with feature changes.
- Run focused tests after each risky refactor, then run the full available test suite before finishing.
- Update README files only if the implementation or deployment behavior changes. These quality-only refactors should normally require no README change.

## Phase 1: Baseline And Issue Reconciliation

1. Capture the current branch and worktree state.
   - Run `git status --short`.
   - Run `git branch --show-current`.
   - Confirm the worktree already contains local modifications in API and web files, so edits must be scoped carefully.

2. Validate that `FIX.md` line numbers match the current files.
   - `cmd/api/main.go` currently ends around line 149, while `FIX.md` reports issues at lines 478, 543, and 626.
   - Treat the existing `cmd/api/main.go` issue at line 23 as the current `main` function complexity issue.
   - Search for moved code before closing the stale `cmd/api/main.go` findings.
   - If the analyzer report was generated from a different revision, regenerate the quality report after refactoring and use the fresh output as the source of truth.

3. Establish a behavior baseline.
   - Run `go test ./...`.
   - Run available frontend checks if configured in `package.json`.
   - Record any failures that existed before the refactor so they are not confused with new regressions.

## Phase 2: Low-Risk Formatting And Literal Cleanup

1. Fix `Dockerfile.web`.
   - Split the long `sed` command into multiple backslash-continued lines.
   - Keep the same substitutions and file path.
   - Verify `docker build -f Dockerfile.web .` only if Docker is available in the environment.

2. Fix the duplicated PostgreSQL revision SQL literal.
   - In `internal/api/postgres_store.go`, introduce a package-level constant such as `bumpProductionLineRevisionSQL`.
   - Replace all repeated uses of the exact SQL statement with the constant.
   - Do not change transaction boundaries or audit behavior.

3. Fix small frontend quality findings in `web/app.js`.
   - Replace the startup promise chain with top-level `await` inside the module script flow.
   - Keep the same 401 handling, session clearing, workspace rendering, and warning messages.
   - Update `cssEscape` fallback to use `String.raw` so the escape replacement remains explicit and analyzer-compliant.

## Phase 3: API Startup Refactor

1. Split `cmd/api/main.go` startup wiring into focused helpers.
   - Extract store initialization into `buildStore(config apiConfig) (api.Store, func(), error)`.
   - Extract publisher initialization into `buildPublisher(config apiConfig) (api.ScheduleJobPublisher, func(), error)`.
   - Extract token session initialization into `buildTokenSessions(config apiConfig) (api.TokenSessionStore, func(), error)`.
   - Extract retry wrappers for PostgreSQL, Kafka, and Redis dependencies to reduce nested branches in `main`.

2. Keep `main` as orchestration only.
   - Parse config.
   - Build dependencies.
   - Defer cleanup callbacks.
   - Create `http.Server`.
   - Start listening.

3. Verify startup tests or add focused tests if they do not exist.
   - Cover memory store selection.
   - Cover demo memory store selection.
   - Cover Redis session validation error when the address is missing.
   - Cover dependency retry helper behavior through injectable functions if practical.

## Phase 4: Scheduler Algorithm Refactor

1. Split `internal/scheduler/scheduler.go` `Plan` into explicit steps.
   - `validateRequest(req Request) error`
   - `sortedOrders(req.Orders) []OrderInput`
   - `buildCapacityLedger(req Request) capacityLedger`
   - `planOrder(req Request, ledger *capacityLedger, order OrderInput, result *Result) error`
   - `availableCapacity(req Request, ledger capacityLedger, day time.Time) int`
   - `recordManualForceConflict(...)`
   - `recordLateCapacityConflict(...)`

2. Introduce a small internal state type.
   - Include high-priority usage, low-priority usage, new usage, low-priority order IDs by date, locked order IDs by date, and reported manual-force conflicts.
   - Keep maps private to the scheduler package.

3. Preserve deterministic behavior.
   - Maintain the exact sort order: high priority first, then due date, then order ID.
   - Keep date truncation behavior unchanged.
   - Keep manual force conflict semantics unchanged.

4. Verification.
   - Run scheduler tests.
   - Add focused tests if any branch is not already covered: invalid request, late conflict, manual force affected order reporting, locked allocation capacity, and same-input deterministic ordering.

## Phase 5: Shared Order Update Helpers

Several memory and PostgreSQL methods repeat authorization, update validation, quantity validation, due-date parsing, revision bumping, and audit behavior.

1. Extract shared order update validation in `internal/api/server.go`.
   - Create helper functions for role authorization:
     - `canUpdateOrderDetails(order domain.Order, claims auth.Claims) error`
     - `canCancelOrder(order domain.Order, claims auth.Claims) error`
     - `canRejectOrder(order domain.Order, claims auth.Claims) error`
   - Create helper functions for request validation:
     - `applyOptionalQuantity(order *domain.Order, quantity int) error`
     - `applyOptionalDueDate(order *domain.Order, dueDate string, currentDate time.Time) error`
     - `resetRejectedState(order *domain.Order)`

2. Apply these helpers to memory store methods.
   - `UpdateOrderDueDate`
   - `RejectOrders`
   - `ResubmitOrder`
   - `CancelOrders`

3. Mirror compatible helpers in `internal/api/postgres_store.go`.
   - Reuse shared package-level helpers when they do not depend on locks or SQL.
   - Keep database-specific work in PostgreSQL methods.
   - Ensure all successful mutations still bump line revision and write audits.

4. Verification.
   - Run order lifecycle tests:
     - create order validation
     - update due date
     - reject
     - resubmit
     - cancel
     - sales and scheduler authorization boundaries

## Phase 6: Schedule Preview And Job Refactor

1. Split memory planning in `internal/api/server.go`.
   - Break `planLocked` into:
     - `resolveScheduleLineLocked`
     - `parseScheduleDatesLocked`
     - `draftOrderInputsLocked`
     - `selectedOrderInputsLocked`
     - `resolutionOrderIDSetLocked`
     - `existingAllocationInputsLocked`
     - `runSchedulePlan`
   - Keep `planLocked` as the high-level coordinator.

2. Split PostgreSQL preview planning in `internal/api/postgres_store.go`.
   - Break `previewFromDB` into line resolution, date parsing, input loading, existing allocation loading, plan execution, draft filtering, and preview record creation.
   - Keep JSON persistence in `PreviewSchedule`, not in the lower-level planner.

3. Refactor `CreateScheduleJob` and `ExecuteScheduleJob`.
   - Extract preview validation into a helper.
   - Extract stale revision validation.
   - Extract job creation.
   - Extract job state transitions such as fail, run, complete.
   - Preserve line lock behavior and queued job deletion behavior.

4. Verification.
   - Run schedule preview and job tests.
   - Pay special attention to stale preview revision, manual force audit creation, line lock handling, and conflict persistence rules.

## Phase 7: Calendar Refactor

1. Split memory calendar generation.
   - Extract line access validation.
   - Extract month parsing and calendar window creation.
   - Extract persisted allocation mapping.
   - Extract sales pending backlog allocation generation.
   - Keep the response shape unchanged.

2. Split PostgreSQL calendar generation similarly.
   - Extract SQL row scanning into `calendarAllocationsFromRows`.
   - Extract pending sales backlog lookup into `postgresPendingBacklogCalendarAllocations`.
   - Keep query ordering unchanged.

3. Verification.
   - Run calendar tests:
     - month boundaries
     - adjacent visible days
     - other-month exclusion
     - sales pending preview allocations
     - scheduler line visibility restrictions

## Phase 8: Production Flow Refactor

1. Refactor production start and confirm methods.
   - In memory store and PostgreSQL store, split validation from mutation.
   - Extract allocation lookup.
   - Extract full-completion mutation.
   - Extract partial-completion and remainder creation.
   - Extract production audit reason formatting.

2. Preserve production invariants.
   - Starting production must lock scheduled allocations.
   - Confirming production must reject quantities above the scheduled allocation quantity.
   - Partial production must keep completed allocation history and return the remainder to the pending queue.
   - Line revision must bump after production state changes.

3. Verification.
   - Run production tests for start, full confirm, partial confirm, and over-quantity rejection.

## Phase 9: User Deletion And HPA Demo Refactor

1. Refactor `DeleteUser`.
   - Extract `userHasOrderReferencesLocked`.
   - Extract `userHasAuditReferencesLocked`.
   - Extract `userHasPreviewReferencesLocked`.
   - Extract `disableUserLocked`.
   - Keep self-delete behavior as disable, not hard delete.

2. Refactor HPA demo cleanup and summary.
   - Split job cancellation/deletion, order deletion, allocation filtering, line deletion, and audit filtering into helpers.
   - Split summary collection into helpers for line counts, order counts, job statuses, failed messages, and recent jobs.
   - Preserve the current summary fields and defaults.

3. Refactor Kubernetes autoscaling state loading.
   - Extract service account client construction.
   - Extract cache lookup and cache store.
   - Extract HPA fetch, deployment fetch, and pod readiness fetch.
   - Keep timeout and error aggregation behavior unchanged.

4. Verification.
   - Run HPA demo and Kubernetes autoscaling tests.
   - Ensure no real Kubernetes cluster is required for normal unit tests.

## Phase 10: Test Helper Refactor

1. Refactor complex test helpers without weakening assertions.
   - In `internal/api/server_test.go`, split large tests into smaller scenario helpers around setup, request execution, response decoding, and assertion.
   - Target the tests listed in `FIX.md`:
     - demo conflict order handler test
     - sales draft preview pending allocation test
     - conflict solution test
   - Keep test names behavior-focused.

2. Refactor `internal/metrics/metrics_test.go`.
   - Split `gatherLabeledCounterValue` into:
     - metric family lookup
     - metric label matching
     - counter value extraction
   - Consider fixing the helper name typo from `gatherGuageValue` to `gatherGaugeValue` if all call sites are updated in the same patch.

3. Verification.
   - Run all affected package tests.
   - Confirm the refactor does not remove edge-case assertions.

## Phase 11: Parameter List Cleanup

1. Resolve oversized function signatures reported in `FIX.md`.
   - Search the current revision for functions with eight or more parameters, because some reported `cmd/api/main.go` line numbers are stale.
   - Replace long parameter lists with narrow option structs only where the function is internal and call sites stay readable.

2. Candidate option structs.
   - Use a `schedulePlanContext` or similar struct for schedule planning data.
   - Use a `calendarWindow` struct for start, end, month, and timezone data.
   - Use a `productionMutation` struct for production confirmation updates.

3. Verification.
   - Run `go test ./...`.
   - Re-run the analyzer to ensure parameter-count findings are gone.

## Phase 12: Frontend Preview Action Refactor

1. Refactor the complex preview click handler in `web/app.js`.
   - Create a dispatch map from `data-preview-action` to async handler functions.
   - Extract one function per action:
     - return workstation
     - retry tomorrow
     - retry suggested start
     - update conflict due date
     - unselect conflict order
     - reject preview orders
     - preview conflict solution
     - retry manual force
   - Keep the single top-level `try/catch` around dispatch or give each handler consistent error reporting.

2. Preserve UI behavior.
   - Keep all validation messages unchanged.
   - Keep selected order state updates unchanged.
   - Keep conflict acknowledgement and manual-force behavior unchanged.

3. Verification.
   - Manually test preview actions in the browser if a dev server is available.
   - Run any configured JavaScript checks.

## Phase 13: Analyzer Pass And Final Verification

1. Format all touched code.
   - Run `gofmt` on touched Go files.
   - Keep Markdown and JavaScript formatting consistent with existing style.

2. Run tests.
   - `go test ./...`
   - Any configured frontend checks from `package.json`
   - Any available quality or Sonar scanner command used by the project

3. Review quality results.
   - Confirm every `FIX.md` finding is resolved or documented as stale against the current revision.
   - Confirm no new cognitive complexity or parameter-count findings were introduced.
   - Confirm no localized strings became corrupted.

4. Final repository review.
   - Run `git diff --check`.
   - Run `git status --short`.
   - Review diffs for accidental behavior changes.

## Recommended Commit Breakdown

1. `feat: split startup and scheduler quality fixes`
   - Dockerfile line split
   - API startup helper extraction
   - scheduler algorithm helper extraction

2. `feat: reduce API store complexity`
   - shared order update helpers
   - PostgreSQL revision SQL constant
   - preview, schedule job, calendar, production, user, and HPA helper extraction

3. `feat: reduce frontend and test complexity`
   - preview action dispatch refactor
   - top-level await cleanup
   - CSS escape cleanup
   - test helper and complex test refactors

## Acceptance Criteria

- `detail.md` remains English-only.
- All findings in `FIX.md` are fixed or explicitly confirmed stale after analyzer rerun.
- `go test ./...` passes.
- Configured frontend checks pass or are documented as unavailable.
- `git diff --check` passes.
- No secrets, build output, cache files, or local environment files are added.
- Existing user-facing behavior and response shapes remain unchanged.
