### GKE Full Deployment With NGINX Ingress

The GKE full overlay enables the web/API/worker stack, chart-managed Kafka/Redis/PostgreSQL, web traffic autoscaling through KEDA and Prometheus, Prometheus/Grafana, and a public NGINX Ingress entry point. It does not deploy Gthulhu. This path uses Ingress path splitting: `/` and `/grafana/` route to the web service, `/api/auth/login` remains public for login, and protected `/api` routes go directly to the API service after `auth-url` verifies the bearer token. The Go API remains the real security boundary for JWT, session verification, claims, and RBAC.

Ingress NGINX was retired after March 2026. Existing deployments continue to work, but long-term production exposure should move to a maintained GKE-native Gateway or Cloud Armor design.

Reserve or reuse a regional static external IP before installing NGINX Ingress Controller. The reserved address must be in the same region as the GKE cluster and use the same Network Service Tier as the LoadBalancer Service. The current DNS records point to `136.110.70.193`, and this project already has that address reserved as `load-balancer`, so reuse it instead of creating a new address.

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

If the address list does not show `136.110.70.193`, reserve that exact address only if Google Cloud still allows it:

```bash
export INGRESS_STATIC_IP_NAME="woms-ingress-ip"
export INGRESS_IP="136.110.70.193"

gcloud compute addresses create "${INGRESS_STATIC_IP_NAME}" \
  --region "${GKE_REGION}" \
  --addresses "${INGRESS_IP}" \
  --network-tier=Premium
```

If `136.110.70.193` is already reserved by a different project or is otherwise unavailable, reserve a new address without `--addresses`, then update the DNS A record in Cloudflare/GoDaddy to the new reserved address:

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

Install NGINX Ingress Controller with the reserved IP and client IP preservation. `externalTrafficPolicy=Local` preserves the real client IP if you later add an Ingress whitelist or Cloud Armor policy:

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

If the Ingress Controller is already installed, upgrade the existing release with the same static IP instead of uninstalling it:

```bash
helm upgrade --install ingress-nginx ingress-nginx/ingress-nginx \
  --namespace ingress-nginx --create-namespace \
  --set controller.service.loadBalancerIP="${INGRESS_IP}" \
  --set controller.service.externalTrafficPolicy=Local \
  --set controller.replicaCount=2 \
  --wait --timeout 10m
```

Confirm the GKE LoadBalancer uses the reserved IP after the controller is ready:

```bash
kubectl get svc -n ingress-nginx ingress-nginx-controller

export INGRESS_IP="$(
  kubectl get svc -n ingress-nginx ingress-nginx-controller \
    -o jsonpath='{.status.loadBalancer.ingress[0].ip}'
)"
test -n "${INGRESS_IP}"
echo "${INGRESS_IP}"
```

Choose the host from the reserved `INGRESS_IP`. For a temporary no-DNS validation host, use `sslip.io`:

```bash
export WOMS_HOST="woms.${INGRESS_IP}.sslip.io"
```

For a real DNS name, point the DNS A record at the same reserved `INGRESS_IP`, then verify it before installing WOMS. The current GKE deployment uses `woms.c1ydeh.net`:

```bash
export WOMS_HOST="woms.c1ydeh.net"
dig +short "${WOMS_HOST}"
test "$(dig +short "${WOMS_HOST}" | tail -n 1)" = "${INGRESS_IP}"
```

Do not reuse a previous `WOMS_HOST` that embeds an old IP, such as an old `woms.<old-ip>.sslip.io` value. That hostname resolves to the old load balancer and will fail even if all WOMS pods are healthy. Do not delete the reserved Google Cloud address while the DNS record points to it.

Install KEDA before deploying WOMS:

```bash
helm repo add kedacore https://kedacore.github.io/charts
helm repo update

helm upgrade --install keda kedacore/keda \
  --namespace keda --create-namespace \
  --wait --timeout 10m
```

Install cert-manager directly in the GKE cluster and create the Let’s Encrypt ClusterIssuers used by the WOMS Ingress:

```bash
helm upgrade --install cert-manager oci://quay.io/jetstack/charts/cert-manager \
  --version v1.20.2 \
  --namespace cert-manager --create-namespace \
  --set crds.enabled=true \
  --wait --timeout 10m

kubectl apply -f ./deploy/helm/woms/cert-manager-clusterissuers.yaml
kubectl get clusterissuer
```

The `cert-manager-clusterissuers.yaml` manifest creates `letsencrypt-staging` and `letsencrypt-prod` with an HTTP-01 solver that uses the `nginx` IngressClass. The GKE full values file adds `cert-manager.io/cluster-issuer: letsencrypt-prod` and `acme.cert-manager.io/http01-ingress-class: nginx` to the WOMS Ingress annotations, so cert-manager will create the TLS Secret automatically from the Ingress `tls` block.

Prepare deployment-time values. `WOMS_TLS_SECRET` is the Secret that cert-manager will create; do not create it manually for the cert-manager path.

```bash
export WOMS_TLS_SECRET="$(echo "${WOMS_HOST}" | tr '.' '-')-tls"
export WOMS_JWT_SECRET="<strong-secret>"
export GRAFANA_ADMIN_PASSWORD="<strong-password>"

kubectl create namespace woms --dry-run=client -o yaml | kubectl apply -f -
```

Then deploy the full stack with the host that resolves to the current load balancer:

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

Do not include the legacy `values-gthulhu-monitor.yaml` overlay. If an older failed deployment left `woms-gthulhu-scheduler` resources in `InvalidImageName` or `InvalidName`, rerun the corrected Helm command above; if the release is stuck in a pending state, uninstall the `woms` release and delete the `woms` namespace before deploying again.

The API and scheduler-worker containers run the distroless `nonroot` image with numeric `runAsUser: 65532` and `runAsGroup: 65532`. If a pod previously failed with `CreateContainerConfigError` because Kubernetes could not verify the image user `nonroot`, rerun the Helm upgrade above so the Deployment template is refreshed.

By default, `values-gke.yaml` does not set `nginx.ingress.kubernetes.io/whitelist-source-range`, so `https://${WOMS_HOST}/` is reachable from normal public client networks. To restrict access later, add that annotation in a private override values file or pass an explicit `--set-string` range during Helm upgrade.

Validate the external endpoint, DNS, TLS, Kafka topic hook, KEDA, Prometheus, and Grafana:

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

Requests to `/` and `/api/auth/login` should reach the application from ordinary public client networks. Protected `/api` routes should return an auth response unless a valid bearer token is provided. For temporary no-DNS testing, `woms.${INGRESS_IP}.sslip.io` can point at the current load balancer IP, but with TLS enabled the certificate must cover that hostname or the browser will report a certificate mismatch.

#### Fully Shut Down The GKE Deployment

When the GKE validation is finished, remove WOMS, NGINX Ingress, and KEDA promptly to avoid continuing charges from GKE LoadBalancers and Persistent Disks. Delete the `woms` namespace, not only the Helm release, because StatefulSet PVCs can keep GCE PD-backed PersistentVolumes allocated after uninstall.

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

Confirm that no WOMS workloads, public load balancers, PVCs, PVs, or Helm releases remain:

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

Expected shutdown state: `woms`, `ingress-nginx`, `keda`, and `cert-manager` namespaces are absent; `helm list -A` has no WOMS/KEDA/ingress/cert-manager releases; there are no WOMS PVCs/PVs; and no `ingress-nginx-controller` LoadBalancer remains. The reserved static IP address can intentionally remain allocated so Cloudflare/GoDaddy DNS continues to point to the same future ingress address. If permanently retiring the environment, delete it explicitly with `gcloud compute addresses delete "${INGRESS_STATIC_IP_NAME}" --region "${GKE_REGION}"` after removing or changing DNS. If a GKE Persistent Disk or forwarding rule still appears in the Google Cloud console after namespace deletion completes, recheck `kubectl get pv` and `kubectl get svc -A --field-selector spec.type=LoadBalancer` before deleting the orphaned cloud resource manually.
