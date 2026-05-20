# WOMS 實作後驗證指南

## 1. 本機靜態與單元測試

```bash
go test ./...
npm run test:web
test -z "$(gofmt -l .)"
```

期望結果：

- 所有 Go tests 通過。
- 前端 mock tests 通過。
- `gofmt` 沒有輸出。

## 2. API/JWT/RBAC 驗證

啟動 API：

```bash
JWT_SECRET=local-dev-secret go run ./cmd/api
```

登入 sales：

```bash
curl -s http://localhost:8080/api/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"sales","password":"demo"}'
```

檢查無 token：

```bash
curl -i http://localhost:8080/internal/auth/verify
```

期望：`401 Unauthorized`。

檢查 sales 禁止建立排程任務：

```bash
curl -i http://localhost:8080/api/schedules/jobs \
  -H "Authorization: Bearer <sales-token>" \
  -H 'Content-Type: application/json' \
  -d '{"lineId":"A","startDate":"2026-05-01"}'
```

期望：`403 Forbidden`。

檢查排程工程師產線隔離：

- 用 `scheduler-b` 建立 B 線 job。
- 用 `scheduler-a` 查詢該 job。
- 期望：`403 Forbidden`。

檢查月曆行為：

- 用排程工程師建立排程任務。
- `GET /api/schedules/calendar?lineId=A&month=2026-05` 會回傳已保存 allocations。
- 查詢其他排程工程師的產線會回錯誤。

## 3. Docker 驗證

```bash
docker build -f Dockerfile.api -t woms-api:local .
docker build -f Dockerfile.worker -t woms-scheduler-worker:local .
docker build -f Dockerfile.web -t woms-web:local .
docker compose up --build
```

期望：

- API health: `curl http://localhost:8080/healthz`
- Web: `http://localhost:8081`

## 4. Helm Render 驗證

```bash
helm template woms ./deploy/helm/woms
./scripts/verify-hpa-render.sh
```

期望輸出包含：

- `Deployment`：api、worker、web。
- `Ingress`：public、api-secure。
- `ScaledObject`：worker Kafka/CPU triggers。
- `ScaledObject.spec.advanced.horizontalPodAutoscalerConfig.name`：`woms-woms-worker-hpa`。
- `PodDisruptionBudget`：api 與 web，且 `minAvailable: 1`。

## 5. Ingress / Gateway 驗證

部署後執行：

```bash
curl -i https://woms.local/api/orders
curl -i https://woms.local/api/orders -H "Authorization: Bearer <valid-token>"
```

期望：

- 無 token 回 `401`。
- 有效 token 通過 Ingress auth。
- API 仍會執行自身 JWT/RBAC 檢查。
- HTTP 會 redirect HTTPS。

## 6. KEDA / HPA 驗證

確認資源：

```bash
kubectl get scaledobject,hpa -n woms
kubectl describe scaledobject -n woms
```

用 admin 登入 web，按「建立多產線排程尖峰」。確認畫面顯示 200 條產線、1,000 張訂單與 200 個 queued jobs，並顯示 Kafka topic、consumer group、HPA 與 deployment 名稱。接著觀察：

```bash
kubectl get deploy -n woms -w
kubectl get hpa -n woms -w
NAMESPACE=woms ./scripts/verify-k8s.sh
```

`verify-k8s.sh` 會驗證預設不啟用 Ingress 的 render。若部署時啟用 Ingress，請先用 `--set ingress.enabled=true` 安裝，再執行 `INGRESS_ENABLED=true NAMESPACE=woms ./scripts/verify-k8s.sh`。

期望：

- Kafka lag 上升。
- worker replicas 超過 `minReplicaCount`。
- lag 清空並等待 cooldown 後 replicas scale down。
- 若 CPU trigger 未生效，先確認 metrics-server 與 pod resource requests。
- demo 後按「清除排程尖峰資料」，確認 `L001-L200` 訂單與 jobs 清空。

## 7. API/Web High Availability 驗證

```bash
kubectl get deploy,pdb -n woms
kubectl describe pdb woms-woms-api -n woms
kubectl describe pdb woms-woms-web -n woms
```

期望：

- API 與 web 預設各有兩個 replicas。
- API 與 web PDB 都要求 `minAvailable: 1`。
- 在多節點 cluster 發生 voluntary disruption 時，至少保留一個 API pod 與一個 web pod 可用。

## 8. Gthulhu HPA Demo 驗證

前置條件：

- MicroK8s 已啟用 `dns`、`hostpath-storage` 或 `storage`、`metrics-server`、`keda`，如需 local image fallback 則啟用 `registry`。
- 從 `/home/ubuntu/Gthulhu` 的 `feat/woms-poc` branch 用 `./scripts/build-push-gthulhu-images.sh` build Gthulhu images。
- 用 `-f deploy/helm/woms/values-gthulhu-monitor.yaml` 安裝 WOMS，並把 scheduler、sidecar、manager image tag 都設為驗證 tag。
- 內建 Prometheus target 會加上 scrape-level `namespace` label，因此 Gthulhu 原始 pod namespace 需要用 `exported_namespace="woms"` 查詢。
- 這個 PoC image 的 integration overlay 會啟用 `gthulhu.scheduler.monitor.monitorAll=true`；worker 篩選由 Prometheus `pod_name` query 負責。

安裝範例：

```bash
helm upgrade --install woms ./deploy/helm/woms \
  --namespace woms --create-namespace \
  -f ./deploy/helm/woms/values-gthulhu-monitor.yaml \
  --set gthulhu.scheduler.image.tag=woms-integration-<gthulhu-short-sha> \
  --set gthulhu.scheduler.sidecar.image.tag=woms-integration-<gthulhu-short-sha> \
  --set gthulhu.manager.image.tag=woms-integration-<gthulhu-short-sha>
```

確認 trigger wiring：

```bash
./scripts/verify-gthulhu-monitoring.sh
kubectl get scaledobject woms-woms-worker -n woms -o yaml
kubectl describe hpa woms-woms-worker-hpa -n woms
```

期望：

- `ScaledObject` 有三個 triggers：Kafka、CPU、Prometheus。
- Kafka 仍為 `lagThreshold: "10"`。
- CPU 仍為 `value: "70"`。
- Gthulhu scaler health 為 `Happy`。
- Prometheus 查得到 WOMS API metrics 與 `woms-woms-worker-*` 的 `gthulhu_pod_*` metrics。
- Grafana dashboard config 內有三個 Gthulhu panels。

Proof demo 流程：

三種 scenario 分開跑，避免其他 trigger 干擾：

```bash
HPA_SCENARIO=cpu ./scripts/verify-hpa-behavior.sh
HPA_SCENARIO=kafka ./scripts/verify-hpa-behavior.sh
HPA_SCENARIO=gthulhu ./scripts/verify-hpa-behavior.sh
```

`verify-hpa-behavior.sh` 預設使用 `GTHULHU_IMAGE_TAG=woms-integration-f71f78a`；驗證其他 Gthulhu tag 時請覆寫這個環境變數。

成功時會看到類似：

```text
New size: 4; reason: external metric s2-prometheus-woms_worker_gthulhu_involuntary_ctx_switches_rate above target
New size: 8; reason: external metric s2-prometheus-woms_worker_gthulhu_involuntary_ctx_switches_rate above target
```

也可查 Prometheus：

```promql
avg(rate(gthulhu_pod_involuntary_ctx_switches_total{exported_namespace="woms",pod_name=~"woms-woms-worker-.*"}[2m]))
avg(rate(gthulhu_pod_wait_time_nanoseconds_total{exported_namespace="woms",pod_name=~"woms-woms-worker-.*"}[2m])) / 1000000000
sum(gthulhu_pod_process_count{exported_namespace="woms",pod_name=~"woms-woms-worker-.*"})
```

Scripts 只會清理臨時壓測 pods/jobs，不會移除 WOMS、Gthulhu、Prometheus 或 Grafana，方便後續檢查。

## 9. Redis Lock 驗證

同產線同時送兩個排程 job：

- 期望不產生重疊 schedule version。
- 其中一個 job 應等待、重試或乾淨失敗。

不同產線同時送 job：

- 期望可並行處理。

## 10. 完成功能標準

- 測試通過。
- README zh-TW/en 更新。
- `.gitignore` 已涵蓋新增 generated/local files。
- Docker/Helm/CI 設定同步。
- `git add`、commit、push 完成。

## 11. 前端 Smoke 驗證

- 在 `http://127.0.0.1:8081` 登入。
- 重新整理瀏覽器，確認 session 會恢復。
- 確認登入後會隱藏帳號密碼欄位，頁首顯示目前帳號與登出按鈕。
- 使用 `admin` / `demo` 登入，確認 Admin panel 可見，且非 admin 看不到。
- 切換客戶、產線、優先級精準篩選；確認狀態篩選是單選，且客戶選單只列出目前狀態/優先級範圍內的客戶。
- 用 scheduler 確認待排程訂單不能拖到當日或過去月曆日期，再拖到指定的未來月曆日期；接受 preview 後確認正式 allocation 保留在拖放日期。
- 用 scheduler 建立衝突，在衝突面板選取衝突訂單與可移動的低優先級已排程訂單，預覽最早完成解法，接受後確認被移動訂單的舊未鎖定 allocation 已被替換。
- 用 scheduler 點擊月曆內的已排程訂單，確認可轉為生產中；再點擊生產中訂單，確認可開啟回報生產。
- 輸入部分完成數量後送出，確認同一張訂單編號會以剩餘數量回到待排程。
- 用 sales 以未來交期建立草稿訂單 preview，確認 preview page 會高亮日曆結果，再確認放到待排程訂單；同時確認今日與過去交期會以 `無法被接受的交期` 阻擋。
- 用 scheduler 選取待排程訂單，先 preview，再從 preview page 確認執行。缺少 `previewId` 的直接排程 API 必須失敗。
- 刪除已選取的待排程/已排程訂單，確認被刪除訂單的月曆 allocation 也會消失。
- 使用衝突測試按鈕建立同日大量訂單，preview 後確認衝突面板佔滿預覽視窗右側，且解法控制項不會被裁切。
- 確認權限不足與操作錯誤都會用彈出訊息視窗顯示。
