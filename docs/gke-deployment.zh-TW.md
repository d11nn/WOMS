### GKE 完整部署與 NGINX Ingress

GKE full overlay 會啟用 web/API/worker、chart-managed Kafka/Redis/PostgreSQL、透過 KEDA 與 Prometheus 進行的 web traffic autoscaling、Prometheus/Grafana，以及公開的 NGINX Ingress 入口。此流程不會部署 Gthulhu。這條路徑使用 Ingress path splitting：`/` 與 `/grafana/` route 到 web service，`/api/auth/login` 保持公開供登入使用，受保護的 `/api` 則在 `auth-url` 驗證 bearer token 後直接 route 到 API service。Go API 仍是 JWT、session verification、claims 與 RBAC 的真正安全邊界。

Ingress NGINX 已在 2026 年 3 月後停止維護。既有部署仍可運作，但長期正式環境應規劃遷移到仍維護中的 GKE-native Gateway 或 Cloud Armor 架構。

安裝 NGINX Ingress Controller 前，先保留或重用 regional static external IP。此 IP 必須和 GKE cluster 在同一個 region，且 Network Service Tier 要和 LoadBalancer Service 相同。目前 DNS records 指向 `136.110.70.193`，而此 project 已經用 `load-balancer` 名稱保留該 IP，因此請重用它，不要再建立新的 address。

```bash
export GKE_REGION="asia-northeast1"
export INGRESS_IP="136.110.70.193"

gcloud compute addresses list \
  --regions="${GKE_REGION}" \
  --filter="address=${INGRESS_IP}"

export INGRESS_STATIC_IP_NAME="load-balancer"

export INGRESS_IP="$(
  gcloud compute addresses describe "${INGRESS_STATIC_IP_NAME}" \
    --region "${GKE_REGION}" \
    --format='value(address)'
)"
echo "${INGRESS_IP}"
```

若 address list 沒有顯示 `136.110.70.193`，只有在 Google Cloud 仍允許保留該 IP 時，才建立這個指定地址：

```bash
export INGRESS_STATIC_IP_NAME="woms-ingress-ip"
export INGRESS_IP="136.110.70.193"

gcloud compute addresses create "${INGRESS_STATIC_IP_NAME}" \
  --region "${GKE_REGION}" \
  --addresses "${INGRESS_IP}" \
  --network-tier=Premium
```

若 `136.110.70.193` 已被其他 project 保留或無法使用，請不指定 `--addresses`，改保留新的 IP，然後把 Cloudflare/GoDaddy 的 DNS A record 改到該 IP：

```bash
gcloud compute addresses create "${INGRESS_STATIC_IP_NAME}" \
  --region "${GKE_REGION}" \
  --network-tier=Premium

export INGRESS_IP="$(
  gcloud compute addresses describe "${INGRESS_STATIC_IP_NAME}" \
    --region "${GKE_REGION}" \
    --format='value(address)'
)"
echo "${INGRESS_IP}"
```

接著安裝 NGINX Ingress Controller，綁定保留 IP 並保留 client IP。若後續要加 Ingress 白名單或 Cloud Armor policy，`externalTrafficPolicy=Local` 可保留真實使用者來源 IP：

```bash
helm repo add ingress-nginx https://kubernetes.github.io/ingress-nginx
helm repo update

helm upgrade --install ingress-nginx ingress-nginx/ingress-nginx \
  --namespace ingress-nginx --create-namespace \
  --set controller.service.loadBalancerIP="${INGRESS_IP}" \
  --set controller.service.externalTrafficPolicy=Local \
  --set controller.replicaCount=2 \
  --wait --timeout 10m
```

若 Ingress Controller 已經安裝，請 upgrade 既有 release 並指定同一個 static IP，不要 uninstall 後重裝：

```bash
helm upgrade --install ingress-nginx ingress-nginx/ingress-nginx \
  --namespace ingress-nginx --create-namespace \
  --set controller.service.loadBalancerIP="${INGRESS_IP}" \
  --set controller.service.externalTrafficPolicy=Local \
  --set controller.replicaCount=2 \
  --wait --timeout 10m
```

Controller 就緒後，確認 GKE LoadBalancer 使用的是保留 IP：

```bash
kubectl get svc -n ingress-nginx ingress-nginx-controller

export INGRESS_IP="$(
  kubectl get svc -n ingress-nginx ingress-nginx-controller \
    -o jsonpath='{.status.loadBalancer.ingress[0].ip}'
)"
test -n "${INGRESS_IP}"
echo "${INGRESS_IP}"
```

請用保留的 `INGRESS_IP` 決定 host。若只是臨時驗證、不要設定 DNS，可使用 `sslip.io`：

```bash
export WOMS_HOST="woms.${INGRESS_IP}.sslip.io"
```

若使用正式 DNS，請先把 DNS A record 指到同一個保留的 `INGRESS_IP`，並在安裝 WOMS 前確認解析結果正確。目前 GKE 部署使用 `woms.c1ydeh.net`：

```bash
export WOMS_HOST="woms.c1ydeh.net"
dig +short "${WOMS_HOST}"
test "$(dig +short "${WOMS_HOST}" | tail -n 1)" = "${INGRESS_IP}"
```

不要沿用包含舊 IP 的 `WOMS_HOST`，例如舊的 `woms.<old-ip>.sslip.io`。這類 hostname 會解析到舊 load balancer，就算 WOMS pods 全部健康也會連線失敗。DNS record 仍指向該 IP 時，不要刪除 Google Cloud 保留的 address。

部署 WOMS 前先安裝 KEDA：

```bash
helm repo add kedacore https://kedacore.github.io/charts
helm repo update

helm upgrade --install keda kedacore/keda \
  --namespace keda --create-namespace \
  --wait --timeout 10m
```

直接在 GKE 叢集裡安裝 cert-manager，並建立 WOMS Ingress 會使用的 Let’s Encrypt ClusterIssuers：

```bash
helm upgrade --install cert-manager oci://quay.io/jetstack/charts/cert-manager \
  --version v1.20.2 \
  --namespace cert-manager --create-namespace \
  --set crds.enabled=true \
  --wait --timeout 10m

kubectl apply -f ./deploy/helm/woms/cert-manager-clusterissuers.yaml
kubectl get clusterissuer
```

`cert-manager-clusterissuers.yaml` 會建立 `letsencrypt-staging` 與 `letsencrypt-prod`，HTTP-01 solver 會使用 `nginx` IngressClass。GKE full values file 會把 `cert-manager.io/cluster-issuer: letsencrypt-prod` 與 `acme.cert-manager.io/http01-ingress-class: nginx` 加到 WOMS Ingress annotations，因此 cert-manager 會依 Ingress `tls` block 自動建立 TLS Secret。

準備部署時的動態值。`WOMS_TLS_SECRET` 是 cert-manager 會建立的 Secret；走 cert-manager 流程時不要手動建立這個 Secret。

```bash
export WOMS_TLS_SECRET="$(echo "${WOMS_HOST}" | tr '.' '-')-tls"
export WOMS_JWT_SECRET="$(openssl rand -base64 48)"
export GRAFANA_ADMIN_PASSWORD="demo"

kubectl create namespace woms --dry-run=client -o yaml | kubectl apply -f -
```

接著使用會解析到目前 load balancer 的 host 部署完整 stack：

```bash
helm upgrade --install woms ./deploy/helm/woms \
  --namespace woms --create-namespace \
  --dependency-update \
  --wait --timeout 30m \
  -f ./deploy/helm/woms/values-gke.yaml \
  --set ingress.host="${WOMS_HOST}" \
  --set ingress.tls.secretName="${WOMS_TLS_SECRET}" \
  --set gthulhu.enabled=false \
  --set keda.gthulhu.enabled=false \
  --set api.jwtSecret="${WOMS_JWT_SECRET}" \
  --set monitoring.grafana.admin.password="${GRAFANA_ADMIN_PASSWORD}"
```

不要再套用舊的 `values-gthulhu-monitor.yaml` overlay。若舊版失敗部署留下 `InvalidImageName` 或 `InvalidName` 狀態的 `woms-gthulhu-scheduler` resources，請重新執行上方修正後的 Helm 指令；若 release 卡在 pending 狀態，先 uninstall `woms` release 並刪除 `woms` namespace，再重新部署。

API 與 scheduler-worker containers 使用 distroless `nonroot` image，chart 會明確設定數字型 `runAsUser: 65532` 與 `runAsGroup: 65532`。若 pod 曾因 Kubernetes 無法驗證 image user `nonroot` 而進入 `CreateContainerConfigError`，請重新執行上方 Helm upgrade，讓 Deployment template 更新。

`values-gke.yaml` 預設不設定 `nginx.ingress.kubernetes.io/whitelist-source-range`，因此一般公開 client network 可以連到 `https://${WOMS_HOST}/`。若之後要限制來源，請在私有 override values file 加上該 annotation，或在 Helm upgrade 時用明確的 `--set-string` range 設定。

驗證外部入口、DNS、TLS、Kafka topic hook、KEDA、Prometheus 與 Grafana：

```bash
kubectl get svc -n ingress-nginx ingress-nginx-controller -o wide
kubectl get ingress woms-woms-public -n woms -o wide

dig +short "${WOMS_HOST}"
test "$(dig +short "${WOMS_HOST}" | tail -n 1)" = "${INGRESS_IP}"

curl -I "https://${WOMS_HOST}/"
curl -I "https://${WOMS_HOST}/grafana/"
curl -i "https://${WOMS_HOST}/api/orders"
curl -i "https://${WOMS_HOST}/api/auth/login" \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"demo"}'
curl -kI "https://${INGRESS_IP}/" -H "Host: ${WOMS_HOST}"
kubectl logs -n ingress-nginx deploy/ingress-nginx-controller --tail=100

kubectl get certificate,certificaterequest,order,challenge -A
kubectl describe certificate -n woms "${WOMS_TLS_SECRET}"
kubectl get secret "${WOMS_TLS_SECRET}" -n woms
echo | openssl s_client -connect "${WOMS_HOST}:443" -servername "${WOMS_HOST}" -showcerts 2>/dev/null | \
  openssl x509 -noout -subject -issuer -dates -ext subjectAltName

kubectl get job,pod,scaledobject,hpa -n woms
kubectl logs job/woms-woms-kafka-topic -n woms
kubectl exec -n woms kafka-controller-0 -- \
  kafka-topics.sh --bootstrap-server kafka.woms.svc.cluster.local:9092 \
  --describe --topic woms.schedule.jobs
kubectl describe scaledobject woms-woms-web -n woms
kubectl describe hpa woms-woms-web-hpa -n woms
kubectl get deploy,pod,svc -n woms
```

對 `/` 與 `/api/auth/login` 的請求應可從一般公開 client network 進入應用程式。受保護的 `/api` routes 若沒有有效 bearer token，應回傳 auth response。臨時免設定 DNS 測試時，可以用 `woms.${INGRESS_IP}.sslip.io` 指到目前的 load balancer IP；但本方案啟用 TLS，因此憑證必須涵蓋該 hostname，否則瀏覽器會回報憑證名稱不符。

#### 完整關閉 GKE 部署

GKE 驗證結束後，請盡快移除 WOMS、NGINX Ingress 與 KEDA，避免 GKE LoadBalancer 與 Persistent Disk 持續收費。不要只做 Helm uninstall；請刪除整個 `woms` namespace，因為 StatefulSet PVC 可能在 release 移除後繼續保留 GCE PD-backed PersistentVolume。

```bash
helm uninstall woms -n woms --ignore-not-found
kubectl delete namespace woms --wait=true --timeout=15m

helm uninstall ingress-nginx -n ingress-nginx --ignore-not-found
kubectl delete namespace ingress-nginx --wait=true --timeout=10m

helm uninstall keda -n keda --ignore-not-found
kubectl delete namespace keda --wait=true --timeout=10m

helm uninstall cert-manager -n cert-manager --ignore-not-found
kubectl delete namespace cert-manager --wait=true --timeout=10m
```

確認 WOMS workload、公網 LoadBalancer、PVC、PV 與 Helm release 都已清乾淨：

```bash
helm list -A
kubectl get ns
kubectl get pods -n woms
kubectl get pvc -A
kubectl get pv
kubectl get svc -A --field-selector spec.type=LoadBalancer
kubectl get crd scaledobjects.keda.sh
kubectl get crd certificates.cert-manager.io
```

預期關閉狀態：`woms`、`ingress-nginx`、`keda`、`cert-manager` namespaces 都不存在；`helm list -A` 沒有 WOMS/KEDA/ingress/cert-manager releases；沒有 WOMS PVC/PV；也沒有 `ingress-nginx-controller` LoadBalancer。保留的 static IP address 可以刻意留下，讓 Cloudflare/GoDaddy DNS 下次仍指到同一個 ingress address。若環境永久退場，請先移除或修改 DNS，再用 `gcloud compute addresses delete "${INGRESS_STATIC_IP_NAME}" --region "${GKE_REGION}"` 明確刪除。若 namespace 刪除完成後，Google Cloud console 仍顯示 GKE Persistent Disk 或 forwarding rule，請先重查 `kubectl get pv` 與 `kubectl get svc -A --field-selector spec.type=LoadBalancer`，再手動刪除 orphaned cloud resource。
