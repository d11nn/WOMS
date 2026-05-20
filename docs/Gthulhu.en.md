# Gthulhu Prometheus Metrics Scrape Fix Plan

This document details the plan to fix the issue preventing Prometheus from retrieving metrics from Gthulhu in the Wafer Order Management and Scheduling (WOMS) system.

## Current State & Root Cause Analysis

### 1. Rendering Error in Prometheus Scrape Configuration
In `deploy/helm/woms/templates/prometheus-configmap.yaml`, the service name filter rule for the `gthulhu-monitor` scrape job is defined as:

```yaml
        relabel_configs:
          - source_labels: [__meta_kubernetes_service_name]
            action: keep
            regex: {{ printf "%s-scheduler-sidecar" (include "gthulhu.fullname" .Subcharts.gthulhu) | quote }}
```

**Root Cause:**
- `.Subcharts.gthulhu` contains only the static subchart metadata/definitions and **lacks the active `.Release` runtime context**.
- Gthulhu's name helper `gthulhu.fullname` (defined in `charts/gthulhu/templates/_helpers.tpl`) references `.Release.Name`.
- When rendering using `(include "gthulhu.fullname" .Subcharts.gthulhu)`, the absence of `.Release` results in a null pointer evaluation error (`nil pointer evaluating interface {}`) during `.Release.Name` resolution. This crashes the Helm template engine or prevents it from resolving to `woms-gthulhu-scheduler-sidecar`.

### 2. Missing Default Value in main Chart
In the main `deploy/helm/woms/values.yaml`, the `service` key is missing under the `monitoring.prometheus.scrape.gthulhu` block. This causes the template expression to resolve to `nil` when the `values-gthulhu-monitor.yaml` values overlay is not applied.

---

## Proposed Changes

The proposed fix resolves the template expression error and provides the default values without modifying any other functional settings.

### 1. Modify `values.yaml`
Add the default `service` template string to `values.yaml` under `monitoring.prometheus.scrape.gthulhu`. This ensures that the Service name helper can be parsed in all deployment configurations:

#### [MODIFY] [values.yaml](file:///c:/Users/Alen%20Chen/Desktop/WMOS/WOMS/deploy/helm/woms/values.yaml)
```yaml
      # Gthulhu pods: scrape /metrics on port 9090
      gthulhu:
        enabled: true
        path: /metrics
        service: '{{ .Release.Name }}-gthulhu-scheduler-sidecar'
        port: "9090"
        interval: 15s
```

### 2. Modify `prometheus-configmap.yaml`
Update the regex expression for the service name in `relabel_configs` to use the `tpl` function evaluating the configured `service` value. This is also the exact syntax expected by the static tests in `chart-static.test.mjs`:

#### [MODIFY] [prometheus-configmap.yaml](file:///c:/Users/Alen%20Chen/Desktop/WMOS/WOMS/deploy/helm/woms/templates/prometheus-configmap.yaml)
```yaml
        relabel_configs:
          - source_labels: [__meta_kubernetes_service_name]
            action: keep
            regex: {{ tpl .Values.monitoring.prometheus.scrape.gthulhu.service . | quote }}
```

---

## Verification Plan

### 1. Dry-Run Template Rendering (Render Check)
Run the Helm template command in a cluster environment or locally to verify the rendered output compiles without errors and evaluates to the correct service name:

```bash
# Render using the Gthulhu monitor overlay values
helm template woms ./deploy/helm/woms -f ./deploy/helm/woms/values-gthulhu-monitor.yaml --namespace woms
```

**Expected Rendered ConfigMap Snippet:**
```yaml
      - job_name: gthulhu-monitor
        metrics_path: /metrics
        scrape_interval: 15s
        kubernetes_sd_configs:
          - role: endpoints
            namespaces:
              names:
                - woms
        relabel_configs:
          - source_labels: [__meta_kubernetes_service_name]
            action: keep
            regex: "woms-gthulhu-scheduler-sidecar"
          - source_labels: [__meta_kubernetes_endpoint_port_name]
            action: keep
            regex: monitor-metrics
```

### 2. Unit Test Validation
Run the static test suite in a compatible environment:
```bash
npm run test:web
```
After the changes, the test `Alan monitoring templates scrape WOMS and Gthulhu metrics` in `chart-static.test.mjs` will pass, satisfying the following assertion:
```javascript
assert.match(prometheusConfig, /tpl \.Values\.monitoring\.prometheus\.scrape\.gthulhu\.service \./);
```

### 3. Runtime Verification
Once deployed to a live cluster, verify using the instructions in `docs/verification.en.md`:
1. Ensure the Gthulhu scheduler DaemonSet is active and exposing metrics.
2. Confirm the metrics endpoint via port forwarding:
   ```bash
   kubectl port-forward -n woms svc/woms-gthulhu-scheduler-sidecar 9090:9090
   curl -s http://127.0.0.1:9090/metrics | grep gthulhu_pod_
   ```
3. Verify that the `gthulhu-monitor` target status in Prometheus is `UP`.
4. Run Prometheus queries to verify `gthulhu_pod_involuntary_ctx_switches_total` is correctly populated.
