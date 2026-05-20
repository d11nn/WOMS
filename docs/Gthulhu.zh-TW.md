# Gthulhu 監控指標收集修復計畫 (Prometheus Metrics Scrape Fix Plan)

本文件詳細說明如何修復 Prometheus 無法正常從 Gthulhu 收集監控指標（metrics）的問題。

## 目前基準與問題分析 (Current State & Root Cause Analysis)

### 1. Prometheus 抓取設定中的渲染錯誤
在 `deploy/helm/woms/templates/prometheus-configmap.yaml` 中，`gthulhu-monitor` 抓取工作（scrape job）的服務名稱過濾規則定義如下：

```yaml
        relabel_configs:
          - source_labels: [__meta_kubernetes_service_name]
            action: keep
            regex: {{ printf "%s-scheduler-sidecar" (include "gthulhu.fullname" .Subcharts.gthulhu) | quote }}
```

**根本原因：**
- `.Subcharts.gthulhu` 僅包含該子 chart 的靜態定義與 metadata，**並不包含運行時的 `.Release` 上下文**。
- Gthulhu 子 chart 的命名 Helper `gthulhu.fullname`（定義於 `charts/gthulhu/templates/_helpers.tpl`）內部會引用 `.Release.Name`。
- 當使用 `(include "gthulhu.fullname" .Subcharts.gthulhu)` 渲染時，由於傳入的上下文缺乏 `.Release`，導致 `.Release.Name` 評估時出現空指針錯誤（`nil pointer evaluating interface {}`），致使整個 Helm 模板渲染失敗，或無法正確解析為 `woms-gthulhu-scheduler-sidecar`。

### 2. 預設值缺失
在 `deploy/helm/woms/values.yaml` 中，`monitoring.prometheus.scrape.gthulhu` 區塊缺少了 `service` 欄位定義。這會導致在未使用 `values-gthulhu-monitor.yaml` 覆蓋設定時，渲染模板嘗試讀取該變數會得到 `nil` 值。

---

## 提案修改方案 (Proposed Changes)

本修復方案遵循「不改變任何其他設定」的原則，修正模板語法以正確獲取服務名稱。

### 1. 修改 `values.yaml`
在主 `values.yaml` 中，為 `gthulhu` 抓取設定補上預設的 `service` 模板字串，使其在所有佈署模式下均能獲取對應的 Service 名稱：

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

### 2. 修改 `prometheus-configmap.yaml`
將 relabeling 規則中的服務名稱過濾正則表達式，改為使用 `tpl` 解析 values 中配置的 service 名稱。這也是 `chart-static.test.mjs` 單元測試中明確期望的寫法：

#### [MODIFY] [prometheus-configmap.yaml](file:///c:/Users/Alen%20Chen/Desktop/WMOS/WOMS/deploy/helm/woms/templates/prometheus-configmap.yaml)
```yaml
        relabel_configs:
          - source_labels: [__meta_kubernetes_service_name]
            action: keep
            regex: {{ tpl .Values.monitoring.prometheus.scrape.gthulhu.service . | quote }}
```

---

## 驗證計畫 (Verification Plan)

### 1. 靜態模板渲染測試 (Dry-Run / Render Check)
在 Kubernetes 叢集環境中（或本地），執行以下 Helm 渲染指令以確認無錯誤且生成正確的 Service 名稱：

```bash
# 使用 gthulhu 監控覆蓋檔渲染
helm template woms ./deploy/helm/woms -f ./deploy/helm/woms/values-gthulhu-monitor.yaml --namespace woms
```

**預期輸出片段 (ConfigMap 中)：**
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

### 2. 單元測試驗證 (Unit Test Validation)
在支援測試的環境中執行 Node.js 靜態測試：
```bash
npm run test:web
```
修改後，`chart-static.test.mjs` 中的 `Alan monitoring templates scrape WOMS and Gthulhu metrics` 測試將能順利通過，因為其斷言：
```javascript
assert.match(prometheusConfig, /tpl \.Values\.monitoring\.prometheus\.scrape\.gthulhu\.service \./);
```
此時將完全吻合。

### 3. 運行期狀態驗證 (Runtime Verification)
待佈署至實際環境後，依循 `docs/verification.zh-TW.md` 指引進行驗證：
1. 確保 Gthulhu Scheduler DaemonSet 啟動並輸出 metrics。
2. 透過 Port-Forward 驗證指標端點可通：
   ```bash
   kubectl port-forward -n woms svc/woms-gthulhu-scheduler-sidecar 9090:9090
   curl -s http://127.0.0.1:9090/metrics | grep gthulhu_pod_
   ```
3. 確認 Prometheus 抓取目標（targets）中 `gthulhu-monitor` 狀態為 `UP`。
4. 驗證 Prometheus Query 可查出 `gthulhu_pod_involuntary_ctx_switches_total` 指標。
