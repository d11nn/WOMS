#!/usr/bin/env sh
set -eu

KUBECTL="${KUBECTL:-kubectl}"
ARGOCD_NAMESPACE="${ARGOCD_NAMESPACE:-argocd}"
ARGOCD_APP="${ARGOCD_APP:-woms}"
TIMEOUT_SECONDS="${TIMEOUT_SECONDS:-300}"
SLEEP_SECONDS="${SLEEP_SECONDS:-10}"
EXPECTED_ARGOCD_REVISION="${EXPECTED_ARGOCD_REVISION:-}"

deadline="$(($(date +%s) + TIMEOUT_SECONDS))"

while [ "$(date +%s)" -le "$deadline" ]; do
  phase="$("$KUBECTL" -n "$ARGOCD_NAMESPACE" get application "$ARGOCD_APP" -o jsonpath='{.status.operationState.phase}' 2>/dev/null || true)"
  sync_status="$("$KUBECTL" -n "$ARGOCD_NAMESPACE" get application "$ARGOCD_APP" -o jsonpath='{.status.sync.status}' 2>/dev/null || true)"
  health_status="$("$KUBECTL" -n "$ARGOCD_NAMESPACE" get application "$ARGOCD_APP" -o jsonpath='{.status.health.status}' 2>/dev/null || true)"
  revision="$("$KUBECTL" -n "$ARGOCD_NAMESPACE" get application "$ARGOCD_APP" -o jsonpath='{.status.sync.revision}' 2>/dev/null || true)"

  if [ "$phase" = "Succeeded" ] && [ "$sync_status" = "Synced" ] && [ "$health_status" = "Healthy" ]; then
    if [ -n "$EXPECTED_ARGOCD_REVISION" ] && [ "$revision" != "$EXPECTED_ARGOCD_REVISION" ]; then
      echo "ArgoCD synced revision $revision, expected $EXPECTED_ARGOCD_REVISION" >&2
      exit 1
    fi
    echo "ArgoCD application $ARGOCD_NAMESPACE/$ARGOCD_APP is Synced and Healthy at $revision"
    exit 0
  fi

  echo "Waiting for ArgoCD application: phase=${phase:-unknown}, sync=${sync_status:-unknown}, health=${health_status:-unknown}, revision=${revision:-unknown}"
  sleep "$SLEEP_SECONDS"
done

echo "Timed out waiting for ArgoCD application $ARGOCD_NAMESPACE/$ARGOCD_APP" >&2
"$KUBECTL" -n "$ARGOCD_NAMESPACE" get application "$ARGOCD_APP" -o yaml >&2 || true
exit 1
