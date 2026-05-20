# PR Documentation: Harden Clean Deployment Validation

## zh-TW

### Summary

本次變更修正 PR #36 `v0.1.37` clean install 驗證後發現的兩個問題：

- `scripts/verify-hpa-behavior.sh` 的 CPU scenario cleanup 未移除測試 sidecar，導致 HPA 持續維持高 replicas。
- API / scheduler-worker 在 PostgreSQL 或 Kafka 尚未 ready 時會直接退出，clean install 初期容易產生 transient CrashLoop/BackOff。

### Root Cause

CPU HPA 驗證腳本會用 `kubectl patch deployment` 把 `hpa-cpu-load` sidecar 注入 `woms-woms-worker`。原本 cleanup 只用 Helm values 還原 KEDA triggers，沒有移除這個 patch 注入的 sidecar，因此 sidecar 會繼續燒 CPU，HPA 就不會 scale down。

API 與 worker 的啟動流程原本只做單次 PostgreSQL/Kafka readiness check。Clean install 時 PostgreSQL、Kafka、topic hook 與 app pods 會同時建立，app container 可能早於 dependency ready 而退出。

### Changes

- 在 `scripts/verify-hpa-behavior.sh` 加入 idempotent cleanup guard。
- 新增 `remove_worker_deployment_cpu_load`，用 strategic merge patch 刪除 `hpa-cpu-load` sidecar，並等待 worker rollout 完成。
- 將 startup retry 與 TCP readiness helper 抽到 `internal/startup`，避免 API 與 worker 兩份實作 drift。
- API 啟動時對 PostgreSQL store 與 Kafka broker TCP readiness 做 bounded retry/backoff；PostgreSQL 初始化使用 `PingContext` 接受 retry timeout 控制，Kafka readiness 會嘗試所有設定的 brokers，只要任一 broker 可連即通過。
- Scheduler worker 啟動時對 PostgreSQL `PingContext` 與 Kafka broker TCP readiness 做 bounded retry/backoff；Kafka reader 也使用 trim 後的 broker 清單。
- HPA cleanup 分離 Helm restore 與 CPU sidecar cleanup flags，讓 Kafka/Gthulhu scenario 若提早失敗也會還原 scenario-specific Helm values；正常完成路徑不再重設 `CLEANED_UP` guard，避免 EXIT trap 重複做完整 cleanup。
- Helm chart 新增 retry env defaults：
  - `API_DEPENDENCY_RETRY_TIMEOUT_MS`
  - `API_DEPENDENCY_RETRY_INTERVAL_MS`
  - `WORKER_DEPENDENCY_RETRY_TIMEOUT_MS`
  - `WORKER_DEPENDENCY_RETRY_INTERVAL_MS`
- README / README.en.md / README.zh-TW.md 補上 dependency retry 設定說明。
- 增加 Go tests 與 Helm static tests，覆蓋 shared retry helper、Kafka broker readiness 與 HPA cleanup 行為。

### Validation

已執行：

```bash
GOCACHE=/tmp/woms-go-build-cache GOMODCACHE=/tmp/woms-go-mod-cache go test ./...
node --test web/*.test.mjs deploy/helm/woms/*.test.mjs
./scripts/verify-hpa-render.sh
GTHULHU_ENABLED=true ./scripts/verify-hpa-render.sh
HPA_SCENARIO=cpu TIMEOUT_SECONDS=600 ./scripts/verify-hpa-behavior.sh
helm lint ./deploy/helm/woms
helm template woms ./deploy/helm/woms --namespace woms --show-only templates/api-deployment.yaml
helm template woms ./deploy/helm/woms --namespace woms --show-only templates/worker-deployment.yaml
```

CPU HPA scenario 實跑後確認 `woms-woms-worker` 已恢復為單一 `scheduler-worker` container，沒有殘留 `hpa-cpu-load` sidecar。

### Notes

Zed editor 可能會在 raw Helm template 中對 `{{ ... }}` 顯示 YAML 語法誤報；`helm lint` 與 `helm template --show-only` 均確認 `api-deployment.yaml` 與 `worker-deployment.yaml` render 後是合法 YAML。

## en

### Summary

This change fixes two issues found during the PR #36 `v0.1.37` clean install validation:

- The CPU scenario in `scripts/verify-hpa-behavior.sh` did not remove its injected test sidecar, leaving HPA at elevated replicas.
- API / scheduler-worker exited immediately when PostgreSQL or Kafka was not ready during fresh installs.

### Root Cause

The CPU HPA validation script injects an `hpa-cpu-load` sidecar into `woms-woms-worker` with `kubectl patch deployment`. The old cleanup only restored Helm values for KEDA triggers and did not remove the sidecar. The sidecar kept burning CPU, so HPA could not scale down.

API and worker startup previously performed only one PostgreSQL/Kafka readiness check. During clean installs, PostgreSQL, Kafka, the topic hook, and app pods are created concurrently, so application containers can start before dependencies are ready.

### Changes

- Add an idempotent cleanup guard to `scripts/verify-hpa-behavior.sh`.
- Add `remove_worker_deployment_cpu_load`, using strategic merge patch to delete the `hpa-cpu-load` sidecar and wait for worker rollout.
- Move startup retry and TCP readiness helpers into `internal/startup` so API and worker behavior cannot drift.
- Add bounded startup retry/backoff for API PostgreSQL store and Kafka broker TCP readiness; PostgreSQL initialization uses `PingContext` under the retry timeout, and Kafka readiness now tries all configured brokers and succeeds if any broker is reachable.
- Add bounded startup retry/backoff for scheduler-worker PostgreSQL `PingContext` and Kafka broker TCP readiness; the Kafka reader also uses the trimmed broker list.
- Split HPA cleanup into Helm restore and CPU sidecar cleanup flags so Kafka/Gthulhu scenarios restore scenario-specific Helm values even when they exit early; the success path no longer resets the `CLEANED_UP` guard, avoiding duplicate full cleanup from the EXIT trap.
- Add Helm retry env defaults:
  - `API_DEPENDENCY_RETRY_TIMEOUT_MS`
  - `API_DEPENDENCY_RETRY_INTERVAL_MS`
  - `WORKER_DEPENDENCY_RETRY_TIMEOUT_MS`
  - `WORKER_DEPENDENCY_RETRY_INTERVAL_MS`
- Document the dependency retry settings in README / README.en.md / README.zh-TW.md.
- Add Go tests and Helm static tests for the shared retry helper, Kafka broker readiness, and HPA cleanup behavior.

### Validation

Executed:

```bash
GOCACHE=/tmp/woms-go-build-cache GOMODCACHE=/tmp/woms-go-mod-cache go test ./...
node --test web/*.test.mjs deploy/helm/woms/*.test.mjs
./scripts/verify-hpa-render.sh
GTHULHU_ENABLED=true ./scripts/verify-hpa-render.sh
HPA_SCENARIO=cpu TIMEOUT_SECONDS=600 ./scripts/verify-hpa-behavior.sh
helm lint ./deploy/helm/woms
helm template woms ./deploy/helm/woms --namespace woms --show-only templates/api-deployment.yaml
helm template woms ./deploy/helm/woms --namespace woms --show-only templates/worker-deployment.yaml
```

After the real CPU HPA scenario, `woms-woms-worker` was confirmed to have only the `scheduler-worker` container, with no leftover `hpa-cpu-load` sidecar.

### Notes

Zed may report YAML syntax warnings on raw Helm templates because of `{{ ... }}` expressions. `helm lint` and `helm template --show-only` both confirm that `api-deployment.yaml` and `worker-deployment.yaml` render to valid YAML.
