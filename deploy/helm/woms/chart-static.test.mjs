import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

// ── Template sources ────────────────────────────────────────────────────────
const values             = readFileSync(new URL("./values.yaml",                              import.meta.url), "utf8");
const chart              = readFileSync(new URL("./Chart.yaml",                               import.meta.url), "utf8");
const helpers            = readFileSync(new URL("./templates/_helpers.tpl",                   import.meta.url), "utf8");
const notes              = readFileSync(new URL("./templates/NOTES.txt",                      import.meta.url), "utf8");
const secret             = readFileSync(new URL("./templates/secret.yaml",                    import.meta.url), "utf8");
const services           = readFileSync(new URL("./templates/services.yaml",                  import.meta.url), "utf8");
const apiDeployment      = readFileSync(new URL("./templates/api-deployment.yaml",            import.meta.url), "utf8");
const workerDeployment   = readFileSync(new URL("./templates/worker-deployment.yaml",         import.meta.url), "utf8");
const webDeployment      = readFileSync(new URL("./templates/web-deployment.yaml",            import.meta.url), "utf8");
const scaledObject       = readFileSync(new URL("./templates/keda-scaledobject.yaml",         import.meta.url), "utf8");
const kafkaTopicJob      = readFileSync(new URL("./templates/kafka-topic-job.yaml",           import.meta.url), "utf8");
const ingress            = readFileSync(new URL("./templates/ingress.yaml",                   import.meta.url), "utf8");
const pdb                = readFileSync(new URL("./templates/poddisruptionbudgets.yaml",      import.meta.url), "utf8");
const prometheusConfigmap   = readFileSync(new URL("./templates/prometheus-configmap.yaml",   import.meta.url), "utf8");
const prometheusDeployment  = readFileSync(new URL("./templates/prometheus-deployment.yaml",  import.meta.url), "utf8");
const prometheusService     = readFileSync(new URL("./templates/prometheus-service.yaml",     import.meta.url), "utf8");
const grafanaConfigmap      = readFileSync(new URL("./templates/grafana-configmap.yaml",      import.meta.url), "utf8");
const grafanaDeployment     = readFileSync(new URL("./templates/grafana-deployment.yaml",     import.meta.url), "utf8");
const grafanaService        = readFileSync(new URL("./templates/grafana-service.yaml",        import.meta.url), "utf8");
const gthulhuDeployment     = readFileSync(new URL("./templates/gthulhu-deployment.yaml",    import.meta.url), "utf8");
const gthulhuService        = readFileSync(new URL("./templates/gthulhu-service.yaml",       import.meta.url), "utf8");

// ── Helpers ─────────────────────────────────────────────────────────────────
/**
 * Extract the `tag` value under a given top-level section (e.g. "api", "worker").
 * Works for sections that have an `image.tag` sub-key.
 */
function imageTag(section) {
  const match = values.match(new RegExp(`${section}:\\n[\\s\\S]*?image:\\n[\\s\\S]*?tag:\\s+([^\\s]+)`));
  assert.ok(match, `missing ${section}.image.tag`);
  return match[1];
}

// ── 1. Core values: async scheduling / HPA / KEDA gthulhu defaults ──────────
test("Helm values keep async scheduling and HPA demo defaults wired", () => {
  assert.match(values, /store:\s+postgres/);
  assert.match(values, /databaseUrl:\s+postgres:\/\/woms:woms@postgres:5432\/woms\?sslmode=disable/);
  assert.match(values, /redisAddr:\s+redis-master:6379/);
  assert.match(values, /kafkaBrokers:\s+kafka:9092/);
  assert.match(values, /scheduleTopic:\s+woms\.schedule\.jobs/);
  assert.match(values, /kafkaPublishEnabled:\s+"true"/);
  assert.match(values, /minJobDurationMs:\s+"0"/);
  assert.match(values, /maxRetries:\s+"3"/);
  assert.match(values, /consumerGroup:\s+woms-scheduler-workers/);
  assert.match(values, /bootstrapServers:\s+"kafka\.\{\{ \.Release\.Namespace \}\}\.svc\.cluster\.local:9092"/);
  assert.match(values, /lagThreshold:\s+"10"/);
  assert.match(values, /targetUtilization:\s+"70"/);
  // keda.gthulhu defaults (separate from monitoring.gthulhu)
  assert.match(values, /keda:[\s\S]*gthulhu:[\s\S]*enabled:\s+false/);
  assert.match(values, /prometheusServerAddress:\s+"http:\/\/\{\{/);
  assert.match(values, /metricName:\s+woms_worker_gthulhu_involuntary_ctx_switches_rate/);
  assert.match(values, /threshold:\s+"20"/);
  assert.match(values, /query:\s+\|-/);
  assert.match(values, /gthulhu_pod_involuntary_ctx_switches_total\{exported_namespace="\{\{ \.Release\.Namespace \}\}"/);
  assert.match(values, /pod_name=~"\{\{ include "woms\.fullname" \. \}\}-worker-\.\*"/);
});

// ── 2. Platform dependency chart defaults ────────────────────────────────────
test("Helm chart deploys required platform dependencies by default", () => {
  assert.match(chart, /name:\s+postgresql/);
  assert.match(chart, /condition:\s+postgresql\.enabled/);
  assert.match(chart, /name:\s+redis/);
  assert.match(chart, /condition:\s+redis\.enabled/);
  assert.match(chart, /name:\s+kafka/);
  assert.match(chart, /condition:\s+kafka\.enabled/);
  assert.match(values, /postgresql:[\s\S]*enabled:\s+true/);
  assert.match(values, /fullnameOverride:\s+postgres/);
  assert.match(values, /redis:[\s\S]*enabled:\s+true/);
  assert.match(values, /fullnameOverride:\s+redis/);
  assert.match(values, /kafka:[\s\S]*enabled:\s+true/);
  assert.match(values, /fullnameOverride:\s+kafka/);
});

// ── 3. Image tags ────────────────────────────────────────────────────────────
test("Default Docker image tags use v-prefixed release tags", () => {
  assert.match(values, /^imageRegistry:\s+docker\.io\/d11nn/m);
  const apiTag = imageTag("api");
  assert.match(apiTag, /^v0\.\d+\.\d+$/);
  assert.equal(imageTag("worker"), apiTag);
  assert.equal(imageTag("web"), apiTag);
  assert.match(apiDeployment,    /include "woms\.image"/);
  assert.match(workerDeployment, /include "woms\.image"/);
  assert.match(webDeployment,    /include "woms\.image"/);
});

// ── 4. KEDA ScaledObject ─────────────────────────────────────────────────────
test("KEDA ScaledObject template points at scheduler worker backlog", () => {
  assert.match(scaledObject, /kind:\s+ScaledObject/);
  assert.match(scaledObject, /horizontalPodAutoscalerConfig:/);
  assert.match(scaledObject, /name:\s+\{\{ include "woms\.fullname" \. \}\}-worker-hpa/);
  assert.match(scaledObject, /scaleTargetRef:[\s\S]*name:\s+\{\{ include "woms\.fullname" \. \}\}-worker/);
  assert.match(scaledObject, /type:\s+kafka/);
  assert.match(scaledObject, /bootstrapServers:\s+\{\{ tpl \.Values\.keda\.kafka\.bootstrapServers \. \| quote \}\}/);
  assert.match(scaledObject, /topic:\s+\{\{ \.Values\.keda\.kafka\.topic \| quote \}\}/);
  assert.match(scaledObject, /consumerGroup:\s+\{\{ \.Values\.keda\.kafka\.consumerGroup \| quote \}\}/);
  assert.match(scaledObject, /lagThreshold:\s+\{\{ \.Values\.keda\.kafka\.lagThreshold \| quote \}\}/);
  assert.match(scaledObject, /type:\s+cpu/);
  assert.match(scaledObject, /metricType:\s+Utilization/);
  assert.match(scaledObject, /if \.Values\.keda\.gthulhu\.enabled/);
  assert.match(scaledObject, /type:\s+prometheus/);
  assert.match(scaledObject, /serverAddress:\s+\{\{ \.Values\.keda\.gthulhu\.prometheusServerAddress \| quote \}\}/);
  assert.match(scaledObject, /metricName:\s+\{\{ \.Values\.keda\.gthulhu\.metricName \| quote \}\}/);
  assert.match(scaledObject, /query:\s+\{\{ tpl \.Values\.keda\.gthulhu\.query \. \| quote \}\}/);
  assert.match(scaledObject, /threshold:\s+\{\{ \.Values\.keda\.gthulhu\.threshold \| quote \}\}/);
});

// ── 5. Kafka topic hook ──────────────────────────────────────────────────────
test("Kafka topic hook creates the scheduling topic with enough partitions for HPA", () => {
  assert.match(values, /kafkaTopic:[\s\S]*repository:\s+docker\.io\/bitnamilegacy\/kafka/);
  assert.match(values, /kafkaTopic:[\s\S]*tag:\s+3\.7\.1-debian-12-r4/);
  assert.match(kafkaTopicJob, /kind:\s+Job/);
  assert.match(kafkaTopicJob, /helm\.sh\/hook/);
  assert.match(kafkaTopicJob, /activeDeadlineSeconds:\s+\{\{ \.Values\.kafkaTopic\.activeDeadlineSeconds \}\}/);
  assert.match(kafkaTopicJob, /bootstrap=\{\{ tpl \.Values\.keda\.kafka\.bootstrapServers \. \| quote \}\}/);
  assert.match(kafkaTopicJob, /kafka-topics\.sh/);
  assert.match(kafkaTopicJob, /max_attempts=\{\{ \.Values\.kafkaTopic\.wait\.maxAttempts \| int \}\}/);
  assert.match(kafkaTopicJob, /exit 1/);
  assert.match(kafkaTopicJob, /--create/);
  assert.match(kafkaTopicJob, /--if-not-exists/);
  assert.match(kafkaTopicJob, /--alter/);
  assert.match(kafkaTopicJob, /\$partitions = \(\.Values\.keda\.maxReplicaCount \| int\)/);
});

// ── 6. Bitnami legacy image overrides ────────────────────────────────────────
test("Bitnami dependency image overrides use the legacy repository for retained tags", () => {
  assert.match(values, /postgresql:[\s\S]*repository:\s+bitnamilegacy\/postgresql/);
  assert.match(values, /postgresql:[\s\S]*tag:\s+16\.4\.0-debian-12-r14/);
  assert.match(values, /redis:[\s\S]*repository:\s+bitnamilegacy\/redis/);
  assert.match(values, /redis:[\s\S]*tag:\s+7\.2\.5-debian-12-r4/);
  assert.match(values, /^kafka:\n(?:^[ \t]+[^\n]*\n)*?^[ \t]+image:\n(?:^[ \t]+[^\n]*\n)*?^[ \t]+repository:\s+bitnamilegacy\/kafka\s*$/m);
  assert.match(values, /^kafka:\n(?:^[ \t]+[^\n]*\n)*?^[ \t]+image:\n(?:^[ \t]+[^\n]*\n)*?^[ \t]+tag:\s+3\.7\.1-debian-12-r4\s*$/m);
});

// ── 7. Single-node Kafka defaults ────────────────────────────────────────────
test("Single-node Kafka defaults keep internal topics usable on a clean VM", () => {
  assert.match(values, /controller:[\s\S]*replicaCount:\s+1/);
  assert.match(values, /broker:[\s\S]*replicaCount:\s+0/);
  assert.match(values, /controller:[\s\S]*extraConfigYaml:[\s\S]*default\.replication\.factor:\s+1/);
  assert.match(values, /controller:[\s\S]*extraConfigYaml:[\s\S]*min\.insync\.replicas:\s+1/);
  assert.match(values, /controller:[\s\S]*extraConfigYaml:[\s\S]*offsets\.topic\.replication\.factor:\s+1/);
  assert.match(values, /controller:[\s\S]*extraConfigYaml:[\s\S]*transaction\.state\.log\.min\.isr:\s+1/);
  assert.match(values, /controller:[\s\S]*extraConfigYaml:[\s\S]*transaction\.state\.log\.replication\.factor:\s+1/);
});

// ── 8. JWT secret generation ─────────────────────────────────────────────────
test("API JWT secret is generated when unset and documented for retrieval", () => {
  assert.match(values, /jwtSecret:\s+""/);
  assert.match(secret,  /lookup "v1" "Secret"/);
  assert.match(secret,  /randAlphaNum 64/);
  assert.match(notes,   /generated or reused a JWT secret/);
  assert.match(notes,   /kubectl get secret/);
});

// ── 9. API and worker deployment env vars ────────────────────────────────────
test("API and worker deployments expose PostgreSQL, Kafka, and retry env", () => {
  assert.match(apiDeployment, /name:\s+API_STORE/);
  assert.match(apiDeployment, /name:\s+DATABASE_URL/);
  assert.match(apiDeployment, /name:\s+KAFKA_SCHEDULE_TOPIC/);
  assert.match(apiDeployment, /name:\s+KAFKA_PUBLISH_ENABLED/);
  assert.match(workerDeployment, /name:\s+KAFKA_SCHEDULE_TOPIC/);
  assert.match(workerDeployment, /value:\s+\{\{ tpl \.Values\.keda\.kafka\.bootstrapServers \. \| quote \}\}/);
  assert.match(workerDeployment, /name:\s+KAFKA_CONSUMER_GROUP/);
  assert.match(workerDeployment, /name:\s+DATABASE_URL/);
  assert.match(workerDeployment, /name:\s+WORKER_MIN_JOB_DURATION_MS/);
  assert.match(workerDeployment, /name:\s+WORKER_MAX_RETRIES/);
  assert.match(workerDeployment, /if not \.Values\.keda\.enabled/);
  assert.match(workerDeployment, /replicas:\s+\{\{ \.Values\.worker\.replicaCount \}\}/);
});

// ── 10. Web deployment & core services ──────────────────────────────────────
test("Web deployment is runnable without manual securityContext patches", () => {
  assert.doesNotMatch(services, /name:\s+api\s*\n/);
  assert.match(services, /name:\s+\{\{ include "woms\.fullname" \. \}\}-api/);
  assert.match(webDeployment, /name:\s+API_UPSTREAM/);
  assert.match(webDeployment, /value:\s+\{\{ printf "%s-api:8080" \(include "woms\.fullname" \.\) \| quote \}\}/);
  assert.match(webDeployment, /fsGroup:\s+101/);
  assert.match(webDeployment, /runAsNonRoot:\s+true/);
  assert.match(webDeployment, /runAsUser:\s+101/);
  assert.match(webDeployment, /readOnlyRootFilesystem:\s+true/);
  assert.match(webDeployment, /mountPath:\s+\/etc\/nginx\/conf\.d/);
  assert.match(webDeployment, /mountPath:\s+\/var\/cache\/nginx/);
  assert.match(webDeployment, /mountPath:\s+\/var\/run/);
  assert.match(webDeployment, /mountPath:\s+\/tmp/);
});

// ── 11. Ingress template ─────────────────────────────────────────────────────
test("Ingress template is gated by ingress.enabled and routes correctly", () => {
  // disabled by default in values
  assert.match(values, /ingress:[\s\S]*enabled:\s+false/);
  // public ingress: login route → api, root → web
  assert.match(ingress, /if \.Values\.ingress\.enabled/);
  assert.match(ingress, /path:\s+\/api\/auth\/login/);
  assert.match(ingress, /\{\{ include "woms\.fullname" \. \}\}-api/);
  assert.match(ingress, /path:\s+\//);
  assert.match(ingress, /\{\{ include "woms\.fullname" \. \}\}-web/);
  // secure api ingress with optional JWT auth
  assert.match(ingress, /name:\s+\{\{ include "woms\.fullname" \. \}\}-api-secure/);
  assert.match(ingress, /if \.Values\.ingress\.auth\.enabled/);
  assert.match(ingress, /nginx\.ingress\.kubernetes\.io\/auth-url/);
  assert.match(ingress, /\/internal\/auth\/verify/);
  assert.match(ingress, /X-User-ID,X-User-Role,X-User-Line/);
  // TLS block present
  assert.match(ingress, /if \.Values\.ingress\.tls\.enabled/);
  assert.match(ingress, /\{\{ \.Values\.ingress\.tls\.secretName \| quote \}\}/);
});

// ── 12. PodDisruptionBudgets ──────────────────────────────────────────────────
test("PodDisruptionBudgets protect api and web with configurable minAvailable", () => {
  assert.match(pdb, /kind:\s+PodDisruptionBudget/);
  assert.match(pdb, /name:\s+\{\{ include "woms\.fullname" \. \}\}-api/);
  assert.match(pdb, /minAvailable:\s+\{\{ \.Values\.api\.pdb\.minAvailable \}\}/);
  assert.match(pdb, /name:\s+\{\{ include "woms\.fullname" \. \}\}-web/);
  assert.match(pdb, /minAvailable:\s+\{\{ \.Values\.web\.pdb\.minAvailable \}\}/);
  // values default to minAvailable: 1 for both
  assert.match(values, /api:[\s\S]*pdb:[\s\S]*minAvailable:\s+1/);
  assert.match(values, /web:[\s\S]*pdb:[\s\S]*minAvailable:\s+1/);
});

// ── 13. Prometheus deployment and configuration ───────────────────────────────
test("Prometheus deployment is gated by monitoring flags and configures scrape jobs", () => {
  // default enabled in values
  assert.match(values, /monitoring:[\s\S]*enabled:\s+true/);
  assert.match(values, /prometheus:[\s\S]*enabled:\s+true/);
  assert.match(values, /prometheus:[\s\S]*image:[\s\S]*repository:\s+prom\/prometheus/);
  assert.match(values, /prometheus:[\s\S]*image:[\s\S]*tag:\s+"v2\.53\.0"/);
  // deployment
  assert.match(prometheusDeployment, /if and \.Values\.monitoring\.enabled \.Values\.monitoring\.prometheus\.enabled/);
  assert.match(prometheusDeployment, /name:\s+\{\{ include "woms\.fullname" \. \}\}-prometheus/);
  assert.match(prometheusDeployment, /app\.kubernetes\.io\/component:\s+prometheus/);
  assert.match(prometheusDeployment, /fsGroup:\s+65534/);
  assert.match(prometheusDeployment, /runAsNonRoot:\s+true/);
  assert.match(prometheusDeployment, /--config\.file=\/etc\/prometheus\/prometheus\.yml/);
  assert.match(prometheusDeployment, /--storage\.tsdb\.retention\.time=7d/);
  assert.match(prometheusDeployment, /path:\s+\/-\/ready/);
  assert.match(prometheusDeployment, /path:\s+\/-\/healthy/);
  // configmap
  assert.match(prometheusConfigmap, /if and \.Values\.monitoring\.enabled \.Values\.monitoring\.prometheus\.enabled/);
  assert.match(prometheusConfigmap, /name:\s+\{\{ include "woms\.fullname" \. \}\}-prometheus/);
  assert.match(prometheusConfigmap, /scrape_interval:\s+\{\{ \.Values\.monitoring\.prometheus\.scrape\.api\.interval \}\}/);
  assert.match(prometheusConfigmap, /job_name:\s+woms-api/);
  assert.match(prometheusConfigmap, /metrics_path:\s+\{\{ \.Values\.monitoring\.prometheus\.scrape\.api\.path \}\}/);
  assert.match(prometheusConfigmap, /if and \.Values\.monitoring\.gthulhu\.enabled \.Values\.monitoring\.prometheus\.scrape\.gthulhu/);
  assert.match(prometheusConfigmap, /job_name:\s+gthulhu/);
  // service
  assert.match(prometheusService, /if and \.Values\.monitoring\.enabled \.Values\.monitoring\.prometheus\.enabled/);
  assert.match(prometheusService, /name:\s+\{\{ include "woms\.fullname" \. \}\}-prometheus/);
  assert.match(prometheusService, /port:\s+9090/);
});

// ── 14. Grafana deployment, configmaps, and service ───────────────────────────
test("Grafana deployment provisions datasources, dashboards, and anonymous access", () => {
  // default enabled in values
  assert.match(values, /grafana:[\s\S]*enabled:\s+true/);
  assert.match(values, /grafana:[\s\S]*image:[\s\S]*repository:\s+grafana\/grafana/);
  assert.match(values, /grafana:[\s\S]*image:[\s\S]*tag:\s+"10\.4\.7"/);
  assert.match(values, /anonymousEnabled:\s+"true"/);
  assert.match(values, /anonymousOrgRole:\s+Viewer/);
  assert.match(values, /allowEmbedding:\s+"true"/);
  // deployment
  assert.match(grafanaDeployment, /if and \.Values\.monitoring\.enabled \.Values\.monitoring\.grafana\.enabled/);
  assert.match(grafanaDeployment, /name:\s+\{\{ include "woms\.fullname" \. \}\}-grafana/);
  assert.match(grafanaDeployment, /app\.kubernetes\.io\/component:\s+grafana/);
  assert.match(grafanaDeployment, /GF_AUTH_ANONYMOUS_ENABLED/);
  assert.match(grafanaDeployment, /GF_AUTH_ANONYMOUS_ORG_ROLE/);
  assert.match(grafanaDeployment, /GF_SECURITY_ALLOW_EMBEDDING/);
  assert.match(grafanaDeployment, /mountPath:\s+\/etc\/grafana\/provisioning\/datasources/);
  assert.match(grafanaDeployment, /mountPath:\s+\/etc\/grafana\/provisioning\/dashboards/);
  assert.match(grafanaDeployment, /mountPath:\s+\/var\/lib\/grafana\/dashboards/);
  assert.match(grafanaDeployment, /path:\s+\/api\/health/);
  // datasource configmap wires Prometheus
  assert.match(grafanaConfigmap, /name:\s+\{\{ include "woms\.fullname" \. \}\}-grafana-datasources/);
  assert.match(grafanaConfigmap, /type:\s+prometheus/);
  assert.match(grafanaConfigmap, /url:\s+http:\/\/\{\{ include "woms\.fullname" \. \}\}-prometheus:9090/);
  assert.match(grafanaConfigmap, /isDefault:\s+true/);
  // dashboard provider configmap
  assert.match(grafanaConfigmap, /name:\s+\{\{ include "woms\.fullname" \. \}\}-grafana-dashboard-provider/);
  assert.match(grafanaConfigmap, /path:\s+\/var\/lib\/grafana\/dashboards/);
  // dashboard JSON configmap uses .Files.Glob
  assert.match(grafanaConfigmap, /name:\s+\{\{ include "woms\.fullname" \. \}\}-grafana-dashboards/);
  assert.match(grafanaConfigmap, /\.Files\.Glob "dashboards\/\*\.json"/);
  // service
  assert.match(grafanaService, /if and \.Values\.monitoring\.enabled \.Values\.monitoring\.grafana\.enabled/);
  assert.match(grafanaService, /name:\s+\{\{ include "woms\.fullname" \. \}\}-grafana/);
  assert.match(grafanaService, /port:\s+3000/);
});

// ── 15. Gthulhu deployment and service ───────────────────────────────────────
test("Gthulhu deployment is gated by monitoring flags and exposes infra metrics env", () => {
  // default enabled in values
  assert.match(values, /monitoring:[\s\S]*gthulhu:[\s\S]*enabled:\s+true/);
  assert.match(values, /gthulhu:[\s\S]*image:[\s\S]*repository:\s+ghcr\.io\/gthulhu\/gthulhu/);
  assert.match(values, /metricsPort:\s+9091/);
  assert.match(values, /postgresDsn:\s+postgres:\/\/woms:woms@postgres:5432\/woms\?sslmode=disable/);
  // deployment
  assert.match(gthulhuDeployment, /if and \.Values\.monitoring\.enabled \.Values\.monitoring\.gthulhu\.enabled/);
  assert.match(gthulhuDeployment, /name:\s+\{\{ include "woms\.fullname" \. \}\}-gthulhu/);
  assert.match(gthulhuDeployment, /app\.kubernetes\.io\/component:\s+gthulhu/);
  assert.match(gthulhuDeployment, /GTHULHU_POSTGRES_DSN/);
  assert.match(gthulhuDeployment, /GTHULHU_REDIS_ADDR/);
  assert.match(gthulhuDeployment, /GTHULHU_KAFKA_BROKERS/);
  assert.match(gthulhuDeployment, /GTHULHU_METRICS_PORT/);
  assert.match(gthulhuDeployment, /containerPort:\s+\{\{ \.Values\.monitoring\.gthulhu\.metricsPort \}\}/);
  // service
  assert.match(gthulhuService, /if and \.Values\.monitoring\.enabled \.Values\.monitoring\.gthulhu\.enabled/);
  assert.match(gthulhuService, /name:\s+\{\{ include "woms\.fullname" \. \}\}-gthulhu/);
  assert.match(gthulhuService, /port:\s+\{\{ \.Values\.monitoring\.gthulhu\.metricsPort \}\}/);
});

// ── 16. _helpers.tpl sanity ──────────────────────────────────────────────────
test("_helpers.tpl defines fullname, labels, and image builder", () => {
  assert.match(helpers, /define "woms\.fullname"/);
  assert.match(helpers, /define "woms\.labels"/);
  assert.match(helpers, /define "woms\.image"/);
  // image helper composes registry + repository + tag
  assert.match(helpers, /printf "%s\/%s:%s" \$registry \.repository \.tag/);
  assert.match(helpers, /printf "%s:%s" \.repository \.tag/);
});
