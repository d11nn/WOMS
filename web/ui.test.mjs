import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import {
  compareOrderIds,
  compareNatural,
  defaultLine,
  conflictExplanation,
  customerFilterValues,
  dateKeyInTimeZone,
  escapeHtml,
  exactFilterOrders,
  filtersForCreatedOrder,
  groupAllocationsByDate,
  isChildOrderId,
  isFutureDateKey,
  lineScopedOrders,
  mergePreviewCalendarAllocations,
  matchesOrder,
  monthGrid,
  priorityLabel,
  sortOrdersForWorkstation,
  statusClass,
  statusCounts,
  tomorrowDateKey,
  uniqueValues,
  unacceptableDueDateMessage,
  waterlineMetrics,
} from "./ui.js";

function sharedNginxServerConfig(config) {
  return config
    .replace(/^[^\S\n]*resolver \$\{NGINX_RESOLVER\} valid=10s ipv6=off;\n/m, "")
    .replace(/^[^\S\n]*set \$api_upstream http:\/\/\$\{API_UPSTREAM\};\n/m, "")
    .replace("proxy_pass $api_upstream;", "proxy_pass http://${API_UPSTREAM};");
}

test("preview copy uses state-specific titles instead of mixed conflict/allocation wording", () => {
  const html = readFileSync(new URL("./index.html", import.meta.url), "utf8");
  const app = readFileSync(new URL("./app.js", import.meta.url), "utf8");
  assert.equal(html.includes("衝突與分配"), false);
  assert.equal(app.includes("衝突與分配"), false);
  assert.equal(app.includes("衝突處理"), true);
  assert.equal(app.includes("訂單分配預覽"), true);
});

test("front-end visible HPA status labels are zh-TW", () => {
  const app = readFileSync(new URL("./app.js", import.meta.url), "utf8");
  const html = readFileSync(new URL("./index.html", import.meta.url), "utf8");
  assert.equal(app.includes(">Jobs "), false);
  assert.equal(app.includes(">queued "), false);
  assert.equal(app.includes(">running "), false);
  assert.equal(app.includes(">completed "), false);
  assert.equal(app.includes(">failed "), false);
  assert.equal(app.includes(">cancelled "), false);
  assert.equal(app.includes("Kafka topic"), false);
  assert.equal(app.includes("Consumer group"), false);
  assert.equal(app.includes("Deployment 名稱"), false);
  assert.equal(html.includes(">Orders<"), false);
  assert.equal(html.includes(">Status<"), false);
  assert.equal(html.includes(">Sales Follow-up<"), false);
});

test("scheduler panel starts collapsed behind an explicit toggle", () => {
  const app = readFileSync(new URL("./app.js", import.meta.url), "utf8");
  const html = readFileSync(new URL("./index.html", import.meta.url), "utf8");
  const styles = readFileSync(new URL("./styles.css", import.meta.url), "utf8");
  assert.equal(app.includes("schedulerPanelOpen: false"), true);
  assert.equal(app.includes("setSchedulerPanelOpen(true)"), true);
  assert.equal(html.includes("scheduler-panel-toggle"), true);
  assert.match(styles, /body\[data-role="scheduler"\]\[data-scheduler-panel-open="false"\] \.layout/);
});

test("sales pending order edits are isolated from scheduler pending cards", () => {
  const app = readFileSync(new URL("./app.js", import.meta.url), "utf8");
  const styles = readFileSync(new URL("./styles.css", import.meta.url), "utf8");
  const orderActionStart = app.indexOf("function renderOrderAction(order)");
  const orderActionEnd = app.indexOf("async function handleOrderAction", orderActionStart);
  const orderAction = app.slice(orderActionStart, orderActionEnd);

  assert.match(app, /function canSalesEditPendingOrder\(order\) \{\n\s+return state\.user\?\.role === "sales" && order\?\.status === "待排程" && order\.createdBy === state\.user\.id;/);
  assert.match(orderAction, /data-order-action="toggle-sales-pending-edit"/);
  assert.match(orderAction, /aria-expanded="\$\{expanded \? "true" : "false"\}"/);
  assert.match(orderAction, />訂單修改<\/button>/);
  assert.doesNotMatch(orderAction, />\$\{expanded \? "▴" : "▾"\}<\/button>/);
  assert.match(orderAction, /修改：業務修改/);
  assert.match(orderAction, /deleteLabel: "刪除訂單"/);
  assert.match(orderAction, /if \(order\.status === "待排程"\) \{\n\s+return `<span class="row-hint">可拖曳到月曆<\/span>`;/);
  assert.match(styles, /\.sales-pending-toggle\s*\{/);
});

test("order status badges stay on one line in scheduler and sales cards", () => {
  const styles = readFileSync(new URL("./styles.css", import.meta.url), "utf8");
  assert.match(styles, /\.order-card-main div\s*\{[\s\S]*min-width:\s+0;/);
  assert.match(styles, /\.tag\s*\{[\s\S]*flex:\s+0 0 auto;[\s\S]*white-space:\s+nowrap;/);
});

test("sales draft preview calendar can switch pending draft and scheduled allocations", () => {
  const html = readFileSync(new URL("./index.html", import.meta.url), "utf8");
  const app = readFileSync(new URL("./app.js", import.meta.url), "utf8");
  assert.match(html, /id="preview-calendar-mode"[\s\S]*data-preview-calendar-mode="pending"[\s\S]*待排程/);
  assert.match(html, /data-preview-calendar-mode="scheduled"[\s\S]*已排程/);
  assert.match(html, /data-preview-calendar-mode="all"[\s\S]*所有訂單/);
  assert.match(app, /previewCalendarMode:\s+"pending"/);
  assert.match(app, /document\.getElementById\("preview-calendar-mode"\)\.addEventListener\("click"/);
  assert.match(app, /const isSalesDraft = state\.preview\?\.kind === "sales-draft"/);
  assert.match(app, /function previewCalendarAllocationsForMode\(mode, pendingAllocations\)/);
  assert.match(app, /return \[\.\.\.state\.calendarAllocations, \.\.\.pendingAllocations\]/);
  assert.match(app, /const previewAllocations = conflicts\.length > 0 \? \[\] : allocations;/);
  assert.match(app, /const markedPreviewAllocations = markMovedPreviewAllocations\(previewAllocations\)/);
  assert.match(app, /const pendingAllocations = markedPreviewAllocations\.map\(\(allocation\) => \(\{ \.\.\.allocation, preview: true \}\)\)/);
});

test("sales main calendar can switch pending scheduled and all allocations", () => {
  const html = readFileSync(new URL("./index.html", import.meta.url), "utf8");
  const app = readFileSync(new URL("./app.js", import.meta.url), "utf8");
  assert.match(html, /id="main-calendar-mode"[\s\S]*data-calendar-mode="pending"[\s\S]*待排程/);
  assert.match(html, /data-calendar-mode="scheduled"[\s\S]*已排程/);
  assert.match(html, /data-calendar-mode="all"[\s\S]*所有訂單/);
  assert.match(app, /pendingCalendarAllocations:\s+\[\]/);
  assert.match(app, /calendarMode:\s+"scheduled"/);
  assert.match(app, /payload\.pendingAllocations/);
  assert.match(app, /function mainCalendarAllocations\(\)/);
  assert.match(app, /return \[\.\.\.state\.calendarAllocations, \.\.\.state\.pendingCalendarAllocations\]/);
});

test("web nginx proxy preserves API request paths", () => {
  const nginx = readFileSync(new URL("./nginx.conf.template", import.meta.url), "utf8");
  // const composeNginx = readFileSync(new URL("./nginx.compose.conf.template", import.meta.url), "utf8");
  const compose = readFileSync(new URL("../docker-compose.yml", import.meta.url), "utf8");
  // assert.equal(sharedNginxServerConfig(composeNginx), sharedNginxServerConfig(nginx));
  assert.match(nginx, /location \/api\/ \{/);
  assert.match(nginx, /proxy_pass http:\/\/\$\{API_UPSTREAM\};/);
  assert.match(nginx, /resolver \$\{NGINX_RESOLVER\} valid=10s ipv6=off;/);
  assert.doesNotMatch(nginx, /proxy_pass http:\/\/\$\{API_UPSTREAM\}\/api\//);
  assert.doesNotMatch(nginx, /proxy_pass \$api_upstream;/);
  // assert.match(composeNginx, /resolver \${NGINX_RESOLVER} valid=10s ipv6=off;/);
  // assert.match(composeNginx, /set \$api_upstream http:\/\/\$\{API_UPSTREAM\};/);
  // assert.match(composeNginx, /proxy_pass \$api_upstream;/);
  // assert.doesNotMatch(composeNginx, /proxy_pass \$api_upstream\/api\//);
  assert.match(compose, /nginx\.compose\.conf\.template:\/etc\/nginx\/templates\/default\.conf\.template:ro/);
});

test("compose NGINX status allows the separate exporter container", () => {
  const composeNginx = readFileSync(new URL("./nginx.compose.conf.template", import.meta.url), "utf8");
  const nginxStatus = composeNginx.match(/location = \/nginx_status \{[\s\S]*?\n    \}/)?.[0] ?? "";

  assert.match(nginxStatus, /stub_status;/);
  assert.match(nginxStatus, /allow 127\.0\.0\.1;/);
  assert.match(nginxStatus, /allow 172\.16\.0\.0\/12;/);
  assert.doesNotMatch(nginxStatus, /allow 10\.0\.0\.0\/8;/);
  assert.doesNotMatch(nginxStatus, /allow 192\.168\.0\.0\/16;/);
  assert.match(nginxStatus, /deny all;/);
});

test("web Dockerfile keeps non-root NGINX pid in writable tmp", () => {
  const dockerfile = readFileSync(new URL("../Dockerfile.web", import.meta.url), "utf8");

  assert.match(dockerfile, /sed -i -E 's\|pid\[\[:space:\]\]\+\/run\/nginx\.pid;\|pid\s+\/tmp\/nginx\.pid;\|; s\|pid\[\[:space:\]\]\+\/var\/run\/nginx\.pid;\|pid\s+\/tmp\/nginx\.pid;\|'/);
  assert.match(dockerfile, /USER nginx/);
  assert.doesNotMatch(dockerfile, /USER root/);
});

test("web nginx proxies Grafana under the local 8081 web URL", () => {
  const nginx = readFileSync(new URL("./nginx.conf.template", import.meta.url), "utf8");
  const composeNginx = readFileSync(new URL("./nginx.compose.conf.template", import.meta.url), "utf8");
  const compose = readFileSync(new URL("../docker-compose.yml", import.meta.url), "utf8");
  const grafanaLocation = nginx.match(/location \/grafana\/ \{[\s\S]*?\n    \}/)?.[0] ?? "";
  const appLocation = nginx.match(/location \/ \{[\s\S]*?\n    \}/)?.[0] ?? "";

  assert.match(compose, /"8081:8080"/);
  assert.doesNotMatch(compose, /"80:8080"/);
  assert.doesNotMatch(compose, /"9113:9113"/);
  assert.match(compose, /GRAFANA_UPSTREAM:\s+\$\{GRAFANA_UPSTREAM:-grafana:3000\}/);
  assert.match(compose, /GF_AUTH_ANONYMOUS_ENABLED:\s+"false"/);
  assert.doesNotMatch(compose, /GF_AUTH_ANONYMOUS_ENABLED:\s+"true"/);
  assert.match(compose, /GF_SECURITY_ADMIN_USER:\s+\$\{GRAFANA_ADMIN_USER:-admin\}/);
  assert.match(compose, /GF_SECURITY_ADMIN_PASSWORD:\s+"\$\{GRAFANA_ADMIN_PASSWORD:\?Set GRAFANA_ADMIN_PASSWORD before starting Grafana\}"/);
  assert.match(compose, /GF_USERS_ALLOW_SIGN_UP:\s+"false"/);
  assert.match(compose, /GF_SECURITY_ALLOW_EMBEDDING:\s+"false"/);
  assert.match(compose, /GF_SERVER_ROOT_URL:\s+http:\/\/localhost:8081\/grafana\//);
  assert.match(compose, /GF_SERVER_SERVE_FROM_SUB_PATH:\s+"true"/);
  assert.match(nginx, /map \$http_upgrade \$connection_upgrade \{/);
  assert.match(nginx, /location = \/grafana \{[\s\S]*return 301 \/grafana\/;/);
  assert.match(nginx, /location \/grafana\/api\/live\/ \{[\s\S]*proxy_set_header Upgrade \$http_upgrade;[\s\S]*proxy_pass http:\/\/\$\{GRAFANA_UPSTREAM\};/);
  assert.doesNotMatch(grafanaLocation, /rewrite \^\/grafana\/\(\.\*\) \/\$1 break;/);
  assert.doesNotMatch(composeNginx, /rewrite \^\/grafana\/\(\.\*\) \/\$1 break;/);
  assert.match(grafanaLocation, /proxy_pass http:\/\/\$\{GRAFANA_UPSTREAM\};/);
  assert.doesNotMatch(grafanaLocation, /Content-Security-Policy/);
  assert.match(appLocation, /Content-Security-Policy/);
  assert.match(composeNginx, /set \$grafana_upstream http:\/\/\$\{GRAFANA_UPSTREAM\};/);
  assert.match(composeNginx, /proxy_pass \$grafana_upstream;/);
});

test("order cards support pointer fallback drag scheduling", () => {
  const app = readFileSync(new URL("./app.js", import.meta.url), "utf8");
  const styles = readFileSync(new URL("./styles.css", import.meta.url), "utf8");
  assert.match(app, /attachPointerScheduleDrag\(card, order\.id\)/);
  assert.match(app, /attachMouseScheduleDrag\(card, order\.id\)/);
  assert.equal(app.includes('document.elementFromPoint(clientX, clientY)?.closest?.(".calendar-day")'), true);
  assert.match(app, /await scheduleDroppedOrders\(drag\.orderIds, targetDate\)/);
  assert.match(app, /const orderIds = selectedPendingOrderIds\(\)/);
  assert.match(app, /document\.addEventListener\("mousemove", onMouseScheduleDragMove\)/);
  assert.match(styles, /\.order-card\.selectable\s*\{[\s\S]*touch-action:\s+none;/);
});

test("schedule preview uses API currentDate as the confirmation payload source", () => {
  const app = readFileSync(new URL("./app.js", import.meta.url), "utf8");
  assert.doesNotMatch(app, /currentDate:\s*requestData\.currentDate\s*\?\?\s*todayDateInputValue\(\)/);
  assert.match(app, /currentDate:\s*result\.currentDate\s*\?\?\s*payloadData\.currentDate\s*\?\?\s*todayDateInputValue\(\)/);
});

test("main monthly calendar uses only selected calendar data source", () => {
  const app = readFileSync(new URL("./app.js", import.meta.url), "utf8");
  const start = app.indexOf("function renderCalendar()");
  const end = app.indexOf("function renderPreviewSummary()", start);
  const body = app.slice(start, end);
  assert.match(body, /groupAllocationsByDate\(mainCalendarAllocations\(\)\)/);
  assert.match(body, /if \(state\.user\?\.role !== "sales"\) \{\n\s+return state\.calendarAllocations;/);
  assert.doesNotMatch(body, /mergePreviewCalendarAllocations/);
  assert.doesNotMatch(body, /state\.preview\?\.allocations/);
});

test("sales draft conflict copy is separate from scheduler conflict actions", () => {
  const app = readFileSync(new URL("./app.js", import.meta.url), "utf8");
  const salesBranchStart = app.indexOf("if (salesDraft)");
  const schedulerBranchStart = app.indexOf("return `", app.indexOf("}", salesBranchStart));
  const salesBranch = app.slice(salesBranchStart, schedulerBranchStart);
  assert.match(salesBranch, /這張待排程訂單由於新訂單的影響/);
  assert.doesNotMatch(salesBranch, /可在下方選取衝突訂單與可移動訂單，產生最早完成解法/);
  assert.doesNotMatch(salesBranch, /data-preview-action="unselect-conflict-order"/);
  assert.match(app, /可在下方選取衝突訂單與可移動訂單，產生最早完成解法/);
  assert.match(app, /data-preview-action="unselect-conflict-order"/);
});

const order = {
  id: "ORD-0000001",
  customer: "ACME Silicon",
  lineId: "A",
  status: "待排程",
  priority: "high",
};

test("matchesOrder filters by id, customer, line, status, and priority", () => {
  assert.equal(matchesOrder(order, ""), true);
  assert.equal(matchesOrder(order, "acme"), true);
  assert.equal(matchesOrder(order, "ORD-0000001".toLowerCase()), true);
  assert.equal(matchesOrder(order, "待排程"), true);
  assert.equal(matchesOrder(order, "missing"), false);
});

test("statusClass maps WOMS statuses to stable CSS classes", () => {
  assert.equal(statusClass("待排程"), "status-pending");
  assert.equal(statusClass("已排程"), "status-scheduled");
  assert.equal(statusClass("生產中"), "status-running");
  assert.equal(statusClass("已完成"), "status-completed");
  assert.equal(statusClass("需業務處理"), "status-rejected");
  assert.equal(statusClass("已取消"), "status-cancelled");
  assert.equal(statusClass("unknown"), "");
});

test("exactFilterOrders applies OR within fields and AND across fields", () => {
  const orders = [
    { id: "ORD-0000001", customer: "ACME", lineId: "A", status: "待排程", priority: "high" },
    { id: "ORD-0000002", customer: "ACME", lineId: "B", status: "已排程", priority: "low" },
    { id: "ORD-0000003", customer: "Orion", lineId: "A", status: "待排程", priority: "low" },
  ];
  const result = exactFilterOrders(orders, {
    customers: new Set(["ACME"]),
    lines: new Set(["A", "B"]),
    status: "待排程",
    priorities: new Set(),
  });
  assert.deepEqual(result.map((item) => item.id), ["ORD-0000001"]);
});

test("exactFilterOrders treats status as single-select", () => {
  const orders = [
    { id: "ORD-0000001", customer: "ACME", lineId: "A", status: "待排程", priority: "high" },
    { id: "ORD-0000002", customer: "ACME", lineId: "A", status: "已排程", priority: "low" },
  ];
  const result = exactFilterOrders(orders, {
    customers: new Set(),
    lines: new Set(),
    status: "已排程",
    priorities: new Set(),
  });
  assert.deepEqual(result.map((item) => item.id), ["ORD-0000002"]);
});

test("filtersForCreatedOrder clears filters and focuses the created order status", () => {
  const filters = filtersForCreatedOrder({ status: "待排程" });

  assert.equal(filters.status, "待排程");
  assert.deepEqual(Array.from(filters.customers), []);
  assert.deepEqual(Array.from(filters.priorities), []);
});

test("customerFilterValues follows the active exact filters except customer", () => {
  const orders = [
    { id: "ORD-0000001", customer: "TSMC Demo", status: "pending", priority: "high" },
    { id: "ORD-0000002", customer: "ACME", status: "scheduled", priority: "low" },
    { id: "ORD-0000003", customer: "ACME Silicon", status: "pending", priority: "low" },
  ];
  assert.deepEqual(customerFilterValues(orders, {
    customers: new Set(),
    status: "pending",
    priorities: new Set(),
  }), ["ACME Silicon", "TSMC Demo"]);
  assert.deepEqual(customerFilterValues(orders, {
    customers: new Set(["ACME"]),
    status: "scheduled",
    priorities: new Set(["low"]),
  }), ["ACME"]);
});

test("sortOrdersForWorkstation sorts by priority, due date, and natural order number", () => {
  const orders = [
    { id: "ORD-0000010", status: "待排程", dueDate: "2026-04-30", priority: "low" },
    { id: "ORD-0000002", status: "已排程", dueDate: "2026-05-04", priority: "low" },
    { id: "ORD-0000007", status: "待排程", dueDate: "2026-04-30", priority: "low" },
    { id: "ORD-0000001", status: "已完成", dueDate: "2026-04-29", priority: "high" },
    { id: "ORD-0000006", status: "待排程", dueDate: "2026-04-30", priority: "low" },
  ];
  assert.deepEqual(sortOrdersForWorkstation(orders).map((item) => item.id), ["ORD-0000001", "ORD-0000006", "ORD-0000007", "ORD-0000010", "ORD-0000002"]);
});

test("sortOrdersForWorkstation keeps trailing-number ordering and non-numeric fallback", () => {
  const orders = [
    { id: "ORD-B", dueDate: "2026-04-30", priority: "low" },
    { id: "ORD-10", dueDate: "2026-04-30", priority: "low" },
    { id: "ORD-A", dueDate: "2026-04-30", priority: "low" },
    { id: "ORD-2", dueDate: "2026-04-30", priority: "low" },
  ];
  assert.deepEqual(sortOrdersForWorkstation(orders).map((item) => item.id), ["ORD-2", "ORD-10", "ORD-A", "ORD-B"]);
});

test("compareOrderIds avoids sorting elements alphabetically and sorts naturally", () => {
  const ids = ["ORD-10", "ORD-2", "ORD-B", "ORD-A"];
  assert.deepEqual([...ids].sort(compareOrderIds), ["ORD-2", "ORD-10", "ORD-A", "ORD-B"]);
});

test("compareOrderIds handles parent and child remainder order IDs correctly", () => {
  const ids = ["ORD-0000002-1", "ORD-0000001-2", "ORD-0000001", "ORD-0000001-1"];
  assert.deepEqual(
    [...ids].sort(compareOrderIds),
    ["ORD-0000001", "ORD-0000001-1", "ORD-0000001-2", "ORD-0000002-1"]
  );
});

test("compareOrderIds avoids equivalence collision for parents and child suffixes of 0", () => {
  const ids = ["ORD-0000001-0", "ORD-0000001"];
  assert.deepEqual(
    [...ids].sort(compareOrderIds),
    ["ORD-0000001", "ORD-0000001-0"]
  );
});

test("compareOrderIds handles parent and child remainder order IDs with hyphens in parent ID correctly", () => {
  const ids = ["ORD-PROD-12-1", "ORD-PROD-2", "ORD-PROD-12"];
  assert.deepEqual(
    [...ids].sort(compareOrderIds),
    ["ORD-PROD-2", "ORD-PROD-12", "ORD-PROD-12-1"]
  );
});

test("compareNatural sorts elements naturally rather than alphabetically", () => {
  const lines = ["Line 10", "Line 2", "Line B", "Line A"];
  assert.deepEqual([...lines].sort(compareNatural), ["Line 2", "Line 10", "Line A", "Line B"]);
});

test("isChildOrderId accepts only ORD parent ids with numeric child suffixes", () => {
  assert.equal(isChildOrderId("ORD-ABC-1"), true);
  assert.equal(isChildOrderId("ORD-0000001-0002"), true);
  assert.equal(isChildOrderId("ORD--1"), false);
  assert.equal(isChildOrderId("ORD-ABC-"), false);
  assert.equal(isChildOrderId("ORD-ABC-X"), false);
  assert.equal(isChildOrderId("ORD-ABC-1-X"), false);
  assert.equal(isChildOrderId("ORD-ABC-1X"), false);
  assert.equal(isChildOrderId("ORD-ABC-DEF-1"), false);
  assert.equal(isChildOrderId("PO-ABC-1"), false);
});

test("uniqueValues and statusCounts provide sidebar/filter data", () => {
  const orders = [
    { customer: "ACME", status: "待排程" },
    { customer: "ACME", status: "已完成" },
    { customer: "Orion", status: "待排程" },
  ];
  assert.deepEqual(uniqueValues(orders, "customer"), ["ACME", "Orion"]);
  assert.deepEqual(statusCounts(orders), {
    "待排程": 2,
    "已排程": 0,
    "生產中": 0,
    "已完成": 1,
    "需業務處理": 0,
    "已取消": 0,
  });
});

test("defaultLine chooses the lexicographically lowest production line", () => {
  assert.equal(defaultLine(["C", "A", "B"]), "A");
});

test("lineScopedOrders limits status counts and tables to the selected line", () => {
  const orders = [
    { id: "ORD-0000001", lineId: "A", status: "待排程" },
    { id: "ORD-0000002", lineId: "B", status: "已排程" },
    { id: "ORD-0000003", lineId: "A", status: "已完成" },
  ];
  const scoped = lineScopedOrders(orders, "A");
  assert.deepEqual(scoped.map((item) => item.id), ["ORD-0000001", "ORD-0000003"]);
  assert.deepEqual(statusCounts(scoped), {
    "待排程": 1,
    "已排程": 0,
    "生產中": 0,
    "已完成": 1,
    "需業務處理": 0,
    "已取消": 0,
  });
});

test("monthGrid builds a stable six-week calendar grid", () => {
  const days = monthGrid(2026, 4);
  assert.equal(days.length, 42);
  assert.equal(days[0].key, "2026-04-26");
  assert.equal(days.some((day) => day.key === "2026-05-01" && day.inMonth), true);
});

test("groupAllocationsByDate groups calendar allocations by ISO date", () => {
  const groups = groupAllocationsByDate([
    { orderId: "ORD-0000001", date: "2026-05-02T00:00:00Z" },
    { orderId: "ORD-0000002", date: "2026-05-02T00:00:00Z" },
    { orderId: "ORD-0000003", date: "2026-05-03T00:00:00Z" },
  ]);
  assert.deepEqual(groups["2026-05-02"].map((item) => item.orderId), ["ORD-0000001", "ORD-0000002"]);
  assert.deepEqual(groups["2026-05-03"].map((item) => item.orderId), ["ORD-0000003"]);
});

test("mergePreviewCalendarAllocations replaces touched orders with preview entries", () => {
  const calendar = [
    { orderId: "ORD-0000001", date: "2026-05-15", quantity: 2500, status: "已排程" },
    { orderId: "ORD-0000002", date: "2026-05-15", quantity: 2500, status: "已排程" },
  ];
  const preview = [
    { orderId: "ORD-0000001", date: "2026-05-16", quantity: 2500 },
    { orderId: "ORD-0000003", date: "2026-05-15", quantity: 2500 },
  ];
  const merged = mergePreviewCalendarAllocations(preview, calendar, ["ORD-0000002"]);

  assert.equal(merged.some((item) => item.orderId === "ORD-0000001" && item.date === "2026-05-15"), false);
  assert.equal(merged.some((item) => item.orderId === "ORD-0000002"), false);
  assert.equal(merged.filter((item) => item.preview).length, 2);
});

test("sales due date helpers allow only tomorrow or later", () => {
  assert.equal(isFutureDateKey("2026-04-29", "2026-04-30"), false);
  assert.equal(isFutureDateKey("2026-04-30", "2026-04-30"), false);
  assert.equal(isFutureDateKey("2026-05-01", "2026-04-30"), true);
  assert.equal(tomorrowDateKey("2026-04-30"), "2026-05-01");
  assert.equal(unacceptableDueDateMessage, "無法被接受的交期");
});

test("tomorrowDateKey crosses month year and leap-year boundaries", () => {
  assert.equal(tomorrowDateKey("2026-01-31"), "2026-02-01");
  assert.equal(tomorrowDateKey("2026-12-31"), "2027-01-01");
  assert.equal(tomorrowDateKey("2028-02-28"), "2028-02-29");
});

test("dateKeyInTimeZone returns the plant-local calendar date", () => {
  const now = new Date("2026-05-04T16:30:00Z");
  assert.equal(dateKeyInTimeZone(now, "Asia/Taipei"), "2026-05-05");
  assert.equal(dateKeyInTimeZone(now, "America/New_York"), "2026-05-04");
});

test("dateKeyInTimeZone rejects invalid dates and falls back for invalid time zones", () => {
  assert.equal(dateKeyInTimeZone("not-a-date", "Asia/Taipei"), "");
  assert.equal(dateKeyInTimeZone(new Date("2026-05-04T16:30:00Z"), "Invalid/Zone"), "2026-05-04");
});

test("waterlineMetrics summarizes daily capacity usage", () => {
  const metrics = waterlineMetrics([
    { quantity: 1800 },
    { quantity: 700 },
  ]);
  assert.equal(metrics.total, 2500);
  assert.equal(metrics.capacity, 10000);
  assert.equal(metrics.remaining, 7500);
  assert.equal(metrics.overloaded, false);
  assert.equal(metrics.remainingPercent, 75);
  assert.equal(metrics.percent, 25);
  assert.equal(metrics.tone, "safe");
  assert.match(metrics.color, /^hsl\(\d+ 88% 48%\)$/);

  const full = waterlineMetrics([{ quantity: 12000 }]);
  assert.equal(full.total, 12000);
  assert.equal(full.remaining, 0);
  assert.equal(full.overloaded, true);
  assert.equal(full.remainingPercent, 0);
  assert.equal(full.percent, 100);
  assert.equal(full.tone, "danger");
  assert.equal(full.color, "hsl(0 88% 48%)");

  const warning = waterlineMetrics([{ quantity: 8000 }]);
  assert.equal(warning.tone, "warning");
});

test("waterlineMetrics handles zero and negative capacity without division drift", () => {
  const zeroCapacity = waterlineMetrics([{ quantity: 500 }], 0);
  assert.equal(zeroCapacity.capacity, 0);
  assert.equal(zeroCapacity.remaining, 0);
  assert.equal(zeroCapacity.overloaded, true);
  assert.equal(zeroCapacity.percent, 0);
  assert.equal(zeroCapacity.remainingPercent, 0);
  assert.equal(zeroCapacity.tone, "safe");
  assert.equal(zeroCapacity.color, "hsl(210 88% 48%)");

  const negativeCapacity = waterlineMetrics([{ quantity: 500 }], -100);
  assert.equal(negativeCapacity.capacity, -100);
  assert.equal(negativeCapacity.remaining, 0);
  assert.equal(negativeCapacity.overloaded, true);
  assert.equal(negativeCapacity.percent, 0);
  assert.equal(negativeCapacity.remainingPercent, 0);
});

test("waterlineMetrics exposes safe warning danger and clamped colors", () => {
  const safe = waterlineMetrics([{ quantity: 0 }], 10000);
  assert.equal(safe.tone, "safe");
  assert.equal(safe.color, "hsl(210 88% 48%)");

  const warning = waterlineMetrics([{ quantity: 7000 }], 10000);
  assert.equal(warning.tone, "warning");
  assert.equal(warning.color, "hsl(54 88% 48%)");

  const danger = waterlineMetrics([{ quantity: 9000 }], 10000);
  assert.equal(danger.tone, "danger");
  assert.equal(danger.color, "hsl(16 88% 48%)");

  const overloaded = waterlineMetrics([{ quantity: 15000 }], 10000);
  assert.equal(overloaded.tone, "danger");
  assert.equal(overloaded.color, "hsl(0 88% 48%)");

  const negativeTotal = waterlineMetrics([{ quantity: -100 }], 10000);
  assert.equal(negativeTotal.tone, "safe");
  assert.equal(negativeTotal.color, "hsl(210 88% 48%)");
});

test("conflictExplanation gives actionable guidance", () => {
  assert.match(conflictExplanation({ reason: "capacity cannot satisfy order before due date" }), /提前開始/);
  assert.match(conflictExplanation({ reason: "existing allocations require manual review or reschedule" }), /人工強制介入/);
  assert.match(conflictExplanation({ reason: "unknown" }), /檢查產能/);
});

test("priorityLabel returns zh-TW display labels", () => {
  assert.equal(priorityLabel("high"), "高");
  assert.equal(priorityLabel("low"), "低");
});

test("escapeHtml prevents HTML injection in table rendering", () => {
  assert.equal(escapeHtml(`<script>"x"&'</script>`), "&lt;script&gt;&quot;x&quot;&amp;&#039;&lt;/script&gt;");
});
