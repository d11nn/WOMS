# WOMS Verification Guide

This workstation was used only for static, unit, Go, and Helm render checks. Local UI visual verification was not performed on this machine. Browser and GKE checks below must be run in an environment with a browser and a reachable NGINX Ingress or LoadBalancer entry point.

## 1. Local Non-UI Verification

```bash
npm run test:web
go test ./...
npm run test:coverage
helm template woms ./deploy/helm/woms --namespace woms
./scripts/verify-hpa-render.sh
test -z "$(gofmt -l .)"
```

Expected:

- Frontend and Helm static tests pass.
- Go tests pass.
- Fast coverage passes the current short-term gate. `npm run test:go:coverage` writes `coverage.out`; `npm run test:web:coverage` prints Node coverage and enforces the web line threshold.
- Helm renders `Deployment/api`, `Deployment/worker`, `Deployment/web`, Prometheus/Grafana, the web `ScaledObject`, and PDBs.
- `ScaledObject` targets `Deployment/woms-woms-web` and creates HPA `woms-woms-web-hpa`.
- Active KEDA triggers contain only the Prometheus trigger for `woms_web_nginx_requests_per_second_per_pod`.
- Rendered manifests do not include worker Kafka lag, worker CPU, Gthulhu Prometheus triggers, `PodSchedulingMetrics`, or the Gthulhu child chart.

## 2. Manual Integration Coverage

PostgreSQL and Redis integration tests are manual checks. They run either through the GitHub Actions `manual-integration-coverage` workflow or against developer-provided services. Docker Compose is not the standard local integration fixture runner for these tests.

Run against developer-provided services:

```bash
WOMS_INTEGRATION_TESTS=1 DATABASE_URL=postgres://... npm run test:integration
WOMS_INTEGRATION_TESTS=1 REDIS_ADDR=127.0.0.1:6379 npm run test:integration
WOMS_INTEGRATION_TESTS=1 DATABASE_URL=postgres://... REDIS_ADDR=127.0.0.1:6379 npm run test:integration
```

Expected:

- Missing `WOMS_INTEGRATION_TESTS=1` skips the command with a clear message.
- Missing `DATABASE_URL` skips only PostgreSQL packages.
- Missing `REDIS_ADDR` skips only Redis packages.
- The manual CI workflow starts its own PostgreSQL and Redis services and uploads Go coverage profiles.

## 3. Manual Browser UI Verification

Run the app in a browser-capable environment, then verify:

1. Scheduler pending badge:
   - Log in as `scheduler-a` / `demo`.
   - Use a normal desktop browser width.
   - Confirm pending order cards show the `待排程` status badge on one line; `程` must not wrap.
   - Select high-priority pending orders to run preview, and verify on the preview calendar:
     - The currently selected preview orders are highlighted with a thick orange border (preview-draft).
     - Orders moved to other dates due to schedule conflicts show a dashed gray box "已移出" (Moved Out) on their original date.
     - Conflicted orders with deferred completion dates show their updated (rescheduled) due dates dynamically.
   - Log in as `sales` / `demo` and confirm pending cards use the same badge shape and spacing.

2. Sales pending order editing:
   - Log in as `sales` / `demo`.
   - Create or locate a `待排程` order created by the same sales user.
   - Confirm the old triangle-only button is now the text button `訂單修改`.
   - Click it once: the existing due-date/quantity edit form expands.
   - Click it again: the form collapses.
   - Re-expand, change due date or quantity, submit, and confirm the order remains in the normal pending order workflow.

3. Sales draft preview calendar switch:
   - Log in as `sales` / `demo`.
   - Create a draft order with a future due date and open the schedule preview.
   - Click `待排程`: the preview calendar shows the current sales draft preview allocations.
   - Confirm orders moved to other dates due to schedule conflicts show a dashed gray box with a "已移出" label.
   - Confirm conflicted orders with deferred completion dates show their updated (rescheduled) due dates dynamically.
   - Click `已排程`: the preview calendar switches to formal persisted calendar allocations.
   - Switch back to `待排程` and confirm the draft preview allocations return.
   - In a conflicted preview, confirm `接受目前解法並加入待排程` still creates a pending order and moves checked conflicted pending orders to `需業務處理`.
   - Click `取消選取目前訂單` and confirm the current draft appears under `需業務處理` without requiring a rejection reason.

4. Sales main calendar switch:
   - Log in as `sales` / `demo`.
   - Click `待排程` and `需業務處理` in the status sidebar; confirm the order heading shows only the matching context instead of both `業務處理 / 需處理訂單` and `訂單任務 / 訂單`.
   - Click `待排程`: the main calendar shows pending backlog preview allocations only.
   - Click `已排程`: the main calendar shows persisted schedule allocations only.
   - Click `所有訂單`: the main calendar shows both sets, with pending backlog allocations using preview styling and pending status.

## 4. GKE Ingress Or LoadBalancer Web HPA Verification

Deploy to GKE or an equivalent Ingress/LoadBalancer-capable cluster:

```bash
helm upgrade --install woms ./deploy/helm/woms \
  --namespace woms --create-namespace \
  --set ingress.enabled=true \
  --set ingress.host=woms.c1ydeh.net \
  --set ingress.tls.enabled=true \
  --set web.service.type=ClusterIP \
  --set-json 'web.service.annotations={"cloud.google.com/neg":"{\"ingress\":true}"}'
```

Confirm active resources:

```bash
kubectl get scaledobject,hpa,deploy,pod,svc -n woms
kubectl describe hpa woms-woms-web-hpa -n woms
kubectl get scaledobject woms-woms-web -n woms -o yaml
```

Expected:

- `woms-woms-web` Service is `ClusterIP` when using NGINX Ingress, or explicitly configured as `LoadBalancer` in non-Ingress environments.
- `woms-woms-public` Ingress routes `/` and `/grafana/` traffic to `woms-woms-web`, and keeps exact `/api/auth/login` public to `woms-woms-api`.
- `woms-woms-api-secure` Ingress routes protected `/api` traffic directly to `woms-woms-api` with NGINX Ingress `auth-url`.
- `woms-woms-web` ScaledObject targets `Deployment/woms-woms-web`.
- HPA name is `woms-woms-web-hpa`.
- Trigger metric is `woms_web_nginx_requests_per_second_per_pod`.

Send multi-user traffic:

```bash
INGRESS_HOST="$(kubectl get ingress woms-woms-public -n woms -o jsonpath='{.spec.rules[0].host}')"
hey -z 5m -c 80 "https://${INGRESS_HOST}/"
```

Observe:

```bash
kubectl get hpa,deploy,pod -n woms -l app.kubernetes.io/component=web -w
```

Grafana:

- Open `https://<INGRESS_HOST>/grafana/`, or the explicit `LOAD_URL` host used for non-Ingress verification.
- Open dashboard `WOMS web autoscaling`.
- Confirm `Per-pod NGINX req/s` rises during load.
- Confirm `NGINX req/s by web pod` shows traffic distributed across pods after scale-out.
- Direct `/api` traffic is served by API pods and is not expected to raise the web NGINX request-rate metric.

Expected:

- KEDA/HPA increases web replicas above `minReplicaCount`.
- New web pods become Ready.
- Traffic spreads across multiple web pods.
- After traffic stops and cooldown passes, replicas scale down.

## 5. ArgoCD CD Verification

After ArgoCD is bootstrapped and GitHub Actions WIF variables are configured, verify the CD path without printing any credentials:

```bash
kubectl -n argocd get deploy,statefulset,pod,svc
kubectl -n argocd get application woms
kubectl -n argocd get application woms \
  -o jsonpath='{.status.sync.status}{" "}{.status.health.status}{" "}{.status.sync.revision}{"\n"}'
```

After a merge to `main`, confirm that CD happened only after the Docker publish tag update:

```bash
git fetch origin
git show origin/main:deploy/helm/woms/values.yaml > /tmp/woms-values.yaml
node scripts/verify-release-tag.mjs /tmp/woms-values.yaml "$(git describe --tags --abbrev=0 origin/main)"
ARGOCD_NAMESPACE=argocd ARGOCD_APP=woms EXPECTED_ARGOCD_REVISION="$(git rev-parse origin/main)" ./scripts/verify-argocd-application.sh
kubectl -n woms get deploy woms-woms-api woms-woms-scheduler-worker woms-woms-web \
  -o jsonpath='{range .items[*]}{.metadata.name}{" "}{range .spec.template.spec.containers[*]}{.image}{" "}{end}{"\n"}{end}'
```

Expected:

- GitHub Actions `docker-publish` succeeds before `argocd-cd`.
- `deploy/helm/woms/values.yaml` on `origin/main` uses the latest `v0.1.<run-number>` tag for `api`, `worker`, and `web`.
- ArgoCD Application `woms` is `Synced Healthy`.
- The GKE deployments reference the latest `docker.io/d11nn/*:<tag>` images.

## 6. API, RBAC, And Calendar API Checks

```bash
JWT_SECRET=local-dev-secret go run ./cmd/api
curl -i http://localhost:8080/internal/auth/verify
```

Expected: missing token returns `401`.

Check role boundaries:

- Sales calling scheduler-only schedule job APIs returns `403`.
- Scheduler A cannot read or mutate Scheduler B line data.
- `GET /api/schedules/calendar?lineId=A&month=2026-05` returns persisted allocations for the authorized line.

## 7. Docker And Web Proxy Checks

```bash
docker build -f Dockerfile.api -t woms-api:local .
docker build -f Dockerfile.worker -t woms-scheduler-worker:local .
docker build -f Dockerfile.web -t woms-web:local .
docker compose up --build
```

Expected:

- API health: `curl http://localhost:8080/healthz`
- Web: `http://localhost:8081`
- Grafana through web proxy: `http://localhost:8081/grafana`
- Unauthenticated Grafana users see the Grafana login page.

## 8. Completion Checklist

- Local non-UI tests pass.
- Manual integration coverage is run in CI or against developer-provided services when release validation requires it.
- Browser UI checks above are completed in a browser environment.
- GKE LoadBalancer/HPA checks above are completed in a cluster environment.
- ArgoCD CD checks above are completed after the GitHub Actions run.
- README and both verification docs are updated in English and zh-TW.
- Generated files, secrets, local volumes, and build output remain uncommitted.
