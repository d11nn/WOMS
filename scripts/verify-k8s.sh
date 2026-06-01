#!/usr/bin/env sh
set -eu

NAMESPACE="${NAMESPACE:-woms}"
RELEASE="${RELEASE:-woms}"
CHART="${CHART:-./deploy/helm/woms}"
INGRESS_ENABLED="${INGRESS_ENABLED:-false}"
KUBECTL="${KUBECTL:-kubectl}"
HELM="${HELM:-helm}"
KAFKA_BOOTSTRAP="${KAFKA_BOOTSTRAP:-kafka.${NAMESPACE}.svc.cluster.local:9092}"
KAFKA_TOPIC="${KAFKA_TOPIC:-woms.schedule.jobs}"
KAFKA_CONSUMER_GROUP="${KAFKA_CONSUMER_GROUP:-woms-scheduler-workers}"

retry() {
  label="$1"
  max_attempts="$2"
  sleep_seconds="$3"
  shift 3

  attempt=1
  until "$@"; do
    if [ "$attempt" -ge "$max_attempts" ]; then
      echo "$label failed after $max_attempts attempts" >&2
      return 1
    fi
    attempt=$((attempt + 1))
    sleep "$sleep_seconds"
  done
}

require_rendered_pdb() {
  name="$1"
  awk -v name="$name" '
    /^kind: PodDisruptionBudget$/ { in_pdb = 1; next }
    /^---$/ { in_pdb = 0 }
    in_pdb && $0 ~ "name: " name "$" { found = 1 }
    END { exit found ? 0 : 1 }
  ' /tmp/woms-rendered.yaml
}

require_deployment_rollout() {
  name="$1"
  "$KUBECTL" get deploy "$name" -n "$NAMESPACE" >/dev/null
  "$KUBECTL" rollout status "deploy/$name" -n "$NAMESPACE" --timeout=180s
}

require_statefulset_rollout() {
  name="$1"
  "$KUBECTL" get statefulset "$name" -n "$NAMESPACE" >/dev/null
  "$KUBECTL" rollout status "statefulset/$name" -n "$NAMESPACE" --timeout=300s
}

require_pvc_bound() {
  name="$1"
  phase="$("$KUBECTL" get pvc "$name" -n "$NAMESPACE" -o jsonpath='{.status.phase}')"
  [ "$phase" = "Bound" ]
}

require_service() {
  name="$1"
  "$KUBECTL" get service "$name" -n "$NAMESPACE" >/dev/null
}

require_worker_broker_env() {
  value="$("$KUBECTL" get deploy "$RELEASE-woms-worker" -n "$NAMESPACE" -o jsonpath='{.spec.template.spec.containers[?(@.name=="scheduler-worker")].env[?(@.name=="KAFKA_BROKERS")].value}')"
  [ "$value" = "$KAFKA_BOOTSTRAP" ]
}

require_kafka_topic() {
  "$KUBECTL" exec -n "$NAMESPACE" kafka-controller-0 -- \
    kafka-topics.sh --bootstrap-server "$KAFKA_BOOTSTRAP" --describe --topic "$KAFKA_TOPIC" \
    >/tmp/woms-kafka-topic.txt
  grep -q "Topic: $KAFKA_TOPIC" /tmp/woms-kafka-topic.txt
}

require_kafka_consumer_group() {
  "$KUBECTL" exec -n "$NAMESPACE" kafka-controller-0 -- \
    kafka-consumer-groups.sh --bootstrap-server "$KAFKA_BOOTSTRAP" --list \
    >/tmp/woms-kafka-consumer-groups.txt
  grep -qx "$KAFKA_CONSUMER_GROUP" /tmp/woms-kafka-consumer-groups.txt
}

require_scaledobject_ready() {
  status="$("$KUBECTL" get scaledobject "$RELEASE-woms-web" -n "$NAMESPACE" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')"
  [ "$status" = "True" ]
}

require_keda_external_metric() {
  metric_name="$("$KUBECTL" get scaledobject "$RELEASE-woms-web" -n "$NAMESPACE" -o jsonpath='{.status.externalMetricNames[0]}')"
  [ -n "$metric_name" ]
  "$KUBECTL" get --raw "/apis/external.metrics.k8s.io/v1beta1/namespaces/${NAMESPACE}/${metric_name}?labelSelector=scaledobject.keda.sh%2Fname%3D${RELEASE}-woms-web" \
    >/tmp/woms-keda-external-metric.json
  grep -Eq '"items"[[:space:]]*:[[:space:]]*\[[[:space:]]*\{' /tmp/woms-keda-external-metric.json
  grep -Eq '"value"[[:space:]]*:[[:space:]]*"[^"]+"' /tmp/woms-keda-external-metric.json
}

"$HELM" template "$RELEASE" "$CHART" --dependency-update --namespace "$NAMESPACE" --set "ingress.enabled=${INGRESS_ENABLED}" >/tmp/woms-rendered.yaml
grep -q "kind: ScaledObject" /tmp/woms-rendered.yaml
if [ "$INGRESS_ENABLED" = "true" ]; then
  grep -q "kind: Ingress" /tmp/woms-rendered.yaml
fi
grep -q "name: ${RELEASE}-woms-web" /tmp/woms-rendered.yaml
grep -q "name: ${RELEASE}-woms-web-hpa" /tmp/woms-rendered.yaml
grep -q "bootstrapServers: \"${KAFKA_BOOTSTRAP}\"" /tmp/woms-rendered.yaml
grep -q "value: \"${KAFKA_BOOTSTRAP}\"" /tmp/woms-rendered.yaml
require_rendered_pdb "${RELEASE}-woms-api"
require_rendered_pdb "${RELEASE}-woms-web"

"$HELM" status "$RELEASE" -n "$NAMESPACE" | grep -q "STATUS: deployed"
"$KUBECTL" get namespace "$NAMESPACE" >/dev/null
"$KUBECTL" get scaledobject "$RELEASE-woms-web" -n "$NAMESPACE"
"$KUBECTL" get hpa "$RELEASE-woms-web-hpa" -n "$NAMESPACE"
"$KUBECTL" get poddisruptionbudget "$RELEASE-woms-api" "$RELEASE-woms-web" -n "$NAMESPACE"
require_service "$RELEASE-woms-api"
require_service "$RELEASE-woms-web"
require_service "postgres"
require_service "redis-master"
require_service "kafka"
require_statefulset_rollout "postgres"
require_statefulset_rollout "redis-master"
require_statefulset_rollout "kafka-controller"
require_pvc_bound "data-postgres-0"
require_pvc_bound "redis-data-redis-master-0"
require_pvc_bound "data-kafka-controller-0"
require_deployment_rollout "$RELEASE-woms-api"
require_deployment_rollout "$RELEASE-woms-web"
require_deployment_rollout "$RELEASE-woms-worker"
require_worker_broker_env
retry "Kafka topic $KAFKA_TOPIC" 12 5 require_kafka_topic
retry "Kafka consumer group $KAFKA_CONSUMER_GROUP" 12 5 require_kafka_consumer_group
"$KUBECTL" describe scaledobject "$RELEASE-woms-web" -n "$NAMESPACE"
retry "KEDA ScaledObject Ready" 12 10 require_scaledobject_ready
retry "KEDA external metric" 12 10 require_keda_external_metric

echo "Kubernetes static and resource verification passed"
