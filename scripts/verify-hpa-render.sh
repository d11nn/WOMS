#!/usr/bin/env sh
set -eu

RELEASE="${RELEASE:-woms}"
NAMESPACE="${NAMESPACE:-woms}"
CHART="${CHART:-./deploy/helm/woms}"
RENDERED_MANIFEST="${RENDERED_MANIFEST:-}"
GTHULHU_ENABLED="${GTHULHU_ENABLED:-false}"
VALUES_FILE="${VALUES_FILE:-}"

if [ -n "$RENDERED_MANIFEST" ]; then
  rendered="$RENDERED_MANIFEST"
else
  rendered="$(mktemp)"
  trap 'rm -f "$rendered"' EXIT

  values_args=""
  if [ -n "$VALUES_FILE" ]; then
    values_args="-f $VALUES_FILE"
  fi

  if [ "$GTHULHU_ENABLED" = "true" ]; then
    # shellcheck disable=SC2086
    helm template "$RELEASE" "$CHART" --dependency-update --namespace "$NAMESPACE" $values_args --set keda.gthulhu.enabled=true >"$rendered"
  else
    # shellcheck disable=SC2086
    helm template "$RELEASE" "$CHART" --dependency-update --namespace "$NAMESPACE" $values_args >"$rendered"
  fi
fi

grep -q "kind: ScaledObject" "$rendered"
grep -q "name: ${RELEASE}-woms-worker-hpa" "$rendered"
grep -q "horizontalPodAutoscalerConfig:" "$rendered"
grep -q "scaleTargetRef:" "$rendered"
grep -q "name: ${RELEASE}-woms-worker" "$rendered"
grep -q "minReplicaCount: 1" "$rendered"
grep -q "maxReplicaCount: 10" "$rendered"
grep -q "type: kafka" "$rendered"
grep -q 'topic: "woms.schedule.jobs"' "$rendered"
grep -q 'consumerGroup: "woms-scheduler-workers"' "$rendered"
grep -q 'lagThreshold: "10"' "$rendered"
grep -q "type: cpu" "$rendered"
grep -q "metricType: Utilization" "$rendered"
grep -q 'value: "70"' "$rendered"
gthulhu_metrics="$(grep -c 'metricName: "woms_worker_gthulhu_involuntary_ctx_switches_rate"' "$rendered" || true)"
prometheus_triggers="$gthulhu_metrics"
if [ "$GTHULHU_ENABLED" = "true" ]; then
  [ "$prometheus_triggers" -eq 1 ]
  [ "$gthulhu_metrics" -eq 1 ]
  grep -Eq "serverAddress: \"http://(monitoring-kube-prometheus-prometheus.monitoring|${RELEASE}-woms-prometheus.${NAMESPACE}):9090\"" "$rendered"
  expected_query="query: \"avg(rate(gthulhu_pod_involuntary_ctx_switches_total{exported_namespace=\\\"${NAMESPACE}\\\",pod_name=~\\\"${RELEASE}-woms-worker-.*\\\"}[2m]))\""
  grep -Fq "$expected_query" "$rendered"
  grep -q 'threshold: "20"' "$rendered"
else
  [ "$prometheus_triggers" -eq 0 ]
  [ "$gthulhu_metrics" -eq 0 ]
fi
grep -q "scaleUp:" "$rendered"
grep -q "stabilizationWindowSeconds: 0" "$rendered"
grep -q "scaleDown:" "$rendered"
grep -q "stabilizationWindowSeconds: 120" "$rendered"
grep -q "kind: PodDisruptionBudget" "$rendered"
grep -q "name: ${RELEASE}-woms-api" "$rendered"
grep -q "name: ${RELEASE}-woms-web" "$rendered"
grep -q "minAvailable: 1" "$rendered"

if [ -n "$VALUES_FILE" ]; then
  grep -q "kind: DaemonSet" "$rendered"
  grep -q "name: ${RELEASE}-gthulhu-scheduler" "$rendered"
  grep -q "name: monitor-metrics" "$rendered"
  grep -q "kind: PodSchedulingMetrics" "$rendered"
  grep -q "name: ${RELEASE}-woms-prometheus" "$rendered"
  grep -q "name: ${RELEASE}-woms-grafana" "$rendered"
fi

echo "HPA/KEDA render verification passed"
