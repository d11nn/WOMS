# WOMS Repo Analysis And Presentation Plan

Language: English | [繁體中文](woms-repo-analysis-presentation.zh-TW.md)

## Scope

This document is the WOMS repository analysis and final-presentation content plan. It does not produce a PPTX. The narrative follows `plan.md`: first explain what users want to accomplish, then show operation flows, then explain why those flows require the application and infrastructure architecture in slides 4 and 5.

All presentation diagrams use Excalidraw source files. The Sales flow source is [sales.excalidraw](sales.excalidraw). The Scheduler flow diagram is intentionally left for the presenter to add later; this document keeps only the textual flow. Other diagram sources are [application-architecture.excalidraw](application-architecture.excalidraw), [infrastructure-architecture.excalidraw](infrastructure-architecture.excalidraw), [monitoring-autoscaling.excalidraw](monitoring-autoscaling.excalidraw), and [deployment-flow.excalidraw](deployment-flow.excalidraw). Repository evidence comes from `cmd`, `internal`, `web`, `db/migrations`, `.github/workflows`, `deploy/helm/woms`, `monitoring`, Dockerfiles, Compose, README, `go.mod`, and `package.json`.

## Requirement And Grading Alignment

`docs/requirement.pdf` defines WOMS as a Wafer Order Management and Scheduling System. The required behavior includes order creation, modification, cancellation, query, filtering, status tracking, automatic scheduling for pending orders, rescheduling after affected changes, calendar/list schedule visibility, and conflict or delay visualization. Advanced expectations include flexibility, scalability, high availability, and data consistency.

The repository maps to the evaluation criteria as follows:

- Requirement conversion and implementation: `web/app.js` and `internal/api/server.go` implement Sales, Scheduler, and Admin workflows; `db/migrations/001_init.sql` defines orders, schedule previews, schedule jobs, allocations, and audit logs.
- Code quality: Go code is split across `internal/api`, `internal/scheduler`, `internal/auth`, `internal/lock`, `internal/metrics`, and `internal/startup`; CI runs `gofmt`, `go test ./...`, web mock tests, Docker builds, and Helm rendering.
- Architecture design and scalability: `deploy/helm/woms` deploys API, web, worker, PostgreSQL, Redis, Kafka, Prometheus/Grafana, KEDA, and Ingress-related resources; image tags are controlled by `values.yaml`.
- System testing and validation: `internal/*_test.go`, `cmd/scheduler-worker/main_test.go`, `web/ui.test.mjs`, `deploy/helm/woms/chart-static.test.mjs`, and `scripts/verify-*.sh` cover auth, RBAC, scheduling, Redis locking, metrics, web helpers, Helm rendering, and HPA behavior.

## Application Roles

- Sales: create wafer orders, check due-date feasibility, preview capacity impact, confirm pending orders, track the calendar, and proactively resubmit or cancel pending orders before formal scheduling.
- Scheduler: view assigned-line pending orders, generate schedule previews, resolve conflicts, accept previews into schedule jobs, inspect history, start production, and confirm production output.
- Admin: manage users and roles, inspect the autoscaling demo panel, and access operational views across lines.

## Sales Operation Flow

The authoritative Sales flow is [sales.excalidraw](sales.excalidraw). The point of this slide is not to list APIs; it is to show how Sales prevents unacceptable due dates and visible conflicts before a draft becomes a pending order. Use the Excalidraw source directly when taking screenshots.

Repository evidence:

- `web/app.js` submits the order form by calling `createPreview(..., "sales-draft")`; the preview dialog then calls `POST /api/orders/preview-confirm`.
- `internal/api/server.go` exposes `GET /api/lines`, `GET/POST/DELETE /api/orders`, `POST /api/orders/preview-confirm`, `POST /api/orders/resubmit`, and `POST /api/schedules/preview`.
- `internal/api/server_test.go` covers Sales due-date validation, draft preview, pending conflict reports, draft confirmation, own pending-order resubmit/cancel, and note immutability during resubmission.

## Scheduler Operation Flow

The Scheduler flow is inferred from the current UI and API. Formal scheduling is preview-backed; this keeps conflict resolution, manual force reason, line revision, and audit data fixed before a job enters Kafka. The Scheduler flow diagram is intentionally not generated, per the user's request.

Repository evidence:

- `internal/api/server.go` rejects direct schedule-job creation without `previewId`; `internal/api/server_test.go` includes tests for preview requirement and stale preview revision.
- `internal/scheduler/scheduler.go` sorts pending orders by priority, due date, and order ID, keeping planning deterministic.
- `cmd/scheduler-worker/main.go` consumes Kafka as `woms-scheduler-workers`, obtains a Redis line lock, and then persists preview allocations.

## System Architecture - Application

Excalidraw source: [application-architecture.excalidraw](application-architecture.excalidraw)

The application architecture follows directly from the workflows. The web frontend owns login, calendars, preview pages, and operation panels. The Go API owns JWT/RBAC, order state, previews, schedule jobs, production state, and audit records. The worker persists accepted schedule jobs asynchronously. PostgreSQL stores durable state, Redis protects same-line schedule consistency, Kafka decouples API requests from execution, and Prometheus/Grafana expose operational feedback.

## System Architecture - Infrastructure

Excalidraw source: [infrastructure-architecture.excalidraw](infrastructure-architecture.excalidraw)

Repository evidence:

- `deploy/helm/woms/Chart.yaml` declares PostgreSQL, Redis, and Kafka chart dependencies.
- `deploy/helm/woms/templates/*deployment.yaml` defines API, web, and scheduler-worker deployments.
- `deploy/helm/woms/templates/keda-scaledobject.yaml` scales the web deployment with a Prometheus trigger.
- `.github/workflows/ci.yml` runs Go/web tests, Docker builds, Helm rendering, and HPA render verification.
- `.github/workflows/docker-publish.yml` publishes Docker Hub images only from `main`, `release/**`, or manual dispatch, then updates Helm image tags.

## Data, Queue, Cache, And Consistency

PostgreSQL is the source of truth. `db/migrations/001_init.sql` creates `users`, `production_lines`, `orders`, `schedule_jobs`, `schedule_previews`, `schedule_allocations`, and `audit_logs`. Order statuses are `待排程`, `已排程`, `生產中`, `已完成`, `需業務處理`, and `已取消`; quantity is constrained to `25` through `2500`, and line capacity defaults to `10000` wafers per day.

Kafka topic `woms.schedule.jobs` carries accepted scheduling work. The worker consumes it through group `woms-scheduler-workers`, takes a Redis line lock, and then persists the preview allocation into PostgreSQL. Lock timeout and transient persistence failures become retry/backfill states instead of silent job loss.

## Scaling And Observability

The active autoscaling scenario in the current repository is web NGINX request rate, not worker backlog. `deploy/helm/woms/values.yaml` defines `woms_web_nginx_requests_per_second_per_pod` from `nginx_http_requests_total{job="woms-web-nginx"}`. `deploy/helm/woms/templates/keda-scaledobject.yaml` uses that Prometheus metric to scale `woms-woms-web`. Grafana and the admin HPA panel show the same signal.

The Monitoring/Autoscaling slide should therefore explain the web traffic path: web pods expose NGINX metrics through an exporter sidecar, Prometheus scrapes the web Service `metrics` port, Grafana displays per-pod request rate, and KEDA uses the same query to drive the HPA.

Excalidraw source: [monitoring-autoscaling.excalidraw](monitoring-autoscaling.excalidraw)

## Deployment Flow

1. Developers open PRs from `feat/**` branches to protected `main`.
2. GitHub Actions CI runs `gofmt`, `go test ./...`, `npm run test:web`, Docker builds, Helm rendering, and HPA render verification.
3. After merge to `main`, `docker-publish` builds and pushes `woms-api`, `woms-scheduler-worker`, and `woms-web` images to Docker Hub.
4. The workflow updates image tags in `deploy/helm/woms/values.yaml` and creates a Git tag.
5. Operators deploy API, web, worker, dependencies, monitoring, and KEDA to Kubernetes with Helm.

Excalidraw source: [deployment-flow.excalidraw](deployment-flow.excalidraw)

## Testing Strategy

- Scheduler correctness: `internal/scheduler/scheduler_test.go` covers split allocation, future start dates, today/past adjustment, high priority, manual force, affected allocations, and earliest late-completion solutions.
- API/RBAC: `internal/api/server_test.go` covers JWT, Ingress auth verification, Sales/Scheduler/Admin permissions, line scoping, preview-backed jobs, calendar, history, production start/confirm, resubmit, and cancel.
- Redis locking: `internal/lock/redis_test.go` and `cmd/scheduler-worker/main_test.go` cover RESP commands, lock retry, contention, and lock config validation.
- Metrics: `internal/metrics/metrics_test.go` verifies the Prometheus text endpoint and custom counters.
- Web behavior: `web/ui.test.mjs` tests UI helpers; `package.json` also runs the Helm chart static tests through `npm run test:web`.
- Deployment validation: `scripts/verify-k8s.sh`, `scripts/verify-hpa-render.sh`, `scripts/verify-hpa-behavior.sh`, and the README verification guide cover deployment and HPA validation.

## Demo Scenario

1. Sales logs in, loads production lines, creates an order, passes future due-date validation, and opens a preview.
2. Sales reviews allocation or conflict output, adjusts if needed, and confirms the draft into a `待排程` order.
3. Scheduler logs in for the assigned line, previews pending orders, accepts the preview, and creates a Kafka-backed schedule job.
4. Worker consumes the job, obtains the Redis line lock, writes PostgreSQL allocations, and Scheduler sees results in calendar/history.
5. Scheduler starts production and reports partial or complete output.
6. Admin opens Grafana/HPA demo and observes Prometheus metrics, Grafana dashboard panels, and KEDA web HPA behavior under web traffic.

## Slide Content Plan

### Slide 1: WOMS

Bullets:

- Cloud-native Wafer Order Management and Scheduling System
- Built around Sales order intake, Scheduler planning, and production feedback
- Deployable with Go, PostgreSQL, Redis, Kafka, Kubernetes, Helm, KEDA, Prometheus, and Grafana

Speaker note:

This slide positions WOMS as more than a form-based CRUD system. The real system goal is to connect wafer order intake, scheduler-reviewed planning, and production feedback in one cloud-native workflow. The rest of the presentation explains why that workflow requires the application and infrastructure boundaries shown later.

### Slide 2: User Story - Sales

Bullets:

- Create wafer orders only after due-date and capacity preview checks
- Preview conflicts before the order enters the scheduler queue
- Track, resubmit, or cancel pending orders from the calendar workflow

Speaker note:

Start with Sales instead of technology. Sales wants to create an order, but the useful behavior is earlier than persistence: the system checks that the due date is acceptable, previews whether capacity can satisfy the draft, and shows conflict reason plus earliest finish date when the draft would cause a problem. Only after that does the order become pending scheduling.

### Slide 3: User Story - Scheduler

Bullets:

- Convert pending orders into preview-backed schedule jobs
- Resolve conflicts before accepting production allocations
- Start production and report completion back to the calendar

Speaker note:

Scheduler receives pending orders and turns them into accepted production allocations. The important design choice is that formal schedule jobs must come from previews. That gives the system a review point for conflicts, manual force reasons, stale line revisions, and audit history before asynchronous execution begins.

### Slide 4: System Architecture - Application

Bullets:

- Web frontend owns login, order forms, calendars, preview pages, and admin panels
- Go API enforces JWT/RBAC and owns order, preview, schedule, production, and audit APIs
- Scheduler Worker persists Kafka-backed schedule jobs with Redis line locks

Speaker note:

This slide connects the user flows to the application architecture. The frontend owns workflow state and user interaction; the API owns authorization and durable business state; the worker owns asynchronous persistence of accepted schedules. PostgreSQL, Redis, Kafka, and monitoring are not decorative components; each one supports a specific workflow constraint.

### Slide 5: System Architecture - Infrastructure

Bullets:

- Kubernetes deploys API, web, worker, PostgreSQL, Redis, Kafka, Prometheus, Grafana, and KEDA
- NGINX Ingress or LoadBalancer exposes the web request path
- GitHub Actions builds images, Docker Hub stores releases, and Helm controls rollout tags

Speaker note:

Infrastructure exists to make the application deployable and observable. Kubernetes runs the service units and dependencies, NGINX Ingress or a LoadBalancer exposes the web path, and Helm controls the deployed image tags. CI/CD builds and publishes the three service images so the deployment is tied to versioned artifacts.

### Slide 6: Frontend Implementation

Bullets:

- Vanilla HTML/CSS/JS with session state stored in browser localStorage
- Sales calendar modes: pending, scheduled, and all orders
- Scheduler workspace supports selection, drag preview, conflict handling, history, and production actions

Speaker note:

The frontend is implemented without a SPA framework, but it still carries meaningful workflow complexity. It gates pages behind login, maintains line and filter state, lets Sales inspect different calendar modes, and lets Scheduler move from order selection to preview, conflict handling, history, and production actions.

### Slide 7: Backend API And Security

Bullets:

- JWT login, logout, token-session revocation, and `/internal/auth/verify`
- RBAC separates Sales, Scheduler, and Admin permissions
- API revalidates authorization even when Ingress auth is enabled

Speaker note:

The Go API is the trust boundary. Ingress auth can verify entry requests, but the API still rechecks JWT and RBAC for business operations. Sales cannot create formal schedule jobs, Scheduler is scoped to an assigned production line, and Admin-only operations remain protected inside the API.

### Slide 8: Scheduling And Job Flow

Bullets:

- Deterministic planner sorts by priority, due date, and order ID
- Preview-backed jobs prevent unreviewed direct schedule writes
- Kafka decouples API requests from worker persistence
- Redis line locks protect same-line schedule consistency

Speaker note:

Scheduling is designed around deterministic preview and controlled persistence. The planner sorts by priority, due date, and order ID, so identical input yields identical output. Accepted previews become Kafka jobs, and workers take Redis line locks before writing allocations to PostgreSQL.

### Slide 9: Database, Queue, And Cache

Bullets:

- PostgreSQL stores users, lines, orders, previews, jobs, allocations, and audit logs
- Kafka topic `woms.schedule.jobs` carries asynchronous schedule work
- Redis stores line locks and optional token-session state

Speaker note:

The data architecture keeps responsibilities separate. PostgreSQL stores facts, Kafka carries work, and Redis protects short-lived consistency boundaries. This makes it clearer which component owns durable state and which component is only coordinating asynchronous behavior.

### Slide 10: Monitoring And Autoscaling

Bullets:

- API exposes Prometheus metrics at `/metrics`
- Web pods expose NGINX request metrics through an exporter sidecar
- KEDA scales web pods using per-pod NGINX requests per second
- Grafana displays the same signal used by the HPA trigger

Speaker note:

The current active HPA demo is web traffic autoscaling. Web pods expose NGINX request metrics, Prometheus scrapes them, Grafana displays the signal, and KEDA uses the same per-pod request-rate query to scale web replicas. This keeps the demo measurable from the request path users actually hit.

### Slide 11: Deployment Flow

Bullets:

- Feature branches open PRs to protected `main`
- CI runs Go tests, web tests, Docker builds, Helm render, and HPA render verification
- Main/release publish pushes Docker Hub images and updates Helm tags

Speaker note:

The deployment flow separates development validation from release publishing. Feature branches run tests and render checks, while Docker Hub publishing happens only from `main`, `release/**`, or manual dispatch. Helm image tags are updated by the release workflow, so Kubernetes rollouts use explicit versions.

### Slide 12: Testing Strategy

Bullets:

- Scheduler unit tests cover allocation, conflicts, late completion, and production remainder logic
- API tests cover JWT/RBAC, line scoping, preview-backed jobs, calendar, history, and production
- Web and Helm tests verify UI helper behavior and rendered deployment contracts

Speaker note:

The tests map back to both the workflows and the deployment architecture. Scheduler tests cover planning correctness; API tests cover authorization and state transitions; web tests cover UI helper behavior; Helm and verification scripts check that the Kubernetes resources render and behave as expected.

### Slide 13: Demo Scenario

Bullets:

- Sales creates and confirms a preview-backed pending order
- Scheduler previews, accepts, and persists the schedule
- Production status updates return to the calendar
- Admin observes Grafana and web HPA during traffic load

Speaker note:

The demo should follow one order through the system. Sales creates and confirms a pending order, Scheduler accepts a preview into a schedule job, the worker persists allocations, production feedback updates the calendar, and Admin observes the web HPA through Grafana under load.

### Slide 14: Conclusion

Bullets:

- WOMS turns wafer order intake into a controlled preview-and-schedule workflow
- The architecture separates UI workflow, API authorization, async scheduling, and persistent state
- Testing, Helm deployment, and observability connect the prototype to operational requirements

Speaker note:

The conclusion should tie back to the grading criteria. WOMS converts wafer-order requirements into a controlled workflow, separates responsibilities across application and infrastructure components, and backs the design with tests, deployment automation, and observability.

## Visual And Icon Guidance

Use official or common brand assets for Go, PostgreSQL, Redis, Apache Kafka, Docker, Kubernetes, Helm, NGINX, KEDA, Prometheus, Grafana, GitHub Actions, and Docker Hub. Slides 4 and 5 can follow the hand-drawn architecture style, but the nodes must be replaced with real WOMS components. Slides 6 through 11 should reuse a small architecture thumbnail and highlight the current component with a red frame.
