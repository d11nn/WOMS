import test from "node:test";
import assert from "node:assert/strict";
import { parseImageTags, runCli, verifyReleaseTag } from "./verify-release-tag.mjs";

const matchingValues = `
api:
  image:
    repository: woms-api
    tag: v0.1.75
worker:
  image:
    repository: woms-scheduler-worker
    tag: v0.1.75
web:
  image:
    repository: woms-web
    tag: v0.1.75
`;

test("parses application image tags from Helm values", () => {
  assert.deepEqual(parseImageTags(matchingValues), {
    api: "v0.1.75",
    worker: "v0.1.75",
    web: "v0.1.75",
  });
});

test("accepts values only when every deployed image uses the expected release tag", () => {
  assert.deepEqual(verifyReleaseTag(matchingValues, "v0.1.75"), {
    api: "v0.1.75",
    worker: "v0.1.75",
    web: "v0.1.75",
  });
});

test("rejects stale image tags before ArgoCD sync can run", () => {
  const staleValues = matchingValues.replace("web:\n  image:\n    repository: woms-web\n    tag: v0.1.75", "web:\n  image:\n    repository: woms-web\n    tag: v0.1.74");
  assert.throws(() => verifyReleaseTag(staleValues, "v0.1.75"), /web=v0\.1\.74/);
});

test("rejects missing application image tags", () => {
  assert.throws(() => verifyReleaseTag("api:\n  image:\n    tag: v0.1.75\n", "v0.1.75"), /Missing image tag/);
});

test("ignores non-image keys inside an application section", () => {
  const values = `
api:
  image:
    tag: v0.1.75
  env:
    tag: not-an-image-tag
worker:
  image:
    tag: v0.1.75
web:
  image:
    tag: v0.1.75
`;
  assert.equal(parseImageTags(values).api, "v0.1.75");
});

test("CLI helper reports success without exiting the process", () => {
  const messages = [];
  const code = runCli(["node", "verify-release-tag.mjs", "values.yaml", "v0.1.75"], {
    readFile: () => matchingValues,
    log: (message) => messages.push(message),
  });
  assert.equal(code, 0);
  assert.match(messages[0], /v0\.1\.75/);
});

test("CLI helper reports usage errors", () => {
  const errors = [];
  const code = runCli(["node", "verify-release-tag.mjs"], {
    error: (message) => errors.push(message),
  });
  assert.equal(code, 2);
  assert.match(errors[0], /Usage:/);
});

test("CLI helper reports stale tag errors", () => {
  const errors = [];
  const code = runCli(["node", "verify-release-tag.mjs", "values.yaml", "v0.1.76"], {
    readFile: () => matchingValues,
    error: (message) => errors.push(message),
  });
  assert.equal(code, 1);
  assert.match(errors[0], /do not match v0\.1\.76/);
});
