import test from "node:test";
import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";

const values = readFileSync(new URL("./values.yaml", import.meta.url), "utf8");
const chart = readFileSync(new URL("./Chart.yaml", import.meta.url), "utf8");
const scaledObject = readFileSync(new URL("./templates/keda-scaledobject.yaml", import.meta.url), "utf8");
const apiDeployment = readFileSync(new URL("./templates/api-deployment.yaml", import.meta.url), "utf8");
const apiRBAC = readFileSync(new URL("./templates/api-rbac.yaml", import.meta.url), "utf8");
const workerDeployment = readFileSync(new URL("./templates/worker-deployment.yaml", import.meta.url), "utf8");
const webDeployment = readFileSync(new URL("./templates/web-deployment.yaml", import.meta.url), "utf8");
const services = readFileSync(new URL("./templates/services.yaml", import.meta.url), "utf8");
const ingress = readFileSync(new URL("./templates/ingress.yaml", import.meta.url), "utf8");
const helpers = readFileSync(new URL("./templates/_helpers.tpl", import.meta.url), "utf8");
const prometheusConfig = readFileSync(new URL("./templates/prometheus-configmap.yaml", import.meta.url), "utf8");
const grafanaConfig = readFileSync(new URL("./templates/grafana-configmap.yaml", import.meta.url), "utf8");
const grafanaDeployment = readFileSync(new URL("./templates/grafana-deployment.yaml", import.meta.url), "utf8");
const grafanaSecret = readFileSync(new URL("./templates/grafana-secret.yaml", import.meta.url), "utf8");
const webDashboard = readFileSync(new URL("./dashboards/woms-monitoring.json", import.meta.url), "utf8");
const gkeFullOverlay = readFileSync(new URL("./values-gke-full.yaml", import.meta.url), "utf8");
const runtimeDashboard = readFileSync(new URL("./dashboards/woms-runtime-monitoring.json", import.meta.url), "utf8");
const composePrometheus = readFileSync(new URL("../../../monitoring/prometheus.yml", import.meta.url), "utf8");
const hpaBehaviorScript = readFileSync(new URL("../../../scripts/verify-hpa-behavior.sh", import.meta.url), "utf8");
const hpaRenderScript = readFileSync(new URL("../../../scripts/verify-hpa-render.sh", import.meta.url), "utf8");
const kafkaTopicJob = readFileSync(new URL("./templates/kafka-topic-job.yaml", import.meta.url), "utf8");
const secret = readFileSync(new URL("./templates/secret.yaml", import.meta.url), "utf8");
const notes = readFileSync(new URL("./templates/NOTES.txt", import.meta.url), "utf8");

function imageTag(section) {
  const match = values.match(new RegExp(`${section}:\\n[\\s\\S]*?image:\\n[\\s\\S]*?tag:\\s+([^\\s]+)`));
  assert.ok(match, `missing ${section}.image.tag`);
  return match[1];
}

test("Helm values keep async scheduling dependencies separate from web autoscaling", () => {
  const kedaBlock = values.slice(values.indexOf("\nkeda:\n"), values.indexOf("\npostgresql:\n"));
  assert.match(values, /scheduleQueue:[\s\S]*bootstrapServers:\s+"kafka\.\{\{ \.Release\.Namespace \}\}\.svc\.cluster\.local:9092"/);
  assert.match(values, /scheduleQueue:[\s\S]*topic:\s+woms\.schedule\.jobs/);
  assert.match(values, /scheduleQueue:[\s\S]*consumerGroup:\s+woms-scheduler-workers/);
  assert.match(values, /store:\s+postgres/);
  assert.match(values, /databaseUrl:\s+postgres:\/\/woms:woms@postgres:5432\/woms\?sslmode=disable/);
  assert.match(values, /redisAddr:\s+redis-master:6379/);
  assert.match(values, /kafkaPublishEnabled:\s+"true"/);
  assert.doesNotMatch(values, /scheduleTopic:\s+woms\.schedule\.jobs/);
  assert.doesNotMatch(values, /kafkaBrokers:\s+kafka:9092/);
  assert.doesNotMatch(kedaBlock, /kafka:/);
  assert.doesNotMatch(kedaBlock, /cpu:/);
  assert.doesNotMatch(kedaBlock, /gthulhu:/);
  assert.doesNotMatch(values, /^gthulhu:/m);
});

test("Helm chart deploys required platform dependencies without active Gthulhu dependency", () => {
  assert.match(chart, /name:\s+postgresql/);
  assert.match(chart, /condition:\s+postgresql\.enabled/);
  assert.match(chart, /name:\s+redis/);
  assert.match(chart, /condition:\s+redis\.enabled/);
  assert.match(chart, /name:\s+kafka/);
  assert.match(chart, /condition:\s+kafka\.enabled/);
  assert.doesNotMatch(chart, /name:\s+gthulhu/);
  assert.equal(existsSync(new URL("./templates/gthulhu-podschedulingmetrics.yaml", import.meta.url)), false);
});

test("KEDA ScaledObject targets the web Deployment using Prometheus per-pod NGINX req/s", () => {
  assert.match(scaledObject, /kind:\s+ScaledObject/);
  assert.match(scaledObject, /name:\s+\{\{ include "woms\.fullname" \. \}\}-web/);
  assert.match(scaledObject, /scaleTargetRef:[\s\S]*name:\s+\{\{ include "woms\.fullname" \. \}\}-web/);
  assert.match(scaledObject, /horizontalPodAutoscalerConfig:[\s\S]*name:\s+\{\{ include "woms\.fullname" \. \}\}-web-hpa/);
  assert.match(scaledObject, /type:\s+prometheus/);
  assert.match(scaledObject, /serverAddress:\s+\{\{ tpl \.Values\.keda\.prometheus\.serverAddress \. \| quote \}\}/);
  assert.match(scaledObject, /metricName:\s+\{\{ \.Values\.keda\.prometheus\.metricName \| quote \}\}/);
  assert.match(scaledObject, /query:\s+\{\{ tpl \.Values\.keda\.prometheus\.query \. \| quote \}\}/);
  assert.match(values, /metricName:\s+woms_web_nginx_requests_per_second_per_pod/);
  assert.match(values, /nginx_http_requests_total\{job="woms-web-nginx"/);
  assert.match(values, /clamp_min\(count\(up\{job="woms-web-nginx"/);
  assert.doesNotMatch(scaledObject, /type:\s+kafka/);
  assert.doesNotMatch(scaledObject, /type:\s+cpu/);
  assert.doesNotMatch(scaledObject, /gthulhu/);
});

test("Web deployment exposes NGINX traffic metrics behind the ingress-backed ClusterIP service", () => {
  assert.match(values, /web:[\s\S]*service:[\s\S]*type:\s+ClusterIP/);
  assert.match(values, /metrics:[\s\S]*repository:\s+nginx\/nginx-prometheus-exporter/);
  assert.match(webDeployment, /replicas:\s+\{\{ ternary \.Values\.keda\.minReplicaCount \.Values\.web\.replicaCount \(and \.Values\.keda\.enabled \.Values\.web\.metrics\.enabled\) \}\}/);
  assert.match(webDeployment, /name:\s+nginx-exporter/);
  assert.match(webDeployment, /-nginx\.scrape-uri=\{\{ \.Values\.web\.metrics\.scrapeUri \}\}/);
  assert.match(webDeployment, /name:\s+metrics[\s\S]*containerPort:\s+\{\{ \.Values\.web\.metrics\.port \}\}/);
  assert.match(services, /type:\s+\{\{ \.Values\.web\.service\.type \}\}/);
  assert.match(services, /name:\s+metrics[\s\S]*targetPort:\s+metrics/);
  assert.match(scaledObject, /if and \.Values\.keda\.enabled \.Values\.web\.metrics\.enabled/);
  assert.match(values, /minReplicaCount:\s+2/);
});

test("Prometheus and Grafana use the same web NGINX traffic signal as KEDA", () => {
  assert.match(prometheusConfig, /job_name:\s+woms-api/);
  assert.match(prometheusConfig, /job_name:\s+woms-web-nginx/);
  assert.match(prometheusConfig, /regex:\s+\{\{ include "woms\.fullname" \. \}\}-web/);
  assert.match(prometheusConfig, /source_labels:\s+\[__meta_kubernetes_pod_name\][\s\S]*target_label:\s+pod/);
  assert.match(grafanaConfig, /\.Files\.Glob "dashboards\/\*\.json"/);
  assert.match(grafanaConfig, /replace "__WOMS_NAMESPACE__" \$\.Release\.Namespace/);
  assert.doesNotMatch(grafanaConfig, /__WOMS_WEB_REGEX__/);
  assert.doesNotMatch(grafanaConfig, /__WOMS_WORKER_REGEX__/);
  assert.match(webDashboard, /Per-pod NGINX req\/s/);
  assert.match(webDashboard, /sum\(rate\(nginx_http_requests_total\{job=\\"woms-web-nginx\\",namespace=\\"__WOMS_NAMESPACE__\\"\}\[1m\]\)\) \/ clamp_min\(count\(up\{job=\\"woms-web-nginx\\"/);
  assert.match(webDashboard, /NGINX req\/s by web pod/);
  assert.match(webDashboard, /nginx_connections_active/);
  assert.match(runtimeDashboard, /"title":\s+"WOMS Monitoring"/);
  assert.match(runtimeDashboard, /HTTP Req\/s - Total \(1 m\)/);
  assert.match(runtimeDashboard, /HTTP Requests by Status \(1 h\)/);
  assert.match(runtimeDashboard, /Go Runtime - Goroutines/);
  assert.match(runtimeDashboard, /go_memstats_heap_alloc_bytes/);
  assert.match(composePrometheus, /job_name:\s+woms-web-nginx/);
  assert.match(composePrometheus, /service:\s+woms-web/);
  assert.doesNotMatch(composePrometheus, /pod:\s+compose-web-1/);
});

test("Verification scripts cover the web HPA render and ingress or LoadBalancer behavior flow", () => {
  assert.match(hpaRenderScript, /web-hpa/);
  assert.match(hpaRenderScript, /woms_web_nginx_requests_per_second_per_pod/);
  assert.match(hpaRenderScript, /type:\s+prometheus/);
  assert.match(hpaRenderScript, /web\.metrics\.enabled=false/);
  assert.match(hpaRenderScript, /assert_manifest_contains "Deployment" "\$\{RELEASE\}-woms-web" "  replicas: 2"/);
  assert.match(hpaRenderScript, /target_label: pod/);
  assert.match(hpaRenderScript, /assert_manifest_contains "Deployment" "\$\{RELEASE\}-woms-web" "  replicas: 4"/);
  assert.match(hpaRenderScript, /trap '\[ -z "\$cleanup_files" \] \|\| rm -f \$cleanup_files' EXIT/);
  assert.match(hpaRenderScript, /unexpected Kafka KEDA trigger/);
  assert.match(hpaRenderScript, /unexpected Gthulhu resources/);
  assert.match(hpaBehaviorScript, /WEB_DEPLOY="\$\{RELEASE\}-woms-web"/);
  assert.match(hpaBehaviorScript, /PUBLIC_INGRESS="\$\{RELEASE\}-woms-public"/);
  assert.match(hpaBehaviorScript, /LOAD_URL/);
  assert.match(hpaBehaviorScript, /LoadBalancer/);
  assert.match(hpaBehaviorScript, /nginx_http_requests_total/);
  assert.match(hpaBehaviorScript, /duration_seconds/);
  assert.match(hpaBehaviorScript, /initial_ready="\$\(current_ready_replicas\)"/);
  assert.match(hpaBehaviorScript, /target_replicas=\$\(\(baseline \+ 1\)\)/);
  assert.match(hpaBehaviorScript, /wait_hpa_desired_replicas "\$target_replicas"/);
  assert.match(hpaBehaviorScript, /wait_replicas "\$target_replicas" ge/);
  assert.doesNotMatch(hpaBehaviorScript, /HPA_SCENARIO/);
  assert.doesNotMatch(hpaBehaviorScript, /WORKER_DEPLOY/);
});

test("Default Docker image tags use v-prefixed release tags", () => {
  assert.match(values, /^imageRegistry:\s+docker\.io\/d11nn/m);
  const apiTag = imageTag("api");
  assert.match(apiTag, /^v0\.1\.\d+$/);
  assert.equal(imageTag("worker"), apiTag);
  assert.equal(imageTag("web"), apiTag);
  assert.match(apiDeployment, /include "woms\.image"/);
  assert.match(workerDeployment, /include "woms\.image"/);
  assert.match(webDeployment, /include "woms\.image"/);
});

test("Kafka topic hook remains for async scheduling but is not an autoscaling trigger", () => {
  assert.match(kafkaTopicJob, /kind:\s+Job/);
  assert.match(kafkaTopicJob, /helm\.sh\/hook/);
  assert.match(kafkaTopicJob, /topic=\{\{ \.Values\.scheduleQueue\.topic \| quote \}\}/);
  assert.match(kafkaTopicJob, /bootstrap=\{\{ tpl \.Values\.scheduleQueue\.bootstrapServers \. \| quote \}\}/);
  assert.doesNotMatch(kafkaTopicJob, /\.Values\.keda\.kafka/);
  assert.match(apiDeployment, /name:\s+KAFKA_BROKERS[\s\S]*\.Values\.scheduleQueue\.bootstrapServers/);
  assert.match(apiDeployment, /name:\s+KAFKA_SCHEDULE_TOPIC[\s\S]*\.Values\.scheduleQueue\.topic/);
  assert.match(workerDeployment, /name:\s+KAFKA_BROKERS[\s\S]*\.Values\.scheduleQueue\.bootstrapServers/);
  assert.match(workerDeployment, /name:\s+KAFKA_CONSUMER_GROUP[\s\S]*\.Values\.scheduleQueue\.consumerGroup/);
  assert.match(workerDeployment, /replicas:\s+\{\{ \.Values\.worker\.replicaCount \}\}/);
});

test("Bitnami dependency image overrides use the legacy repository for retained tags", () => {
  assert.match(values, /postgresql:[\s\S]*repository:\s+bitnamilegacy\/postgresql/);
  assert.match(values, /postgresql:[\s\S]*tag:\s+16\.4\.0-debian-12-r14/);
  assert.match(values, /redis:[\s\S]*repository:\s+bitnamilegacy\/redis/);
  assert.match(values, /redis:[\s\S]*tag:\s+7\.2\.5-debian-12-r4/);
  assert.match(values, /^kafka:\n(?:^[ \t]+[^\n]*\n)*?^[ \t]+image:\n(?:^[ \t]+[^\n]*\n)*?^[ \t]+repository:\s+bitnamilegacy\/kafka\s*$/m);
  assert.match(values, /^kafka:\n(?:^[ \t]+[^\n]*\n)*?^[ \t]+image:\n(?:^[ \t]+[^\n]*\n)*?^[ \t]+tag:\s+3\.7\.1-debian-12-r4\s*$/m);
});

test("API JWT secret and admin autoscaling status RBAC are wired", () => {
  assert.match(values, /jwtSecret:\s+""/);
  assert.match(secret, /lookup "v1" "Secret"/);
  assert.match(secret, /randAlphaNum 64/);
  assert.match(notes, /generated or reused a JWT secret/);
  assert.match(apiDeployment, /name:\s+HPA_DEMO_HPA_NAME[\s\S]*-web-hpa/);
  assert.match(apiDeployment, /name:\s+HPA_DEMO_DEPLOYMENT_NAME[\s\S]*-web/);
  assert.match(apiDeployment, /app\.kubernetes\.io\/component=web/);
  assert.match(apiRBAC, /resources:\s+\["pods"\][\s\S]*verbs:\s+\["get", "list"\]/);
  assert.match(apiRBAC, /apiGroups:\s+\["apps"\][\s\S]*resources:\s+\["deployments"\][\s\S]*verbs:\s+\["get"\]/);
  assert.match(apiRBAC, /apiGroups:\s+\["autoscaling"\][\s\S]*resources:\s+\["horizontalpodautoscalers"\][\s\S]*verbs:\s+\["get"\]/);
});

test("Ingress keeps login public while protecting API prefix", () => {
  assert.match(ingress, /name:\s+\{\{ include "woms\.fullname" \. \}\}-public/);
  assert.match(ingress, /path:\s+\/api\/auth\/login[\s\S]*pathType:\s+Exact[\s\S]*name:\s+\{\{ include "woms\.fullname" \. \}\}-api/);
  assert.match(ingress, /name:\s+\{\{ include "woms\.fullname" \. \}\}-api-secure/);
  assert.match(ingress, /nginx\.ingress\.kubernetes\.io\/auth-url/);
  assert.match(ingress, /path:\s+\/api[\s\S]*pathType:\s+Prefix/);
});

test("Helm exposes Grafana through the web proxy subpath", () => {
  assert.match(values, /externalPath:\s+\/grafana/);
  assert.match(helpers, /define "woms\.grafanaRootUrl"/);
  assert.match(webDeployment, /name:\s+GRAFANA_UPSTREAM/);
  assert.match(grafanaDeployment, /name:\s+GF_SERVER_ROOT_URL/);
  assert.match(grafanaSecret, /kind:\s+Secret/);
});
