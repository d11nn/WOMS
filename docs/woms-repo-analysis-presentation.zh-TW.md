# WOMS Repo 分析與簡報內容建議

語言：繁體中文 | [English](woms-repo-analysis-presentation.en.md)

## 交付範圍

本文件是 WOMS 的 repo 分析與期末簡報內容建議。簡報主線採用 `../plan.md` 的方向：先說明使用者真正想完成什麼，再用操作流程圖描述使用方式，最後說明為了支撐這些 workflow，系統才需要 Web UI、Go API、Scheduler Worker、PostgreSQL、Redis、Kafka、Prometheus/Grafana、KEDA、Kubernetes、Helm、NGINX Ingress、GitHub Actions 與 Docker Hub。

所有簡報圖都使用 Excalidraw source。Sales flow 以 [sales.excalidraw](sales.excalidraw) 為準；Scheduler flow 以 [scheduler.excalidraw](scheduler.excalidraw) 為準；本文件只保留文字 flow。其他圖檔包含 [application-architecture.excalidraw](application-architecture.excalidraw)、[infrastructure-architecture.excalidraw](infrastructure-architecture.excalidraw)、[monitoring-autoscaling.excalidraw](monitoring-autoscaling.excalidraw) 與 [deployment-flow.excalidraw](deployment-flow.excalidraw)。Repo evidence 則來自 `cmd`、`internal`、`web`、`db/migrations`、`.github/workflows`、`deploy/helm/woms`、`monitoring`、Dockerfiles、Compose、README、`go.mod` 與 `package.json`。

## 需求與評分對齊

`docs/requirement.pdf` 把 WOMS 定義為 Wafer 訂單管理與排程系統，核心需求包含訂單新增、修改、取消、查詢、篩選、狀態追蹤、根據排程規則安排待排程訂單、重新觸發受影響排程、日曆或列表查詢，以及衝突/延後可視化。進階要求則強調彈性、可擴展性、高可用性與資料一致性。

評分面向可以直接對應到 repo：

- 需求轉換與實作：`web/app.js` 與 `internal/api/server.go` 實作 Sales、Scheduler、Admin workflow；`db/migrations/001_init.sql` 定義訂單、排程預覽、排程任務、allocation 與 audit log schema。
- 程式碼品質：Go module 分成 `internal/api`、`internal/scheduler`、`internal/auth`、`internal/lock`、`internal/metrics`、`internal/startup`；CI 執行 `gofmt`、`go test ./...`、web mock tests、Docker build 與 Helm render。
- 架構設計與可擴展性：`deploy/helm/woms` 提供 API、web、worker、PostgreSQL、Redis、Kafka、Prometheus/Grafana、KEDA 與 Ingress chart；image tag 由 `values.yaml` 控制。
- 系統測試與驗證：`internal/*_test.go`、`cmd/scheduler-worker/main_test.go`、`web/ui.test.mjs`、`deploy/helm/woms/chart-static.test.mjs` 與 `scripts/verify-*.sh` 覆蓋 auth、RBAC、scheduler、Redis lock、metrics、web UI helper、Helm render 與 HPA 行為。

## Application Roles

- Sales：建立 wafer order、檢查交期是否可接受、先做 preview、確認建立待排程訂單、追蹤月曆，並在 scheduler 正式排程前主動修改、重送或取消自己的訂單。
- Scheduler：查看被指派產線的待排程訂單、產生 schedule preview、處理 conflict、接受 preview 建立排程任務、查看排程紀錄，並對已排程訂單啟動生產或回報產量。
- Admin：管理使用者與角色、查看 HPA/autoscaling demo 狀態，並保留進入所有產線與營運面板的權限。

## Sales Operation Flow

Sales flow 的來源是 [sales.excalidraw](sales.excalidraw)。流程重點不是 API 清單，而是 Sales 如何在建立訂單前先排除不可接受交期與可預期衝突。截圖時請直接開啟該 Excalidraw source。

Repo evidence：

- `web/app.js` 在 order form submit 時先呼叫 `createPreview(..., "sales-draft")`，再由 preview dialog 呼叫 `POST /api/orders/preview-confirm`。
- `internal/api/server.go` 提供 `GET /api/lines`、`GET/POST/DELETE /api/orders`、`POST /api/orders/preview-confirm`、`POST /api/orders/resubmit` 與 `POST /api/schedules/preview`。
- `internal/api/server_test.go` 覆蓋 Sales due date validation、draft preview、pending conflict report、confirm draft preview、resubmit/cancel own pending order 與 note 不可被 resubmit 改寫。

## Scheduler Operation Flow

Scheduler flow 的來源是 [scheduler.excalidraw](scheduler.excalidraw)。Scheduler 的重點是正式排程只接受 preview-backed job；這讓 conflict resolution、manual force reason、line revision 與 audit log 在進入 Kafka 前就被固定下來。

Repo evidence：

- `internal/api/server.go` 拒絕沒有 `previewId` 的 direct schedule job creation；`internal/api/server_test.go` 有 `TestSchedulerCannotCreateScheduleJobWithoutPreview` 與 stale preview revision 測試。
- `internal/scheduler/scheduler.go` 對 pending order 依 priority、due date、ID 做 stable sorting，確保 deterministic planning。
- `cmd/scheduler-worker/main.go` 使用 Kafka consumer group `woms-scheduler-workers`，取得 Redis line lock 後才 persist preview allocations。

## System Architecture - Application

Excalidraw source：[application-architecture.excalidraw](application-architecture.excalidraw)

這個 application 架構是被前面的 Sales/Scheduler workflows 推出來的：Web frontend 負責登入、日曆、preview 與操作面板；Go API 負責 JWT/RBAC、訂單狀態、preview、schedule job 與 audit；worker 負責把正式 schedule job 非同步落地；PostgreSQL 保存長期狀態；Redis 保護同產線排程一致性；Kafka 把 API request path 和排程執行解耦；Prometheus/Grafana 則讓 demo 能看到 API 與 web traffic 指標。

## System Architecture - Infrastructure

Excalidraw source：[infrastructure-architecture.excalidraw](infrastructure-architecture.excalidraw)

Repo evidence：

- `deploy/helm/woms/Chart.yaml` 宣告 PostgreSQL、Redis、Kafka chart dependencies。
- `deploy/helm/woms/templates/*deployment.yaml` 定義 API、web、scheduler-worker deployments。
- `deploy/helm/woms/templates/keda-scaledobject.yaml` 使用 Prometheus trigger scale `web` deployment。
- `.github/workflows/ci.yml` 跑 Go/web tests、Docker build、Helm render 與 HPA render verification。
- `.github/workflows/docker-publish.yml` 僅在 `main`、`release/**` 或 manual dispatch publish Docker Hub images，並更新 Helm image tags。

## Data, Queue, Cache, And Consistency

PostgreSQL 是 source of truth，`db/migrations/001_init.sql` 建立 `users`、`production_lines`、`orders`、`schedule_jobs`、`schedule_previews`、`schedule_allocations` 與 `audit_logs`。訂單狀態包含 `待排程`、`已排程`、`生產中`、`已完成`、`需業務處理`、`已取消`；quantity 受 `25` 到 `2500` 約束，產線 capacity 預設每天 `10000`。

Kafka topic 是 `woms.schedule.jobs`，API 建立 queued job 後 publish 給 worker。Worker 以 `woms-scheduler-workers` consumer group 消費訊息，並用 Redis line lock 保證同一產線的排程落地不會併發覆寫。若 lock timeout 或暫時錯誤發生，worker 會標記 retry/backfill，而不是直接丟失 job。

## Scaling And Observability

目前 repo 的 active autoscaling scenario 是 web NGINX request rate，不是 worker backlog scaling。`deploy/helm/woms/values.yaml` 定義 `woms_web_nginx_requests_per_second_per_pod`，查詢 `nginx_http_requests_total{job="woms-web-nginx"}` 的 per-pod rate；`deploy/helm/woms/templates/keda-scaledobject.yaml` 用這個 Prometheus metric scale `woms-woms-web`。Grafana dashboard 與 admin HPA panel 也以同一個 signal 呈現。

這代表簡報的 Monitoring/Autoscaling 頁應該說明：web pods 透過 NGINX exporter 暴露 metrics，Prometheus scrape web Service 的 `metrics` port，Grafana 顯示 per-pod req/s，而 KEDA 依相同查詢推動 HPA。不要把目前實作描述成 Kafka lag-based worker autoscaling。

Excalidraw source：[monitoring-autoscaling.excalidraw](monitoring-autoscaling.excalidraw)

## Deployment Flow

1. Developer 在 `feat/**` 分支開 PR。
2. GitHub Actions CI 執行 `gofmt`、`go test ./...`、`npm run test:web`、Docker build、Helm render 與 HPA render script。
3. PR merge 到 `main` 後，`docker-publish` workflow build/push `woms-api`、`woms-scheduler-worker`、`woms-web` 到 Docker Hub。
4. Workflow 更新 `deploy/helm/woms/values.yaml` 的 image tags，並建立 Git tag。
5. Operator 透過 Helm 部署 API/web/worker/dependencies/monitoring/KEDA 到 Kubernetes。

Excalidraw source：[deployment-flow.excalidraw](deployment-flow.excalidraw)

## Testing Strategy

- Scheduler correctness：`internal/scheduler/scheduler_test.go` 覆蓋 split allocation、future start date、today/past start adjustment、high priority、manual force、affected allocation 與 earliest late completion solution。
- API/RBAC：`internal/api/server_test.go` 覆蓋 JWT、Ingress auth verify、Sales/Scheduler/Admin 權限、line scoping、preview-backed job、calendar、history、production start/confirm、resubmit/cancel。
- Redis lock：`internal/lock/redis_test.go` 與 `cmd/scheduler-worker/main_test.go` 覆蓋 RESP command、lock retry、contention 與 lock config validation。
- Metrics：`internal/metrics/metrics_test.go` 驗證 Prometheus text endpoint 與 custom counters。
- Web behavior：`web/ui.test.mjs` 測 UI helper；`package.json` 的 `npm run test:web` 也包含 Helm chart static test。
- Deployment validation：`scripts/verify-k8s.sh`、`scripts/verify-hpa-render.sh`、`scripts/verify-hpa-behavior.sh` 與 README verification guide 提供部署與 HPA 驗證路徑。

## Demo Scenario

建議 demo 主線：

1. Sales 登入，載入產線，新增 order，因交期是未來日而進入 preview。
2. Sales preview 看到 allocation 或 conflict，調整後用 `preview-confirm` 建立 `待排程` 訂單。
3. Scheduler 登入指定產線，選取待排程訂單，產生 schedule preview，確認後建立 Kafka-backed schedule job。
4. Worker 消費 job，取得 Redis line lock，寫入 PostgreSQL allocation，Scheduler 在月曆與 history 看到結果。
5. Scheduler 點選已排程訂單開始生產，並回報 partial 或 complete production。
6. Admin 開啟 Grafana/HPA demo，透過 web traffic 觀察 Prometheus metric、Grafana dashboard 與 KEDA web HPA。

## Slide 建議內容

### Slide 1: WOMS

Bullets:

- Cloud-native Wafer Order Management and Scheduling System
- Built around Sales order intake, Scheduler planning, and production feedback
- Deployable with Go, PostgreSQL, Redis, Kafka, Kubernetes, Helm, KEDA, Prometheus, and Grafana

講稿：

這一頁先定位 WOMS。它不是單純的表單系統，而是一個 cloud-native Wafer Order Management and Scheduling System。使用者真正需要的是三件事：Sales 可以把 wafer order 正確送進系統，Scheduler 可以把待排程訂單轉成可確認的產線排程，最後 production feedback 可以回到同一套狀態模型。後面的架構都是為了支撐這三條 workflow。

### Slide 2: User Story - Sales

Bullets:

- Create wafer orders only after due-date and capacity preview checks
- Preview conflicts before the order enters the scheduler queue
- Track, resubmit, or cancel pending orders from the calendar workflow

講稿：

這一頁不要先講 API，而是先講 Sales 想完成什麼。Sales 的問題是，他不只要新增訂單，還要在送出前知道交期是否合理，以及這張訂單會不會造成可預期的 capacity conflict。所以流程從 `GET /api/lines` 載入產線開始，填完資料後先檢查交期是否為未來日，再用 `POST /api/schedules/preview` 做試排。若 preview 有 conflict，就顯示原因與最早完成日，讓 Sales 可以先調整交期、數量、開始日或拆單；確認後才用 `POST /api/orders/preview-confirm` 建立 `待排程` 訂單。

### Slide 3: User Story - Scheduler

Bullets:

- Convert pending orders into preview-backed schedule jobs
- Resolve conflicts before accepting production allocations
- Start production and report completion back to the calendar

講稿：

延續 Sales flow，Scheduler 接到的是已經進入 `待排程` 狀態的訂單。Scheduler 的重點不是直接寫入排程，而是先產生 preview，確認沒有 conflict 或已經處理 conflict 之後，才建立 `POST /api/schedules/jobs`。這個設計讓排程結果、manual force reason、line revision 與 audit log 都能在 job 進入 Kafka 前被固定下來。接著 worker 會負責正式落地，Scheduler 再從 calendar 和 history 追蹤結果，最後處理開始生產與產量回報。

### Slide 4: System Architecture - Application

Bullets:

- Web frontend owns login, order forms, calendars, preview pages, and admin panels
- Go API enforces JWT/RBAC and owns order, preview, schedule, production, and audit APIs
- Scheduler Worker persists Kafka-backed schedule jobs with Redis line locks

講稿：

前面兩頁講完 workflow 之後，這一頁才接 application architecture。因為 Sales 需要即時 preview，Scheduler 需要正式 job，Admin 需要 monitoring panel，所以前端不能只是靜態頁面；它需要完整的 workflow state。Go API 則是所有信任邊界的核心，負責 JWT/RBAC、order state、preview、schedule job 和 audit。Scheduler Worker 被拆出去，是因為正式排程可以是非同步工作，而且同一條產線要用 Redis lock 保證資料一致性。

### Slide 5: System Architecture - Infrastructure

Bullets:

- Kubernetes deploys API, web, worker, PostgreSQL, Redis, Kafka, Prometheus, Grafana, and KEDA
- NGINX Ingress or LoadBalancer exposes the web request path
- GitHub Actions builds images, Docker Hub stores releases, and Helm controls rollout tags

講稿：

這一頁從 infrastructure 的角度看同一套系統。WOMS 的部署單位不是一個 binary，而是 API、web、worker 加上 PostgreSQL、Redis、Kafka、Prometheus、Grafana 和 KEDA。使用者流量先進到 NGINX Ingress 或 LoadBalancer，再到 web service；web 會 proxy API 和 Grafana。CI/CD 則由 GitHub Actions build 三個 image，推到 Docker Hub，最後讓 Helm values 控制 Kubernetes rollout 的 image tag。

### Slide 6: Frontend Implementation

Bullets:

- Vanilla HTML/CSS/JS with session state stored in browser localStorage
- Sales calendar modes: pending, scheduled, and all orders
- Scheduler workspace supports selection, drag preview, conflict handling, history, and production actions

講稿：

這一頁可以紅框 highlight Web Frontend。前端使用 vanilla HTML/CSS/JS，沒有額外 SPA framework，但它其實承載很多 workflow state：登入後才顯示 workspace，Sales 可以在 pending、scheduled、all 三種 calendar mode 間切換；Scheduler 則有選取訂單、拖曳到未來日期、preview conflict、查看 history、開始生產與回報產量。這代表前端不只是展示資料，而是把使用者操作轉成後端可以驗證的 API request。

### Slide 7: Backend API And Security

Bullets:

- JWT login, logout, token-session revocation, and `/internal/auth/verify`
- RBAC separates Sales, Scheduler, and Admin permissions
- API revalidates authorization even when Ingress auth is enabled

講稿：

這一頁要強調 API 是 trust boundary。Ingress auth 可以做入口驗證，但 Go API 仍然會重新驗證 JWT 和 RBAC。Sales 不能直接建立 schedule job，Scheduler 只能看自己產線的資料，Admin 才能管理帳號和 HPA demo。這種設計的重點是，即使 request 經過 proxy 或 ingress，真正的 business authorization 仍然在 API 裡面，避免把安全性只放在邊界設備。

### Slide 8: Scheduling And Job Flow

Bullets:

- Deterministic planner sorts by priority, due date, and order ID
- Preview-backed jobs prevent unreviewed direct schedule writes
- Kafka decouples API requests from worker persistence
- Redis line locks protect same-line schedule consistency

講稿：

這一頁是 scheduling 核心。Planner 會用 priority、due date、order ID 做 stable sorting，所以同樣輸入會得到同樣結果。正式排程必須從 preview 轉成 job，這避免使用者繞過 conflict handling。當 job 進入 Kafka 後，worker 才會消費並寫入 PostgreSQL；而在寫入前，它會先取得 Redis line lock。這個 lock 解決的是同一條 production line 同時被多個 worker 更新時的 consistency 問題。

### Slide 9: Database, Queue, And Cache

Bullets:

- PostgreSQL stores users, lines, orders, previews, jobs, allocations, and audit logs
- Kafka topic `woms.schedule.jobs` carries asynchronous schedule work
- Redis stores line locks and optional token-session state

講稿：

這一頁把資料邊界講清楚。PostgreSQL 是 source of truth，所有長期狀態都在這裡：orders、schedule previews、jobs、allocations 和 audit logs。Kafka 只負責傳遞排程任務，讓 API 不必在 request path 裡等待 worker 完成。Redis 的角色則比較小但很關鍵，它負責同產線排程 lock，必要時也能支援 token session revocation。這樣三者的責任是分開的：DB 管事實，Kafka 管工作流，Redis 管短期一致性控制。

### Slide 10-12: Monitoring And Autoscaling

可以直接考慮沿用 @Monitor.pptx 的內容。

### Slide 13: Deployment Flow

Bullets:

- Feature branches open PRs to protected `main`
- CI runs Go tests, web tests, Docker builds, Helm render, and HPA render verification
- Main/release publish pushes Docker Hub images and updates Helm tags

講稿：

這一頁講 deployment flow。開發是在 `feat/**` 分支，PR 進 `main` 前 CI 會跑 Go tests、web tests、Docker build、Helm render 和 HPA render verification。真正 publish Docker Hub 不會在 feature branch 發生，而是在 `main`、`release/**` 或 manual workflow。publish 完會把 Helm values 的 image tag 更新成 release tag，這樣 Kubernetes rollout 用的是明確版本，而不是模糊的本機 image。

### Slide 14: Testing Strategy

Bullets:

- Scheduler unit tests cover allocation, conflicts, late completion, and production remainder logic
- API tests cover JWT/RBAC, line scoping, preview-backed jobs, calendar, history, and production
- Web and Helm tests verify UI helper behavior and rendered deployment contracts

講稿：

這一頁把測試對回 Slide 4/5 的元件。Scheduler tests 確認 allocation、conflict、manual force 和 late completion；API tests 確認 JWT/RBAC、line scoping、preview-backed job、calendar、history 和 production flow；web tests 則處理 UI helper 的行為；Helm static tests 和 render scripts 確認 deployment contract。換句話說，測試不是只測函式，而是沿著使用者 workflow 和部署架構去覆蓋風險。

### Slide 15: Demo 

Bullets:

- 播影片而已

講稿：

Demo 建議走一條完整主線。先用 Sales 建立訂單，通過 due date validation 和 preview 後建立 pending order。接著切到 Scheduler，選取 pending order 產生 preview，接受後讓 Kafka worker 寫入 allocation。然後在 calendar 上看到排程結果，並演示開始生產或回報產量。最後切到 Admin 或 Grafana，用 web traffic demo 觀察 KEDA web HPA。這樣 demo 可以同時覆蓋需求、排程、資料一致性、部署和 observability。

### Slide 15: Conclusion

Bullets:

- WOMS turns wafer order intake into a controlled preview-and-schedule workflow
- The architecture separates UI workflow, API authorization, async scheduling, and persistent state
- Testing, Helm deployment, and observability connect the prototype to operational requirements

講稿：

最後回扣評分標準。WOMS 的核心貢獻不是只做出訂單 CRUD，而是把 wafer order intake 轉成可驗證的 preview-and-schedule workflow。架構上，UI、API authorization、async scheduling 和 persistent state 被拆成清楚的責任邊界；部署上，Helm、KEDA、Prometheus/Grafana 和 GitHub Actions 讓它比較接近真正可營運的 cloud-native system。這也對應到需求轉換、程式碼品質、架構設計、測試驗證和運維可靠性。

## Icon 與視覺建議

Slide 中的技術 icon 建議使用官方或常見 brand asset：Go、PostgreSQL、Redis、Apache Kafka、Docker、Kubernetes、Helm、NGINX、KEDA、Prometheus、Grafana、GitHub Actions、Docker Hub。我已經把所有照片都放在 docs/image/，你可以自己做使用 ，Slide 4/5 請貼上我們製作的 excalidraw 截圖，但要把節點替換成 WOMS 真實元件對應到的官方圖；每次切換至 Slide 6-13前，會新增一頁，類似回到 Slide 4或5 告訴評審我們現在要講哪一部分，並僅 Highlight 即將介紹的 Component。
