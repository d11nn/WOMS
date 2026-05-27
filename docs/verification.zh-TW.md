# WOMS 驗證指南

這台工作機只執行靜態、單元、Go 與 Helm render 驗證。未在本機做 UI 視覺驗證。以下瀏覽器與 GKE 驗證必須在可開瀏覽器、且能取得 NGINX Ingress 或 LoadBalancer 入口的環境執行。

## 1. 本機非 UI 驗證

```bash
npm run test:web
go test ./...
npm run test:coverage
helm template woms ./deploy/helm/woms --namespace woms
./scripts/verify-hpa-render.sh
test -z "$(gofmt -l .)"
```

期望：

- Frontend 與 Helm static tests 通過。
- Go tests 通過。
- Fast coverage 通過目前 short-term gate。`npm run test:go:coverage` 會寫出 `coverage.out`；`npm run test:web:coverage` 會輸出 Node coverage 並檢查 web line threshold。
- Helm render 出 `Deployment/api`、`Deployment/worker`、`Deployment/web`、Prometheus/Grafana、web `ScaledObject` 與 PDB。
- `ScaledObject` 指向 `Deployment/woms-woms-web`，並建立 HPA `woms-woms-web-hpa`。
- Active KEDA triggers 只有 `woms_web_nginx_requests_per_second_per_pod` 的 Prometheus trigger。
- Rendered manifests 不包含 worker Kafka lag、worker CPU、Gthulhu Prometheus triggers、`PodSchedulingMetrics` 或 Gthulhu child chart。

## 2. 手動 Integration Coverage

PostgreSQL 與 Redis integration tests 是手動檢查。它們透過 GitHub Actions 的 `manual-integration-coverage` workflow 執行，或連到開發者自行提供的服務。Docker Compose 不是這些 tests 的標準本機 integration fixture runner。

連到開發者自行提供的服務執行：

```bash
WOMS_INTEGRATION_TESTS=1 DATABASE_URL=postgres://... npm run test:integration
WOMS_INTEGRATION_TESTS=1 REDIS_ADDR=127.0.0.1:6379 npm run test:integration
WOMS_INTEGRATION_TESTS=1 DATABASE_URL=postgres://... REDIS_ADDR=127.0.0.1:6379 npm run test:integration
```

期望：

- 缺少 `WOMS_INTEGRATION_TESTS=1` 時，命令會用清楚訊息略過。
- 缺少 `DATABASE_URL` 時，只略過 PostgreSQL packages。
- 缺少 `REDIS_ADDR` 時，只略過 Redis packages。
- Manual CI workflow 會啟動自己的 PostgreSQL 與 Redis services，並上傳 Go coverage profiles。

## 3. 手動瀏覽器 UI 驗證

在可使用瀏覽器的環境啟動 WOMS，逐項確認：

1. Scheduler pending badge：
   - 以 `scheduler-a` / `demo` 登入。
   - 使用一般桌機瀏覽器寬度。
   - 確認待排程訂單卡片的 `待排程` badge 單行顯示，`程` 不可換行。
   - 改以 `sales` / `demo` 登入，確認待排程卡片 badge 形狀與間距一致。

2. Sales 待排程訂單修改：
   - 以 `sales` / `demo` 登入。
   - 建立或找到同一 sales 使用者建立的 `待排程` 訂單。
   - 確認舊的純下三角按鈕已改成文字按鈕 `訂單修改`。
   - 點一次：既有交期/數量修改表單展開。
   - 再點一次：表單收合。
   - 再展開並修改交期或數量後送出，確認訂單仍維持既有待排程流程。

3. Sales draft preview calendar 切換：
   - 以 `sales` / `demo` 登入。
   - 建立未來交期的草稿訂單並開啟 schedule preview。
   - 點 `待排程`：preview calendar 顯示本次 sales draft preview allocations。
   - 點 `已排程`：preview calendar 切換為正式 persisted calendar allocations。
   - 再切回 `待排程`，確認 draft preview allocations 回來。
   - 確認最後「放到待排程訂單」流程仍可建立待排程訂單。

4. Sales 主日曆切換：
   - 以 `sales` / `demo` 登入。
   - 點 `待排程`：主日曆只顯示 pending backlog preview allocations。
   - 點 `已排程`：主日曆只顯示正式 persisted schedule allocations。
   - 點 `所有訂單`：主日曆同時顯示兩者，pending backlog allocations 保留 preview 樣式與待排程狀態。

## 4. GKE Ingress 或 LoadBalancer Web HPA 驗證

部署到 GKE 或等價 Ingress/LoadBalancer-capable cluster：

```bash
helm upgrade --install woms ./deploy/helm/woms \
  --namespace woms --create-namespace \
  --set ingress.enabled=true \
  --set ingress.host=woms.c1ydeh.net \
  --set ingress.tls.enabled=true \
  --set web.service.type=ClusterIP \
  --set-json 'web.service.annotations={"cloud.google.com/neg":"{\"ingress\":true}"}'
```

確認 active resources：

```bash
kubectl get scaledobject,hpa,deploy,pod,svc -n woms
kubectl describe hpa woms-woms-web-hpa -n woms
kubectl get scaledobject woms-woms-web -n woms -o yaml
```

期望：

- 使用 NGINX Ingress 時，`woms-woms-web` Service 是 `ClusterIP`；非 Ingress 環境才明確設為 `LoadBalancer`。
- `ingress.enabled=true` 時，`woms-woms-public` Ingress 會把 `/` 與 `/grafana/` 流量 route 到 `woms-woms-web`，並讓 exact `/api/auth/login` 公開進入 `woms-woms-api`。
- `woms-woms-api-secure` Ingress 會把受保護的 `/api` 流量透過 NGINX Ingress `auth-url` 驗證後，直接 route 到 `woms-woms-api`。
- `woms-woms-web` ScaledObject 指向 `Deployment/woms-woms-web`。
- HPA 名稱是 `woms-woms-web-hpa`。
- Trigger metric 是 `woms_web_nginx_requests_per_second_per_pod`。

送入多使用者流量：

```bash
INGRESS_HOST="$(kubectl get ingress woms-woms-public -n woms -o jsonpath='{.spec.rules[0].host}')"
hey -z 5m -c 80 "https://${INGRESS_HOST}/"
```

觀察：

```bash
kubectl get hpa,deploy,pod -n woms -l app.kubernetes.io/component=web -w
```

Grafana：

- 開啟 `https://<INGRESS_HOST>/grafana/`；非 Ingress 驗證則開啟 `LOAD_URL` 對應 host 的 `/grafana/`。
- 開啟 dashboard `WOMS web autoscaling`。
- 確認 `Per-pod NGINX req/s` 在壓測期間上升。
- 確認 `NGINX req/s by web pod` 在 scale-out 後顯示流量分散到多個 pods。
- 直接進 API pods 的 `/api` 流量不預期會拉高 web NGINX request-rate metric。

期望：

- KEDA/HPA 將 web replicas 擴到高於 `minReplicaCount`。
- 新 web pods 進入 Ready。
- 流量分散到多個 web pods。
- 停止流量並等待 cooldown 後 replicas scale down。

## 5. API、RBAC 與 Calendar API 檢查

```bash
JWT_SECRET=local-dev-secret go run ./cmd/api
curl -i http://localhost:8080/internal/auth/verify
```

期望：未帶 token 回 `401`。

檢查角色邊界：

- Sales 呼叫 scheduler-only schedule job APIs 會回 `403`。
- Scheduler A 不能讀取或修改 Scheduler B 產線資料。
- `GET /api/schedules/calendar?lineId=A&month=2026-05` 會回授權產線的 persisted allocations。

## 6. Docker 與 Web Proxy 檢查

```bash
docker build -f Dockerfile.api -t woms-api:local .
docker build -f Dockerfile.worker -t woms-scheduler-worker:local .
docker build -f Dockerfile.web -t woms-web:local .
docker compose up --build
```

期望：

- API health：`curl http://localhost:8080/healthz`
- Web：`http://localhost:8081`
- 透過 web proxy 開啟 Grafana：`http://localhost:8081/grafana`
- 未登入 Grafana 的使用者會看到 Grafana login page。

## 7. 完成檢查清單

- 本機非 UI 測試通過。
- Release validation 需要時，已在 CI 或連到開發者自行提供的服務完成 manual integration coverage。
- 已在瀏覽器環境完成上述 UI 檢查。
- 已在 cluster 環境完成上述 GKE LoadBalancer/HPA 檢查。
- README 與兩份 verification docs 都已同步更新英文與 zh-TW。
- 未提交 generated files、secrets、本機 volumes 或 build output。
