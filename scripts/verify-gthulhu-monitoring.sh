#!/usr/bin/env sh
set -eu

NAMESPACE="${NAMESPACE:-woms}"
RELEASE="${RELEASE:-woms}"
KUBECTL="${KUBECTL:-kubectl}"
PROMETHEUS_SERVICE="${PROMETHEUS_SERVICE:-${RELEASE}-woms-prometheus}"
GTHULHU_SERVICE="${GTHULHU_SERVICE:-${RELEASE}-gthulhu-scheduler-sidecar}"
WORKER_REGEX="${WORKER_REGEX:-${RELEASE}-woms-worker-.*}"
NAMESPACE_LABEL="${NAMESPACE_LABEL:-exported_namespace}"

prom_query() {
  query="$1"
  "$KUBECTL" exec -n "$NAMESPACE" "deploy/${PROMETHEUS_SERVICE}" -- \
    sh -c 'if command -v curl >/dev/null 2>&1; then curl -fsS -G --data-urlencode "query=$1" "http://127.0.0.1:9090/api/v1/query"; else wget -qO- "http://127.0.0.1:9090/api/v1/query?query=$1"; fi' sh "$query"
}

require_success_query() {
  label="$1"
  query="$2"
  output="/tmp/woms-${label}.json"
  prom_query "$query" >"$output"
  grep -q '"status":"success"' "$output"
  grep -Eq '"result"[[:space:]]*:[[:space:]]*\[[[:space:]]*\{' "$output"
}

"$KUBECTL" get daemonset "${RELEASE}-gthulhu-scheduler" -n "$NAMESPACE"
"$KUBECTL" rollout status "daemonset/${RELEASE}-gthulhu-scheduler" -n "$NAMESPACE" --timeout=180s
"$KUBECTL" get service "$GTHULHU_SERVICE" -n "$NAMESPACE"
"$KUBECTL" get service "$PROMETHEUS_SERVICE" -n "$NAMESPACE"
"$KUBECTL" rollout status "deploy/${PROMETHEUS_SERVICE}" -n "$NAMESPACE" --timeout=180s
"$KUBECTL" rollout status "deploy/${RELEASE}-woms-grafana" -n "$NAMESPACE" --timeout=180s

require_success_query api 'up{job="woms-api"}'
require_success_query gthulhu_targets 'up{job="gthulhu-monitor"}'
require_success_query gthulhu_process "sum(gthulhu_pod_process_count{${NAMESPACE_LABEL}=\"${NAMESPACE}\",pod_name=~\"${WORKER_REGEX}\"})"
require_success_query gthulhu_ctx "avg(rate(gthulhu_pod_involuntary_ctx_switches_total{${NAMESPACE_LABEL}=\"${NAMESPACE}\",pod_name=~\"${WORKER_REGEX}\"}[2m]))"
require_success_query gthulhu_wait "(avg(rate(gthulhu_pod_wait_time_nanoseconds_total{${NAMESPACE_LABEL}=\"${NAMESPACE}\",pod_name=~\"${WORKER_REGEX}\"}[2m])))/1000000000"

"$KUBECTL" get configmap "${RELEASE}-woms-grafana-dashboards" -n "$NAMESPACE" -o yaml | \
  grep -q "Worker Involuntary Context Switch Rate"

echo "Gthulhu monitoring verification passed"
