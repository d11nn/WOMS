# Gthulhu Helm Chart - Quick Start Guide

## Prerequisites

Before installing Gthulhu, ensure your cluster meets the following requirements:

### System Requirements
- Kubernetes 1.19+
- Helm 3.0+
- Linux kernel 6.12+ with sched-ext support on worker nodes
- Container runtime with BPF capabilities

### Verify Prerequisites

```bash
# Check Kubernetes version
kubectl version --short

# Check Helm version
helm version --short

# Check node kernel version
kubectl get nodes -o wide

# Verify BPF support (run on nodes)
kubectl debug node/NODE_NAME -it --image=busybox -- sh
# Inside the debug container:
ls /sys/kernel/debug/
```

## Quick Installation

### 1. Basic Installation (Development)

```bash
# From the Gthulhu chart directory

# Install with default values
helm install gthulhu .

# Check installation
kubectl get pods -l app.kubernetes.io/name=gthulhu
```

### 2. Production Installation

```bash
# Install with production values
helm install gthulhu ./gthulhu -f gthulhu/values-production.yaml

# Verify both scheduler and manager are running
kubectl get daemonset gthulhu-scheduler
kubectl get deployment gthulhu-manager
```

### 3. Testing Installation

```bash
# Render a test install with default values
helm install gthulhu-test . --dry-run --debug
```

## Post-Installation Verification

### Check Scheduler Status

```bash
# Verify scheduler DaemonSet
kubectl get daemonset gthulhu-scheduler -o wide

# Check scheduler logs
kubectl logs -l app.kubernetes.io/component=scheduler --tail=100

# Verify BPF programs are loaded
kubectl exec -it daemonset/gthulhu-scheduler -- ls /sys/kernel/debug/
```

### Check Manager Server

```bash
# Verify manager deployment
kubectl get deployment gthulhu-manager -o wide

# Check manager logs
kubectl logs -l app.kubernetes.io/component=manager --tail=100

# Test manager health endpoint
kubectl port-forward svc/gthulhu-manager 8080:80
curl http://localhost:8080/health
```

### Send Test Metrics

```bash
# Send test metrics to API
curl -X POST http://localhost:8080/api/v1/metrics \
  -H "Content-Type: application/json" \
  -d '{
    "usersched_pid": 1234,
    "nr_queued": 10,
    "nr_scheduled": 5,
    "nr_running": 2,
    "nr_online_cpus": 8,
    "nr_user_dispatches": 100,
    "nr_kernel_dispatches": 50,
    "nr_cancel_dispatches": 2,
    "nr_bounce_dispatches": 1,
    "nr_failed_dispatches": 0,
    "nr_sched_congested": 3
  }'
```

## Common Installation Scenarios

### Scenario 1: Development Environment (Manager Only)

```bash
helm install gthulhu-dev ./gthulhu \
  --set scheduler.enabled=false \
  --set manager.service.type=NodePort
```

### Scenario 2: Production with Custom Domain

```bash
helm install gthulhu ./gthulhu \
  --set ingress.enabled=true \
  --set ingress.hosts[0].host=gthulhu.yourdomain.com \
  --set ingress.hosts[0].paths[0].path=/ \
  --set ingress.hosts[0].paths[0].pathType=Prefix
```

### Scenario 3: High Availability Setup

```bash
helm install gthulhu ./gthulhu \
  --set manager.replicaCount=3
```

### Scenario 4: Monitoring Enabled

```bash
helm install gthulhu ./gthulhu \
  --set monitoring.enabled=true \
  --set monitoring.serviceMonitor.enabled=true
```

## Troubleshooting

### Common Issues

1. **Scheduler Won't Start**
   ```bash
   # Check node capabilities
   kubectl describe node NODE_NAME | grep -i kernel
   
   # Check privileged access
   kubectl get pods -l app.kubernetes.io/component=scheduler -o yaml | grep -i security
   ```

2. **Manager Server Not Accessible**
   ```bash
   # Check service
   kubectl get svc gthulhu-manager
   
   # Check endpoints
   kubectl get endpoints gthulhu-manager
   ```

3. **Permission Denied**
   ```bash
   # Check RBAC
   kubectl get clusterrole gthulhu-scheduler
   kubectl get clusterrolebinding gthulhu-scheduler
   ```

### Getting Logs

```bash
# Scheduler logs
kubectl logs -l app.kubernetes.io/component=scheduler -f

# Manager logs
kubectl logs -l app.kubernetes.io/component=manager -f

# All Gthulhu logs
kubectl logs -l app.kubernetes.io/name=gthulhu -f
```

## Upgrading

```bash
# Upgrade to new version
helm upgrade gthulhu ./gthulhu

# Upgrade with new values
helm upgrade gthulhu ./gthulhu -f new-values.yaml

# Check upgrade status
helm status gthulhu
```

## Uninstalling

```bash
# Uninstall Gthulhu
helm uninstall gthulhu

# Verify cleanup
kubectl get all -l app.kubernetes.io/name=gthulhu
```

## Next Steps

1. **Configure Monitoring**: Set up Prometheus and Grafana dashboards
2. **Set up Alerting**: Configure alerts for scheduler and API health
3. **Performance Tuning**: Adjust scheduler parameters based on workload
4. **Security**: Configure network policies and pod security standards

For detailed configuration options, see the main [README.md](README.md) file.
