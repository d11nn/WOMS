# PH09 - User Deletion And HPA Demo Refactor

Language: [English](#english) | [繁體中文](#繁體中文)

## English

Goal: reduce complexity in user deletion, HPA demo cleanup, HPA summaries, and Kubernetes autoscaling state loading.

### Checklist

- [x] Extract `userHasOrderReferencesLocked`.
- [x] Extract `userHasAuditReferencesLocked`.
- [x] Extract `userHasPreviewReferencesLocked`.
- [x] Extract `disableUserLocked`.
- [x] Confirm self-delete remains disable-only, not hard delete.
- [x] Split HPA demo job cancellation and deletion.
- [x] Split HPA demo order deletion.
- [x] Split HPA demo allocation filtering.
- [x] Split HPA demo line deletion.
- [x] Split HPA demo audit filtering.
- [x] Split summary line counts.
- [x] Split summary order counts.
- [x] Split summary job statuses.
- [x] Split summary failed messages.
- [x] Split summary recent jobs.
- [x] Preserve current HPA summary fields and defaults.
- [x] Extract service account client construction for Kubernetes autoscaling state.
- [x] Extract autoscaling cache lookup.
- [x] Extract autoscaling cache store.
- [x] Extract HPA fetch.
- [x] Extract deployment fetch.
- [x] Extract pod readiness fetch.
- [x] Keep timeout and error aggregation behavior unchanged.
- [x] Run HPA demo tests.
- [x] Run Kubernetes autoscaling tests.
- [x] Confirm normal unit tests do not require a real Kubernetes cluster.
- [x] Update `doc/progress.md` when this phase is complete.

## 繁體中文

目標：降低 user deletion、HPA demo cleanup、HPA summary 與 Kubernetes autoscaling state loading 的複雜度。

### Checklist

- [x] 抽出 `userHasOrderReferencesLocked`。
- [x] 抽出 `userHasAuditReferencesLocked`。
- [x] 抽出 `userHasPreviewReferencesLocked`。
- [x] 抽出 `disableUserLocked`。
- [x] 確認 self-delete 維持 disable-only，不做 hard delete。
- [x] 拆分 HPA demo job cancellation 與 deletion。
- [x] 拆分 HPA demo order deletion。
- [x] 拆分 HPA demo allocation filtering。
- [x] 拆分 HPA demo line deletion。
- [x] 拆分 HPA demo audit filtering。
- [x] 拆分 summary line counts。
- [x] 拆分 summary order counts。
- [x] 拆分 summary job statuses。
- [x] 拆分 summary failed messages。
- [x] 拆分 summary recent jobs。
- [x] 保留目前 HPA summary fields 與 defaults。
- [x] 抽出 Kubernetes autoscaling state 的 service account client construction。
- [x] 抽出 autoscaling cache lookup。
- [x] 抽出 autoscaling cache store。
- [x] 抽出 HPA fetch。
- [x] 抽出 deployment fetch。
- [x] 抽出 pod readiness fetch。
- [x] 保持 timeout 與 error aggregation 行為不變。
- [x] 執行 HPA demo tests。
- [x] 執行 Kubernetes autoscaling tests。
- [x] 確認一般 unit tests 不需要真實 Kubernetes cluster。
- [x] 完成本階段後更新 `doc/progress.md`。
