import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const workflow = readFileSync(new URL("../../.github/workflows/argocd-cd.yml", import.meta.url), "utf8");
const application = readFileSync(new URL("./woms-application.yaml", import.meta.url), "utf8");
const gkeValues = readFileSync(new URL("../helm/woms/values-gke.yaml", import.meta.url), "utf8");
const releaseVerifier = readFileSync(new URL("../../scripts/verify-release-tag.mjs", import.meta.url), "utf8");
const argocdVerifier = readFileSync(new URL("../../scripts/verify-argocd-application.sh", import.meta.url), "utf8");

test("ArgoCD Application is namespaced, manual-sync only, and uses GKE override values", () => {
  assert.match(application, /kind:\s+Application/);
  assert.match(application, /namespace:\s+argocd/);
  assert.match(application, /repoURL:\s+https:\/\/github\.com\/d11nn\/WOMS\.git/);
  assert.match(application, /targetRevision:\s+main/);
  assert.match(application, /path:\s+deploy\/helm\/woms/);
  assert.match(application, /releaseName:\s+woms/);
  assert.match(application, /namespace:\s+woms/);
  assert.match(application, /valueFiles:[\s\S]*values\.yaml[\s\S]*values-gke\.yaml/);
  assert.doesNotMatch(application, /automated:/);
  assert.doesNotMatch(application, /CreateNamespace=true/);
});

test("GKE values preserve the existing API JWT Secret instead of rotating it through GitOps", () => {
  assert.match(gkeValues, /jwtSecretExistingSecret:\s+woms-woms-api/);
  assert.match(gkeValues, /jwtSecretExistingSecretKey:\s+JWT_SECRET/);
  assert.doesNotMatch(gkeValues, /jwtSecret:\s+\S/);
});

test("GKE values preserve the live NGINX ingress host, cert-manager TLS, and runtime Secrets", () => {
  assert.match(gkeValues, /ingress:[\s\S]*enabled:\s+true/);
  assert.match(gkeValues, /host:\s+woms\.c1ydeh\.net/);
  assert.match(gkeValues, /tls:[\s\S]*enabled:\s+true/);
  assert.match(gkeValues, /secretName:\s+woms-c1ydeh-net-tls/);
  assert.match(gkeValues, /cert-manager\.io\/cluster-issuer:\s+"letsencrypt-prod"/);
  assert.match(gkeValues, /existingSecret:\s+woms-woms-grafana-admin/);
  assert.doesNotMatch(gkeValues, /public:[\s\S]*tls:/);
  assert.doesNotMatch(gkeValues, /apiSecure:[\s\S]*tls:/);
});

test("CD workflow waits for docker-publish and verifies the tag update before syncing ArgoCD", () => {
  assert.match(workflow, /workflow_run:/);
  assert.match(workflow, /docker-publish/);
  assert.match(workflow, /id-token:\s+write/);
  assert.match(workflow, /github\.event\.workflow_run\.head_branch == 'main'/);
  assert.match(workflow, /github\.event\.workflow_run\.event == 'push'/);
  assert.match(workflow, /release_tag="v0\.1\.\$\{\{ github\.event\.workflow_run\.run_number \}\}"/);
  assert.match(workflow, /Checkout released tag after Docker tag update/);
  assert.match(workflow, /ref:\s+\$\{\{ steps\.release\.outputs\.release_tag \}\}/);
  assert.match(workflow, /git rev-list -n 1 \$\{\{ steps\.release\.outputs\.release_tag \}\}/);
  assert.match(workflow, /node scripts\/verify-release-tag\.mjs deploy\/helm\/woms\/values\.yaml/);
  assert.ok(workflow.indexOf("Verify Helm values use the latest released tag") < workflow.indexOf("Get GKE credentials"));
  assert.match(workflow, /get statefulset argocd-application-controller/);
  assert.match(workflow, /patch application "\$ARGOCD_APP"/);
  assert.match(workflow, /"revision": "\$\{\{ steps\.release\.outputs\.release_tag \}\}"/);
  assert.match(workflow, /EXPECTED_ARGOCD_REVISION/);
});

test("Release tag verifier fails closed on stale image tags", () => {
  assert.match(releaseVerifier, /Missing image tag/);
  assert.match(releaseVerifier, /Helm image tags do not match/);
  assert.match(releaseVerifier, /return 1;/);
  assert.match(releaseVerifier, /process\.exitCode = runCli\(process\.argv\)/);
});

test("ArgoCD verifier requires a completed healthy sync at the expected revision", () => {
  assert.match(argocdVerifier, /operationState\.phase/);
  assert.match(argocdVerifier, /Synced/);
  assert.match(argocdVerifier, /Healthy/);
  assert.match(argocdVerifier, /EXPECTED_ARGOCD_REVISION/);
  assert.match(argocdVerifier, /Timed out waiting for ArgoCD application/);
});
