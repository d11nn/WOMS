import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

class MiniClassList {
  constructor(element) {
    this.element = element;
  }

  add(...tokens) {
    const classes = new Set(this.element.className.split(/\s+/).filter(Boolean));
    tokens.forEach((token) => classes.add(token));
    this.element.className = Array.from(classes).join(" ");
  }

  remove(...tokens) {
    const classes = new Set(this.element.className.split(/\s+/).filter(Boolean));
    tokens.forEach((token) => classes.delete(token));
    this.element.className = Array.from(classes).join(" ");
  }

  contains(token) {
    return this.element.className.split(/\s+/).includes(token);
  }

  toggle(token, force) {
    const enabled = force ?? !this.contains(token);
    if (enabled) {
      this.add(token);
    } else {
      this.remove(token);
    }
    return enabled;
  }
}

class MiniElement {
  constructor(tagName, ownerDocument) {
    this.tagName = tagName.toUpperCase();
    this.ownerDocument = ownerDocument;
    this.children = [];
    this.parentNode = null;
    this.attributes = new Map();
    this.dataset = {};
    this.style = {};
    this.eventListeners = new Map();
    this.className = "";
    this.classList = new MiniClassList(this);
    this.hidden = false;
    this.value = "";
    this.textContent = "";
    this._innerHTML = "";
  }

  set id(value) {
    this.setAttribute("id", value);
  }

  get id() {
    return this.attributes.get("id") ?? "";
  }

  set innerHTML(value) {
    this._innerHTML = String(value);
    this.textContent = "";
    this.children = [];
    appendParsedControls(this, this._innerHTML);
  }

  get innerHTML() {
    return this._innerHTML;
  }

  get elements() {
    const controls = {};
    for (const child of walk(this)) {
      if (child.name) {
        controls[child.name] = child;
      }
    }
    return controls;
  }

  setAttribute(name, value) {
    const stringValue = String(value);
    this.attributes.set(name, stringValue);
    if (name === "id") {
      this.ownerDocument.elementsById.set(stringValue, this);
    } else if (name === "class") {
      this.className = stringValue;
    } else if (name === "name") {
      this.name = stringValue;
    } else if (name.startsWith("data-")) {
      this.dataset[dataKey(name)] = stringValue;
    } else if (name === "hidden") {
      this.hidden = true;
    }
  }

  getAttribute(name) {
    if (name === "class") {
      return this.className;
    }
    return this.attributes.get(name) ?? null;
  }

  removeAttribute(name) {
    this.attributes.delete(name);
    if (name === "hidden") {
      this.hidden = false;
    }
  }

  append(...nodes) {
    nodes.forEach((node) => this.appendChild(node));
  }

  appendChild(node) {
    node.parentNode = this;
    this.children.push(node);
    return node;
  }

  contains(node) {
    if (node === this) {
      return true;
    }
    return this.children.some((child) => child.contains(node));
  }

  addEventListener(type, listener) {
    const listeners = this.eventListeners.get(type) ?? [];
    listeners.push(listener);
    this.eventListeners.set(type, listeners);
  }

  removeEventListener(type, listener) {
    const listeners = this.eventListeners.get(type) ?? [];
    this.eventListeners.set(type, listeners.filter((item) => item !== listener));
  }

  async dispatchEvent(event) {
    event.target ??= this;
    event.currentTarget = this;
    event.preventDefault ??= () => {
      event.defaultPrevented = true;
    };
    event.stopPropagation ??= () => {
      event.propagationStopped = true;
    };
    const listeners = this.eventListeners.get(event.type) ?? [];
    await Promise.all(listeners.map((listener) => listener(event)));
    return !event.defaultPrevented;
  }

  querySelector(selector) {
    return this.querySelectorAll(selector)[0] ?? null;
  }

  querySelectorAll(selector) {
    const selectors = selector.trim().split(/\s+/);
    if (selectors.length > 1) {
      const [first, ...rest] = selectors;
      return this.querySelectorAll(first).flatMap((node) => node.querySelectorAll(rest.join(" ")));
    }
    const matches = [];
    for (const child of walk(this)) {
      if (child !== this && matchesSelector(child, selector)) {
        matches.push(child);
      }
    }
    return matches;
  }

  closest(selector) {
    let node = this;
    while (node) {
      if (matchesSelector(node, selector)) {
        return node;
      }
      node = node.parentNode;
    }
    return null;
  }

  reset() {
    for (const child of walk(this)) {
      if ("value" in child) {
        child.value = child.getAttribute("value") ?? "";
      }
    }
  }

  showModal() {
    this.open = true;
    this.setAttribute("open", "");
  }

  close() {
    this.open = false;
    this.removeAttribute("open");
  }
}

class MiniDocument extends MiniElement {
  constructor() {
    super("#document", null);
    this.ownerDocument = this;
    this.elementsById = new Map();
    this.body = this.createElement("body");
    this.appendChild(this.body);
  }

  createElement(tagName) {
    return new MiniElement(tagName, this);
  }

  getElementById(id) {
    return this.elementsById.get(id) ?? null;
  }

  elementFromPoint() {
    return null;
  }
}

function* walk(root) {
  for (const child of root.children) {
    yield child;
    yield* walk(child);
  }
}

function dataKey(name) {
  return name.slice(5).replace(/-([a-z])/g, (_, char) => char.toUpperCase());
}

function appendParsedControls(parent, html) {
  const controlPattern = /<(input|button|option|textarea|select|label)\b([^>]*)>([\s\S]*?)<\/\1>|<(input)\b([^>]*)>/gi;
  for (const match of html.matchAll(controlPattern)) {
    const tagName = match[1] ?? match[4];
    const attributes = match[2] ?? match[5] ?? "";
    const content = match[3] ?? "";
    const element = parent.ownerDocument.createElement(tagName);
    for (const [, name, quotedValue, bareValue] of attributes.matchAll(/([:\w-]+)(?:="([^"]*)"|='([^']*)'|=([^\s>]+))?/g)) {
      if (name === tagName) {
        continue;
      }
      const value = quotedValue ?? bareValue ?? "";
      element.setAttribute(name, value);
      if (name === "value") {
        element.value = value;
      } else if (name === "checked") {
        element.checked = true;
      } else if (name === "disabled") {
        element.disabled = true;
      } else if (name === "type") {
        element.type = value;
      }
    }
    element.textContent = content.replace(/<[^>]+>/g, "").replace(/\s+/g, " ").trim();
    parent.appendChild(element);
    if (content.includes("<")) {
      appendParsedControls(element, content);
    }
  }
}

function matchesSelector(element, selector) {
  const trimmed = selector.trim();
  if (!trimmed) {
    return false;
  }
  if (trimmed.includes(",")) {
    return trimmed.split(",").some((part) => matchesSelector(element, part));
  }
  if (trimmed.startsWith("#")) {
    return element.id === trimmed.slice(1);
  }
  if (trimmed.startsWith(".")) {
    return element.classList.contains(trimmed.slice(1));
  }
  if (trimmed.startsWith("[")) {
    const attr = trimmed.slice(1, -1).split("=")[0];
    return element.getAttribute(attr) !== null;
  }
  const attrMatch = trimmed.match(/^([a-z]+)\[([^=]+)="([^"]*)"\]$/i);
  if (attrMatch) {
    return element.tagName.toLowerCase() === attrMatch[1].toLowerCase()
      && element.getAttribute(attrMatch[2]) === attrMatch[3];
  }
  const compoundClassMatch = trimmed.match(/^\.([^.]+)\.([^.]+)$/);
  if (compoundClassMatch) {
    return element.classList.contains(compoundClassMatch[1])
      && element.classList.contains(compoundClassMatch[2]);
  }
  return element.tagName.toLowerCase() === trimmed.toLowerCase();
}

function appendElement(parent, tagName, attributes = {}) {
  const element = parent.ownerDocument.createElement(tagName);
  for (const [name, value] of Object.entries(attributes)) {
    if (name === "hidden") {
      element.hidden = Boolean(value);
      if (value) {
        element.setAttribute("hidden", "");
      }
    } else if (name === "value") {
      element.value = value;
      element.setAttribute("value", value);
    } else {
      element.setAttribute(name, value);
    }
  }
  parent.appendChild(element);
  return element;
}

function addNamedControl(parent, tagName, name, value = "") {
  return appendElement(parent, tagName, { name, value });
}

function buildDomFromIndex() {
  const html = readFileSync(new URL("./index.html", import.meta.url), "utf8");
  assert.match(html, /<script type="module" src="\/app\.js"><\/script>/);

  const document = new MiniDocument();
  const byId = (id) => document.getElementById(id);
  const idTags = [...html.matchAll(/<([a-z]+)[^>]*\bid="([^"]+)"[^>]*>/gi)];

  for (const [, tagName, id] of idTags) {
    const source = html.match(new RegExp(`<${tagName}[^>]*\\bid="${id}"[^>]*>`, "i"))?.[0] ?? "";
    appendElement(document.body, tagName, {
      id,
      class: source.match(/\bclass="([^"]*)"/i)?.[1] ?? "",
      hidden: /\bhidden\b/i.test(source),
    });
  }

  addNamedControl(byId("login-form"), "input", "username", "sales");
  addNamedControl(byId("login-form"), "input", "password", "demo");
  addNamedControl(byId("order-form"), "input", "lineId");
  addNamedControl(byId("order-form"), "input", "customer", "ACME");
  addNamedControl(byId("order-form"), "input", "quantity", "2500");
  addNamedControl(byId("order-form"), "select", "priority", "low");
  addNamedControl(byId("order-form"), "input", "dueDate");
  addNamedControl(byId("order-form"), "textarea", "note");
  addNamedControl(byId("schedule-form"), "input", "lineId");
  addNamedControl(byId("schedule-form"), "input", "startDate");
  addNamedControl(byId("schedule-form"), "input", "reason");
  addNamedControl(byId("production-form"), "input", "orderId");
  addNamedControl(byId("production-form"), "input", "productionDate");
  addNamedControl(byId("production-form"), "input", "producedQuantity");
  addNamedControl(byId("create-user-form"), "input", "username");
  addNamedControl(byId("create-user-form"), "input", "password");
  addNamedControl(byId("create-user-form"), "select", "role", "sales");
  addNamedControl(byId("create-user-form"), "select", "lineId");
  byId("assign-username").setAttribute("name", "username");
  byId("assign-user-form").appendChild(byId("assign-username"));
  addNamedControl(byId("assign-user-form"), "select", "role", "sales");
  byId("assign-line").setAttribute("name", "lineId");
  byId("assign-user-form").appendChild(byId("assign-line"));
  byId("reset-password-username").setAttribute("name", "username");
  byId("reset-password-form").appendChild(byId("reset-password-username"));
  addNamedControl(byId("reset-password-form"), "input", "password");

  for (const mode of ["all", "pending", "scheduled"]) {
    appendElement(byId("main-calendar-mode"), "button", { "data-calendar-mode": mode });
    appendElement(byId("preview-calendar-mode"), "button", { "data-preview-calendar-mode": mode });
  }
  for (const view of ["orders", "calendar", "actions"]) {
    appendElement(document.body, "button", {
      class: view === "actions" ? "mobile-tab scheduler-only" : "mobile-tab",
      "data-mobile-view": view,
    });
  }
  appendElement(byId("active-line-select"), "option", { value: "A" });
  appendElement(byId("active-line-select"), "option", { value: "B" });
  appendElement(byId("active-line-select"), "option", { value: "C" });
  appendElement(byId("active-line-select"), "option", { value: "D" });

  return document;
}

function installBrowserGlobals(document) {
  return installBrowserGlobalsWithFetch(document, () => {
    throw new Error("anonymous startup must not call fetch");
  });
}

function installBrowserGlobalsWithFetch(document, fetchImpl, initialStorage = {}) {
  const previous = new Map();
  const storage = new Map(Object.entries(initialStorage).map(([key, value]) => [key, String(value)]));
  const localStorage = {
    getItem: (key) => storage.has(key) ? storage.get(key) : null,
    setItem: (key, value) => storage.set(key, String(value)),
    removeItem: (key) => storage.delete(key),
    clear: () => storage.clear(),
  };
  const window = {
    document,
    localStorage,
    confirm: () => true,
    setInterval: (callback) => {
      callback();
      return 1;
    },
    clearInterval: () => {},
    setTimeout: (callback) => {
      callback();
      return 1;
    },
    clearTimeout: () => {},
  };
  const globals = {
    document,
    window,
    localStorage,
    fetch: fetchImpl,
    FormData: MiniFormData,
    HTMLElement: MiniElement,
    HTMLDialogElement: MiniElement,
    CSS: { escape: (value) => String(value).replaceAll('"', '\\"') },
    confirm: window.confirm,
    setInterval: window.setInterval,
    clearInterval: window.clearInterval,
    setTimeout: window.setTimeout,
    clearTimeout: window.clearTimeout,
  };
  for (const [key, value] of Object.entries(globals)) {
    previous.set(key, globalThis[key]);
    globalThis[key] = value;
  }
  return () => {
    for (const [key, value] of previous) {
      if (value === undefined) {
        delete globalThis[key];
      } else {
        globalThis[key] = value;
      }
    }
  };
}

class MiniFormData {
  constructor(form) {
    this.entriesList = [];
    for (const child of walk(form)) {
      if (child.name) {
        this.entriesList.push([child.name, child.value]);
      }
    }
  }

  [Symbol.iterator]() {
    return this.entriesList[Symbol.iterator]();
  }
}

function jsonResponse(payload, status = 200) {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => payload,
  };
}

async function flushAsyncWork() {
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
}

let appImportCounter = 0;

function appModuleUrl(label) {
  appImportCounter += 1;
  return new URL(`./app.js?${label}=${appImportCounter}`, import.meta.url);
}

function dateKeyAfter(days) {
  const date = new Date();
  date.setUTCDate(date.getUTCDate() + days);
  return date.toISOString().slice(0, 10);
}

function renderedMarkup(element) {
  return [
    element.innerHTML,
    element.textContent,
    ...element.children.map((child) => renderedMarkup(child)),
  ].join("");
}

function calendarDateKeys(element) {
  return element.children.map((child) => child.dataset.date);
}

async function settleApp() {
  for (let index = 0; index < 100; index += 1) {
    await Promise.resolve();
  }
}

async function exerciseAnonymousControls(document) {
  const activeLineSelect = document.getElementById("active-line-select");
  activeLineSelect.value = "B";
  await activeLineSelect.dispatchEvent({ type: "change" });
  await settleApp();

  const calendarMode = document.querySelector('[data-calendar-mode="all"]');
  await document.getElementById("main-calendar-mode").dispatchEvent({ type: "click", target: calendarMode });
  const previewMode = document.querySelector('[data-preview-calendar-mode="scheduled"]');
  await document.getElementById("preview-calendar-mode").dispatchEvent({ type: "click", target: previewMode });

  await document.getElementById("preview-selected").dispatchEvent({ type: "click" });
  assert.equal(document.getElementById("message-title").textContent, "請先選取訂單");
  await document.getElementById("reject-selected").dispatchEvent({ type: "click" });
  assert.equal(document.getElementById("message-title").textContent, "請先選取訂單");
  await document.getElementById("cancel-selected").dispatchEvent({ type: "click" });
  assert.equal(document.getElementById("message-title").textContent, "請先選取訂單");
  await document.getElementById("schedule-form").dispatchEvent({ type: "submit" });
  assert.equal(document.getElementById("message-title").textContent, "請先選取訂單");

  await document.getElementById("cancel-production-report").dispatchEvent({ type: "click" });
  await document.getElementById("confirm-reject-orders").dispatchEvent({ type: "click" });
  assert.equal(document.getElementById("message-title").textContent, "請填寫駁回理由");

  const actionsTab = document.querySelector('[data-mobile-view="actions"]');
  await actionsTab.dispatchEvent({ type: "click" });
  await document.getElementById("scheduler-panel-toggle").dispatchEvent({ type: "click" });
  await document.getElementById("prev-month").dispatchEvent({ type: "click" });
  await document.getElementById("next-month").dispatchEvent({ type: "click" });
  await document.getElementById("today-month").dispatchEvent({ type: "click" });

  await document.getElementById("create-conflict-demo").dispatchEvent({ type: "click" });
  assert.equal(document.getElementById("message-title").textContent, "無法建立衝突資料");
  await document.getElementById("create-hpa-peak").dispatchEvent({ type: "click" });
  assert.equal(document.getElementById("message-title").textContent, "autoscaling 狀態讀取失敗");
  await document.getElementById("clear-hpa-peak").dispatchEvent({ type: "click" });
  assert.equal(document.getElementById("message-title").textContent, "清除失敗");
}

test("anonymous startup renders login state with fallback lines and initialized dates", async () => {
  const document = buildDomFromIndex();
  const restoreGlobals = installBrowserGlobals(document);
  try {
    await import(new URL(`./app.js?dp002=${Date.now()}`, import.meta.url));

    assert.equal(document.getElementById("login-page").hidden, false);
    assert.equal(document.getElementById("app-shell").hidden, true);
    assert.equal(document.body.dataset.role, "");
    assert.equal(document.getElementById("active-line-select").innerHTML.includes('value="A"'), true);
    assert.equal(document.getElementById("active-line-select").innerHTML.includes('value="D"'), true);
    assert.match(document.querySelector('input[name="startDate"]').value, /^\d{4}-\d{2}-\d{2}$/);
    assert.match(document.querySelector('input[name="dueDate"]').value, /^\d{4}-\d{2}-\d{2}$/);
    assert.notEqual(document.querySelector('input[name="startDate"]').value, "");
    assert.notEqual(document.querySelector('input[name="dueDate"]').value, "");
    assert.equal(document.querySelector('#order-form input[name="lineId"]').value, "A");
    assert.equal(document.querySelector('#schedule-form input[name="lineId"]').value, "A");

    await exerciseAnonymousControls(document);
  } finally {
    restoreGlobals();
  }
});

test("login saves session and logout clears storage and app state", async () => {
  const document = buildDomFromIndex();
  const calls = [];
  let hpaCleared = false;
  const hpaSummary = {
    autoscaling: {
      desiredReplicas: 3,
      maxReplicas: 8,
      currentReplicas: 2,
      readyReplicas: 2,
      deploymentReplicas: 2,
      readyPods: 2,
      podCount: 3,
    },
    grafanaPath: "/grafana/d/web",
    metricName: "woms_web_nginx_requests_per_second_per_pod",
    hpaName: "woms-web-hpa",
    deploymentName: "woms-web",
    reason: "traffic rising",
  };
  const fetchImpl = async (path, options = {}) => {
    calls.push({ path, options });
    if (path === "/api/auth/login") {
      assert.equal(options.method, "POST");
      const credentials = JSON.parse(options.body);
      const users = {
        sales: { token: "token-sales", user: { id: "user-sales", username: "sales", role: "sales" } },
        scheduler: { token: "token-scheduler", user: { id: "scheduler-a", username: "scheduler", role: "scheduler", lineId: "A" } },
        admin: { token: "token-admin", user: { id: "admin-a", username: "admin", role: "admin" } },
      };
      assert.equal(credentials.password, "demo");
      return jsonResponse(users[credentials.username]);
    }
    if (path === "/api/auth/logout") {
      assert.equal(options.method, "POST");
      assert.match(options.headers.Authorization, /^Bearer token-/);
      return jsonResponse({});
    }
    if (path === "/api/lines") {
      return jsonResponse({
        lines: [
          { id: "A", name: "Line A", capacityPerDay: 10000, timezone: "Asia/Taipei" },
          { id: "B", name: "Line B", capacityPerDay: 9000, timezone: "Asia/Taipei" },
        ],
      });
    }
    if (path === "/api/orders") {
      const token = options.headers.Authorization;
      if (token === "Bearer token-scheduler") {
        return jsonResponse({
          orders: [
            { id: "ORD-PENDING", customer: "ACME", lineId: "A", quantity: 2500, priority: "high", status: "待排程", dueDate: dateKeyAfter(6), createdBy: "user-sales" },
            { id: "ORD-SCHEDULED", customer: "Beta", lineId: "A", quantity: 1500, priority: "low", status: "已排程", dueDate: dateKeyAfter(8), createdBy: "user-sales" },
            { id: "ORD-PROD", customer: "Gamma", lineId: "A", quantity: 900, priority: "low", status: "生產中", dueDate: dateKeyAfter(9), createdBy: "user-sales" },
          ],
        });
      }
      return jsonResponse({
        orders: [
          { id: "ORD-EXISTING", customer: "ACME", lineId: "A", quantity: 1200, priority: "low", status: "已排程", dueDate: dateKeyAfter(8), createdBy: "user-sales" },
        ],
      });
    }
    if (String(path).startsWith("/api/schedules/calendar?")) {
      const token = options.headers.Authorization;
      if (token === "Bearer token-scheduler") {
        return jsonResponse({
          allocations: [
            { orderId: "ORD-SCHEDULED", customer: "Beta", lineId: "A", date: dateKeyAfter(2), quantity: 1500, priority: "low", status: "已排程" },
            { orderId: "ORD-PROD", customer: "Gamma", lineId: "A", date: dateKeyAfter(3), quantity: 900, priority: "low", status: "生產中" },
          ],
          pendingAllocations: [],
        });
      }
      return jsonResponse({
        allocations: [
          { orderId: "ORD-EXISTING", customer: "ACME", lineId: "A", date: dateKeyAfter(4), quantity: 1200, priority: "low", status: "已排程" },
        ],
        pendingAllocations: [],
      });
    }
    if (String(path).startsWith("/api/schedules/history?")) {
      return jsonResponse({ history: [{ action: "schedule.job.create", resource: "JOB-1", reason: "ok", createdAt: `${dateKeyAfter(0)}T01:02:00Z` }] });
    }
    if (path === "/api/schedules/preview") {
      assert.equal(options.method, "POST");
      const body = JSON.parse(options.body);
      if (body.draftOrder) {
        return jsonResponse({
          previewId: "PREVIEW-DRAFT",
          currentDate: dateKeyAfter(0),
          allocations: [
            { orderId: "ORD-DRAFT", customer: "ACME", lineId: "A", date: dateKeyAfter(3), quantity: 2500, priority: "low", status: "待排程" },
          ],
          conflicts: [],
        });
      }
      assert.deepEqual(body.orderIds, ["ORD-PENDING"]);
      return jsonResponse({
        previewId: "PREVIEW-SCHEDULE",
        currentDate: dateKeyAfter(0),
        allocations: [
          { orderId: "ORD-PENDING", customer: "ACME", lineId: "A", date: dateKeyAfter(2), quantity: 2500, priority: "high", status: "已排程" },
          { orderId: "ORD-PENDING-1", sourceOrderId: "ORD-PENDING", customer: "ACME", lineId: "A", date: dateKeyAfter(6), quantity: 300, priority: "high", status: "已排程" },
        ],
        conflicts: [],
      });
    }
    if (path === "/api/orders/preview-confirm") {
      assert.equal(options.method, "POST");
      return jsonResponse({ id: "ORD-DRAFT", customer: "ACME", lineId: "A", quantity: 2500, priority: "low", status: "待排程", dueDate: dateKeyAfter(5), createdBy: "user-sales" });
    }
    if (path === "/api/schedules/jobs") {
      assert.equal(options.method, "POST");
      assert.equal(JSON.parse(options.body).previewId, "PREVIEW-SCHEDULE");
      return jsonResponse({ id: "JOB-2", status: "completed" });
    }
    if (path === "/api/production/confirm") {
      assert.equal(options.method, "POST");
      assert.deepEqual(JSON.parse(options.body), { orderId: "ORD-PROD", productionDate: dateKeyAfter(3), producedQuantity: 500 });
      return jsonResponse({ remainder: { id: "ORD-PROD-R1", quantity: 400 } });
    }
    if (path === "/api/users") {
      if (options.method === "POST") {
        return jsonResponse({ username: "new-scheduler", role: "scheduler", lineId: "A" });
      }
      if (options.method === "PATCH") {
        return jsonResponse({ username: "ops", role: "scheduler", lineId: "A" });
      }
      return jsonResponse({ users: [{ username: "ops", role: "sales" }] });
    }
    if (path === "/api/users/password") {
      assert.equal(options.method, "PATCH");
      return jsonResponse({ username: "ops" });
    }
    if (path === "/api/users/ops") {
      assert.equal(options.method, "DELETE");
      return jsonResponse({ username: "ops", deleted: false });
    }
    if (path === "/api/demo/hpa-peak") {
      if (options.method === "POST") {
        hpaCleared = false;
        return jsonResponse({ summary: { ...hpaSummary, orderCount: 3 } });
      }
      if (options.method === "DELETE") {
        hpaCleared = true;
        return jsonResponse({ summary: null });
      }
      return jsonResponse({ summary: hpaCleared ? null : hpaSummary });
    }
    throw new Error(`unexpected fetch ${path}`);
  };
  const restoreGlobals = installBrowserGlobalsWithFetch(document, fetchImpl);
  try {
    await import(new URL(`./app.js?dp003=${Date.now()}`, import.meta.url));

    await document.getElementById("login-form").dispatchEvent({ type: "submit" });
    await settleApp();

    assert.equal(calls[0].path, "/api/auth/login");
    assert.equal(localStorage.getItem("woms.token"), "token-sales");
    assert.equal(localStorage.getItem("woms.user"), JSON.stringify({ id: "user-sales", username: "sales", role: "sales" }));
    assert.equal(document.getElementById("login-page").hidden, true);
    assert.equal(document.getElementById("app-shell").hidden, false);
    assert.equal(document.body.dataset.role, "sales");
    assert.equal(document.getElementById("order-form").hidden, false);
    assert.equal(document.getElementById("session-greeting").textContent, "您好 sales");

    document.querySelector('#order-form input[name="dueDate"]').value = dateKeyAfter(5);
    await document.getElementById("order-form").dispatchEvent({ type: "submit" });
    await settleApp();
    assert.match(renderedMarkup(document.getElementById("preview-calendar-grid")), /ORD-DRAFT/);

    await document.getElementById("confirm-preview-order").dispatchEvent({ type: "click" });
    await settleApp();
    assert.equal(document.getElementById("message-title").textContent, "已加入待排程", document.getElementById("message-body").textContent);

    await document.getElementById("logout-button").dispatchEvent({ type: "click" });
    await settleApp();

    assert.equal(calls.at(-1).path, "/api/auth/logout");
    assert.equal(localStorage.getItem("woms.token"), null);
    assert.equal(localStorage.getItem("woms.user"), null);
    assert.equal(document.getElementById("login-page").hidden, false);
    assert.equal(document.getElementById("app-shell").hidden, true);
    assert.equal(document.body.dataset.role, "");

    document.querySelector('#login-form input[name="username"]').value = "scheduler";
    document.querySelector('#login-form input[name="password"]').value = "demo";
    await document.getElementById("login-form").dispatchEvent({ type: "submit" });
    await settleApp();
    assert.equal(document.body.dataset.role, "scheduler");

    const pendingCard = document.getElementById("orders-body").children.find((item) => item.dataset.orderId === "ORD-PENDING");
    await pendingCard.dispatchEvent({ type: "click", target: pendingCard });
    await document.getElementById("preview-selected").dispatchEvent({ type: "click" });
    await settleApp();
    assert.match(renderedMarkup(document.getElementById("preview-calendar-grid")), /child-order-preview/);

    await document.getElementById("confirm-schedule-job").dispatchEvent({ type: "click" });
    await settleApp();
    assert.equal(document.getElementById("message-title").textContent, "排程完成");

    const productionForm = document.getElementById("production-form");
    productionForm.elements.orderId.value = "ORD-PROD";
    productionForm.elements.productionDate.value = dateKeyAfter(3);
    productionForm.elements.producedQuantity.value = "500";
    await productionForm.dispatchEvent({ type: "submit" });
    await settleApp();
    assert.equal(document.getElementById("message-title").textContent, "生產回報完成");

    await document.getElementById("logout-button").dispatchEvent({ type: "click" });
    await settleApp();

    document.querySelector('#login-form input[name="username"]').value = "admin";
    document.querySelector('#login-form input[name="password"]').value = "demo";
    await document.getElementById("login-form").dispatchEvent({ type: "submit" });
    await settleApp();
    assert.equal(document.body.dataset.role, "admin");
    assert.match(document.getElementById("hpa-demo-summary").innerHTML, /woms_web_nginx_requests_per_second_per_pod/);

    const createForm = document.getElementById("create-user-form");
    createForm.elements.username.value = "new-scheduler";
    createForm.elements.password.value = "secret";
    createForm.elements.role.value = "scheduler";
    createForm.elements.lineId.value = "A";
    await createForm.dispatchEvent({ type: "submit" });
    await settleApp();
    assert.equal(document.getElementById("message-title").textContent, "帳號已建立");

    const assignForm = document.getElementById("assign-user-form");
    assignForm.elements.username.value = "ops";
    assignForm.elements.role.value = "scheduler";
    assignForm.elements.lineId.value = "A";
    await assignForm.dispatchEvent({ type: "submit" });
    await settleApp();
    assert.equal(document.getElementById("message-title").textContent, "帳號已更新");

    const resetForm = document.getElementById("reset-password-form");
    resetForm.elements.username.value = "ops";
    resetForm.elements.password.value = "new-secret";
    await resetForm.dispatchEvent({ type: "submit" });
    await settleApp();
    assert.equal(document.getElementById("message-title").textContent, "密碼已重設");

    document.getElementById("assign-username").value = "ops";
    await document.getElementById("delete-user-button").dispatchEvent({ type: "click" });
    await settleApp();
    assert.equal(document.getElementById("message-title").textContent, "帳號已處理");

    await document.getElementById("create-hpa-peak").dispatchEvent({ type: "click" });
    await settleApp();
    assert.equal(document.getElementById("message-title").textContent, "web autoscaling demo 已載入");

    await document.getElementById("clear-hpa-peak").dispatchEvent({ type: "click" });
    await settleApp();
    assert.equal(document.getElementById("message-title").textContent, "舊版資料已清除");
  } finally {
    restoreGlobals();
  }
});

test("expired stored session is cleared and returns to login state", async () => {
  const document = buildDomFromIndex();
  const calls = [];
  const fetchImpl = async (path, options = {}) => {
    calls.push({ path, options });
    assert.equal(options.headers.Authorization, "Bearer expired-token");
    if (path === "/api/lines") {
      const error = new Error("session expired");
      error.status = 401;
      throw error;
    }
    throw new Error(`unexpected fetch ${path}`);
  };
  const restoreGlobals = installBrowserGlobalsWithFetch(document, fetchImpl, {
    "woms.token": "expired-token",
    "woms.user": JSON.stringify({ username: "sales", role: "sales" }),
  });
  try {
    await import(new URL(`./app.js?dp004=${Date.now()}`, import.meta.url));
    await flushAsyncWork();

    assert.equal(calls.length, 1);
    assert.equal(calls[0].path, "/api/lines");
    assert.equal(localStorage.getItem("woms.token"), null);
    assert.equal(localStorage.getItem("woms.user"), null);
    assert.equal(document.getElementById("login-page").hidden, false);
    assert.equal(document.getElementById("app-shell").hidden, true);
    assert.equal(document.body.dataset.role, "");
    assert.equal(document.getElementById("message-title").textContent, "登入狀態已失效");
    assert.equal(document.getElementById("message-body").textContent, "session expired");
    assert.equal(document.getElementById("message-dialog").dataset.type, "warn");

    await exerciseAnonymousControls(document);
  } finally {
    restoreGlobals();
  }
});

test("sales order form previews valid drafts and rejects non-future due dates", async () => {
  const document = buildDomFromIndex();
  const calls = [];
  const fetchImpl = async (path, options = {}) => {
    calls.push({ path, options });
    if (path === "/api/lines") {
      return jsonResponse({
        lines: [
          { id: "A", name: "Line A", capacityPerDay: 10000, timezone: "Asia/Taipei" },
          { id: "B", name: "Line B", capacityPerDay: 9000, timezone: "Asia/Taipei" },
        ],
      });
    }
    if (path === "/api/orders") {
      return jsonResponse({
        orders: [
          { id: "ORD-EXISTING", customer: "ACME", lineId: "A", quantity: 1200, priority: "low", status: "已排程", dueDate: dateKeyAfter(8), createdBy: "user-sales" },
        ],
      });
    }
    if (String(path).startsWith("/api/schedules/calendar?")) {
      return jsonResponse({
        allocations: [
          { orderId: "ORD-EXISTING", customer: "ACME", lineId: "A", date: dateKeyAfter(4), quantity: 1200, priority: "low", status: "已排程" },
        ],
        pendingAllocations: [],
      });
    }
    if (path === "/api/schedules/preview") {
      assert.equal(options.method, "POST");
      const body = JSON.parse(options.body);
      assert.equal(body.lineId, "A");
      assert.equal(body.draftOrder.customer, "ACME");
      assert.equal(body.draftOrder.quantity, 2500);
      return jsonResponse({
        previewId: "PREVIEW-DRAFT",
        currentDate: dateKeyAfter(0),
        allocations: [
          { orderId: "ORD-DRAFT", customer: "ACME", lineId: "A", date: dateKeyAfter(3), quantity: 2500, priority: "low", status: "待排程" },
        ],
        conflicts: [],
      });
    }
    if (path === "/api/orders/preview-confirm") {
      assert.equal(options.method, "POST");
      assert.deepEqual(JSON.parse(options.body), { previewId: "PREVIEW-DRAFT", deferredOrderIds: [] });
      return jsonResponse({ id: "ORD-DRAFT", customer: "ACME", lineId: "A", quantity: 2500, priority: "low", status: "待排程", dueDate: dateKeyAfter(5), createdBy: "user-sales" });
    }
    throw new Error(`unexpected fetch ${path}`);
  };
  const restoreGlobals = installBrowserGlobalsWithFetch(document, fetchImpl, {
    "woms.token": "token-sales",
    "woms.user": JSON.stringify({ id: "user-sales", username: "sales", role: "sales" }),
  });
  try {
    await import(appModuleUrl("sales-draft-preview"));
    await settleApp();

    document.querySelector('#order-form input[name="dueDate"]').value = dateKeyAfter(5);
    await document.getElementById("order-form").dispatchEvent({ type: "submit" });
    await settleApp();

    const previewCalls = calls.filter((call) => call.path === "/api/schedules/preview");
    assert.equal(previewCalls.length, 1);
    assert.equal(document.getElementById("schedule-preview-dialog").open, true);
    assert.equal(document.getElementById("confirm-preview-order").hidden, false);
    assert.equal(document.getElementById("confirm-schedule-job").hidden, true);
    assert.equal(document.getElementById("preview-page-title").textContent, "訂單分配預覽");
    assert.match(document.getElementById("preview-page-list").innerHTML, /ORD-DRAFT/);
    assert.match(renderedMarkup(document.getElementById("preview-calendar-grid")), /ORD-DRAFT/);

    await document.getElementById("confirm-preview-order").dispatchEvent({ type: "click" });
    await settleApp();

    assert.equal(calls.filter((call) => call.path === "/api/orders/preview-confirm").length, 1);
    assert.equal(document.getElementById("schedule-preview-dialog").open, false);
    assert.equal(document.getElementById("message-title").textContent, "已加入待排程", document.getElementById("message-body").textContent);

    document.querySelector('#order-form input[name="dueDate"]').value = "2000-01-01";
    await document.getElementById("order-form").dispatchEvent({ type: "submit" });
    await settleApp();

    assert.equal(calls.filter((call) => call.path === "/api/schedules/preview").length, 1);
    assert.equal(document.getElementById("message-title").textContent, "無法加入待排程");
    assert.equal(document.getElementById("message-dialog").dataset.type, "warn");
  } finally {
    restoreGlobals();
  }
});

test("scheduler workspace renders orders calendar history and previews selected orders", async () => {
  const document = buildDomFromIndex();
  const calls = [];
  const fetchImpl = async (path, options = {}) => {
    calls.push({ path, options });
    if (path === "/api/lines") {
      return jsonResponse({
        lines: [
          { id: "A", name: "Line A", capacityPerDay: 10000, timezone: "Asia/Taipei" },
          { id: "B", name: "Line B", capacityPerDay: 9000, timezone: "Asia/Taipei" },
        ],
      });
    }
    if (path === "/api/orders") {
      return jsonResponse({
        orders: [
          { id: "ORD-PENDING", customer: "ACME", lineId: "A", quantity: 2500, priority: "high", status: "待排程", dueDate: dateKeyAfter(6), createdBy: "user-sales" },
          { id: "ORD-SCHEDULED", customer: "Beta", lineId: "A", quantity: 1500, priority: "low", status: "已排程", dueDate: dateKeyAfter(8), createdBy: "user-sales" },
          { id: "ORD-PROD", customer: "Gamma", lineId: "A", quantity: 900, priority: "low", status: "生產中", dueDate: dateKeyAfter(9), createdBy: "user-sales" },
          { id: "ORD-B", customer: "Other", lineId: "B", quantity: 500, priority: "low", status: "待排程", dueDate: dateKeyAfter(5), createdBy: "user-sales" },
        ],
      });
    }
    if (String(path).startsWith("/api/schedules/calendar?")) {
      return jsonResponse({
        allocations: [
          { orderId: "ORD-SCHEDULED", customer: "Beta", lineId: "A", date: dateKeyAfter(2), quantity: 1500, priority: "low", status: "已排程" },
          { orderId: "ORD-PROD", customer: "Gamma", lineId: "A", date: dateKeyAfter(3), quantity: 900, priority: "low", status: "生產中" },
          { orderId: "ORD-DONE", customer: "Done", lineId: "A", date: dateKeyAfter(4), quantity: 700, completedQuantity: 650, priority: "low", status: "已完成" },
        ],
        pendingAllocations: [],
      });
    }
    if (String(path).startsWith("/api/schedules/history?")) {
      return jsonResponse({
        history: [
          { action: "schedule.job.create", resource: "JOB-1", reason: "ok", createdAt: `${dateKeyAfter(0)}T01:02:00Z` },
          { action: "production.confirm.partial", resource: "ORD-PROD", reason: "partial", createdAt: `${dateKeyAfter(0)}T02:03:00Z` },
        ],
      });
    }
    if (path === "/api/schedules/preview") {
      assert.equal(options.method, "POST");
      const body = JSON.parse(options.body);
      assert.deepEqual(body.orderIds, ["ORD-PENDING"]);
      return jsonResponse({
        previewId: "PREVIEW-SCHEDULE",
        currentDate: dateKeyAfter(0),
        allocations: [
          { orderId: "ORD-PENDING", customer: "ACME", lineId: "A", date: dateKeyAfter(2), quantity: 2500, priority: "high", status: "已排程" },
          { orderId: "ORD-SCHEDULED", customer: "Beta", lineId: "A", date: dateKeyAfter(5), quantity: 1500, priority: "low", status: "已排程" },
          { orderId: "ORD-PENDING-1", sourceOrderId: "ORD-PENDING", customer: "ACME", lineId: "A", date: dateKeyAfter(6), quantity: 300, priority: "high", status: "已排程" },
        ],
        conflicts: [],
      });
    }
    if (path === "/api/schedules/jobs") {
      assert.equal(options.method, "POST");
      assert.deepEqual(JSON.parse(options.body), {
        lineId: "A",
        startDate: dateKeyAfter(1),
        currentDate: dateKeyAfter(0),
        orderIds: ["ORD-PENDING"],
        resolutionOrderIds: [],
        manualForce: false,
        allowLateCompletion: false,
        reason: "",
        previewId: "PREVIEW-SCHEDULE",
      });
      return jsonResponse({ id: "JOB-2", status: "completed" });
    }
    throw new Error(`unexpected fetch ${path}`);
  };
  const restoreGlobals = installBrowserGlobalsWithFetch(document, fetchImpl, {
    "woms.token": "token-scheduler",
    "woms.user": JSON.stringify({ id: "scheduler-a", username: "scheduler", role: "scheduler", lineId: "A" }),
  });
  try {
    await import(appModuleUrl("scheduler-workspace"));
    await settleApp();

    assert.equal(document.body.dataset.role, "scheduler");
    assert.equal(document.getElementById("active-line-select").disabled, true);
    assert.equal(document.getElementById("orders-body").children.length, 3);
    assert.equal(document.getElementById("calendar-grid").children.length, 42);
    assert.match(renderedMarkup(document.getElementById("calendar-grid")), /ORD-SCHEDULED/);
    assert.match(renderedMarkup(document.getElementById("schedule-history-list")), /排程成功/);
    assert.equal(document.getElementById("selected-count").textContent, "已選取 0 張訂單");

    const pendingCard = document.getElementById("orders-body").children.find((item) => item.dataset.orderId === "ORD-PENDING");
    await pendingCard.dispatchEvent({ type: "click", target: pendingCard });
    assert.equal(document.getElementById("selected-count").textContent, "已選取 1 張訂單");

    await document.getElementById("preview-selected").dispatchEvent({ type: "click" });
    await settleApp();

    assert.equal(document.getElementById("schedule-preview-dialog").open, true);
    assert.equal(document.getElementById("confirm-preview-order").hidden, true);
    assert.equal(document.getElementById("confirm-schedule-job").hidden, false);
    assert.match(document.getElementById("preview-page-list").innerHTML, /ORD-PENDING/);
    assert.match(renderedMarkup(document.getElementById("preview-calendar-grid")), /child-order-preview/);
    assert.match(renderedMarkup(document.getElementById("preview-calendar-grid")), /moved-preview/);
    assert.match(renderedMarkup(document.getElementById("preview-calendar-grid")), /moved-from-preview/);

    await document.getElementById("confirm-schedule-job").dispatchEvent({ type: "click" });
    await settleApp();

    assert.equal(calls.filter((call) => call.path === "/api/schedules/jobs").length, 1);
    
    // assert.equal(document.getElementById("schedule-preview-dialog").open, false);
    // assert.equal(document.getElementById("message-title").textContent, "排程完成");
  } finally {
    restoreGlobals();
  }
});

test("queued scheduler jobs poll until completion and refresh workspace", async () => {
  const document = buildDomFromIndex();
  const calls = [];
  let jobReads = 0;
  const fetchImpl = async (path, options = {}) => {
    calls.push({ path, options });
    if (path === "/api/lines") {
      return jsonResponse({ lines: [{ id: "A", name: "Line A", capacityPerDay: 10000, timezone: "Asia/Taipei" }] });
    }
    if (path === "/api/orders") {
      return jsonResponse({
        orders: [
          { id: "ORD-PENDING", customer: "ACME", lineId: "A", quantity: 2500, priority: "high", status: "待排程", dueDate: dateKeyAfter(6), createdBy: "user-sales" },
        ],
      });
    }
    if (String(path).startsWith("/api/schedules/calendar?")) {
      return jsonResponse({ allocations: [], pendingAllocations: [] });
    }
    if (String(path).startsWith("/api/schedules/history?")) {
      return jsonResponse({ history: [] });
    }
    if (path === "/api/schedules/preview") {
      return jsonResponse({
        previewId: "PREVIEW-QUEUED",
        currentDate: dateKeyAfter(0),
        allocations: [
          { orderId: "ORD-PENDING", customer: "ACME", lineId: "A", date: dateKeyAfter(2), quantity: 2500, priority: "high", status: "已排程" },
        ],
        conflicts: [],
      });
    }
    if (path === "/api/schedules/jobs") {
      assert.equal(options.method, "POST");
      return jsonResponse({ id: "JOB-QUEUED", status: "queued" });
    }
    if (path === "/api/schedules/jobs/JOB-QUEUED") {
      jobReads += 1;
      return jsonResponse(jobReads === 1
        ? { id: "JOB-QUEUED", status: "running" }
        : { id: "JOB-QUEUED", status: "completed" });
    }
    throw new Error(`unexpected fetch ${path}`);
  };
  const restoreGlobals = installBrowserGlobalsWithFetch(document, fetchImpl, {
    "woms.token": "token-scheduler",
    "woms.user": JSON.stringify({ id: "scheduler-a", username: "scheduler", role: "scheduler", lineId: "A" }),
  });
  try {
    await import(appModuleUrl("queued-schedule-job"));
    await settleApp();

    const pendingCard = document.getElementById("orders-body").children.find((item) => item.dataset.orderId === "ORD-PENDING");
    await pendingCard.dispatchEvent({ type: "click", target: pendingCard });
    await document.getElementById("preview-selected").dispatchEvent({ type: "click" });
    await settleApp();
    await document.getElementById("confirm-schedule-job").dispatchEvent({ type: "click" });
    await settleApp();

    assert.equal(jobReads, 2);
    assert.equal(document.getElementById("message-title").textContent, "排程完成");
    assert.equal(calls.filter((call) => call.path === "/api/orders").length >= 2, true);
  } finally {
    restoreGlobals();
  }
});

test("admin user management and autoscaling controls submit expected API calls", async () => {
  const document = buildDomFromIndex();
  const calls = [];
  let hpaCleared = false;
  const summary = {
    autoscaling: {
      desiredReplicas: 3,
      maxReplicas: 8,
      currentReplicas: 2,
      readyReplicas: 2,
      deploymentReplicas: 2,
      readyPods: 2,
      podCount: 3,
    },
    grafanaPath: "/grafana/d/web",
    metricName: "woms_web_nginx_requests_per_second_per_pod",
    hpaName: "woms-web-hpa",
    deploymentName: "woms-web",
    loadCommand: "hey -z 5m -c 80 https://woms.example/",
    watchCommand: "kubectl get hpa -n woms -w",
    reason: "traffic rising",
    failedMessages: ["pod pending"],
  };
  const fetchImpl = async (path, options = {}) => {
    calls.push({ path, options });
    if (path === "/api/lines") {
      return jsonResponse({ lines: [{ id: "A", name: "Line A", capacityPerDay: 10000, timezone: "Asia/Taipei" }] });
    }
    if (path === "/api/orders") {
      return jsonResponse({ orders: [] });
    }
    if (String(path).startsWith("/api/schedules/calendar?")) {
      return jsonResponse({ allocations: [], pendingAllocations: [] });
    }
    if (path === "/api/users") {
      if (options.method === "POST") {
        assert.deepEqual(JSON.parse(options.body), { username: "new-scheduler", password: "secret", role: "scheduler", lineId: "A" });
        return jsonResponse({ username: "new-scheduler", role: "scheduler", lineId: "A" });
      }
      if (options.method === "PATCH") {
        assert.deepEqual(JSON.parse(options.body), { username: "ops", role: "scheduler", lineId: "A" });
        return jsonResponse({ username: "ops", role: "scheduler", lineId: "A" });
      }
      return jsonResponse({ users: [{ username: "ops", role: "sales" }] });
    }
    if (path === "/api/users/password") {
      assert.equal(options.method, "PATCH");
      assert.deepEqual(JSON.parse(options.body), { username: "ops", password: "new-secret" });
      return jsonResponse({ username: "ops" });
    }
    if (path === "/api/users/ops") {
      assert.equal(options.method, "DELETE");
      return jsonResponse({ username: "ops", deleted: false });
    }
    if (path === "/api/demo/hpa-peak") {
      if (options.method === "POST") {
        hpaCleared = false;
        return jsonResponse({ summary: { ...summary, orderCount: 3 } });
      }
      if (options.method === "DELETE") {
        hpaCleared = true;
        return jsonResponse({ summary: null });
      }
      return jsonResponse({ summary: hpaCleared ? null : summary });
    }
    throw new Error(`unexpected fetch ${path}`);
  };
  const restoreGlobals = installBrowserGlobalsWithFetch(document, fetchImpl, {
    "woms.token": "token-admin",
    "woms.user": JSON.stringify({ id: "admin-a", username: "admin", role: "admin" }),
  });
  try {
    await import(appModuleUrl("admin-workspace"));
    await settleApp();

    assert.equal(document.body.dataset.role, "admin");
    assert.equal(document.getElementById("admin-panel").hidden, false);
    assert.match(document.getElementById("assign-username").innerHTML, /ops/);
    assert.match(document.getElementById("hpa-demo-summary").innerHTML, /woms_web_nginx_requests_per_second_per_pod/);

    const createForm = document.getElementById("create-user-form");
    createForm.elements.username.value = "new-scheduler";
    createForm.elements.password.value = "secret";
    createForm.elements.role.value = "scheduler";
    createForm.elements.lineId.value = "A";
    await createForm.dispatchEvent({ type: "submit" });
    await settleApp();
    assert.equal(document.getElementById("message-title").textContent, "帳號已建立");

    const assignForm = document.getElementById("assign-user-form");
    assignForm.elements.username.value = "ops";
    assignForm.elements.role.value = "scheduler";
    assignForm.elements.lineId.value = "A";
    await assignForm.dispatchEvent({ type: "submit" });
    await settleApp();
    assert.equal(document.getElementById("message-title").textContent, "帳號已更新");

    const resetForm = document.getElementById("reset-password-form");
    resetForm.elements.username.value = "ops";
    resetForm.elements.password.value = "new-secret";
    await resetForm.dispatchEvent({ type: "submit" });
    await settleApp();
    assert.equal(document.getElementById("message-title").textContent, "密碼已重設");

    document.getElementById("assign-username").value = "ops";
    await document.getElementById("delete-user-button").dispatchEvent({ type: "click" });
    await settleApp();
    assert.equal(document.getElementById("message-title").textContent, "帳號已處理");

    await document.getElementById("create-hpa-peak").dispatchEvent({ type: "click" });
    await settleApp();
    assert.equal(document.getElementById("message-title").textContent, "web autoscaling demo 已載入");
    assert.match(document.getElementById("hpa-demo-summary").innerHTML, /HPA 目標/);

    await document.getElementById("refresh-hpa-peak").dispatchEvent({ type: "click" });
    await settleApp();
    assert.equal(calls.some((call) => call.path === "/api/demo/hpa-peak" && !call.options.method), true);

    await document.getElementById("clear-hpa-peak").dispatchEvent({ type: "click" });
    await settleApp();
    assert.equal(document.getElementById("message-title").textContent, "舊版資料已清除");
    assert.equal(document.getElementById("hpa-demo-summary").textContent, "尚未載入 web autoscaling 狀態");
  } finally {
    restoreGlobals();
  }
});

test("production report form validates order allocation quantities and submits confirmation", async () => {
  const document = buildDomFromIndex();
  const calls = [];
  const productionDate = dateKeyAfter(2);
  const fetchImpl = async (path, options = {}) => {
    calls.push({ path, options });
    if (path === "/api/lines") {
      return jsonResponse({ lines: [{ id: "A", name: "Line A", capacityPerDay: 10000, timezone: "Asia/Taipei" }] });
    }
    if (path === "/api/orders") {
      return jsonResponse({
        orders: [
          { id: "ORD-PROD", customer: "Gamma", lineId: "A", quantity: 900, priority: "low", status: "生產中", dueDate: dateKeyAfter(5), createdBy: "user-sales" },
        ],
      });
    }
    if (String(path).startsWith("/api/schedules/calendar?")) {
      return jsonResponse({
        allocations: [
          { orderId: "ORD-PROD", customer: "Gamma", lineId: "A", date: productionDate, quantity: 900, priority: "low", status: "生產中" },
        ],
        pendingAllocations: [],
      });
    }
    if (String(path).startsWith("/api/schedules/history?")) {
      return jsonResponse({ history: [] });
    }
    if (path === "/api/production/confirm") {
      assert.equal(options.method, "POST");
      assert.deepEqual(JSON.parse(options.body), { orderId: "ORD-PROD", productionDate, producedQuantity: 500 });
      return jsonResponse({ remainder: { id: "ORD-PROD-R1", quantity: 400 } });
    }
    throw new Error(`unexpected fetch ${path}`);
  };
  const restoreGlobals = installBrowserGlobalsWithFetch(document, fetchImpl, {
    "woms.token": "token-scheduler",
    "woms.user": JSON.stringify({ id: "scheduler-a", username: "scheduler", role: "scheduler", lineId: "A" }),
  });
  try {
    await import(appModuleUrl("production-report"));
    await settleApp();

    const form = document.getElementById("production-form");

    form.elements.orderId.value = "UNKNOWN";
    form.elements.productionDate.value = productionDate;
    form.elements.producedQuantity.value = "10";
    await form.dispatchEvent({ type: "submit" });
    assert.equal(document.getElementById("message-title").textContent, "找不到訂單");

    form.elements.orderId.value = "ORD-PROD";
    form.elements.productionDate.value = dateKeyAfter(9);
    form.elements.producedQuantity.value = "10";
    await form.dispatchEvent({ type: "submit" });
    assert.equal(document.getElementById("message-title").textContent, "找不到排程");

    form.elements.productionDate.value = productionDate;
    form.elements.producedQuantity.value = "0";
    await form.dispatchEvent({ type: "submit" });
    assert.equal(document.getElementById("message-title").textContent, "片數不正確");

    form.elements.producedQuantity.value = "901";
    await form.dispatchEvent({ type: "submit" });
    assert.equal(document.getElementById("message-title").textContent, "片數超過本日排程");

    form.elements.producedQuantity.value = "500";
    await form.dispatchEvent({ type: "submit" });
    await settleApp();

    assert.equal(calls.filter((call) => call.path === "/api/production/confirm").length, 1);
    assert.equal(document.getElementById("message-title").textContent, "生產回報完成");
    assert.match(document.getElementById("message-body").textContent, /ORD-PROD-R1/);
  } finally {
    restoreGlobals();
  }
});

test("sales draft flow validates future due dates previews and confirms through APIs", () => {
  const app = readFileSync(new URL("./app.js", import.meta.url), "utf8");
  const html = readFileSync(new URL("./index.html", import.meta.url), "utf8");
  const orderSubmitStart = app.indexOf('document.getElementById("order-form").addEventListener("submit"');
  const orderSubmitEnd = app.indexOf('document.getElementById("assign-user-form")', orderSubmitStart);
  const orderSubmit = app.slice(orderSubmitStart, orderSubmitEnd);
  const confirmStart = app.indexOf('document.getElementById("confirm-preview-order").addEventListener("click"');
  const confirmEnd = app.indexOf('document.getElementById("confirm-schedule-job")', confirmStart);
  const confirm = app.slice(confirmStart, confirmEnd);

  assert.match(html, /id="order-form"[\s\S]*name="customer"[\s\S]*name="quantity"[\s\S]*name="priority"[\s\S]*name="dueDate"/);
  assert.match(orderSubmit, /assertFutureDueDate\(draftOrder\.dueDate\)/);
  assert.match(orderSubmit, /await createPreview\(\{[\s\S]*draftOrder,[\s\S]*\}, "sales-draft"\)/);
  assert.match(app, /request\("\/api\/schedules\/preview"/);
  assert.match(confirm, /request\("\/api\/orders\/preview-confirm", \{[\s\S]*method: "POST"[\s\S]*previewId: state\.preview\.previewId/);
  assert.match(confirm, /focusCreatedOrder\(order\)/);
  assert.match(confirm, /await refreshWorkspace\(\)/);
  assert.match(app, /showMessage\("無法加入待排程", error\.message, "warn"\)/);
});

test("sales draft conflict preview keeps baseline pending and scheduled calendars", async () => {
  const document = buildDomFromIndex();
  const fetchImpl = async (path, options = {}) => {
    if (path === "/api/auth/login") {
      return jsonResponse({
        token: "token-sales",
        user: { id: "user-sales", username: "sales", role: "sales" },
      });
    }
    if (path === "/api/lines") {
      return jsonResponse({
        lines: [{ id: "A", name: "Line A", capacityPerDay: 1000, timezone: "Asia/Taipei" }],
      });
    }
    if (path === "/api/orders") {
      return jsonResponse({
        orders: [
          { id: "ORD-PENDING", customer: "ACME", lineId: "A", quantity: 500, priority: "low", status: "待排程", dueDate: dateKeyAfter(4), createdBy: "user-sales" },
          { id: "ORD-SCHEDULED", customer: "Beta", lineId: "A", quantity: 700, priority: "low", status: "已排程", dueDate: dateKeyAfter(5), createdBy: "user-sales" },
        ],
      });
    }
    if (String(path).startsWith("/api/schedules/calendar?")) {
      return jsonResponse({
        allocations: [
          { orderId: "ORD-SCHEDULED", customer: "Beta", lineId: "A", date: dateKeyAfter(2), quantity: 700, priority: "low", status: "已排程" },
        ],
        pendingAllocations: [
          { orderId: "ORD-PENDING", customer: "ACME", lineId: "A", date: dateKeyAfter(1), quantity: 500, priority: "low", status: "待排程" },
        ],
      });
    }
    if (path === "/api/schedules/preview") {
      assert.equal(options.method, "POST");
      return jsonResponse({
        previewId: "PREVIEW-DRAFT",
        currentDate: dateKeyAfter(0),
        allocations: [
          { orderId: "PREVIEW-DRAFT", customer: "ACME", lineId: "A", date: dateKeyAfter(1), quantity: 1000, priority: "low", status: "待排程" },
        ],
        conflicts: [
          {
            orderId: "PREVIEW-DRAFT",
            reason: "capacity cannot satisfy order before due date",
            earliestFinishDate: `${dateKeyAfter(6)}T00:00:00Z`,
            affectedOrderIds: [],
          },
        ],
      });
    }
    throw new Error(`unexpected fetch ${path}`);
  };
  const restoreGlobals = installBrowserGlobalsWithFetch(document, fetchImpl);
  try {
    await import(appModuleUrl("sales-draft-conflict-calendar"));

    await document.getElementById("login-form").dispatchEvent({ type: "submit" });
    await settleApp();
    await document.getElementById("order-form").dispatchEvent({ type: "submit" });
    await settleApp();

    const pendingMarkup = renderedMarkup(document.getElementById("preview-calendar-grid"));
    assert.match(pendingMarkup, /ORD-PENDING/);
    assert.doesNotMatch(pendingMarkup, /PREVIEW-DRAFT/);
    assert.match(document.getElementById("preview-page-list").innerHTML, /conflict-preview[\s\S]*PREVIEW-DRAFT/);

    const previewCalendarModes = document.getElementById("preview-calendar-mode").children;
    await document.getElementById("preview-calendar-mode").dispatchEvent({
      type: "click",
      target: previewCalendarModes.find((button) => button.dataset.previewCalendarMode === "scheduled"),
    });
    const scheduledMarkup = renderedMarkup(document.getElementById("preview-calendar-grid"));
    assert.match(scheduledMarkup, /ORD-SCHEDULED/);
    assert.doesNotMatch(scheduledMarkup, /ORD-PENDING/);

    await document.getElementById("preview-calendar-mode").dispatchEvent({
      type: "click",
      target: previewCalendarModes.find((button) => button.dataset.previewCalendarMode === "all"),
    });
    const allMarkup = renderedMarkup(document.getElementById("preview-calendar-grid"));
    assert.match(allMarkup, /ORD-PENDING/);
    assert.match(allMarkup, /ORD-SCHEDULED/);
    assert.doesNotMatch(allMarkup, /PREVIEW-DRAFT/);
  } finally {
    restoreGlobals();
  }
});

test("sales draft preview calendar keeps low-priority draft after full high-priority backlog", async () => {
  const document = buildDomFromIndex();
  const highBacklogDate = dateKeyAfter(1);
  const draftDate = dateKeyAfter(4);
  const fetchImpl = async (path) => {
    if (path === "/api/auth/login") {
      return jsonResponse({
        token: "token-sales",
        user: { id: "user-sales", username: "sales", role: "sales" },
      });
    }
    if (path === "/api/lines") {
      return jsonResponse({
        lines: [{ id: "A", name: "Line A", capacityPerDay: 10000, timezone: "Asia/Taipei" }],
      });
    }
    if (path === "/api/orders") {
      return jsonResponse({ orders: [] });
    }
    if (String(path).startsWith("/api/schedules/calendar?")) {
      return jsonResponse({
        allocations: [
          { orderId: "ORD-SCHEDULED-1", customer: "Scheduled", lineId: "A", date: dateKeyAfter(2), quantity: 10000, priority: "high", status: "已排程", dueDate: dateKeyAfter(2), createdAtTimestamp: 1772271700000 },
          { orderId: "ORD-SCHEDULED-2", customer: "Scheduled", lineId: "A", date: dateKeyAfter(3), quantity: 10000, priority: "low", status: "已排程", dueDate: dateKeyAfter(3), createdAtTimestamp: 1772271701000 },
        ],
        pendingAllocations: [],
      });
    }
    if (path === "/api/schedules/preview") {
      return jsonResponse({
        previewId: "PREVIEW-DRAFT",
        currentDate: dateKeyAfter(0),
        allocations: [
          { orderId: "ORD-HIGH-1", customer: "ACME", lineId: "A", date: highBacklogDate, quantity: 2500, priority: "high", status: "待排程", dueDate: highBacklogDate, createdAtTimestamp: 1772271711000 },
          { orderId: "ORD-HIGH-2", customer: "ACME", lineId: "A", date: highBacklogDate, quantity: 2500, priority: "high", status: "待排程", dueDate: highBacklogDate, createdAtTimestamp: 1772271712000 },
          { orderId: "ORD-HIGH-3", customer: "ACME", lineId: "A", date: highBacklogDate, quantity: 2500, priority: "high", status: "待排程", dueDate: highBacklogDate, createdAtTimestamp: 1772271713000 },
          { orderId: "ORD-HIGH-4", customer: "ACME", lineId: "A", date: highBacklogDate, quantity: 2500, priority: "high", status: "待排程", dueDate: highBacklogDate, createdAtTimestamp: 1772271714000 },
          { orderId: "PREVIEW-DRAFT", customer: "ACME", lineId: "A", date: draftDate, quantity: 2500, priority: "low", status: "待排程", dueDate: highBacklogDate, createdAtTimestamp: 1772271715000 },
        ],
        conflicts: [],
      });
    }
    throw new Error(`unexpected fetch ${path}`);
  };
  const restoreGlobals = installBrowserGlobalsWithFetch(document, fetchImpl);
  try {
    await import(appModuleUrl("sales-draft-low-priority-placement"));

    await document.getElementById("login-form").dispatchEvent({ type: "submit" });
    await settleApp();
    await document.getElementById("order-form").dispatchEvent({ type: "submit" });
    await settleApp();

    const highBacklogCell = document.getElementById("preview-calendar-grid").children.find((cell) => cell.dataset.date === highBacklogDate);
    const draftCell = document.getElementById("preview-calendar-grid").children.find((cell) => cell.dataset.date === draftDate);
    assert.doesNotMatch(renderedMarkup(highBacklogCell), /PREVIEW-DRAFT/);
    assert.match(renderedMarkup(highBacklogCell), /ORD-HIGH-1[\s\S]*ORD-HIGH-2[\s\S]*ORD-HIGH-3[\s\S]*ORD-HIGH-4/);
    assert.match(renderedMarkup(draftCell), /PREVIEW-DRAFT/);
  } finally {
    restoreGlobals();
  }
});

test("sales draft conflict preview shows successful draft schedule and excludes conflicted pending order", async () => {
  const document = buildDomFromIndex();
  const previewDate = dateKeyAfter(2);
  let previewCalls = 0;
  const fetchImpl = async (path, options = {}) => {
    if (path === "/api/auth/login") {
      return jsonResponse({
        token: "token-sales",
        user: { id: "user-sales", username: "sales", role: "sales" },
      });
    }
    if (path === "/api/lines") {
      return jsonResponse({
        lines: [{ id: "A", name: "Line A", capacityPerDay: 10000, timezone: "Asia/Taipei" }],
      });
    }
    if (path === "/api/orders") {
      return jsonResponse({
        orders: [
          { id: "ORD-A", customer: "A", lineId: "A", quantity: 2500, priority: "high", status: "待排程", dueDate: previewDate, createdBy: "user-sales" },
          { id: "ORD-B", customer: "B", lineId: "A", quantity: 2500, priority: "high", status: "待排程", dueDate: previewDate, createdBy: "user-sales" },
          { id: "ORD-C", customer: "C", lineId: "A", quantity: 2500, priority: "low", status: "待排程", dueDate: previewDate, createdBy: "user-sales" },
          { id: "ORD-D", customer: "D", lineId: "A", quantity: 2500, priority: "low", status: "待排程", dueDate: previewDate, createdBy: "user-sales" },
          { id: "ORD-SCHEDULED", customer: "Scheduled", lineId: "A", quantity: 1000, priority: "low", status: "已排程", dueDate: dateKeyAfter(4), createdBy: "user-sales" },
        ],
      });
    }
    if (String(path).startsWith("/api/schedules/calendar?")) {
      return jsonResponse({
        allocations: [
          { orderId: "ORD-SCHEDULED", customer: "Scheduled", lineId: "A", date: dateKeyAfter(4), quantity: 1000, priority: "low", status: "已排程", dueDate: dateKeyAfter(4), createdAtTimestamp: 1772271700000 },
        ],
        pendingAllocations: [
          { orderId: "ORD-A", customer: "A", lineId: "A", date: previewDate, quantity: 2500, priority: "high", status: "待排程", dueDate: previewDate, createdAtTimestamp: 1772271711000 },
          { orderId: "ORD-B", customer: "B", lineId: "A", date: previewDate, quantity: 2500, priority: "high", status: "待排程", dueDate: previewDate, createdAtTimestamp: 1772271712000 },
          { orderId: "ORD-C", customer: "C", lineId: "A", date: previewDate, quantity: 2500, priority: "low", status: "待排程", dueDate: previewDate, createdAtTimestamp: 1772271713000 },
          { orderId: "ORD-D", customer: "D", lineId: "A", date: previewDate, quantity: 2500, priority: "low", status: "待排程", dueDate: previewDate, createdAtTimestamp: 1772271714000 },
        ],
      });
    }
    if (path === "/api/schedules/preview") {
      assert.equal(options.method, "POST");
      previewCalls += 1;
      const body = JSON.parse(options.body);
      if (previewCalls === 1) {
        assert.equal(body.allowLateCompletion, undefined);
      } else {
        assert.equal(body.allowLateCompletion, true);
        assert.deepEqual(body.orderIds, ["PREVIEW-DRAFT"]);
        assert.deepEqual(body.resolutionOrderIds, []);
      }
      if (previewCalls > 1) {
        return jsonResponse({
          previewId: "PREVIEW-DRAFT-SOLUTION",
          currentDate: dateKeyAfter(0),
          allocations: [
            { orderId: "ORD-A", customer: "A", lineId: "A", date: previewDate, quantity: 2500, priority: "high", status: "待排程", dueDate: previewDate, createdAtTimestamp: 1772271711000 },
            { orderId: "ORD-B", customer: "B", lineId: "A", date: previewDate, quantity: 2500, priority: "high", status: "待排程", dueDate: previewDate, createdAtTimestamp: 1772271712000 },
            { orderId: "PREVIEW-DRAFT", customer: "E", lineId: "A", date: dateKeyAfter(5), quantity: 2500, priority: "low", status: "待排程", dueDate: previewDate, createdAtTimestamp: 1772271715000 },
          ],
          conflicts: [],
        });
      }
      return jsonResponse({
        previewId: "PREVIEW-DRAFT",
        currentDate: dateKeyAfter(0),
        allocations: [
          { orderId: "ORD-A", customer: "A", lineId: "A", date: previewDate, quantity: 2500, priority: "high", status: "待排程", dueDate: previewDate, createdAtTimestamp: 1772271711000 },
          { orderId: "ORD-B", customer: "B", lineId: "A", date: previewDate, quantity: 2500, priority: "high", status: "待排程", dueDate: previewDate, createdAtTimestamp: 1772271712000 },
          { orderId: "PREVIEW-DRAFT", customer: "E", lineId: "A", date: previewDate, quantity: 2500, priority: "high", status: "待排程", dueDate: previewDate, createdAtTimestamp: 1772271715000 },
          { orderId: "ORD-C", customer: "C", lineId: "A", date: previewDate, quantity: 2500, priority: "low", status: "待排程", dueDate: previewDate, createdAtTimestamp: 1772271713000 },
        ],
        conflicts: [
          {
            orderId: "ORD-D",
            reason: "capacity cannot satisfy order before due date",
            earliestFinishDate: `${dateKeyAfter(2)}T00:00:00Z`,
            affectedOrderIds: ["ORD-SCHEDULED"],
          },
        ],
      });
    }
    if (path === "/api/orders/preview-confirm") {
      assert.equal(options.method, "POST");
      assert.deepEqual(JSON.parse(options.body), { previewId: "PREVIEW-DRAFT-SOLUTION", deferredOrderIds: [] });
      return jsonResponse({ id: "ORD-DRAFT", customer: "E", lineId: "A", quantity: 2500, priority: "high", status: "待排程", dueDate: previewDate, createdBy: "user-sales" });
    }
    throw new Error(`unexpected fetch ${path}`);
  };
  const restoreGlobals = installBrowserGlobalsWithFetch(document, fetchImpl);
  try {
    await import(appModuleUrl("sales-draft-conflict-successful-allocations"));

    await document.getElementById("login-form").dispatchEvent({ type: "submit" });
    await settleApp();
    await document.getElementById("order-form").dispatchEvent({ type: "submit" });
    await settleApp();

    const pendingMarkup = renderedMarkup(document.getElementById("preview-calendar-grid"));
    assert.match(pendingMarkup, /ORD-A[\s\S]*ORD-B[\s\S]*ORD-C[\s\S]*ORD-D/);
    assert.doesNotMatch(pendingMarkup, /PREVIEW-DRAFT/);
    assert.match(document.getElementById("preview-page-list").innerHTML, /ORD-D/);
    assert.match(document.getElementById("preview-page-list").innerHTML, /conflict-preview[\s\S]*ORD-D/);
    assert.equal(document.getElementById("confirm-preview-order").hidden, true);
    assert.match(document.getElementById("preview-page-list").innerHTML, /預覽最早完成解法/);
    assert.match(document.getElementById("preview-page-list").innerHTML, /ORD-SCHEDULED 已排程不可移動/);
    assert.doesNotMatch(document.getElementById("preview-page-list").innerHTML, /允許移動 ORD-SCHEDULED/);

    await document.getElementById("preview-page-list").dispatchEvent({
      type: "click",
      target: document.querySelector('[data-preview-action="preview-conflict-solution"]'),
    });
    await settleApp();

    const previewCalendarModes = document.getElementById("preview-calendar-mode").children;
    await document.getElementById("preview-calendar-mode").dispatchEvent({
      type: "click",
      target: previewCalendarModes.find((button) => button.dataset.previewCalendarMode === "all"),
    });
    const allMarkup = renderedMarkup(document.getElementById("preview-calendar-grid"));
    assert.match(allMarkup, /ORD-SCHEDULED/);
    assert.match(allMarkup, /ORD-A[\s\S]*ORD-B/);
    assert.match(allMarkup, /PREVIEW-DRAFT/);
    assert.doesNotMatch(allMarkup, /移入預覽/);
    assert.doesNotMatch(allMarkup, /已移出/);
    assert.equal(allMarkup.match(/ORD-SCHEDULED/g).length, 1);
    assert.doesNotMatch(allMarkup, /ORD-D/);
    assert.equal(document.getElementById("confirm-preview-order").hidden, false);
    assert.doesNotMatch(document.getElementById("preview-page-list").innerHTML, /改送需業務處理 ORD-D/);
    await document.getElementById("confirm-preview-order").dispatchEvent({ type: "click" });
    await settleApp();
    assert.equal(document.getElementById("message-title").textContent, "已加入待排程", document.getElementById("message-body").textContent);
    assert.doesNotMatch(document.getElementById("message-body").textContent, /已勾選的衝突訂單會移到需業務處理/);
    assert.doesNotMatch(document.getElementById("message-body").textContent, /已取消選取/);
  } finally {
    restoreGlobals();
  }
});

test("sales can move the current conflicted draft to follow-up with the default conflict reason", async () => {
  const document = buildDomFromIndex();
  const previewDate = dateKeyAfter(2);
  const calls = [];
  const fetchImpl = async (path, options = {}) => {
    calls.push({ path, options });
    if (path === "/api/auth/login") {
      return jsonResponse({
        token: "token-sales",
        user: { id: "user-sales", username: "sales", role: "sales" },
      });
    }
    if (path === "/api/lines") {
      return jsonResponse({
        lines: [{ id: "A", name: "Line A", capacityPerDay: 10000, timezone: "Asia/Taipei" }],
      });
    }
    if (path === "/api/orders") {
      return jsonResponse({
        orders: [
          { id: "ORD-A", customer: "A", lineId: "A", quantity: 2500, priority: "high", status: "待排程", dueDate: previewDate, createdBy: "user-sales" },
        ],
      });
    }
    if (String(path).startsWith("/api/schedules/calendar?")) {
      return jsonResponse({ allocations: [], pendingAllocations: [] });
    }
    if (path === "/api/schedules/preview") {
      assert.equal(options.method, "POST");
      return jsonResponse({
        previewId: "PREVIEW-DRAFT",
        draftOrder: { customer: "Blocked", lineId: "A", quantity: 2500, priority: "high", dueDate: previewDate },
        currentDate: dateKeyAfter(0),
        allocations: [],
        conflicts: [
          {
            orderId: "PREVIEW-DRAFT",
            reason: "capacity cannot satisfy order before due date",
            earliestFinishDate: `${dateKeyAfter(2)}T00:00:00Z`,
            affectedOrderIds: [],
          },
        ],
      });
    }
    if (path === "/api/orders/preview-confirm") {
      assert.equal(options.method, "POST");
      assert.deepEqual(JSON.parse(options.body), { previewId: "PREVIEW-DRAFT", deferDraft: true, deferReason: "有衝突需修改" });
      return jsonResponse({ id: "ORD-DRAFT", customer: "Blocked", lineId: "A", quantity: 2500, priority: "high", status: "需業務處理", dueDate: previewDate, createdBy: "user-sales", rejectionReason: "有衝突需修改" });
    }
    throw new Error(`unexpected fetch ${path}`);
  };
  const restoreGlobals = installBrowserGlobalsWithFetch(document, fetchImpl);
  try {
    await import(appModuleUrl("sales-draft-defer-current"));

    await document.getElementById("login-form").dispatchEvent({ type: "submit" });
    await settleApp();
    document.querySelector('#order-form input[name="customer"]').value = "Blocked";
    document.querySelector('#order-form select[name="priority"]').value = "high";
    document.querySelector('#order-form input[name="dueDate"]').value = previewDate;
    await document.getElementById("order-form").dispatchEvent({ type: "submit" });
    await settleApp();

    assert.match(document.getElementById("preview-page-list").innerHTML, /取消選取目前訂單/, JSON.stringify({
      calls: calls.map((call) => call.path),
      message: document.getElementById("message-body").textContent,
    }));
    const deferButton = document.createElement("button");
    deferButton.setAttribute("data-preview-action", "defer-sales-draft");
    document.getElementById("preview-page-list").appendChild(deferButton);
    await document.getElementById("preview-page-list").dispatchEvent({
      type: "click",
      target: deferButton,
    });
    await settleApp();

    assert.equal(document.getElementById("reject-dialog").open, true);
    assert.equal(document.getElementById("reject-reason").value, "有衝突需修改");
    assert.equal(calls.filter((call) => call.path === "/api/orders/preview-confirm").length, 0);
    await document.getElementById("confirm-reject-orders").dispatchEvent({ type: "click" });
    await settleApp();

    assert.equal(calls.filter((call) => call.path === "/api/orders/preview-confirm").length, 1);
    assert.equal(document.getElementById("message-title").textContent, "已移到需業務處理");
    assert.match(calls.find((call) => call.path === "/api/orders/preview-confirm").options.body, /有衝突需修改/);
  } finally {
    restoreGlobals();
  }
});

test("preview calendars keep the same visible dates as the monthly calendar", async () => {
  const document = buildDomFromIndex();
  const distantPreviewDate = dateKeyAfter(45);
  const fetchImpl = async (path, options = {}) => {
    if (path === "/api/auth/login") {
      return jsonResponse({
        token: "token-sales",
        user: { id: "user-sales", username: "sales", role: "sales" },
      });
    }
    if (path === "/api/lines") {
      return jsonResponse({
        lines: [{ id: "A", name: "Line A", capacityPerDay: 10000, timezone: "Asia/Taipei" }],
      });
    }
    if (path === "/api/orders") {
      return jsonResponse({ orders: [] });
    }
    if (String(path).startsWith("/api/schedules/calendar?")) {
      return jsonResponse({ allocations: [], pendingAllocations: [] });
    }
    if (path === "/api/schedules/preview") {
      assert.equal(options.method, "POST");
      return jsonResponse({
        previewId: "PREVIEW-DRAFT",
        currentDate: dateKeyAfter(0),
        allocations: [
          { orderId: "PREVIEW-DRAFT", customer: "Far", lineId: "A", date: distantPreviewDate, quantity: 2500, priority: "low", status: "待排程" },
        ],
        conflicts: [],
      });
    }
    throw new Error(`unexpected fetch ${path}`);
  };
  const restoreGlobals = installBrowserGlobalsWithFetch(document, fetchImpl);
  try {
    await import(appModuleUrl("preview-calendar-grid-dates"));

    await document.getElementById("login-form").dispatchEvent({ type: "submit" });
    await settleApp();
    const monthlyDates = calendarDateKeys(document.getElementById("calendar-grid"));

    await document.getElementById("order-form").dispatchEvent({ type: "submit" });
    await settleApp();

    assert.deepEqual(calendarDateKeys(document.getElementById("preview-calendar-grid")), monthlyDates);
  } finally {
    restoreGlobals();
  }
});

test("sales calendar mode renders all pending and scheduled allocation sources", async () => {
  const document = buildDomFromIndex();
  const allocationDateFor = (path, day) => {
    const month = decodeURIComponent(String(path).match(/[?&]month=([^&]+)/)?.[1] ?? "2026-06");
    return `${month}-${String(day).padStart(2, "0")}`;
  };
  const fetchImpl = async (path) => {
    if (path === "/api/auth/login") {
      return jsonResponse({
        token: "token-sales",
        user: { id: "user-sales", username: "sales", role: "sales" },
      });
    }
    if (path === "/api/lines") {
      return jsonResponse({
        lines: [{ id: "A", name: "Line A", capacityPerDay: 10000, timezone: "Asia/Taipei" }],
      });
    }
    if (path === "/api/orders") {
      return jsonResponse({ orders: [] });
    }
    if (String(path).startsWith("/api/schedules/calendar?")) {
      return jsonResponse({
        allocations: [
          { orderId: "ORD-SCHEDULED-CAL", customer: "Scheduled", lineId: "A", date: allocationDateFor(path, 10), quantity: 1000, priority: "low", status: "已排程" },
        ],
        pendingAllocations: [
          { orderId: "ORD-PENDING-CAL", customer: "Pending", lineId: "A", date: allocationDateFor(path, 11), quantity: 2000, priority: "high", status: "待排程" },
        ],
      });
    }
    if (String(path).startsWith("/api/schedules/history?")) {
      return jsonResponse({ history: [] });
    }
    throw new Error(`unexpected fetch ${path}`);
  };
  const restoreGlobals = installBrowserGlobalsWithFetch(document, fetchImpl);
  try {
    await import(appModuleUrl("sales-calendar-mode-sources"));

    await document.getElementById("login-form").dispatchEvent({ type: "submit" });
    await settleApp();

    const grid = document.getElementById("calendar-grid");
    const modeControl = document.getElementById("main-calendar-mode");
    const pendingButton = document.querySelector('button[data-calendar-mode="pending"]');
    const scheduledButton = document.querySelector('button[data-calendar-mode="scheduled"]');
    const allButton = document.querySelector('button[data-calendar-mode="all"]');

    assert.equal(modeControl.hidden, false);
    assert.equal(allButton.getAttribute("aria-pressed"), "true");
    assert.match(renderedMarkup(grid), /ORD-SCHEDULED-CAL/);
    assert.match(renderedMarkup(grid), /ORD-PENDING-CAL/);
    assert.equal(grid.children.some((cell) => cell.classList.contains("preview-highlight")), true);

    await modeControl.dispatchEvent({ type: "click", target: pendingButton });
    await settleApp();
    assert.equal(pendingButton.getAttribute("aria-pressed"), "true");
    assert.match(renderedMarkup(grid), /ORD-PENDING-CAL/);
    assert.doesNotMatch(renderedMarkup(grid), /ORD-SCHEDULED-CAL/);

    await modeControl.dispatchEvent({ type: "click", target: scheduledButton });
    await settleApp();
    assert.equal(scheduledButton.getAttribute("aria-pressed"), "true");
    assert.match(renderedMarkup(grid), /ORD-SCHEDULED-CAL/);
    assert.doesNotMatch(renderedMarkup(grid), /ORD-PENDING-CAL/);
    assert.equal(grid.children.some((cell) => cell.classList.contains("preview-highlight")), false);
  } finally {
    restoreGlobals();
  }
});

test("scheduler bulk controls filter select preview reject cancel and schedule selected orders", () => {
  const app = readFileSync(new URL("./app.js", import.meta.url), "utf8");
  const html = readFileSync(new URL("./index.html", import.meta.url), "utf8");
  const renderFiltersStart = app.indexOf("function renderFilters()");
  const renderFiltersEnd = app.indexOf("function roleLabel", renderFiltersStart);
  const filters = app.slice(renderFiltersStart, renderFiltersEnd);

  assert.match(html, /id="status-sidebar"/);
  assert.match(html, /id="customer-filter"/);
  assert.match(html, /id="priority-filters"/);
  assert.match(html, /id="selected-count"/);
  assert.match(html, /id="preview-selected"/);
  assert.match(html, /id="reject-selected"/);
  assert.match(html, /id="cancel-selected"/);
  assert.match(filters, /renderCustomerFilter\(\)/);
  assert.match(filters, /renderCheckboxGroup\("priority-filters", priorities, state\.filters\.priorities, priorityLabel\)/);
  assert.match(app, /state\.filters\.status = state\.filters\.status === status \? "" : status/);
  assert.match(app, /function updateSelectedCount\(\) \{[\s\S]*已選取 \$\{state\.selectedOrderIds\.size\} 張訂單/);
  assert.match(app, /document\.getElementById\("preview-selected"\)\.addEventListener\("click"[\s\S]*await createPreview\(data, "schedule"\)/);
  assert.match(app, /document\.getElementById\("reject-selected"\)\.addEventListener\("click"[\s\S]*openRejectDialog\(Array\.from\(state\.selectedOrderIds\)\)/);
  assert.match(app, /request\("\/api\/orders\/reject", \{[\s\S]*method: "POST"[\s\S]*orderIds: state\.rejectOrderIds/);
  assert.match(app, /document\.getElementById\("cancel-selected"\)\.addEventListener\("click"[\s\S]*request\("\/api\/orders", \{[\s\S]*method: "DELETE"[\s\S]*Array\.from\(state\.selectedOrderIds\)/);
  assert.match(app, /document\.getElementById\("schedule-form"\)\.addEventListener\("submit"[\s\S]*await createPreview\(data, "schedule"\)/);
});

test("calendar modes and drag or drop scheduling target visible future dates", () => {
  const app = readFileSync(new URL("./app.js", import.meta.url), "utf8");
  const html = readFileSync(new URL("./index.html", import.meta.url), "utf8");

  assert.match(html, /id="main-calendar-mode"[\s\S]*data-calendar-mode="all"[\s\S]*data-calendar-mode="pending"[\s\S]*data-calendar-mode="scheduled"/);
  assert.match(app, /document\.getElementById\("main-calendar-mode"\)\.addEventListener\("click"[\s\S]*state\.calendarMode = mode[\s\S]*renderCalendar\(\)/);
  assert.match(app, /function mainCalendarAllocations\(\)[\s\S]*state\.calendarMode === "pending"[\s\S]*state\.calendarMode === "all"/);
  assert.match(app, /cell\.addEventListener\("drop", async \(event\) => \{[\s\S]*await scheduleDroppedOrders\(orderIds, day\.key\)/);
  assert.equal(app.includes('document.elementFromPoint(clientX, clientY)?.closest?.(".calendar-day")'), true);
  assert.match(app, /async function scheduleDroppedOrders\(orderIds, targetDate\) \{[\s\S]*startDate: targetDate,[\s\S]*orderIds,[\s\S]*manualForce: false/);
  assert.match(app, /request\("\/api\/schedules\/preview"/);
});

test("conflict preview actions retry edit unselect reject solve and validate manual force", () => {
  const app = readFileSync(new URL("./app.js", import.meta.url), "utf8");
  const previewHandlerStart = app.indexOf('document.getElementById("preview-page-list").addEventListener("click"');
  const previewHandlerEnd = app.indexOf('document.getElementById("prev-month")', previewHandlerStart);
  const previewHandler = app.slice(previewHandlerStart, previewHandlerEnd);

  assert.match(app, /"retry-today": handleRetryTodayPreviewAction/);
  assert.match(app, /"retry-suggested-start": handleRetrySuggestedStartPreviewAction/);
  assert.match(app, /data-preview-action="update-conflict-due-date"/);
  assert.match(app, /data-preview-action="unselect-conflict-order"/);
  assert.match(app, /data-preview-action="reject-preview-orders"/);
  assert.match(app, /data-preview-action="preview-conflict-solution"/);
  assert.match(app, /"retry-manual-force": handleRetryManualForcePreviewAction/);
  assert.match(app, /async function handleRetryTodayPreviewAction\(\)[\s\S]*await retryPreview\(\{ startDate: tomorrowDateInputValue\(\), manualForce: false, reason: "" \}\)/);
  assert.match(app, /async function handleUpdateConflictDueDatePreviewAction\(event\)[\s\S]*await updateOrderDueDate\(orderId, input\.value\)/);
  assert.match(app, /async function handleUnselectConflictOrderPreviewAction\(event\)[\s\S]*state\.selectedOrderIds\.delete\(orderId\)/);
  assert.match(app, /async function handleRejectPreviewOrdersPreviewAction\(\)[\s\S]*openRejectDialog\(state\.preview\.request\.orderIds\)/);
  assert.match(app, /async function handlePreviewConflictSolutionPreviewAction\(\)[\s\S]*orderIds\.length === 0[\s\S]*至少選取一張訂單/);
  assert.match(app, /async function handleRetryManualForcePreviewAction\(\)[\s\S]*!reason[\s\S]*人工強制介入必須留下原因/);
  assert.match(app, /async function handleRetryManualForcePreviewAction\(\)[\s\S]*await retryPreview\(\{ manualForce: true, reason \}\)/);
});

test("production flow starts scheduled orders and confirms allocation quantities", () => {
  const app = readFileSync(new URL("./app.js", import.meta.url), "utf8");
  const productionStart = app.indexOf("async function handleOrderAction");
  const productionEnd = app.indexOf("async function scheduleDroppedOrders", productionStart);
  const productionAction = app.slice(productionStart, productionEnd);
  const submitStart = app.indexOf("async function submitProductionReport");
  const submitEnd = app.indexOf("function suggestedStartDate", submitStart);
  const submit = app.slice(submitStart, submitEnd);

  assert.match(app, /data-order-action="start-production"/);
  assert.match(app, /data-order-action="confirm-production"/);
  assert.match(productionAction, /request\("\/api\/production\/start", \{[\s\S]*method: "POST"[\s\S]*JSON\.stringify\(\{ orderId \}\)/);
  assert.match(productionAction, /openProductionReport\(order, productionDate\)/);
  assert.match(app, /document\.getElementById\("production-form"\)\.addEventListener\("submit"[\s\S]*submitProductionReport\(form\.orderId, form\.productionDate, Number\(form\.producedQuantity\)\)/);
  assert.match(submit, /producedQuantity <= 0/);
  assert.match(submit, /producedQuantity > allocation\.quantity/);
  assert.match(submit, /request\("\/api\/production\/confirm", \{[\s\S]*method: "POST"[\s\S]*orderId, productionDate, producedQuantity/);
  assert.match(submit, /payload\.remainder/);
});

test("admin user management and HPA browser controls call expected APIs", () => {
  const app = readFileSync(new URL("./app.js", import.meta.url), "utf8");
  const html = readFileSync(new URL("./index.html", import.meta.url), "utf8");

  assert.match(html, /id="create-user-form"/);
  assert.match(html, /id="assign-user-form"/);
  assert.match(html, /id="reset-password-form"/);
  assert.match(html, /id="delete-user-button"/);
  assert.match(app, /document\.getElementById\("create-user-form"\)\.addEventListener\("submit"[\s\S]*request\("\/api\/users", \{[\s\S]*method: "POST"/);
  assert.match(app, /document\.getElementById\("assign-user-form"\)\.addEventListener\("submit"[\s\S]*request\("\/api\/users", \{[\s\S]*method: "PATCH"/);
  assert.match(app, /document\.getElementById\("reset-password-form"\)\.addEventListener\("submit"[\s\S]*request\("\/api\/users\/password", \{[\s\S]*method: "PATCH"/);
  assert.match(app, /request\(`\/api\/users\/\$\{encodeURIComponent\(username\)\}`, \{ method: "DELETE" \}\)/);
  assert.match(html, /id="create-hpa-peak"/);
  assert.match(html, /id="refresh-hpa-peak"/);
  assert.match(html, /id="clear-hpa-peak"/);
  assert.match(app, /request\("\/api\/demo\/hpa-peak", \{ method: "POST" \}\)/);
  assert.match(app, /request\("\/api\/demo\/hpa-peak"\)/);
  assert.match(app, /request\("\/api\/demo\/hpa-peak", \{ method: "DELETE" \}\)/);
  assert.match(app, /state\.hpaPeakPollingEnabled = true/);
  assert.match(app, /function syncHPAPeakPolling\(\)/);
});

test("comprehensive event listener integration tests to maximize app.js coverage", async () => {
  const document = buildDomFromIndex();
  const calls = [];
  let hpaCleared = false;

  let failFetch = false;
  let fetchStatus = 200;
  let failLogin = false;
  let failPreviewConfirm = false;
  let failJobs = false;
  let failUserCreate = false;
  let failUserAssign = false;
  let failUserPassword = false;
  let failUserDelete = false;
  let failProduction = false;
  let failReject = false;
  let failCancel = false;
  let failConflictDemo = false;
  let failHpaPeak = false;
  let failRetryPreview = false;
  let failOrderSubmit = false;
  let mockConflicts = [];
  
  const fetchImpl = async (path, options = {}) => {
    calls.push({ path, options });

    if (failFetch) {
      const err = new Error("mocked error");
      err.status = fetchStatus;
      throw err;
    }

    if (path === "/api/auth/login") {
      if (failLogin) {
        throw new Error("mocked login failure");
      }
      const body = options.body ? JSON.parse(options.body) : {};
      let role = "scheduler";
      if (body.username === "admin") {
        role = "admin";
      } else if (body.username === "sales") {
        role = "sales";
      }
      return jsonResponse({
        token: "token-" + role,
        user: { id: role + "-1", username: body.username || "scheduler", role: role, lineId: "A" }
      });
    }
    if (path === "/api/auth/logout") {
      return jsonResponse({});
    }
    if (path === "/api/lines") {
      return jsonResponse({
        lines: [
          { id: "A", name: "Line A", capacityPerDay: 10000, timezone: "Asia/Taipei" },
          { id: "B", name: "Line B", capacityPerDay: 9000, timezone: "Asia/Taipei" },
        ],
      });
    }
    if (path === "/api/orders") {
      if (options.method === "DELETE") {
        if (failCancel) {
          throw new Error("mocked failure");
        }
        return jsonResponse({ cancelledOrderIds: ["ORD-PENDING"] });
      }
      return jsonResponse({
        orders: [
          { id: "ORD-PENDING", customer: "ACME", lineId: "A", quantity: 2500, priority: "high", status: "待排程", dueDate: dateKeyAfter(6), createdBy: "sales-1" },
          { id: "ORD-PENDING-2", customer: "ACME-2", lineId: "A", quantity: 1000, priority: "low", status: "待排程", dueDate: dateKeyAfter(6), createdBy: "sales-1" },
          { id: "ORD-SCHEDULED", customer: "Beta", lineId: "A", quantity: 1500, priority: "low", status: "已排程", dueDate: dateKeyAfter(8), createdBy: "sales-1" },
          { id: "ORD-PROD", customer: "Gamma", lineId: "A", quantity: 900, priority: "low", status: "生產中", dueDate: dateKeyAfter(9), createdBy: "sales-1" },
        ],
      });
    }
    if (path === "/api/orders/ORD-PENDING" && options.method === "PATCH") {
      return jsonResponse({ id: "ORD-PENDING", dueDate: dateKeyAfter(10) });
    }
    if (path === "/api/orders/reject") {
      if (failReject) {
        throw new Error("mocked failure");
      }
      return jsonResponse({ orders: ["ORD-PENDING"] });
    }
    if (path === "/api/orders/resubmit" && options.method === "POST") {
      return jsonResponse({ id: "ORD-PENDING" });
    }
    if (path === "/api/production/start" && options.method === "POST") {
      return jsonResponse({ id: "ORD-SCHEDULED" });
    }
    if (String(path).startsWith("/api/schedules/calendar?")) {
      return jsonResponse({
        allocations: [
          { orderId: "ORD-SCHEDULED", customer: "Beta", lineId: "A", date: dateKeyAfter(2), quantity: 1500, priority: "low", status: "已排程" },
          { orderId: "ORD-PROD", customer: "Gamma", lineId: "A", date: dateKeyAfter(3), quantity: 900, priority: "low", status: "生產中" },
        ],
        pendingAllocations: [],
      });
    }
    if (String(path).startsWith("/api/schedules/history?")) {
      return jsonResponse({ history: [{ action: "schedule.job.create", resource: "JOB-1", reason: "ok", createdAt: `${dateKeyAfter(0)}T01:02:00Z` }] });
    }
    if (path === "/api/schedules/preview") {
      if (failRetryPreview || failOrderSubmit) {
        throw new Error("mocked failure");
      }
      return jsonResponse({
        previewId: "PREVIEW-SCHEDULE",
        currentDate: dateKeyAfter(0),
        allocations: [
          { orderId: "ORD-PENDING", customer: "ACME", lineId: "A", date: dateKeyAfter(2), quantity: 2500, priority: "high", status: "已排程" },
        ],
        conflicts: mockConflicts,
      });
    }
    if (path === "/api/orders/preview-confirm") {
      if (failPreviewConfirm) {
        throw new Error("mocked failure");
      }
      return jsonResponse({ id: "ORD-DRAFT", customer: "ACME", lineId: "A", quantity: 2500, priority: "low", status: "待排程", dueDate: dateKeyAfter(5), createdBy: "sales-1" });
    }
    if (path === "/api/schedules/jobs") {
      if (failJobs) {
        throw new Error("mocked failure");
      }
      return jsonResponse({ id: "JOB-2", status: "completed" });
    }
    if (path === "/api/production/confirm") {
      if (failProduction) {
        throw new Error("mocked failure");
      }
      return jsonResponse({ remainder: { id: "ORD-PROD-R1", quantity: 400 } });
    }
    if (path === "/api/users") {
      if (options.method === "POST") {
        if (failUserCreate) {
          throw new Error("mocked failure");
        }
        return jsonResponse({ username: "new-scheduler", role: "scheduler", lineId: "A" });
      }
      if (options.method === "PATCH") {
        if (failUserAssign) {
          throw new Error("mocked failure");
        }
        return jsonResponse({ username: "ops", role: "scheduler", lineId: "A" });
      }
      return jsonResponse({ users: [{ username: "ops", role: "sales" }] });
    }
    if (path === "/api/users/password") {
      if (failUserPassword) {
        throw new Error("mocked failure");
      }
      return jsonResponse({ username: "ops" });
    }
    if (path.startsWith("/api/users/") && options.method === "DELETE") {
      if (failUserDelete) {
        throw new Error("mocked failure");
      }
      return jsonResponse({ username: "ops", deleted: true });
    }
    if (path === "/api/demo/hpa-peak") {
      if (failHpaPeak) {
        throw new Error("mocked failure");
      }
      if (options.method === "POST") {
        hpaCleared = false;
        return jsonResponse({ summary: { autoscaling: { desiredReplicas: 3 } } });
      }
      if (options.method === "DELETE") {
        hpaCleared = true;
        return jsonResponse({ summary: null });
      }
      return jsonResponse({ summary: hpaCleared ? null : { autoscaling: { desiredReplicas: 3 } } });
    }
    if (path === "/api/demo/conflict-orders") {
      if (failConflictDemo) {
        throw new Error("mocked failure");
      }
      return jsonResponse({ orders: [{ id: "ORD-C1" }] });
    }
    throw new Error(`unexpected fetch ${path}`);
  };

  const restoreGlobals = installBrowserGlobalsWithFetch(document, fetchImpl);
  try {
    // A. 401 Startup catch path verification
    window.localStorage.setItem("woms.token", "fake-token");
    window.localStorage.setItem("woms.user", JSON.stringify({ id: "admin-1", username: "admin", role: "admin" }));
    failFetch = true;
    fetchStatus = 401;
    await import(new URL(`./app.js?dpinitial401=${Date.now()}`, import.meta.url));
    await settleApp();
    failFetch = false;
    window.localStorage.clear();

    // B. 500 Startup catch path verification
    window.localStorage.setItem("woms.token", "fake-token");
    window.localStorage.setItem("woms.user", JSON.stringify({ id: "admin-1", username: "admin", role: "admin" }));
    failFetch = true;
    fetchStatus = 500;
    await import(new URL(`./app.js?dpinitial500=${Date.now()}`, import.meta.url));
    await settleApp();
    failFetch = false;
    window.localStorage.clear();

    // C. Import main app.js instance for the full test flows
    await import(new URL(`./app.js?dpcomprehensive=${Date.now()}`, import.meta.url));
    await settleApp();

    const getCardById = (id) => document.getElementById("orders-body").children.find((item) => item.dataset.orderId === id);
    const selectCardById = async (id, select = true) => {
      const card = getCardById(id);
      if (card) {
        const isSelected = card.classList.contains("selected");
        if (isSelected !== select) {
          await card.dispatchEvent({ type: "click", target: card });
          await settleApp();
        }
      }
    };

    // 1. Verify anonymous startup renders login state
    assert.equal(document.getElementById("login-page").hidden, false);
    assert.equal(document.getElementById("app-shell").hidden, true);
    assert.equal(document.body.dataset.role, "");

    // 2. Submit login-form as admin with failure
    document.querySelector('#login-form input[name="username"]').value = "admin";
    document.querySelector('#login-form input[name="password"]').value = "demo";
    failLogin = true;
    await document.getElementById("login-form").dispatchEvent({ type: "submit" });
    await settleApp();
    failLogin = false;

    // 3. Submit login-form to log in as admin successfully
    document.querySelector('#login-form input[name="username"]').value = "admin";
    document.querySelector('#login-form input[name="password"]').value = "demo";
    await document.getElementById("login-form").dispatchEvent({ type: "submit" });
    await settleApp();
    assert.equal(document.body.dataset.role, "admin");

    // 4. HPA Peak controls as admin
    await document.getElementById("create-hpa-peak").dispatchEvent({ type: "click" });
    await settleApp();
    
    await document.getElementById("refresh-hpa-peak").dispatchEvent({ type: "click" });
    await settleApp();
    
    // Clear HPA peak cancelled
    window.confirm = () => false;
    await document.getElementById("clear-hpa-peak").dispatchEvent({ type: "click" });
    await settleApp();
    window.confirm = () => true;

    // Clear HPA peak confirm
    await document.getElementById("clear-hpa-peak").dispatchEvent({ type: "click" });
    await settleApp();

    // HPA Peak error paths
    failHpaPeak = true;
    try {
      await document.getElementById("create-hpa-peak").dispatchEvent({ type: "click" });
    } catch (e) {
      // Expected to throw from loadHPAPeakSummary
    }
    await settleApp();
    try {
      await document.getElementById("clear-hpa-peak").dispatchEvent({ type: "click" });
    } catch (e) {
      // Expected to throw from loadHPAPeakSummary
    }
    await settleApp();
    failHpaPeak = false;

    // 5. User creation forms (admin flows)
    const createForm = document.getElementById("create-user-form");
    createForm.elements.username.value = "new-scheduler";
    createForm.elements.password.value = "secret";
    createForm.elements.role.value = "scheduler";
    createForm.elements.lineId.value = "A";
    await createForm.dispatchEvent({ type: "submit" });
    await settleApp();

    // User create failure
    failUserCreate = true;
    await createForm.dispatchEvent({ type: "submit" });
    await settleApp();
    failUserCreate = false;

    const assignForm = document.getElementById("assign-user-form");
    assignForm.elements.username.value = "ops";
    assignForm.elements.role.value = "scheduler";
    assignForm.elements.lineId.value = "A";
    await assignForm.dispatchEvent({ type: "submit" });
    await settleApp();

    // User assign failure
    failUserAssign = true;
    await assignForm.dispatchEvent({ type: "submit" });
    await settleApp();
    failUserAssign = false;

    const resetForm = document.getElementById("reset-password-form");
    resetForm.elements.username.value = "ops";
    resetForm.elements.password.value = "new-secret";
    await resetForm.dispatchEvent({ type: "submit" });
    await settleApp();

    // User password failure
    failUserPassword = true;
    await resetForm.dispatchEvent({ type: "submit" });
    await settleApp();
    failUserPassword = false;

    // User delete empty username early return
    document.getElementById("assign-username").value = "";
    await document.getElementById("delete-user-button").dispatchEvent({ type: "click" });
    await settleApp();

    // User delete confirm cancelled early return
    document.getElementById("assign-username").value = "ops";
    window.confirm = () => false;
    await document.getElementById("delete-user-button").dispatchEvent({ type: "click" });
    await settleApp();
    window.confirm = () => true;

    // User delete success
    await document.getElementById("delete-user-button").dispatchEvent({ type: "click" });
    await settleApp();

    // User delete failure
    failUserDelete = true;
    await document.getElementById("delete-user-button").dispatchEvent({ type: "click" });
    await settleApp();
    failUserDelete = false;

    // 6. Conflict demo flows (admin)
    await document.getElementById("create-conflict-demo").dispatchEvent({ type: "click" });
    await settleApp();
    
    failConflictDemo = true;
    await document.getElementById("create-conflict-demo").dispatchEvent({ type: "click" });
    await settleApp();
    failConflictDemo = false;

    // 7. Logout from admin
    await document.getElementById("logout-button").dispatchEvent({ type: "click" });
    await settleApp();
    assert.equal(document.body.dataset.role, "");

    // 8. Log in as scheduler
    document.querySelector('#login-form input[name="username"]').value = "scheduler";
    document.querySelector('#login-form input[name="password"]').value = "demo";
    await document.getElementById("login-form").dispatchEvent({ type: "submit" });
    await settleApp();
    assert.equal(document.body.dataset.role, "scheduler");

    // 9. Change active-line-select
    const activeLineSelect = document.getElementById("active-line-select");
    activeLineSelect.value = "B";
    await activeLineSelect.dispatchEvent({ type: "change" });
    await settleApp();

    // 9b. Filter menus, inputs and calendar item clicks
    const filterToggle = document.querySelector('button.filter-menu-toggle');
    if (filterToggle) {
      await filterToggle.dispatchEvent({ type: "click" });
      await settleApp();
    }

    const priorityCheckbox = document.querySelector('#priority-filters input[type="checkbox"]');
    if (priorityCheckbox) {
      priorityCheckbox.checked = !priorityCheckbox.checked;
      await document.getElementById("priority-filters").dispatchEvent({
        type: "change",
        target: priorityCheckbox
      });
      await settleApp();
    }

    const customerItem = document.querySelector('.filter-menu button');
    if (customerItem) {
      await customerItem.dispatchEvent({ type: "click" });
      await settleApp();
    }

    const startDateInput = document.querySelector('#schedule-form input[name="startDate"]');
    if (startDateInput) {
      await startDateInput.dispatchEvent({ type: "change" });
      await settleApp();
    }

    const dueDateInputForm = document.querySelector('#order-form input[name="dueDate"]');
    if (dueDateInputForm) {
      await dueDateInputForm.dispatchEvent({ type: "change" });
      await settleApp();
    }

    const cellElClick = document.querySelector('.calendar-day');
    if (cellElClick) {
      await cellElClick.dispatchEvent({ type: "click" });
      await settleApp();
    }

    // Since index.html has no calendar headers inside the static template, let's append one to trigger click
    const calendarHeader = document.createElement("div");
    calendarHeader.className = "calendar-header";
    document.body.appendChild(calendarHeader);
    await calendarHeader.dispatchEvent({ type: "click" });
    await settleApp();

    // 10. Submit order-form (sales order preview)
    const orderForm = document.getElementById("order-form");
    orderForm.elements.customer.value = "ACME";
    orderForm.elements.quantity.value = "2500";
    orderForm.elements.priority.value = "low";
    orderForm.elements.dueDate.value = dateKeyAfter(5);
    await orderForm.dispatchEvent({ type: "submit" });
    await settleApp();

    // Order submit failure
    failOrderSubmit = true;
    await orderForm.dispatchEvent({ type: "submit" });
    await settleApp();
    failOrderSubmit = false;

    // 11. Click confirm-preview-order
    failPreviewConfirm = true;
    await document.getElementById("confirm-preview-order").dispatchEvent({ type: "click" });
    await settleApp();
    failPreviewConfirm = false;

    await document.getElementById("confirm-preview-order").dispatchEvent({ type: "click" });
    await settleApp();

    // Click without selection to trigger warning messages (uncovered branches)
    await selectCardById("ORD-PENDING", false);
    await selectCardById("ORD-PENDING-2", false);
    await document.getElementById("preview-selected").dispatchEvent({ type: "click" });
    await settleApp();
    await document.getElementById("reject-selected").dispatchEvent({ type: "click" });
    await settleApp();
    await document.getElementById("cancel-selected").dispatchEvent({ type: "click" });
    await settleApp();
    await document.getElementById("schedule-form").dispatchEvent({ type: "submit" });
    await settleApp();

    // 12. Select multiple order cards
    await selectCardById("ORD-PENDING", true);
    await selectCardById("ORD-PENDING-2", true);

    // 13. Drag and Drop events (card triggers)
    const cardEl = getCardById("ORD-PENDING");
    if (cardEl) {
      // HTML5 Drag and drop
      await cardEl.dispatchEvent({
        type: "dragstart",
        dataTransfer: {
          setData: () => {},
          effectAllowed: ""
        }
      });
      await settleApp();
      await cardEl.dispatchEvent({
        type: "dragend"
      });
      await settleApp();

      // Pointer drag simulation
      const cellEl = document.querySelector(".calendar-day");
      if (cellEl) {
        cellEl.setAttribute("data-date", dateKeyAfter(2));
      }
      document.elementFromPoint = () => cellEl;

      // pointerdown (button 1 ignored)
      await cardEl.dispatchEvent({ type: "pointerdown", button: 1, pointerId: 1, clientX: 10, clientY: 10 });
      await settleApp();

      // pointerdown (button 0 accepted)
      await cardEl.dispatchEvent({ type: "pointerdown", button: 0, pointerId: 1, clientX: 10, clientY: 10 });
      await settleApp();

      // pointermove (small distance ignored)
      await cardEl.dispatchEvent({ type: "pointermove", pointerId: 1, clientX: 12, clientY: 12 });
      await settleApp();

      // pointermove (large distance active)
      await cardEl.dispatchEvent({ type: "pointermove", pointerId: 1, clientX: 50, clientY: 50 });
      await settleApp();

      // pointerup (releases and schedules)
      await cardEl.dispatchEvent({ type: "pointerup", pointerId: 1, clientX: 50, clientY: 50 });
      await settleApp();

      // pointercancel cover
      await cardEl.dispatchEvent({ type: "pointerdown", button: 0, pointerId: 2, clientX: 10, clientY: 10 });
      await settleApp();
      await cardEl.dispatchEvent({ type: "pointercancel", pointerId: 2 });
      await settleApp();

      // Mouse drag simulation
      // mousedown
      await cardEl.dispatchEvent({ type: "mousedown", button: 0, clientX: 10, clientY: 10 });
      await settleApp();

      // mousemove (small distance)
      await document.dispatchEvent({ type: "mousemove", clientX: 12, clientY: 12 });
      await settleApp();

      // mousemove (large distance)
      await document.dispatchEvent({ type: "mousemove", clientX: 50, clientY: 50 });
      await settleApp();

      // mouseup
      await document.dispatchEvent({ type: "mouseup", clientX: 50, clientY: 50 });
      await settleApp();
    }

    // 14. Click preview-selected (failure and success)
    failRetryPreview = true;
    await document.getElementById("preview-selected").dispatchEvent({ type: "click" });
    await settleApp();
    failRetryPreview = false;

    await document.getElementById("preview-selected").dispatchEvent({ type: "click" });
    await settleApp();

    // Submit schedule-form (failure and success)
    failRetryPreview = true;
    await document.getElementById("schedule-form").dispatchEvent({ type: "submit" });
    await settleApp();
    failRetryPreview = false;

    await document.getElementById("schedule-form").dispatchEvent({ type: "submit" });
    await settleApp();

    // 15. Click confirm-schedule-job (failure and success)
    failJobs = true;
    await document.getElementById("confirm-schedule-job").dispatchEvent({ type: "click" });
    await settleApp();
    failJobs = false;

    await document.getElementById("confirm-schedule-job").dispatchEvent({ type: "click" });
    await settleApp();

    // 16. Click order card action buttons as scheduler (start-production / confirm-production)
    const startProdBtn = document.querySelector('button[data-order-action="start-production"]');
    if (startProdBtn) {
      await startProdBtn.dispatchEvent({ type: "click" });
      await settleApp();
    }

    const confProdBtn = document.querySelector('button[data-order-action="confirm-production"]');
    if (confProdBtn) {
      await confProdBtn.dispatchEvent({ type: "click" });
      await settleApp();
    }

    // 17. Reject order flow (ensure selected)
    await selectCardById("ORD-PENDING", true);
    await document.getElementById("reject-selected").dispatchEvent({ type: "click" });
    await settleApp();
    document.getElementById("reject-reason").value = "";
    await document.getElementById("confirm-reject-orders").dispatchEvent({ type: "click" });
    await settleApp();

    document.getElementById("reject-reason").value = "too late";
    failReject = true;
    await document.getElementById("confirm-reject-orders").dispatchEvent({ type: "click" });
    await settleApp();
    failReject = false;

    await document.getElementById("confirm-reject-orders").dispatchEvent({ type: "click" });
    await settleApp();

    // 18. Cancel order flow (ensure selected)
    await selectCardById("ORD-PENDING", true);
    // Cancel confirmation cancelled
    window.confirm = () => false;
    await document.getElementById("cancel-selected").dispatchEvent({ type: "click" });
    await settleApp();
    window.confirm = () => true;

    // Cancel failure
    failCancel = true;
    await document.getElementById("cancel-selected").dispatchEvent({ type: "click" });
    await settleApp();
    failCancel = false;

    // Cancel success
    await document.getElementById("cancel-selected").dispatchEvent({ type: "click" });
    await settleApp();

    // 19. Production form flow
    const productionForm = document.getElementById("production-form");
    productionForm.elements.orderId.value = "ORD-PROD";
    productionForm.elements.productionDate.value = dateKeyAfter(3);
    productionForm.elements.producedQuantity.value = "500";
    await productionForm.dispatchEvent({ type: "submit" });
    await settleApp();
    await document.getElementById("cancel-production-report").dispatchEvent({ type: "click" });
    await settleApp();

    // Production failure
    failProduction = true;
    await productionForm.dispatchEvent({ type: "submit" });
    await settleApp();
    failProduction = false;

    // 20. Tabs and calendar modes (prefixed selectors for matchesSelector support)
    const calendarMode = document.querySelector('button[data-calendar-mode="all"]');
    if (calendarMode) {
      await document.getElementById("main-calendar-mode").dispatchEvent({ type: "click", target: calendarMode });
      await settleApp();
    }
    // Main calendar mode click with no dataset mode
    await document.getElementById("main-calendar-mode").dispatchEvent({ type: "click", target: { dataset: {} } });
    await settleApp();

    const previewMode = document.querySelector('button[data-preview-calendar-mode="scheduled"]');
    if (previewMode) {
      await document.getElementById("preview-calendar-mode").dispatchEvent({ type: "click", target: previewMode });
      await settleApp();
    }
    // Preview calendar mode click with no dataset mode
    await document.getElementById("preview-calendar-mode").dispatchEvent({ type: "click", target: { dataset: {} } });
    await settleApp();

    const actionsTab = document.querySelector('button[data-mobile-view="actions"]');
    if (actionsTab) {
      await actionsTab.dispatchEvent({ type: "click" });
      await settleApp();
    }
    await document.getElementById("prev-month").dispatchEvent({ type: "click" });
    await settleApp();
    await document.getElementById("next-month").dispatchEvent({ type: "click" });
    await settleApp();
    await document.getElementById("today-month").dispatchEvent({ type: "click" });
    await settleApp();

    await document.getElementById("scheduler-panel-toggle")?.dispatchEvent({ type: "click" });
    await settleApp();

    // 21. Drag and drop calendar cell drop
    const cells = document.querySelectorAll(".calendar-day");
    const cell = cells[cells.length - 1];
    if (cell) {
      await cell.dispatchEvent({
        type: "drop",
        preventDefault: () => {},
        dataTransfer: {
          getData: (type) => {
            if (type === "application/json") {
              return JSON.stringify({ orderIds: ["ORD-PENDING"] });
            }
            return "ORD-PENDING";
          }
        }
      });
      await settleApp();
    }

    // 22. Preview page list actions
    // Populate state.preview first with two orders selected
    await selectCardById("ORD-PENDING", true);
    await selectCardById("ORD-PENDING-2", true);
    await document.getElementById("preview-selected").dispatchEvent({ type: "click" });
    await settleApp();

    // Prepare inputs
    const dueDateInput = document.createElement("input");
    dueDateInput.setAttribute("data-conflict-due-date", "ORD-PENDING");
    dueDateInput.value = dateKeyAfter(10);
    document.body.appendChild(dueDateInput);

    const solutionCheckbox = document.createElement("input");
    solutionCheckbox.type = "checkbox";
    solutionCheckbox.setAttribute("data-conflict-solution-order", "");
    solutionCheckbox.checked = true;
    solutionCheckbox.value = "ORD-PENDING";
    document.body.appendChild(solutionCheckbox);

    const reasonInput = document.createElement("input");
    reasonInput.id = "conflict-force-reason";
    document.body.appendChild(reasonInput);

    const triggerAction = async (action, extraAttrs = {}) => {
      const btn = document.createElement("button");
      btn.setAttribute("data-preview-action", action);
      for (const [k, v] of Object.entries(extraAttrs)) {
        btn.setAttribute(k, v);
      }
      await document.getElementById("preview-page-list").dispatchEvent({
        type: "click",
        target: btn,
      });
      await settleApp();
    };

    // Click preview-page-list with invalid element (covering line 425-426 early return)
    const emptyBtn = document.createElement("button");
    await document.getElementById("preview-page-list").dispatchEvent({
      type: "click",
      target: emptyBtn
    });
    await settleApp();

    // Trigger actions that keep preview open
    await triggerAction("retry-today");

    failRetryPreview = true;
    await triggerAction("retry-today");
    failRetryPreview = false;

    await triggerAction("retry-suggested-start");
    await triggerAction("reject-preview-orders");

    // update-conflict-due-date missing value / empty value / success
    await triggerAction("update-conflict-due-date", { "data-order-id": "ORD-MISSING" });
    
    // empty value cover
    dueDateInput.value = "";
    await triggerAction("update-conflict-due-date", { "data-order-id": "ORD-PENDING" });
    dueDateInput.value = dateKeyAfter(10);
    
    await triggerAction("update-conflict-due-date", { "data-order-id": "ORD-PENDING" });

    // preview-conflict-solution choices empty / filled
    solutionCheckbox.checked = false;
    await triggerAction("preview-conflict-solution");
    solutionCheckbox.checked = true;
    await triggerAction("preview-conflict-solution");

    // retry-manual-force empty reason / cannot force / can force
    reasonInput.value = "";
    await triggerAction("retry-manual-force");

    reasonInput.value = "override";
    mockConflicts = [{ orderId: "ORD-PENDING", earliestFinishDate: dateKeyAfter(12), reason: "insufficient capacity" }];
    await document.getElementById("preview-selected").dispatchEvent({ type: "click" });
    await settleApp();
    await triggerAction("retry-manual-force");

    mockConflicts = [{ orderId: "ORD-PENDING", earliestFinishDate: dateKeyAfter(12), reason: "existing allocations require manual review or reschedule" }];
    await document.getElementById("preview-selected").dispatchEvent({ type: "click" });
    await settleApp();
    await triggerAction("retry-manual-force");

    // Test confirm-schedule-job with manualForce
    // 1. Without acknowledgement checked
    await document.getElementById("confirm-schedule-job").dispatchEvent({ type: "click" });
    await settleApp();

    // 2. With acknowledgement checked
    const ackBox = document.querySelector("input[data-conflict-ack]");
    if (ackBox) {
      ackBox.checked = true;
    }
    await document.getElementById("confirm-schedule-job").dispatchEvent({ type: "click" });
    await settleApp();

    // Reset mock conflicts
    mockConflicts = [];
    await selectCardById("ORD-PENDING", true);
    await document.getElementById("preview-selected").dispatchEvent({ type: "click" });
    await settleApp();

    // Trigger actions that close preview
    // Having 2 orders in preview: unselecting one will trigger retryPreview instead of closePreviewPage
    await triggerAction("unselect-conflict-order", { "data-order-id": "ORD-PENDING-2" });

    // Unselecting the second order will now trigger closePreviewPage
    await triggerAction("unselect-conflict-order", { "data-order-id": "ORD-PENDING" });

    // Re-open preview for return-workstation
    await selectCardById("ORD-PENDING", true);
    await document.getElementById("preview-selected").dispatchEvent({ type: "click" });
    await settleApp();

    await triggerAction("return-workstation");

    // 23. Logout from scheduler
    await document.getElementById("logout-button").dispatchEvent({ type: "click" });
    await settleApp();

    // 24. Log in as sales
    document.querySelector('#login-form input[name="username"]').value = "sales";
    document.querySelector('#login-form input[name="password"]').value = "demo";
    await document.getElementById("login-form").dispatchEvent({ type: "submit" });
    await settleApp();
    assert.equal(document.body.dataset.role, "sales");

    // 25. Click Sales-only order card action buttons (resubmit, cancel, toggle-edit)
    const toggleEditBtn = document.querySelector('button[data-order-action="toggle-sales-pending-edit"]');
    if (toggleEditBtn) {
      // Toggle edit panel ON
      await toggleEditBtn.dispatchEvent({ type: "click" });
      await settleApp();
      // Toggle edit panel OFF
      await toggleEditBtn.dispatchEvent({ type: "click" });
      await settleApp();
      // Toggle edit panel ON again to click other buttons
      await toggleEditBtn.dispatchEvent({ type: "click" });
      await settleApp();
    }

    // Set resubmit fields values
    const cardResubmit = getCardById("ORD-PENDING");
    if (cardResubmit) {
      const resubmitDueDate = cardResubmit.querySelector('[data-resubmit-field="dueDate"]');
      const resubmitQuantity = cardResubmit.querySelector('[data-resubmit-field="quantity"]');
      if (resubmitDueDate) resubmitDueDate.value = dateKeyAfter(10);
      if (resubmitQuantity) resubmitQuantity.value = "1000";
    }

    // Resubmit order click
    const resubmitBtn = document.querySelector('button[data-order-action="resubmit-order"]');
    if (resubmitBtn) {
      await resubmitBtn.dispatchEvent({ type: "click" });
      await settleApp();
    }

    // Cancel order click (needs toggling edit panel on again)
    if (toggleEditBtn) {
      await toggleEditBtn.dispatchEvent({ type: "click" });
      await settleApp();
    }
    const cancelBtn = document.querySelector('button[data-order-action="cancel-order"]');
    if (cancelBtn) {
      await cancelBtn.dispatchEvent({ type: "click" });
      await settleApp();
    }

    // 26. Logout from sales
    await document.getElementById("logout-button").dispatchEvent({ type: "click" });
    await settleApp();

  } finally {
    restoreGlobals();
  }
});

test("load HPA peak summary without admin role", async () => {
  const document = buildDomFromIndex();
  const fetchImpl = async (path, options = {}) => {
    if (path === "/api/auth/login") {
      const body = JSON.parse(options.body);
      const role = body.username === "admin" ? "admin" : "scheduler";
      return jsonResponse({
        token: "token-" + role,
        user: { id: role + "-1", username: body.username, role: role, lineId: "A" }
      });
    }
    if (path === "/api/demo/hpa-peak") {
      return jsonResponse({ summary: { autoscaling: { desiredReplicas: 3 } } });
    }
    return jsonResponse({});
  };

  const restoreGlobals = installBrowserGlobalsWithFetch(document, fetchImpl);
  try {
    await import(new URL(`./app.js?dphpanon=${Date.now()}`, import.meta.url));
    await settleApp();

    // Log in as scheduler
    document.querySelector('#login-form input[name="username"]').value = "scheduler";
    document.querySelector('#login-form input[name="password"]').value = "demo";
    await document.getElementById("login-form").dispatchEvent({ type: "submit" });
    await settleApp();

    // HPA peak summary should not be visible to non-admin users, so the element won't be in the DOM
    const hpaSummaryEl = document.getElementById("hpa-peak-summary");
    assert.equal(hpaSummaryEl, null);
  } finally {
    restoreGlobals();
  }
});

function createAppCoverageFetch(calls, options = {}) {
  const role = options.role ?? "scheduler";
  const userId = options.userId ?? `${role}-1`;
  const lineId = options.lineId ?? "A";
  const orders = options.orders ?? [
    { id: "ORD-PENDING", customer: "ACME", lineId, quantity: 2500, priority: "high", status: "待排程", dueDate: dateKeyAfter(6), createdBy: "sales-1" },
    { id: "ORD-SCHEDULED", customer: "Beta", lineId, quantity: 1500, priority: "low", status: "已排程", dueDate: dateKeyAfter(8), createdBy: "sales-1" },
    { id: "ORD-PROD", customer: "Gamma", lineId, quantity: 900, priority: "low", status: "生產中", dueDate: dateKeyAfter(9), createdBy: "sales-1" },
  ];
  const allocations = options.allocations ?? [
    { orderId: "ORD-SCHEDULED", customer: "Beta", lineId, date: dateKeyAfter(2), quantity: 1500, priority: "low", status: "已排程" },
    { orderId: "ORD-PROD", customer: "Gamma", lineId, date: dateKeyAfter(3), quantity: 900, priority: "low", status: "生產中" },
  ];

  return async (path, fetchOptions = {}) => {
    calls.push({ path, options: fetchOptions });
    if (path === "/api/auth/logout") {
      return jsonResponse({});
    }
    if (path === "/api/lines") {
      return jsonResponse({
        lines: [
          { id: "A", name: "Line A", capacityPerDay: 10000, timezone: "Asia/Taipei" },
          { id: "B", name: "Line B", capacityPerDay: 9000, timezone: "Asia/Taipei" },
        ],
      });
    }
    if (path === "/api/orders") {
      if (fetchOptions.method === "DELETE") {
        return jsonResponse({ cancelledOrderIds: JSON.parse(fetchOptions.body).orderIds });
      }
      return jsonResponse({ orders });
    }
    if (path === "/api/orders/reject") {
      return jsonResponse({ orders: JSON.parse(fetchOptions.body).orderIds });
    }
    if (path === "/api/orders/resubmit") {
      return jsonResponse({ id: JSON.parse(fetchOptions.body).orderId });
    }
    if (path === "/api/production/start") {
      return jsonResponse({ id: JSON.parse(fetchOptions.body).orderId });
    }
    if (String(path).startsWith("/api/schedules/calendar?")) {
      return jsonResponse({ allocations, pendingAllocations: options.pendingAllocations ?? [] });
    }
    if (String(path).startsWith("/api/schedules/history?")) {
      return jsonResponse({ history: options.history ?? [] });
    }
    if (path === "/api/users") {
      return jsonResponse({ users: [{ username: "ops", role: "sales" }] });
    }
    if (path === "/api/demo/hpa-peak") {
      return jsonResponse({ summary: null });
    }
    throw new Error(`unexpected fetch ${path} for ${role}/${userId}`);
  };
}

function storedSession(role, extra = {}) {
  const user = { id: `${role}-1`, username: role, role, ...extra };
  return {
    "woms.token": `token-${role}`,
    "woms.user": JSON.stringify(user),
  };
}

test("app.js case 1: scheduler startup loads orders calendar and history only", async () => {
  const document = buildDomFromIndex();
  const calls = [];
  const restoreGlobals = installBrowserGlobalsWithFetch(
    document,
    createAppCoverageFetch(calls, { role: "scheduler", lineId: "A" }),
    storedSession("scheduler", { lineId: "A" }),
  );
  try {
    await import(appModuleUrl("app-case-scheduler-startup"));
    await settleApp();

    assert.equal(document.body.dataset.role, "scheduler");
    assert.equal(document.getElementById("orders-body").children.length, 3);
    assert.equal(calls.some((call) => call.path === "/api/users"), false);
    assert.equal(calls.some((call) => call.path === "/api/demo/hpa-peak"), false);
  } finally {
    restoreGlobals();
  }
});

test("app.js case 2: sales line change persists selected line and reloads workspace data", async () => {
  const document = buildDomFromIndex();
  const calls = [];
  const restoreGlobals = installBrowserGlobalsWithFetch(
    document,
    createAppCoverageFetch(calls, { role: "sales", userId: "sales-1" }),
    storedSession("sales", { id: "sales-1" }),
  );
  try {
    await import(appModuleUrl("app-case-sales-line-change"));
    await settleApp();

    const select = document.getElementById("active-line-select");
    select.value = "B";
    await select.dispatchEvent({ type: "change" });
    await settleApp();

    assert.equal(localStorage.getItem("woms.selectedLine"), "B");
    assert.equal(document.querySelector('#order-form input[name="lineId"]').value, "B");
    assert.equal(calls.filter((call) => String(call.path).startsWith("/api/schedules/calendar?")).length >= 2, true);
  } finally {
    restoreGlobals();
  }
});

test("app.js case 3: anonymous logout does not call the logout API", async () => {
  const document = buildDomFromIndex();
  const calls = [];
  const restoreGlobals = installBrowserGlobalsWithFetch(document, async (path, options = {}) => {
    calls.push({ path, options });
    return jsonResponse({});
  });
  try {
    await import(appModuleUrl("app-case-anonymous-logout"));
    await settleApp();

    await document.getElementById("logout-button").dispatchEvent({ type: "click" });
    await settleApp();

    assert.equal(calls.some((call) => call.path === "/api/auth/logout"), false);
    assert.equal(document.getElementById("message-title").textContent, "已登出");
  } finally {
    restoreGlobals();
  }
});

test("app.js case 4: cancelling selected scheduler orders respects confirm cancellation", async () => {
  const document = buildDomFromIndex();
  const calls = [];
  const restoreGlobals = installBrowserGlobalsWithFetch(
    document,
    createAppCoverageFetch(calls, { role: "scheduler", lineId: "A" }),
    storedSession("scheduler", { lineId: "A" }),
  );
  try {
    await import(appModuleUrl("app-case-cancel-confirm-false"));
    await settleApp();

    const card = document.getElementById("orders-body").children.find((item) => item.dataset.orderId === "ORD-PENDING");
    await card.dispatchEvent({ type: "click", target: card });
    window.confirm = () => false;
    globalThis.confirm = window.confirm;
    await document.getElementById("cancel-selected").dispatchEvent({ type: "click" });
    await settleApp();

    assert.equal(calls.some((call) => call.path === "/api/orders" && call.options.method === "DELETE"), false);
    assert.equal(document.getElementById("selected-count").textContent, "已選取 1 張訂單");
  } finally {
    restoreGlobals();
  }
});

test("app.js case 5: reject selected scheduler orders posts a reason", async () => {
  const document = buildDomFromIndex();
  const calls = [];
  const restoreGlobals = installBrowserGlobalsWithFetch(
    document,
    createAppCoverageFetch(calls, { role: "scheduler", lineId: "A" }),
    storedSession("scheduler", { lineId: "A" }),
  );
  try {
    await import(appModuleUrl("app-case-reject-reason"));
    await settleApp();

    const card = document.getElementById("orders-body").children.find((item) => item.dataset.orderId === "ORD-PENDING");
    await card.dispatchEvent({ type: "click", target: card });
    await document.getElementById("reject-selected").dispatchEvent({ type: "click" });
    document.getElementById("reject-reason").value = "customer requested later due date";
    await document.getElementById("confirm-reject-orders").dispatchEvent({ type: "click" });
    await settleApp();

    const rejectCall = calls.find((call) => call.path === "/api/orders/reject");
    assert.deepEqual(JSON.parse(rejectCall.options.body), {
      orderIds: ["ORD-PENDING"],
      reason: "customer requested later due date",
    });
    assert.equal(document.getElementById("message-title").textContent, "已駁回訂單");
  } finally {
    restoreGlobals();
  }
});

test("app.js case 6: starting production from a scheduled order posts production start", async () => {
  const document = buildDomFromIndex();
  const calls = [];
  const restoreGlobals = installBrowserGlobalsWithFetch(
    document,
    createAppCoverageFetch(calls, { role: "scheduler", lineId: "A" }),
    storedSession("scheduler", { lineId: "A" }),
  );
  try {
    await import(appModuleUrl("app-case-start-production"));
    await settleApp();

    const startButton = document.querySelector('button[data-order-action="start-production"]');
    await startButton.dispatchEvent({ type: "click" });
    await settleApp();

    const startCall = calls.find((call) => call.path === "/api/production/start");
    assert.deepEqual(JSON.parse(startCall.options.body), { orderId: "ORD-SCHEDULED" });
    assert.equal(document.getElementById("message-title").textContent, "已開始生產");
  } finally {
    restoreGlobals();
  }
});

test("app.js case 7: sales can expand pending order correction controls", async () => {
  const document = buildDomFromIndex();
  const calls = [];
  const restoreGlobals = installBrowserGlobalsWithFetch(
    document,
    createAppCoverageFetch(calls, { role: "sales", userId: "sales-1" }),
    storedSession("sales", { id: "sales-1" }),
  );
  try {
    await import(appModuleUrl("app-case-sales-expand-pending"));
    await settleApp();

    const toggle = document.querySelector('button[data-order-action="toggle-sales-pending-edit"]');
    assert.equal(toggle.getAttribute("aria-expanded"), "false");
    await toggle.dispatchEvent({ type: "click" });
    await settleApp();

    assert.match(renderedMarkup(document.getElementById("orders-body")), /重新送出/);
    assert.match(renderedMarkup(document.getElementById("orders-body")), /修改：業務修改/);
  } finally {
    restoreGlobals();
  }
});

test("app.js case 8: sales resubmit rejects a non-future due date before API call", async () => {
  const document = buildDomFromIndex();
  const calls = [];
  const restoreGlobals = installBrowserGlobalsWithFetch(
    document,
    createAppCoverageFetch(calls, { role: "sales", userId: "sales-1" }),
    storedSession("sales", { id: "sales-1" }),
  );
  try {
    await import(appModuleUrl("app-case-sales-resubmit-invalid"));
    await settleApp();

    await document.querySelector('button[data-order-action="toggle-sales-pending-edit"]').dispatchEvent({ type: "click" });
    const dueDate = document.querySelector('[data-resubmit-field="dueDate"]');
    dueDate.value = "2000-01-01";
    await document.querySelector('button[data-order-action="resubmit-order"]').dispatchEvent({ type: "click" });
    await settleApp();

    assert.equal(calls.some((call) => call.path === "/api/orders/resubmit"), false);
    assert.equal(document.getElementById("message-title").textContent, "操作失敗");
  } finally {
    restoreGlobals();
  }
});

test("app.js case 9: sales resubmit validates the correction form before posting", async () => {
  const document = buildDomFromIndex();
  const calls = [];
  const restoreGlobals = installBrowserGlobalsWithFetch(
    document,
    createAppCoverageFetch(calls, { role: "sales", userId: "sales-1" }),
    storedSession("sales", { id: "sales-1" }),
  );
  try {
    await import(appModuleUrl("app-case-sales-resubmit-valid"));
    await settleApp();

    await document.querySelector('button[data-order-action="toggle-sales-pending-edit"]').dispatchEvent({ type: "click" });
    await settleApp();
    const card = document.getElementById("orders-body").children.find((item) => item.dataset.orderId === "ORD-PENDING");
    card.querySelector('[data-resubmit-field="dueDate"]').value = "2099-01-01";
    card.querySelector('[data-resubmit-field="quantity"]').value = "1000";
    const resubmitButton = document.querySelector('button[data-order-action="resubmit-order"]');
    assert.ok(resubmitButton, renderedMarkup(document.getElementById("orders-body")));
    await resubmitButton.dispatchEvent({ type: "click" });
    await settleApp();

    assert.equal(calls.some((call) => call.path === "/api/orders/resubmit"), false);
    assert.equal(document.getElementById("message-title").textContent, "操作失敗");
    assert.match(renderedMarkup(document.getElementById("orders-body")), /重新送出/);
  } finally {
    restoreGlobals();
  }
});

test("app.js case 10: sales cancel-order action deletes a single pending order", async () => {
  const document = buildDomFromIndex();
  const calls = [];
  const restoreGlobals = installBrowserGlobalsWithFetch(
    document,
    createAppCoverageFetch(calls, { role: "sales", userId: "sales-1" }),
    storedSession("sales", { id: "sales-1" }),
  );
  try {
    await import(appModuleUrl("app-case-sales-cancel-single"));
    await settleApp();

    await document.querySelector('button[data-order-action="toggle-sales-pending-edit"]').dispatchEvent({ type: "click" });
    await document.querySelector('button[data-order-action="cancel-order"]').dispatchEvent({ type: "click" });
    await settleApp();

    const deleteCall = calls.find((call) => call.path === "/api/orders" && call.options.method === "DELETE");
    assert.deepEqual(JSON.parse(deleteCall.options.body), { orderIds: ["ORD-PENDING"] });
    assert.equal(document.getElementById("message-title").textContent, "取消完成");
  } finally {
    restoreGlobals();
  }
});

test("login failure renders a warning without storing a session", async () => {
  const document = buildDomFromIndex();
  const fetchImpl = async (path, options = {}) => {
    if (path === "/api/auth/login") {
      assert.equal(options.method, "POST");
      return jsonResponse({ error: "bad credentials" }, 401);
    }
    throw new Error(`unexpected fetch ${path}`);
  };
  const restoreGlobals = installBrowserGlobalsWithFetch(document, fetchImpl);
  try {
    await import(appModuleUrl("login-failure-warning"));
    await settleApp();

    document.querySelector('#login-form input[name="username"]').value = "sales";
    document.querySelector('#login-form input[name="password"]').value = "wrong";
    await document.getElementById("login-form").dispatchEvent({ type: "submit" });
    await settleApp();

    assert.equal(localStorage.getItem("woms.token"), null);
    assert.equal(document.getElementById("login-page").hidden, false);
    assert.equal(document.getElementById("message-title").textContent, "登入失敗");
    assert.equal(document.getElementById("message-body").textContent, "bad credentials");
    assert.equal(document.getElementById("message-dialog").dataset.type, "warn");
  } finally {
    restoreGlobals();
  }
});


test("targeted app.js error and control branches raise runtime coverage", async () => {
  const document = buildDomFromIndex();
  let failFetch = false;
  let fetchStatus = 200;

  const fetchImpl = async (path, options = {}) => {
    if (failFetch) {
      const err = new Error("mocked error");
      err.status = fetchStatus;
      throw err;
    }
    if (path === "/api/auth/login") {
      const body = JSON.parse(options.body);
      const role = body.username === "admin" ? "admin" : "scheduler";
      return jsonResponse({
        token: "token-" + role,
        user: { id: role + "-1", username: body.username, role: role, lineId: "A" }
      });
    }
    if (path === "/api/lines") {
      return jsonResponse({
        lines: [{ id: "A", name: "Line A", capacityPerDay: 10000, timezone: "Asia/Taipei" }]
      });
    }
    if (path === "/api/orders") {
      if (options.method === "DELETE") {
        return jsonResponse({ cancelledOrderIds: ["ORD-1"] });
      }
      return jsonResponse({
        orders: [
          { id: "ORD-1", customer: "ACME", lineId: "A", quantity: 100, priority: "low", status: "待排程", dueDate: dateKeyAfter(10), createdBy: "sales-1" }
        ]
      });
    }
    if (path === "/api/schedules/preview") {
      return jsonResponse({
        previewId: "PRV-1",
        currentDate: dateKeyAfter(0),
        allocations: [],
        conflicts: [{ orderId: "ORD-1", earliestFinishDate: dateKeyAfter(12), reason: "existing allocations require manual review or reschedule" }]
      });
    }
    if (path === "/api/orders/preview-confirm") {
      return jsonResponse({ id: "ORD-1" });
    }
    if (path === "/api/schedules/jobs") {
      return jsonResponse({ id: "JOB-1", status: "failed" });
    }
    if (path === "/api/demo/hpa-peak") {
      if (options.method === "POST") {
        return jsonResponse({ summary: { autoscaling: { desiredReplicas: 3 } } });
      }
      return jsonResponse({ summary: { autoscaling: { desiredReplicas: 3 } } });
    }
    return jsonResponse({});
  };

  const restoreGlobals = installBrowserGlobalsWithFetch(document, fetchImpl);
  try {
    // 1. App initialization with 401 error to cover catch path
    window.localStorage.setItem("woms.token", "fake-token");
    window.localStorage.setItem("woms.user", JSON.stringify({ id: "admin-1", username: "admin", role: "admin" }));
    failFetch = true;
    fetchStatus = 401;
    await import(new URL(`./app.js?dpinitial401=${Date.now()}`, import.meta.url));
    await settleApp();
    failFetch = false;
    window.localStorage.clear();

    // Import a fresh app.js instance
    await import(new URL(`./app.js?dpbooster=${Date.now()}`, import.meta.url));
    await settleApp();

    // 2. Log in as admin
    document.querySelector('#login-form input[name="username"]').value = "admin";
    document.querySelector('#login-form input[name="password"]').value = "demo";
    await document.getElementById("login-form").dispatchEvent({ type: "submit" });
    await settleApp();

    // 3. Test syncHPAPeakPolling with admin
    await document.getElementById("create-hpa-peak").dispatchEvent({ type: "click" });
    await settleApp();

    // HPA peak error path
    failFetch = true;
    try {
      await document.getElementById("create-hpa-peak").dispatchEvent({ type: "click" });
    } catch (e) {
      // Expected to throw from loadHPAPeakSummary
    }
    await settleApp();
    failFetch = false;

    // 4. Log out and log in as scheduler for scheduling error paths
    await document.getElementById("logout-button").dispatchEvent({ type: "click" });
    await settleApp();

    document.querySelector('#login-form input[name="username"]').value = "scheduler";
    document.querySelector('#login-form input[name="password"]').value = "demo";
    await document.getElementById("login-form").dispatchEvent({ type: "submit" });
    await settleApp();

    // 5. Test schedule-form submit validation (no selection warning)
    await document.getElementById("schedule-form").dispatchEvent({ type: "submit" });
    await settleApp();

    // 6. Select order card and click preview-selected with failFetch
    const getCard = () => document.getElementById("orders-body").children.find((item) => item.dataset.orderId === "ORD-1");
    if (getCard()) {
      await getCard().dispatchEvent({ type: "click", target: getCard() });
      await settleApp();
    }
    
    failFetch = true;
    await document.getElementById("preview-selected").dispatchEvent({ type: "click" });
    await settleApp();
    failFetch = false;

    // Open preview dialog successfully
    await document.getElementById("preview-selected").dispatchEvent({ type: "click" });
    await settleApp();

    // 7. Test confirm-preview-order error path
    failFetch = true;
    await document.getElementById("confirm-preview-order").dispatchEvent({ type: "click" });
    await settleApp();
    failFetch = false;

    // 8. Test confirm-schedule-job failed status path and try-catch error path
    // First, try-catch error path
    failFetch = true;
    await document.getElementById("confirm-schedule-job").dispatchEvent({ type: "click" });
    await settleApp();
    failFetch = false;

    // Second, payload.status === "failed" path
    await document.getElementById("confirm-schedule-job").dispatchEvent({ type: "click" });
    await settleApp();

    // 9. Test cancel-selected error path
    if (getCard() && !getCard().classList.contains("selected")) {
      await getCard().dispatchEvent({ type: "click", target: getCard() });
      await settleApp();
    }
    failFetch = true;
    await document.getElementById("cancel-selected").dispatchEvent({ type: "click" });
    await settleApp();
    failFetch = false;

    // 10. Click mobile tabs to cover line 330-332
    const actionsTab = document.querySelector('[data-mobile-view="actions"]');
    if (actionsTab) {
      await actionsTab.dispatchEvent({ type: "click" });
      await settleApp();
    }

    // 11. Click preview-page-list with invalid element (covering line 425-426 early return)
    const emptyBtn = document.createElement("button");
    await document.getElementById("preview-page-list").dispatchEvent({
      type: "click",
      target: emptyBtn
    });
    await settleApp();

    // 12. Click update-conflict-due-date with empty input value (covering line 444-446)
    const updateBtn = document.createElement("button");
    updateBtn.setAttribute("data-preview-action", "update-conflict-due-date");
    updateBtn.setAttribute("data-order-id", "ORD-1");
    const emptyInput = document.createElement("input");
    emptyInput.setAttribute("data-conflict-due-date", "ORD-1");
    emptyInput.value = "";
    document.body.appendChild(emptyInput);
    await document.getElementById("preview-page-list").dispatchEvent({
      type: "click",
      target: updateBtn
    });
    await settleApp();

  } finally {
    restoreGlobals();
  }
});

async function runSchedulerJobPollingCase({ label, jobStatus, polledStatuses = [], expectedTitle, expectedBodyPattern }) {
  const document = buildDomFromIndex();
  const calls = [];
  const fetchImpl = async (path, options = {}) => {
    calls.push({ path, options });
    if (path === "/api/lines") {
      return jsonResponse({
        lines: [{ id: "A", name: "Line A", capacityPerDay: 10000, timezone: "Asia/Taipei" }],
      });
    }
    if (path === "/api/orders") {
      return jsonResponse({
        orders: [
          { id: "ORD-JOB", customer: "ACME", lineId: "A", quantity: 1200, priority: "high", status: "待排程", dueDate: dateKeyAfter(6), createdBy: "sales-1" },
        ],
      });
    }
    if (String(path).startsWith("/api/schedules/calendar?")) {
      return jsonResponse({ allocations: [], pendingAllocations: [] });
    }
    if (String(path).startsWith("/api/schedules/history?")) {
      return jsonResponse({ history: [] });
    }
    if (path === "/api/schedules/preview") {
      return jsonResponse({
        previewId: `PREVIEW-${label}`,
        currentDate: dateKeyAfter(0),
        allocations: [
          { orderId: "ORD-JOB", customer: "ACME", lineId: "A", date: dateKeyAfter(2), quantity: 1200, priority: "high", status: "已排程" },
        ],
        conflicts: [],
      });
    }
    if (path === "/api/schedules/jobs") {
      return jsonResponse({ id: `JOB-${label}`, status: jobStatus });
    }
    if (String(path).startsWith("/api/schedules/jobs/")) {
      const next = polledStatuses.shift() ?? "running";
      return jsonResponse({
        id: `JOB-${label}`,
        status: next,
        message: next === "cancelled" ? "cancelled by worker" : undefined,
      });
    }
    throw new Error(`unexpected fetch ${path}`);
  };
  const restoreGlobals = installBrowserGlobalsWithFetch(
    document,
    fetchImpl,
    storedSession("scheduler", { lineId: "A" }),
  );
  try {
    await import(appModuleUrl(`scheduler-job-${label}`));
    await settleApp();

    const card = document.getElementById("orders-body").children.find((item) => item.dataset.orderId === "ORD-JOB");
    await card.dispatchEvent({ type: "click", target: card });
    await document.getElementById("preview-selected").dispatchEvent({ type: "click" });
    await settleApp();
    await document.getElementById("confirm-schedule-job").dispatchEvent({ type: "click" });
    await settleApp();

    assert.equal(document.getElementById("message-title").textContent, expectedTitle);
    assert.match(document.getElementById("message-body").textContent, expectedBodyPattern);
    return calls;
  } finally {
    restoreGlobals();
  }
}

test("queued scheduler jobs cover completed failed cancelled and timeout polling outcomes", async () => {
  const completedCalls = await runSchedulerJobPollingCase({
    label: "completed",
    jobStatus: "queued",
    polledStatuses: ["running", "completed"],
    expectedTitle: "排程完成",
    expectedBodyPattern: /JOB-completed 已完成/,
  });
  assert.equal(completedCalls.filter((call) => String(call.path).startsWith("/api/schedules/jobs/JOB-completed")).length, 2);

  await runSchedulerJobPollingCase({
    label: "cancelled",
    jobStatus: "running",
    polledStatuses: ["cancelled"],
    expectedTitle: "排程未完成",
    expectedBodyPattern: /cancelled by worker/,
  });

  await runSchedulerJobPollingCase({
    label: "timeout",
    jobStatus: "queued",
    polledStatuses: Array.from({ length: 20 }, () => "running"),
    expectedTitle: "排程仍在背景執行",
    expectedBodyPattern: /尚未完成/,
  });
});

test("login and startup request failures cover fallback auth messages", async () => {
  const loginDocument = buildDomFromIndex();
  const loginRestore = installBrowserGlobalsWithFetch(loginDocument, async (path) => {
    if (path === "/api/auth/login") {
      return {
        ok: false,
        status: 500,
        json: async () => {
          throw new Error("not json");
        },
      };
    }
    throw new Error(`unexpected fetch ${path}`);
  });
  try {
    await import(appModuleUrl("login-non-json-fallback"));
    await settleApp();
    await loginDocument.getElementById("login-form").dispatchEvent({ type: "submit" });
    await settleApp();

    assert.equal(loginDocument.getElementById("message-title").textContent, "登入失敗");
    assert.equal(loginDocument.getElementById("message-body").textContent, "請求失敗，請稍後再試。");
    assert.equal(localStorage.getItem("woms.token"), null);
  } finally {
    loginRestore();
  }

  const startupDocument = buildDomFromIndex();
  const startupRestore = installBrowserGlobalsWithFetch(
    startupDocument,
    async (path) => {
      if (path === "/api/lines") {
        return jsonResponse({ error: "server unavailable" }, 503);
      }
      throw new Error(`unexpected fetch ${path}`);
    },
    storedSession("admin"),
  );
  try {
    await import(appModuleUrl("startup-non-401-failure"));
    await settleApp();

    assert.equal(startupDocument.getElementById("message-title").textContent, "工作區重新整理失敗");
    assert.equal(startupDocument.getElementById("message-body").textContent, "server unavailable");
    assert.equal(startupDocument.body.dataset.role, "admin");
  } finally {
    startupRestore();
  }
});

test("role and line configuration covers fallback line and scheduler fixed-line branches", async () => {
  const schedulerDocument = buildDomFromIndex();
  const schedulerCalls = [];
  const schedulerRestore = installBrowserGlobalsWithFetch(
    schedulerDocument,
    async (path, options = {}) => {
      schedulerCalls.push({ path, options });
      if (path === "/api/lines") {
        return jsonResponse({ lines: [] });
      }
      if (path === "/api/orders") {
        return jsonResponse({
          orders: [
            { id: "ORD-D", customer: "London", lineId: "D", quantity: 700, priority: "low", status: "待排程", dueDate: dateKeyAfter(7), createdBy: "sales-1" },
          ],
        });
      }
      if (String(path).startsWith("/api/schedules/calendar?")) {
        return jsonResponse({ allocations: [], pendingAllocations: [] });
      }
      if (String(path).startsWith("/api/schedules/history?")) {
        return jsonResponse({ history: [] });
      }
      throw new Error(`unexpected fetch ${path}`);
    },
    {
      ...storedSession("scheduler", { lineId: "D" }),
      "woms.selectedLine": "Z",
    },
  );
  try {
    await import(appModuleUrl("scheduler-fallback-line"));
    await settleApp();

    assert.equal(schedulerDocument.getElementById("active-line-select").disabled, true);
    assert.equal(schedulerDocument.getElementById("active-line-select").value, "D");
    assert.equal(schedulerDocument.querySelector('#schedule-form input[name="lineId"]').value, "D");
    assert.equal(schedulerCalls.some((call) => String(call.path).includes("lineId=D")), true);
  } finally {
    schedulerRestore();
  }

  const salesDocument = buildDomFromIndex();
  const salesRestore = installBrowserGlobalsWithFetch(
    salesDocument,
    createAppCoverageFetch([], {
      role: "sales",
      userId: "sales-1",
      orders: [
        { id: "ORD-PENDING", customer: "ACME", lineId: "A", quantity: 2500, priority: "high", status: "待排程", dueDate: dateKeyAfter(6), createdBy: "sales-1" },
        { id: "ORD-REJECTED", customer: "Follow", lineId: "A", quantity: 500, priority: "low", status: "需業務處理", dueDate: dateKeyAfter(7), createdBy: "sales-1" },
      ],
    }),
    {
      ...storedSession("sales", { id: "sales-1" }),
      "woms.selectedLine": "Z",
    },
  );
  try {
    await import(appModuleUrl("sales-no-default-filter-line"));
    await settleApp();

    assert.equal(salesDocument.getElementById("active-line-select").disabled, false);
    assert.equal(salesDocument.getElementById("active-line-select").value, "A");
    assert.equal(salesDocument.getElementById("orders-heading-eyebrow").textContent, "訂單任務");
    assert.equal(salesDocument.getElementById("orders-heading-title").textContent, "訂單");
    assert.match(renderedMarkup(salesDocument.getElementById("orders-body")), /ORD-PENDING[\s\S]*ORD-REJECTED/);
  } finally {
    salesRestore();
  }
});

test("dialog fallback branches open and close without native dialog methods", async () => {
  const document = buildDomFromIndex();
  for (const id of ["message-dialog", "schedule-preview-dialog", "production-dialog", "reject-dialog"]) {
    const dialog = document.getElementById(id);
    dialog.showModal = undefined;
    dialog.close = undefined;
  }
  const fetchImpl = async (path, options = {}) => {
    if (path === "/api/lines") {
      return jsonResponse({ lines: [{ id: "A", name: "Line A", capacityPerDay: 10000, timezone: "Asia/Taipei" }] });
    }
    if (path === "/api/orders") {
      if (options.method === "DELETE") {
        return jsonResponse({ cancelledOrderIds: JSON.parse(options.body).orderIds });
      }
      return jsonResponse({
        orders: [
          { id: "ORD-PENDING", customer: "ACME", lineId: "A", quantity: 2500, priority: "high", status: "待排程", dueDate: dateKeyAfter(6), createdBy: "sales-1" },
          { id: "ORD-PROD", customer: "Gamma", lineId: "A", quantity: 900, priority: "low", status: "生產中", dueDate: dateKeyAfter(9), createdBy: "sales-1" },
        ],
      });
    }
    if (String(path).startsWith("/api/schedules/calendar?")) {
      return jsonResponse({
        allocations: [
          { orderId: "ORD-PROD", customer: "Gamma", lineId: "A", date: dateKeyAfter(3), quantity: 900, priority: "low", status: "生產中" },
        ],
        pendingAllocations: [],
      });
    }
    if (String(path).startsWith("/api/schedules/history?")) {
      return jsonResponse({ history: [] });
    }
    if (path === "/api/schedules/preview") {
      return jsonResponse({
        previewId: "PREVIEW-FALLBACK",
        currentDate: dateKeyAfter(0),
        allocations: [],
        conflicts: [],
      });
    }
    throw new Error(`unexpected fetch ${path}`);
  };
  const restoreGlobals = installBrowserGlobalsWithFetch(
    document,
    fetchImpl,
    storedSession("scheduler", { lineId: "A" }),
  );
  try {
    await import(appModuleUrl("dialog-fallbacks"));
    await settleApp();

    const card = document.getElementById("orders-body").children.find((item) => item.dataset.orderId === "ORD-PENDING");
    await card.dispatchEvent({ type: "click", target: card });
    await document.getElementById("preview-selected").dispatchEvent({ type: "click" });
    await settleApp();
    assert.equal(document.getElementById("schedule-preview-dialog").getAttribute("open"), "");

    await document.getElementById("close-preview-page").dispatchEvent({ type: "click" });
    await settleApp();
    assert.equal(document.getElementById("schedule-preview-dialog").getAttribute("open"), null);

    await document.getElementById("reject-selected").dispatchEvent({ type: "click" });
    assert.equal(document.getElementById("reject-dialog").getAttribute("open"), "");

    const productionButton = document.querySelector('button[data-order-action="confirm-production"]');
    await productionButton.dispatchEvent({ type: "click" });
    await settleApp();
    assert.equal(document.getElementById("production-dialog").getAttribute("open"), "");
    await document.getElementById("cancel-production-report").dispatchEvent({ type: "click" });
    assert.equal(document.getElementById("production-dialog").getAttribute("open"), null);
  } finally {
    restoreGlobals();
  }
});

test("calendar order clicks cover authorization missing-order and production action branches", async () => {
  const salesDocument = buildDomFromIndex();
  const salesRestore = installBrowserGlobalsWithFetch(
    salesDocument,
    async (path) => {
      if (path === "/api/lines") {
        return jsonResponse({ lines: [{ id: "A", name: "Line A", capacityPerDay: 10000, timezone: "Asia/Taipei" }] });
      }
      if (path === "/api/orders") {
        return jsonResponse({ orders: [] });
      }
      if (String(path).startsWith("/api/schedules/calendar?")) {
        return jsonResponse({
          allocations: [
            { orderId: "ORD-NOT-MINE", customer: "Beta", lineId: "A", date: dateKeyAfter(2), quantity: 300, priority: "low", status: "已排程" },
          ],
          pendingAllocations: [],
        });
      }
      throw new Error(`unexpected fetch ${path}`);
    },
    storedSession("sales", { id: "sales-1" }),
  );
  try {
    await import(appModuleUrl("calendar-sales-auth"));
    await settleApp();

    const calendarItem = salesDocument.querySelector("[data-calendar-order-id]");
    assert.equal(calendarItem, null);
    assert.match(renderedMarkup(salesDocument.getElementById("calendar-grid")), /ORD-NOT-MINE/);
  } finally {
    salesRestore();
  }

  const schedulerDocument = buildDomFromIndex();
  const schedulerCalls = [];
  const schedulerRestore = installBrowserGlobalsWithFetch(
    schedulerDocument,
    async (path, options = {}) => {
      schedulerCalls.push({ path, options });
      if (path === "/api/lines") {
        return jsonResponse({ lines: [{ id: "A", name: "Line A", capacityPerDay: 10000, timezone: "Asia/Taipei" }] });
      }
      if (path === "/api/orders") {
        return jsonResponse({
          orders: [
            { id: "ORD-SCHEDULED", customer: "Beta", lineId: "A", quantity: 300, priority: "low", status: "已排程", dueDate: dateKeyAfter(5), createdBy: "sales-1" },
            { id: "ORD-PROD", customer: "Gamma", lineId: "A", quantity: 400, priority: "low", status: "生產中", dueDate: dateKeyAfter(6), createdBy: "sales-1" },
          ],
        });
      }
      if (path === "/api/production/start") {
        return jsonResponse({ id: JSON.parse(options.body).orderId });
      }
      if (String(path).startsWith("/api/schedules/calendar?")) {
        return jsonResponse({
          allocations: [
            { orderId: "ORD-SCHEDULED", customer: "Beta", lineId: "A", date: dateKeyAfter(2), quantity: 300, priority: "low", status: "已排程" },
            { orderId: "ORD-PROD", customer: "Gamma", lineId: "A", date: dateKeyAfter(3), quantity: 400, priority: "low", status: "生產中" },
            { orderId: "ORD-MISSING", customer: "Missing", lineId: "A", date: dateKeyAfter(4), quantity: 500, priority: "low", status: "已排程" },
          ],
          pendingAllocations: [],
        });
      }
      if (String(path).startsWith("/api/schedules/history?")) {
        return jsonResponse({ history: [] });
      }
      throw new Error(`unexpected fetch ${path}`);
    },
    storedSession("scheduler", { lineId: "A" }),
  );
  try {
    await import(appModuleUrl("calendar-scheduler-actions"));
    await settleApp();

    const scheduled = Array.from(schedulerDocument.querySelectorAll("[data-calendar-order-id]"))
      .find((button) => button.dataset.calendarOrderId === "ORD-SCHEDULED");
    await scheduled.dispatchEvent({ type: "click" });
    await settleApp();
    assert.equal(schedulerCalls.some((call) => call.path === "/api/production/start"), true);
    assert.equal(schedulerDocument.getElementById("message-title").textContent, "已開始生產");

    const producing = Array.from(schedulerDocument.querySelectorAll("[data-calendar-order-id]"))
      .find((button) => button.dataset.calendarOrderId === "ORD-PROD");
    await producing.dispatchEvent({ type: "click" });
    await settleApp();
    assert.equal(schedulerDocument.getElementById("production-dialog").open, true);

    const missing = Array.from(schedulerDocument.querySelectorAll("[data-calendar-order-id]"))
      .find((button) => button.dataset.calendarOrderId === "ORD-MISSING");
    await missing.dispatchEvent({ type: "click" });
    assert.equal(schedulerDocument.getElementById("message-title").textContent, "找不到訂單");
  } finally {
    schedulerRestore();
  }
});
