import {
  compareOrderIds,
  conflictExplanation,
  customerFilterValues,
  dateKeyInTimeZone,
  defaultLine,
  defaultTimezone,
  escapeHtml,
  exactFilterOrders,
  filtersForCreatedOrder,
  groupAllocationsByDate,
  isChildOrderId,
  lineScopedOrders,
  mergePreviewCalendarAllocations,
  monthGrid,
  priorityClass,
  priorityLabel,
  isFutureDateKey,
  sortOrdersForWorkstation,
  statusClass,
  statusCounts,
  tomorrowDateKey,
  waterlineMetrics,
  unacceptableDueDateMessage,
} from "./ui.js";

const statuses = ["待排程", "已排程", "生產中", "已完成", "需業務處理", "已取消"];
const fallbackLines = ["A", "B", "C", "D"].map((id) => ({
  id,
  name: `Line ${id}`,
  capacityPerDay: 10000,
  timezone: id === "D" ? "Europe/London" : defaultTimezone,
}));
const priorities = ["low", "high"];
const hpaJobStatusLabels = {
  queued: "佇列中",
  running: "執行中",
  completed: "已完成",
  failed: "失敗",
  cancelled: "已取消",
};

const state = {
  token: localStorage.getItem("woms.token") ?? "",
  user: JSON.parse(localStorage.getItem("woms.user") ?? "null"),
  lines: fallbackLines,
  users: [],
  orders: [],
  calendarAllocations: [],
  pendingCalendarAllocations: [],
  calendarMode: "scheduled",
  preview: null,
  previewCalendarMode: "pending",
  hpaPeak: null,
  hpaPeakPollingEnabled: false,
  productionOrderId: "",
  scheduleHistory: [],
  rejectOrderIds: [],
  schedulerPanelOpen: false,
  selectedOrderIds: new Set(),
  expandedSalesPendingOrderIds: new Set(),
  mobileView: "orders",
  selectedLine: localStorage.getItem("woms.selectedLine") || defaultLine(fallbackLines),
  filters: {
    customers: new Set(),
    status: "",
    priorities: new Set(),
  },
  calendarDate: calendarDateFromDateKey(dateKeyInTimeZone(new Date(), defaultTimezone)),
};

let pointerScheduleDrag = null;
let mouseScheduleDrag = null;
let suppressNextOrderClick = false;
let hpaPeakPoller = null;
let hpaPeakPollInFlight = false;

document.querySelector('input[name="startDate"]').value = tomorrowDateInputValue();
document.querySelector('input[name="dueDate"]').value = addDaysToDateKey(todayDateInputValue(), 3);
syncDueDateMinimums();

document.getElementById("login-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  try {
    const data = Object.fromEntries(new FormData(event.currentTarget));
    const payload = await request("/api/auth/login", {
      method: "POST",
      body: JSON.stringify(data),
    }, false);
    saveSession(payload.token, payload.user);
    configureLineForUser();
    showMessage("登入成功", `您好 ${payload.user.username}`);
    await refreshWorkspace();
  } catch (error) {
    showMessage("登入失敗", error.message, "warn");
  }
});

document.getElementById("logout-button").addEventListener("click", () => {
  if (state.token) {
    request("/api/auth/logout", { method: "POST" }).catch(() => {});
  }
  clearSession();
  renderAuthState();
  renderWorkspace();
  showMessage("已登出", "你已回到未登入狀態");
});

document.getElementById("active-line-select").addEventListener("change", async (event) => {
  state.selectedLine = event.currentTarget.value;
  localStorage.setItem("woms.selectedLine", state.selectedLine);
  state.selectedOrderIds.clear();
  state.filters.customers.clear();
  syncLineInputs();
  syncDateDefaults();
  renderWorkspace();
  await loadCalendar();
  await loadScheduleHistory();
});

document.getElementById("order-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  try {
    const draftOrder = orderFormData();
    assertFutureDueDate(draftOrder.dueDate);
    const result = await createPreview({
      lineId: activeLine(),
      startDate: tomorrowDateInputValue(),
      draftOrder,
    }, "sales-draft");
    openPreviewDialog(result);
  } catch (error) {
    showMessage("無法加入待排程", error.message, "warn");
  }
});

document.getElementById("assign-user-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  try {
    const data = Object.fromEntries(new FormData(event.currentTarget));
    const user = await request("/api/users", {
      method: "PATCH",
      body: JSON.stringify(data),
    });
    const lineInfo = user.lineId ? `，產線 ${user.lineId}` : "";
    showMessage("帳號已更新", `${user.username} 現在是${roleLabel(user.role)}${lineInfo}`);
    await loadUsers();
  } catch (error) {
    showMessage("帳號更新失敗", error.message, "warn");
  }
});

document.getElementById("create-user-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  try {
    const data = Object.fromEntries(new FormData(event.currentTarget));
    const user = await request("/api/users", {
      method: "POST",
      body: JSON.stringify(data),
    });
    event.currentTarget.reset();
    const lineInfo = user.lineId ? `，產線 ${user.lineId}` : "";
    showMessage("帳號已建立", `${user.username} 可用 ${roleLabel(user.role)} 權限登入${lineInfo}`);
    await loadUsers();
  } catch (error) {
    showMessage("帳號建立失敗", error.message, "warn");
  }
});

document.getElementById("reset-password-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  try {
    const data = Object.fromEntries(new FormData(event.currentTarget));
    const user = await request("/api/users/password", {
      method: "PATCH",
      body: JSON.stringify(data),
    });
    event.currentTarget.reset();
    showMessage("密碼已重設", `${user.username} 可以使用新密碼登入`);
  } catch (error) {
    showMessage("密碼重設失敗", error.message, "warn");
  }
});

document.getElementById("delete-user-button").addEventListener("click", async () => {
  const username = document.getElementById("assign-username").value;
  if (!username || !globalThis.confirm(`確定要刪除或停用 ${username} 嗎？`)) {
    return;
  }
  try {
    const user = await request(`/api/users/${encodeURIComponent(username)}`, { method: "DELETE" });
    const lineInfo = user.lineId ? `，產線 ${user.lineId}` : "";
    showMessage("帳號已處理", user.deleted ? `${username} 已刪除` : `${user.username} 已停用${lineInfo}`);
    await loadUsers();
  } catch (error) {
    showMessage("帳號刪除失敗", error.message, "warn");
  }
});

document.getElementById("create-hpa-peak").addEventListener("click", async () => {
  try {
    renderHPAPeakLoading("正在讀取 web Deployment、HPA 與 pod 狀態...");
    const payload = await request("/api/demo/hpa-peak", { method: "POST" });
    state.hpaPeak = payload.summary;
    state.hpaPeakPollingEnabled = true;
    renderHPAPeakSummary();
    syncHPAPeakPolling();
    showMessage("web autoscaling demo 已載入", "請由 NGINX Ingress 或 LoadBalancer 送入多使用者流量，觀察 Grafana per-pod NGINX req/s 與 web HPA。");
  } catch (error) {
    showMessage("autoscaling 狀態讀取失敗", error.message, "warn");
    await loadHPAPeakSummary();
  }
});

document.getElementById("refresh-hpa-peak").addEventListener("click", async () => {
  await loadHPAPeakSummary();
});

document.getElementById("clear-hpa-peak").addEventListener("click", async () => {
  const ok = globalThis.confirm("確定要清除舊版 L001-L200 排程尖峰資料嗎？web traffic autoscaling demo 本身不需要建立訂單或任務。");
  if (!ok) {
    return;
  }
  try {
    renderHPAPeakLoading("正在清除舊版 L001-L200 的訂單、排程與紀錄...");
    const payload = await request("/api/demo/hpa-peak", { method: "DELETE" });
    state.hpaPeak = payload.summary;
    state.hpaPeakPollingEnabled = false;
    renderHPAPeakSummary();
    syncHPAPeakPolling();
    showMessage("舊版資料已清除", "web autoscaling demo 請改用 Ingress/LoadBalancer 壓測與 Grafana 指標觀察。");
    await refreshWorkspace();
  } catch (error) {
    showMessage("清除失敗", error.message, "warn");
    await loadHPAPeakSummary();
  }
});

document.getElementById("main-calendar-mode").addEventListener("click", (event) => {
  const mode = event.target.dataset.calendarMode;
  if (!mode) {
    return;
  }
  state.calendarMode = mode;
  renderCalendar();
});

document.getElementById("preview-calendar-mode").addEventListener("click", (event) => {
  const mode = event.target.dataset.previewCalendarMode;
  if (!mode) {
    return;
  }
  state.previewCalendarMode = mode;
  renderPreviewPage();
});

document.getElementById("preview-selected").addEventListener("click", async () => {
  if (state.selectedOrderIds.size === 0) {
    showMessage("請先選取訂單", "至少選取一張待排程訂單再進行試排。", "warn");
    return;
  }
  try {
    const data = scheduleFormData();
    const result = await createPreview(data, "schedule");
    openPreviewDialog(result);
  } catch (error) {
    showMessage("試排失敗", error.message, "warn");
  }
});

document.getElementById("reject-selected").addEventListener("click", () => {
  if (state.selectedOrderIds.size === 0) {
    showMessage("請先選取訂單", "至少選取一張待排程訂單再駁回。", "warn");
    return;
  }
  openRejectDialog(Array.from(state.selectedOrderIds));
});

document.getElementById("cancel-selected").addEventListener("click", async () => {
  if (state.selectedOrderIds.size === 0) {
    showMessage("請先選取訂單", "沒有可以取消的訂單。", "warn");
    return;
  }
  const ok = globalThis.confirm(`確定取消 ${state.selectedOrderIds.size} 張已選取訂單嗎？`);
  if (!ok) {
    return;
  }
  try {
    const payload = await request("/api/orders", {
      method: "DELETE",
      body: JSON.stringify({ orderIds: Array.from(state.selectedOrderIds) }),
    });
    state.selectedOrderIds.clear();
    showMessage("取消完成", `已取消 ${payload.cancelledOrderIds?.length ?? 0} 張訂單。`);
    await refreshWorkspace();
  } catch (error) {
    showMessage("取消失敗", error.message, "warn");
  }
});

document.getElementById("schedule-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  if (state.selectedOrderIds.size === 0) {
    showMessage("請先選取訂單", "排程工程師必須先選取待排程訂單，才能進入試排確認流程。", "warn");
    return;
  }
  try {
    const data = scheduleFormData();
    const result = await createPreview(data, "schedule");
    openPreviewDialog(result);
  } catch (error) {
    showMessage("試排失敗", error.message, "warn");
  }
});

document.getElementById("production-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const form = Object.fromEntries(new FormData(event.currentTarget));
  await submitProductionReport(form.orderId, form.productionDate, Number(form.producedQuantity));
});

document.getElementById("cancel-production-report").addEventListener("click", () => {
  closeProductionReport();
});

document.getElementById("confirm-reject-orders").addEventListener("click", async () => {
  await submitRejectOrders();
});

document.querySelectorAll("[data-mobile-view]").forEach((button) => {
  button.addEventListener("click", () => {
    state.mobileView = button.dataset.mobileView;
    if (state.user?.role === "scheduler" && state.mobileView === "actions") {
      setSchedulerPanelOpen(true);
    }
    renderMobileView();
  });
});

document.getElementById("scheduler-panel-toggle")?.addEventListener("click", () => {
  setSchedulerPanelOpen(!state.schedulerPanelOpen);
});

document.getElementById("create-conflict-demo").addEventListener("click", async () => {
  try {
    const form = scheduleFormData();
    const payload = await request("/api/demo/conflict-orders", {
      method: "POST",
      body: JSON.stringify({
        lineId: form.lineId,
        dueDate: form.startDate,
        count: 6,
      }),
    });
    showMessage("衝突測試訂單已建立", `已建立 ${payload.orders?.length ?? 0} 張同日大量訂單，現在可以選取它們並試排。`);
    await refreshWorkspace();
  } catch (error) {
    showMessage("無法建立衝突資料", error.message, "warn");
  }
});

document.getElementById("confirm-preview-order").addEventListener("click", async () => {
  if (!state.preview?.previewId) {
    return;
  }
  try {
    const order = await request("/api/orders/preview-confirm", {
      method: "POST",
      body: JSON.stringify({ previewId: state.preview.previewId }),
    });
    focusCreatedOrder(order);
    closePreviewPage();
    document.getElementById("order-form").reset();
    showMessage("已加入待排程", "新訂單已正式放入待排程訂單。");
    await refreshWorkspace();
  } catch (error) {
    showMessage("無法加入待排程", error.message, "warn");
  }
});

document.getElementById("confirm-schedule-job").addEventListener("click", async () => {
  if (!state.preview?.previewId) {
    return;
  }
  const conflicts = state.preview.conflicts ?? [];
  const manualForce = state.preview.request.manualForce;
  if (conflicts.length > 0 && !manualForce) {
    showMessage("仍有衝突", "請勾選人工強制介入、填寫原因並重新試排。", "warn");
    return;
  }
  if (manualForce && !conflictsCanBeManuallyForced(conflicts)) {
    showMessage("無法人工介入", "容量無法在交期前排完的衝突不能強制送出，請調整開始日期、交期或拆單。", "warn");
    return;
  }
  if (manualForce && !allConflictAcknowledgementsChecked()) {
    showMessage("請確認衝突清單", "人工介入前必須逐項確認衝突與受影響訂單。", "warn");
    return;
  }
  try {
    const payload = await request("/api/schedules/jobs", {
      method: "POST",
      body: JSON.stringify({ ...state.preview.request, previewId: state.preview.previewId }),
    });
    if (payload.status === "failed") {
      showMessage("排程未完成", payload.message ?? "排程任務失敗。", "warn");
      return;
    }
    const scheduledOrderIds = state.preview.request.orderIds;
    closePreviewPage();
    scheduledOrderIds.forEach((orderId) => state.selectedOrderIds.delete(orderId));
    if (payload.status === "queued" || payload.status === "running") {
      showMessage("排程任務已送出", `任務 ${payload.id} 已進入背景排程，完成後會更新日曆。`);
      await waitForScheduleJob(payload.id);
      return;
    }
    showMessage("排程完成", `任務 ${payload.id} 已完成，日曆已更新。`);
    await refreshWorkspace();
  } catch (error) {
    showMessage("排程失敗", error.message, "warn");
  }
});

document.getElementById("close-preview-page").addEventListener("click", closePreviewPage);
const previewActionHandlers = {
  "return-workstation": handleReturnWorkstationPreviewAction,
  "retry-today": handleRetryTodayPreviewAction,
  "retry-suggested-start": handleRetrySuggestedStartPreviewAction,
  "update-conflict-due-date": handleUpdateConflictDueDatePreviewAction,
  "unselect-conflict-order": handleUnselectConflictOrderPreviewAction,
  "reject-preview-orders": handleRejectPreviewOrdersPreviewAction,
  "preview-conflict-solution": handlePreviewConflictSolutionPreviewAction,
  "retry-manual-force": handleRetryManualForcePreviewAction,
};

document.getElementById("preview-page-list").addEventListener("click", async (event) => {
  const action = event.target.dataset.previewAction;
  if (!action || !state.preview) {
    return;
  }
  const handler = previewActionHandlers[action];
  if (!handler) {
    return;
  }
  try {
    await handler(event);
  } catch (error) {
    showMessage("重新試排失敗", error.message, "warn");
  }
});

document.getElementById("prev-month").addEventListener("click", async () => {
  state.calendarDate.setUTCMonth(state.calendarDate.getUTCMonth() - 1);
  await loadCalendar();
});

document.getElementById("next-month").addEventListener("click", async () => {
  state.calendarDate.setUTCMonth(state.calendarDate.getUTCMonth() + 1);
  await loadCalendar();
});

document.getElementById("today-month").addEventListener("click", async () => {
  state.calendarDate = calendarDateFromDateKey(todayDateInputValue());
  await loadCalendar();
});

configureLineForUser();
renderAuthState();
if (state.token) {
  try {
    await refreshWorkspace();
  } catch (error) {
    renderAuthState();
    if (error.status === 401) {
      clearSession();
      renderAuthState();
      showMessage("登入狀態已失效", error.message, "warn");
    } else {
      renderWorkspace();
      showMessage("工作區重新整理失敗", error.message, "warn");
    }
  }
} else {
  renderWorkspace();
}

async function refreshWorkspace() {
  await loadLines();
  await loadOrders();
  await loadCalendar();
  await loadScheduleHistory();
  if (state.user?.role === "admin") {
    await loadUsers();
    await loadHPAPeakSummary();
  }
  renderAuthState();
}

function renderWorkspace() {
  syncLineInputs();
  renderMobileView();
  renderSchedulerPanelState();
  renderFilters();
  renderStatusSidebar();
  renderOrders();
  renderSalesRejectedOrders();
  renderCalendar();
  renderPreviewSummary();
  renderScheduleHistory();
  renderHPAPeakSummary();
  syncDueDateMinimums();
}

async function handleReturnWorkstationPreviewAction() {
  closePreviewPage();
}

async function handleRetryTodayPreviewAction() {
  await retryPreview({ startDate: tomorrowDateInputValue(), manualForce: false, reason: "" });
}

async function handleRetrySuggestedStartPreviewAction() {
  await retryPreview({ startDate: suggestedStartDate(state.preview), manualForce: false, reason: "" });
}

async function handleUpdateConflictDueDatePreviewAction(event) {
  const orderId = event.target.dataset.orderId;
  const input = document.querySelector(`[data-conflict-due-date="${cssEscape(orderId)}"]`);
  if (!input?.value) {
    showMessage("請選擇交期", "修改交期後才能重新試排。", "warn");
    return;
  }
  assertFutureDueDate(input.value);
  await updateOrderDueDate(orderId, input.value);
  await loadOrders();
  await retryPreview({});
}

async function handleUnselectConflictOrderPreviewAction(event) {
  const orderId = event.target.dataset.orderId;
  state.selectedOrderIds.delete(orderId);
  const orderIds = state.preview.request.orderIds.filter((id) => id !== orderId);
  if (orderIds.length === 0) {
    closePreviewPage();
    renderOrders();
    showMessage("已取消選取", "沒有剩餘訂單可試排。");
    return;
  }
  await retryPreview({ orderIds });
  renderOrders();
}

async function handleRejectPreviewOrdersPreviewAction() {
  openRejectDialog(state.preview.request.orderIds);
}

async function handlePreviewConflictSolutionPreviewAction() {
  const orderIds = checkedValues("[data-conflict-solution-order]");
  const resolutionOrderIds = checkedValues("[data-conflict-resolution-order]");
  if (orderIds.length === 0) {
    showMessage("請選取訂單", "至少選取一張訂單才能產生最早完成解法。", "warn");
    return;
  }
  await retryPreview({
    orderIds,
    resolutionOrderIds,
    allowLateCompletion: true,
    manualForce: false,
    reason: "",
  });
}

async function handleRetryManualForcePreviewAction() {
  const conflicts = state.preview.conflicts ?? [];
  if (!conflictsCanBeManuallyForced(conflicts)) {
    showMessage("無法人工介入", "容量無法在交期前排完的衝突不能強制送出，請調整開始日期、交期或拆單。", "warn");
    return;
  }
  const reason = document.getElementById("conflict-force-reason").value.trim();
  if (!reason) {
    showMessage("請填寫原因", "人工強制介入必須留下原因，才能重新試排。", "warn");
    return;
  }
  await retryPreview({ manualForce: true, reason });
}

async function loadOrders() {
  const payload = await request("/api/orders");
  state.orders = payload.orders ?? [];
  state.selectedOrderIds = new Set(Array.from(state.selectedOrderIds).filter((id) => selectableOrders().some((order) => order.id === id)));
  state.expandedSalesPendingOrderIds = new Set(Array.from(state.expandedSalesPendingOrderIds).filter((id) => {
    const order = state.orders.find((item) => item.id === id);
    return canSalesEditPendingOrder(order);
  }));
  renderFilters();
  renderStatusSidebar();
  renderOrders();
  renderSalesRejectedOrders();
}

async function loadUsers() {
  const payload = await request("/api/users");
  state.users = payload.users ?? [];
  renderUsers();
}

async function loadHPAPeakSummary() {
  if (!state.token || state.user?.role !== "admin") {
    state.hpaPeak = null;
    state.hpaPeakPollingEnabled = false;
    renderHPAPeakSummary();
    return;
  }
  const payload = await request("/api/demo/hpa-peak");
  state.hpaPeak = payload.summary;
  renderHPAPeakSummary();
  syncHPAPeakPolling();
}

function syncHPAPeakPolling() {
  const active = state.hpaPeakPollingEnabled
    && state.token
    && state.user?.role === "admin"
    && isHPAPeakActive(state.hpaPeak);
  if (!active) {
    if (hpaPeakPoller) {
      globalThis.clearInterval(hpaPeakPoller);
      hpaPeakPoller = null;
    }
    return;
  }
  if (hpaPeakPoller) {
    return;
  }
  hpaPeakPoller = globalThis.setInterval(async () => {
    if (hpaPeakPollInFlight) {
      return;
    }
    hpaPeakPollInFlight = true;
    try {
      await loadHPAPeakSummary();
    } catch {
      // Keep the current panel visible; manual refresh will surface errors.
    } finally {
      hpaPeakPollInFlight = false;
    }
  }, 3000);
}

function isHPAPeakActive(summary) {
  if (!summary) {
    return false;
  }
  return Boolean(summary.autoscaling)
    || Number(summary.lineCount ?? 0) > 0
    || Number(summary.orderCount ?? 0) > 0
    || Number(summary.jobCount ?? 0) > 0;
}

async function loadLines() {
  if (!state.token) {
    state.lines = fallbackLines;
    renderLineOptions();
    return;
  }
  const payload = await request("/api/lines");
  state.lines = payload.lines?.length ? payload.lines : fallbackLines;
  configureLineForUser();
  renderLineOptions();
  syncDateDefaults();
}

async function loadCalendar() {
  if (!state.token) {
    state.calendarAllocations = [];
    state.pendingCalendarAllocations = [];
    renderCalendar();
    return;
  }
  const lineId = activeLine();
  const month = monthKey(state.calendarDate);
  const payload = await request(`/api/schedules/calendar?lineId=${encodeURIComponent(lineId)}&month=${encodeURIComponent(month)}`);
  state.calendarAllocations = payload.allocations ?? [];
  state.pendingCalendarAllocations = (payload.pendingAllocations ?? []).map((allocation) => ({ ...allocation, preview: true }));
  renderCalendar();
}

async function loadScheduleHistory() {
  if (!state.token || state.user?.role !== "scheduler") {
    state.scheduleHistory = [];
    renderScheduleHistory();
    return;
  }
  const lineId = activeLine();
  const payload = await request(`/api/schedules/history?lineId=${encodeURIComponent(lineId)}`);
  state.scheduleHistory = payload.history ?? [];
  renderScheduleHistory();
}

async function createPreview(requestData, kind) {
  const payloadData = { ...requestData };
  const result = await request("/api/schedules/preview", {
    method: "POST",
    body: JSON.stringify(payloadData),
  });
  state.preview = {
    ...result,
    kind,
    request: {
      lineId: payloadData.lineId,
      startDate: payloadData.startDate,
      currentDate: result.currentDate ?? payloadData.currentDate ?? todayDateInputValue(),
      orderIds: payloadData.orderIds ?? [],
      resolutionOrderIds: payloadData.resolutionOrderIds ?? [],
      manualForce: payloadData.manualForce === "on" || payloadData.manualForce === true,
      allowLateCompletion: payloadData.allowLateCompletion === "on" || payloadData.allowLateCompletion === true,
      reason: payloadData.reason ?? "",
    },
  };
  renderPreviewSummary();
  renderCalendar();
  return state.preview;
}

function renderAuthState() {
  const loggedIn = Boolean(state.token && state.user);
  document.body.dataset.role = state.user?.role ?? "";
  if (state.user?.role !== "scheduler") {
    state.schedulerPanelOpen = false;
  }
  renderSchedulerPanelState();
  document.getElementById("login-page").hidden = loggedIn;
  document.getElementById("app-shell").hidden = !loggedIn;
  document.getElementById("monitor-link").hidden = state.user?.role !== "admin";
  document.getElementById("admin-panel").hidden = state.user?.role !== "admin";
  document.getElementById("order-form").hidden = state.user?.role !== "sales";
  document.getElementById("sales-rejected-panel").hidden = state.user?.role !== "sales";
  document.getElementById("batch-bar").hidden = state.user?.role !== "scheduler";
  document.querySelectorAll(".scheduler-only").forEach((node) => {
    node.hidden = state.user?.role !== "scheduler";
  });
  if (state.user?.role !== "scheduler" && state.mobileView === "actions") {
    state.mobileView = "orders";
    renderMobileView();
  }
  document.getElementById("active-line-select").disabled = state.user?.role === "scheduler";
  if (loggedIn) {
    document.getElementById("session-greeting").textContent = `您好 ${state.user.username}`;
  } else {
    closePreviewPage();
    closeProductionReport();
  }
}

function renderUsers() {
  const select = document.getElementById("assign-username");
  select.innerHTML = state.users.map((user) => `
    <option value="${escapeHtml(user.username)}">${escapeHtml(user.username)} (${roleLabel(user.role)}${user.lineId ? `/${escapeHtml(user.lineId)}` : ""}${user.disabled ? "，停用" : ""})</option>
  `).join("");
  const resetSelect = document.getElementById("reset-password-username");
  resetSelect.innerHTML = select.innerHTML;
}

function renderHPAPeakLoading(message) {
  const panel = document.getElementById("hpa-demo-summary");
  if (!panel) {
    return;
  }
  panel.innerHTML = `<div class="hpa-loading">${escapeHtml(message)}</div>`;
}

function renderHPAPeakSummary() {
  const panel = document.getElementById("hpa-demo-summary");
  if (!panel) {
    return;
  }
  const summary = state.hpaPeak;
  if (!summary) {
    panel.textContent = "尚未載入 web autoscaling 狀態";
    return;
  }
  const failedMessages = summary.failedMessages?.length
    ? `<div class="hpa-failures">${summary.failedMessages.map((message) => `<span>${escapeHtml(message)}</span>`).join("")}</div>`
    : "";
  const autoscaling = summary.autoscaling;
  const autoscalingError = autoscaling?.error
    ? `<div class="hpa-failures"><span>${escapeHtml(autoscaling.error)}</span></div>`
    : "";
  const autoscalingPanel = autoscaling
    ? `
    <div class="hpa-autoscaling">
      <span>HPA 目標 ${Number(autoscaling.desiredReplicas ?? 0).toLocaleString()} / ${Number(autoscaling.maxReplicas ?? 0).toLocaleString()}</span>
      <span>目前 replicas ${Number(autoscaling.currentReplicas ?? 0).toLocaleString()}</span>
      <span>Deployment ready ${Number(autoscaling.readyReplicas ?? 0).toLocaleString()} / ${Number(autoscaling.deploymentReplicas ?? 0).toLocaleString()}</span>
      <span>web pods ready ${Number(autoscaling.readyPods ?? 0).toLocaleString()} / ${Number(autoscaling.podCount ?? 0).toLocaleString()}</span>
    </div>
    ${autoscalingError}`
    : `
    <div class="hpa-autoscaling">
      <span>Kubernetes 狀態 未連線</span>
    </div>`;
  panel.innerHTML = `
    <div class="hpa-metrics">
      <span>NGINX Ingress / LoadBalancer</span>
      <span>多使用者 web traffic</span>
      <span>KEDA Prometheus trigger</span>
    </div>
    ${autoscalingPanel}
    <dl class="hpa-metadata">
      <div><dt>Grafana</dt><dd>${escapeHtml(summary.grafanaPath ?? "/grafana/")}</dd></div>
      <div><dt>指標</dt><dd>${escapeHtml(summary.metricName ?? "woms_web_nginx_requests_per_second_per_pod")}</dd></div>
      <div><dt>HPA 名稱</dt><dd>${escapeHtml(summary.hpaName ?? "woms-woms-web-hpa")}</dd></div>
      <div><dt>部署名稱</dt><dd>${escapeHtml(summary.deploymentName ?? "woms-woms-web")}</dd></div>
      <div><dt>壓測範例</dt><dd>${escapeHtml(summary.loadCommand ?? "hey -z 5m -c 80 https://<INGRESS_HOST>/")}</dd></div>
      <div><dt>觀察指令</dt><dd>${escapeHtml(summary.watchCommand ?? "kubectl get hpa,deploy,pod -n woms -w")}</dd></div>
    </dl>
    <p class="hpa-reason">${escapeHtml(summary.reason ?? "NGINX per-pod req/s 上升時，KEDA 會擴充 web pods。")}</p>
    ${failedMessages}
  `;
}

function renderLineOptions() {
  const activeSelect = document.getElementById("active-line-select");
  const options = state.lines.map((line) => `
    <option value="${escapeHtml(line.id)}">${escapeHtml(line.id)} · ${escapeHtml(line.timezone ?? defaultTimezone)}</option>
  `).join("");
  activeSelect.innerHTML = options;
  document.getElementById("assign-line").innerHTML = `
    <option value="">無</option>
    ${state.lines.map((line) => `
    <option value="${escapeHtml(line.id)}">${escapeHtml(line.id)} · ${escapeHtml(line.timezone ?? defaultTimezone)}</option>
  `).join("")}
  `;
  activeSelect.value = activeLine();
}

function renderOrders() {
  const visibleOrders = state.user?.role === "sales"
    ? visibleLineOrders().filter((order) => order.status !== "需業務處理")
    : visibleLineOrders();
  const filtered = sortOrdersForWorkstation(exactFilterOrders(visibleOrders, state.filters));
  const body = document.getElementById("orders-body");
  body.innerHTML = "";
  for (const order of filtered) {
    body.appendChild(renderOrderCard(order));
  }
  body.querySelectorAll("[data-order-action]").forEach((button) => {
    button.addEventListener("click", (event) => {
      event.stopPropagation();
      handleOrderAction(button.dataset.orderAction, button.dataset.orderId);
    });
  });
  updateSelectedCount();
}

function renderOrderCard(order) {
  const selected = state.selectedOrderIds.has(order.id);
  const selectable = state.user?.role === "scheduler" && order.status === "待排程";
  const salesPendingEditable = canSalesEditPendingOrder(order);
  const salesPendingExpanded = salesPendingEditable && state.expandedSalesPendingOrderIds.has(order.id);
  const card = document.createElement("article");
  card.className = `order-card ${selectable ? "selectable" : ""} ${selected ? "selected" : ""} ${salesPendingExpanded ? "editing" : ""}`;
  card.dataset.orderId = order.id;
  card.draggable = selectable;
  card.innerHTML = `
    <div class="order-card-main">
      <div>
        <strong>${escapeHtml(order.id)}</strong>
        <span>${escapeHtml(order.customer)}</span>
      </div>
      <span class="tag ${statusClass(order.status)}">${escapeHtml(order.status)}</span>
    </div>
    <dl class="order-card-meta">
      <div><dt>數量</dt><dd>${order.quantity.toLocaleString()} 片</dd></div>
      <div><dt>交期</dt><dd>${dateOnly(order.dueDate)}</dd></div>
      <div><dt>產線</dt><dd>${escapeHtml(order.lineId)}</dd></div>
      <div><dt>優先級</dt><dd><span class="tag ${priorityClass(order.priority)}">${priorityLabel(order.priority)}</span></dd></div>
    </dl>
    ${order.note ? `<p class="order-note" title="${escapeHtml(order.note)}">備註：${escapeHtml(order.note)}</p>` : ""}
    ${order.rejectionReason ? `<p class="rejection-reason">駁回：${escapeHtml(order.rejectionReason)}</p>` : ""}
    ${renderOrderAction(order)}
  `;
  if (selectable) {
    card.addEventListener("click", (event) => {
      if (suppressNextOrderClick) {
        suppressNextOrderClick = false;
        event.preventDefault();
        return;
      }
      toggleSelectedOrder(order.id);
    });
    card.addEventListener("dragstart", (event) => {
      const orderIds = draggedOrderIds(order.id);
      event.dataTransfer.setData("application/json", JSON.stringify({ orderIds }));
      event.dataTransfer.setData("text/plain", orderIds.join(","));
      event.dataTransfer.effectAllowed = "move";
      card.classList.add("dragging");
    });
    card.addEventListener("dragend", () => {
      card.classList.remove("dragging");
      clearCalendarDropTargets();
    });
    attachPointerScheduleDrag(card, order.id);
    attachMouseScheduleDrag(card, order.id);
  }
  return card;
}

function renderSalesRejectedOrders() {
  const list = document.getElementById("sales-rejected-list");
  if (!list) {
    return;
  }
  const rejected = state.user?.role === "sales" ? state.orders.filter((order) => order.status === "需業務處理" && order.createdBy === state.user.id) : [];
  list.innerHTML = "";
  for (const order of rejected) {
    list.appendChild(renderOrderCard(order));
  }
  list.querySelectorAll("[data-order-action]").forEach((button) => {
    button.addEventListener("click", (event) => {
      event.stopPropagation();
      handleOrderAction(button.dataset.orderAction, button.dataset.orderId);
    });
  });
  document.getElementById("sales-rejected-panel").hidden = state.user?.role !== "sales" || rejected.length === 0;
}

function draggedOrderIds(orderId) {
  const selected = selectedPendingOrderIds();
  if (selected.includes(orderId)) {
    return selected;
  }
  return [orderId];
}

function selectedPendingOrderIds() {
  return Array.from(state.selectedOrderIds).filter((id) => {
    const order = state.orders.find((item) => item.id === id);
    return order?.status === "待排程";
  });
}

function attachPointerScheduleDrag(card, orderId) {
  card.addEventListener("pointerdown", (event) => {
    if (event.button !== undefined && event.button !== 0) {
      return;
    }
    if (event.target.closest("button, input, select, textarea, a")) {
      return;
    }
    pointerScheduleDrag = {
      card,
      orderIds: draggedOrderIds(orderId),
      pointerId: event.pointerId,
      startX: event.clientX,
      startY: event.clientY,
      active: false,
    };
    card.setPointerCapture?.(event.pointerId);
  });
  card.addEventListener("pointermove", (event) => {
    if (!pointerScheduleDrag || pointerScheduleDrag.pointerId !== event.pointerId) {
      return;
    }
    const distance = Math.hypot(event.clientX - pointerScheduleDrag.startX, event.clientY - pointerScheduleDrag.startY);
    if (!pointerScheduleDrag.active && distance < 8) {
      return;
    }
    pointerScheduleDrag.active = true;
    event.preventDefault();
    card.classList.add("dragging");
    updatePointerDropTarget(event.clientX, event.clientY);
  });
  card.addEventListener("pointerup", async (event) => {
    if (!pointerScheduleDrag || pointerScheduleDrag.pointerId !== event.pointerId) {
      return;
    }
    const drag = pointerScheduleDrag;
    pointerScheduleDrag = null;
    card.releasePointerCapture?.(event.pointerId);
    card.classList.remove("dragging");
    clearCalendarDropTargets();
    if (!drag.active) {
      return;
    }
    suppressNextOrderClick = true;
    const cell = calendarCellFromPoint(event.clientX, event.clientY);
    const targetDate = cell?.dataset.date ?? "";
    if (targetDate && canScheduleOnDate(targetDate)) {
      await scheduleDroppedOrders(drag.orderIds, targetDate);
    }
  });
  card.addEventListener("pointercancel", (event) => {
    if (!pointerScheduleDrag || pointerScheduleDrag.pointerId !== event.pointerId) {
      return;
    }
    pointerScheduleDrag = null;
    card.releasePointerCapture?.(event.pointerId);
    card.classList.remove("dragging");
    clearCalendarDropTargets();
  });
}

function attachMouseScheduleDrag(card, orderId) {
  card.addEventListener("mousedown", (event) => {
    if (pointerScheduleDrag || mouseScheduleDrag || event.button !== 0) {
      return;
    }
    if (event.target.closest("button, input, select, textarea, a")) {
      return;
    }
    mouseScheduleDrag = {
      card,
      orderIds: draggedOrderIds(orderId),
      startX: event.clientX,
      startY: event.clientY,
      active: false,
    };
    document.addEventListener("mousemove", onMouseScheduleDragMove);
    document.addEventListener("mouseup", onMouseScheduleDragUp, { once: true });
  });
}

function onMouseScheduleDragMove(event) {
  if (!mouseScheduleDrag) {
    return;
  }
  const distance = Math.hypot(event.clientX - mouseScheduleDrag.startX, event.clientY - mouseScheduleDrag.startY);
  if (!mouseScheduleDrag.active && distance < 8) {
    return;
  }
  mouseScheduleDrag.active = true;
  event.preventDefault();
  mouseScheduleDrag.card.classList.add("dragging");
  updatePointerDropTarget(event.clientX, event.clientY);
}

async function onMouseScheduleDragUp(event) {
  if (!mouseScheduleDrag) {
    return;
  }
  const drag = mouseScheduleDrag;
  mouseScheduleDrag = null;
  document.removeEventListener("mousemove", onMouseScheduleDragMove);
  drag.card.classList.remove("dragging");
  clearCalendarDropTargets();
  if (!drag.active) {
    return;
  }
  suppressNextOrderClick = true;
  const cell = calendarCellFromPoint(event.clientX, event.clientY);
  const targetDate = cell?.dataset.date ?? "";
  if (targetDate && canScheduleOnDate(targetDate)) {
    await scheduleDroppedOrders(drag.orderIds, targetDate);
  }
}

function updatePointerDropTarget(clientX, clientY) {
  clearCalendarDropTargets();
  const cell = calendarCellFromPoint(clientX, clientY);
  if (cell?.dataset.date && canScheduleOnDate(cell.dataset.date)) {
    cell.classList.add("drop-target");
  }
}

function calendarCellFromPoint(clientX, clientY) {
  return document.elementFromPoint(clientX, clientY)?.closest?.(".calendar-day") ?? null;
}

function toggleSelectedOrder(orderId) {
  if (state.selectedOrderIds.has(orderId)) {
    state.selectedOrderIds.delete(orderId);
  } else {
    state.selectedOrderIds.add(orderId);
  }
  renderOrders();
}

function renderFilters() {
  renderCustomerFilter();
  renderCheckboxGroup("priority-filters", priorities, state.filters.priorities, priorityLabel);
}

function renderCustomerFilter() {
  const container = document.getElementById("customer-filter");
  const current = Array.from(state.filters.customers)[0] ?? "";
  const customers = customerFilterValues(visibleLineOrders(), state.filters);
  const nextCurrent = current && customers.includes(current) ? current : "";
  if (nextCurrent !== current) {
    state.filters.customers.clear();
  }
  const options = [{ value: "", label: "全部" }, ...customers.map((customer) => ({ value: customer, label: customer }))];
  const activeLabel = options.find((option) => option.value === nextCurrent)?.label ?? "全部";
  container.innerHTML = "";
  const toggle = document.createElement("button");
  toggle.type = "button";
  toggle.className = "filter-menu-toggle";
  toggle.setAttribute("aria-expanded", "false");
  toggle.textContent = activeLabel;
  const menu = document.createElement("div");
  menu.className = "filter-menu";
  menu.hidden = true;
  toggle.addEventListener("click", () => {
    const open = menu.hidden;
    menu.hidden = !open;
    toggle.setAttribute("aria-expanded", String(open));
  });
  for (const option of options) {
    const button = document.createElement("button");
    button.type = "button";
    button.className = `filter-chip ${option.value === nextCurrent ? "active" : ""}`;
    button.textContent = option.label;
    button.addEventListener("click", () => {
      state.filters.customers.clear();
      if (option.value) {
        state.filters.customers.add(option.value);
      }
      menu.hidden = true;
      toggle.setAttribute("aria-expanded", "false");
      renderCustomerFilter();
      renderOrders();
    });
    menu.appendChild(button);
  }
  container.append(toggle, menu);
}

function renderCheckboxGroup(containerId, values, selected, labelFor) {
  const container = document.getElementById(containerId);
  container.innerHTML = "";
  for (const value of values) {
    const id = `${containerId}-${String(value).replaceAll(/\W/g, "-")}`;
    const label = document.createElement("label");
    label.className = "check-option";
    label.innerHTML = `
      <input type="checkbox" id="${escapeHtml(id)}" value="${escapeHtml(value)}" ${selected.has(value) ? "checked" : ""}>
      <span>${escapeHtml(labelFor(value))}</span>
    `;
    label.querySelector("input").addEventListener("change", (event) => {
      if (event.currentTarget.checked) {
        selected.add(value);
      } else {
        selected.delete(value);
      }
      renderCustomerFilter();
      renderOrders();
    });
    container.appendChild(label);
  }
}

function renderStatusSidebar() {
  const counts = statusCounts(visibleLineOrders());
  const container = document.getElementById("status-sidebar");
  container.innerHTML = "";
  for (const status of statuses) {
    const button = document.createElement("button");
    button.type = "button";
    button.className = `status-filter-button ${state.filters.status === status ? "active" : ""}`;
    button.innerHTML = `
      <span>${escapeHtml(status)}</span>
      <span class="status-count">${counts[status]}</span>
    `;
    button.addEventListener("click", () => {
      state.filters.status = state.filters.status === status ? "" : status;
      renderStatusSidebar();
      renderFilters();
      renderOrders();
    });
    container.appendChild(button);
  }
}

function roleLabel(role) {
  return {
    admin: "管理員",
    sales: "業務",
    scheduler: "排程工程師",
  }[role] ?? "未知角色";
}

function renderCalendar() {
  const year = state.calendarDate.getUTCFullYear();
  const monthIndex = state.calendarDate.getUTCMonth();
  document.getElementById("calendar-title").textContent = `${year}-${String(monthIndex + 1).padStart(2, "0")}`;

  renderCalendarModeControl();
  const groups = groupAllocationsByDate(mainCalendarAllocations());
  const grid = document.getElementById("calendar-grid");
  grid.innerHTML = "";
  for (const day of monthGrid(year, monthIndex)) {
    const allocations = groups[day.key] ?? [];
    const cell = document.createElement("div");
    cell.className = `calendar-day ${day.inMonth ? "" : "outside"} ${allocations.some((item) => item.preview) ? "preview-highlight" : ""}`;
    cell.dataset.date = day.key;
    cell.innerHTML = `
      <div class="calendar-day-number">
        <span>${day.date.getUTCDate()}</span>
        <span>${allocations.length ? allocations.length : ""}</span>
      </div>
      ${renderWaterline(allocations)}
      ${allocations.map(renderCalendarItem).join("")}
    `;
    cell.addEventListener("dragover", (event) => {
      if (canScheduleOnDate(day.key)) {
        event.preventDefault();
        cell.classList.add("drop-target");
      }
    });
    cell.addEventListener("dragleave", (event) => {
      if (!cell.contains(event.relatedTarget)) {
        cell.classList.remove("drop-target");
      }
    });
    cell.addEventListener("drop", async (event) => {
      event.preventDefault();
      cell.classList.remove("drop-target");
      const orderIds = droppedOrderIds(event.dataTransfer);
      if (orderIds.length > 0 && canScheduleOnDate(day.key)) {
        await scheduleDroppedOrders(orderIds, day.key);
      }
    });
    cell.addEventListener("click", async (event) => {
      if (event.target.closest("[data-calendar-order-id]")) {
        return;
      }
      const orderIds = selectedPendingOrderIds();
      if (orderIds.length > 0 && canScheduleOnDate(day.key)) {
        await scheduleDroppedOrders(orderIds, day.key);
      }
    });
    grid.appendChild(cell);
  }
  grid.querySelectorAll("[data-calendar-order-id]").forEach((button) => {
    button.addEventListener("click", (event) => {
      event.stopPropagation();
      handleCalendarOrderClick(button.dataset.calendarOrderId, button.dataset.calendarDate);
    });
  });
}

function renderCalendarModeControl() {
  const control = document.getElementById("main-calendar-mode");
  const visible = state.user?.role === "sales";
  control.hidden = !visible;
  if (!visible) {
    return;
  }
  control.querySelectorAll("button").forEach((button) => {
    const active = button.dataset.calendarMode === state.calendarMode;
    button.classList.toggle("active", active);
    button.setAttribute("aria-pressed", active ? "true" : "false");
  });
}

function mainCalendarAllocations() {
  if (state.user?.role !== "sales") {
    return state.calendarAllocations;
  }
  if (state.calendarMode === "pending") {
    return state.pendingCalendarAllocations;
  }
  if (state.calendarMode === "all") {
    return [...state.calendarAllocations, ...state.pendingCalendarAllocations];
  }
  return state.calendarAllocations;
}

function droppedOrderIds(dataTransfer) {
  const payload = dataTransfer.getData("application/json");
  if (payload) {
    try {
      const parsed = JSON.parse(payload);
      if (Array.isArray(parsed.orderIds)) {
        return parsed.orderIds.filter(Boolean);
      }
    } catch {
      return [];
    }
  }
  return dataTransfer.getData("text/plain").split(",").map((id) => id.trim()).filter(Boolean);
}

function clearCalendarDropTargets() {
  document.querySelectorAll(".calendar-day.drop-target").forEach((cell) => {
    cell.classList.remove("drop-target");
  });
}

function renderPreviewSummary() {
  const preview = document.getElementById("preview-page-list");
  if (!state.preview) {
    preview.textContent = "尚未試排";
    document.getElementById("preview-page-title").textContent = "訂單分配預覽";
    document.getElementById("preview-detail-title").textContent = "分配明細";
    document.getElementById("confirm-preview-order").hidden = true;
    document.getElementById("confirm-schedule-job").hidden = true;
    document.getElementById("close-preview-page").hidden = true;
    return;
  }
  renderPreviewPage();
}

function openPreviewDialog(preview) {
  state.preview = preview;
  state.previewCalendarMode = "pending";
  closeProductionReport();
  renderPreviewPage();
  renderPreviewSummary();
  renderCalendar();
  const dialog = document.getElementById("schedule-preview-dialog");
  if (typeof dialog.showModal === "function" && !dialog.open) {
    dialog.showModal();
  } else {
    dialog.setAttribute("open", "");
  }
}

function openPreviewPage(preview) {
  openPreviewDialog(preview);
}

function closePreviewPage() {
  const dialog = document.getElementById("schedule-preview-dialog");
  if (dialog?.open && typeof dialog.close === "function") {
    dialog.close();
  } else {
    dialog?.removeAttribute("open");
  }
  state.preview = null;
  state.previewCalendarMode = "pending";
  renderPreviewSummary();
  renderCalendar();
}

function renderPreviewPage() {
  const pageList = document.getElementById("preview-page-list");
  const allocations = state.preview?.allocations ?? [];
  const conflicts = state.preview?.conflicts ?? [];
  const manualForce = state.preview?.request?.manualForce ?? false;
  const canConfirmSchedule = state.preview?.kind === "schedule"
    && state.user?.role === "scheduler"
    && (conflicts.length === 0 || (manualForce && conflictsCanBeManuallyForced(conflicts)));
  document.getElementById("preview-page-title").textContent = conflicts.length > 0 ? "衝突處理" : "訂單分配預覽";
  document.getElementById("preview-detail-title").textContent = conflicts.length > 0 ? "衝突清單" : "分配明細";
  document.getElementById("close-preview-page").hidden = false;
  pageList.innerHTML = [
    ...conflicts.map((conflict, index) => renderConflictItem(conflict, index, manualForce)),
    renderConflictActions(conflicts, manualForce),
    renderSolutionNotice(allocations),
    ...allocations.map((allocation) => `
      <div class="preview-item ${priorityClass(allocation.priority)}">
        <strong>${escapeHtml(allocation.orderId)}</strong>
        <span>${dateOnly(allocation.date)} · ${escapeHtml(allocation.lineId)} · ${allocation.quantity.toLocaleString()} 片</span>
        <span>${priorityLabel(allocation.priority)}</span>
      </div>
    `),
  ].join("") || "沒有可顯示的結果";

  document.getElementById("confirm-preview-order").hidden = state.preview?.kind !== "sales-draft";
  document.getElementById("confirm-schedule-job").hidden = !canConfirmSchedule;
  renderPreviewCalendar(allocations);
}

function renderPreviewCalendar(allocations) {
  const isSalesDraft = state.preview?.kind === "sales-draft";
  const mode = isSalesDraft ? state.previewCalendarMode : "pending";
  const toggle = document.getElementById("preview-calendar-mode");
  toggle.hidden = !isSalesDraft;
  toggle.querySelectorAll("button").forEach((button) => {
    const active = button.dataset.previewCalendarMode === mode;
    button.classList.toggle("active", active);
    button.setAttribute("aria-pressed", active ? "true" : "false");
  });
  const conflicts = state.preview?.conflicts ?? [];
  const resolutionOrderIds = state.preview?.request?.resolutionOrderIds ?? [];
  const previewAllocations = conflicts.length > 0 ? [] : allocations;
  const markedPreviewAllocations = markMovedPreviewAllocations(previewAllocations);
  const movedFromAllocations = buildMovedFromAllocations(markedPreviewAllocations);
  const pendingAllocations = markedPreviewAllocations.map((allocation) => ({ ...allocation, preview: true }));
  const visibleAllocations = isSalesDraft
    ? previewCalendarAllocationsForMode(mode, pendingAllocations)
    : markedPreviewAllocations;
  const calendarAllocations = isSalesDraft
    ? visibleAllocations
    : [...mergePreviewCalendarAllocations(markedPreviewAllocations, state.calendarAllocations, resolutionOrderIds), ...movedFromAllocations];
  const previewMonth = firstPreviewDate(visibleAllocations) ?? firstPreviewDate(previewAllocations) ?? state.calendarDate;
  const year = previewMonth.getUTCFullYear();
  const monthIndex = previewMonth.getUTCMonth();
  const groups = groupAllocationsByDate(calendarAllocations);
  const grid = document.getElementById("preview-calendar-grid");
  grid.innerHTML = "";
  for (const day of monthGrid(year, monthIndex)) {
    const dayAllocations = groups[day.key] ?? [];
    const effectiveAllocations = dayAllocations.filter((allocation) => !allocation.movedFromPreview);
    const cell = document.createElement("div");
    cell.className = `calendar-day ${day.inMonth ? "" : "outside"} ${dayAllocations.some((item) => item.preview) ? "preview-highlight" : ""}`;
    cell.innerHTML = `
      <div class="calendar-day-number">
        <span>${day.date.getUTCDate()}</span>
        <span>${effectiveAllocations.length ? effectiveAllocations.length : ""}</span>
      </div>
      ${renderWaterline(effectiveAllocations)}
      ${dayAllocations.map(renderCalendarItem).join("")}
    `;
    grid.appendChild(cell);
  }
}

function markMovedPreviewAllocations(previewAllocations) {
  if (previewAllocations.length === 0) {
    return previewAllocations;
  }
  const existingByOrder = new Map();
  const existingTotals = new Map();
  for (const allocation of state.calendarAllocations) {
    if (allocation.status !== statuses[1]) {
      continue;
    }
    const dateKey = dateOnly(allocation.date);
    if (!existingByOrder.has(allocation.orderId)) {
      existingByOrder.set(allocation.orderId, new Set());
    }
    existingByOrder.get(allocation.orderId).add(dateKey);
    const existingKey = `${allocation.orderId}|${dateKey}`;
    existingTotals.set(existingKey, (existingTotals.get(existingKey) ?? 0) + Number(allocation.quantity ?? 0));
  }
  const previewTotals = new Map();
  for (const allocation of previewAllocations) {
    const dateKey = dateOnly(allocation.date);
    const previewKey = `${allocation.orderId}|${dateKey}`;
    previewTotals.set(previewKey, (previewTotals.get(previewKey) ?? 0) + Number(allocation.quantity ?? 0));
  }
  return previewAllocations.map((allocation) => {
    const dates = existingByOrder.get(allocation.orderId);
    if (!dates) {
      return allocation;
    }
    const dateKey = dateOnly(allocation.date);
    const moved = !dates.has(dateKey);
    if (moved) {
      return { ...allocation, movedPreview: true };
    }
    const key = `${allocation.orderId}|${dateKey}`;
    const existingTotal = existingTotals.get(key);
    const previewTotal = previewTotals.get(key);
    if (existingTotal !== undefined && previewTotal !== undefined && existingTotal !== previewTotal) {
      return { ...allocation, quantityChangedPreview: true };
    }
    return allocation;
  });
}

function buildMovedFromAllocations(previewAllocations) {
  if (previewAllocations.length === 0) {
    return [];
  }
  const previewDatesByOrder = new Map();
  for (const allocation of previewAllocations) {
    if (!previewDatesByOrder.has(allocation.orderId)) {
      previewDatesByOrder.set(allocation.orderId, new Set());
    }
    previewDatesByOrder.get(allocation.orderId).add(dateOnly(allocation.date));
  }
  const movedFrom = [];
  for (const allocation of state.calendarAllocations) {
    if (allocation.status !== statuses[1]) {
      continue;
    }
    const previewDates = previewDatesByOrder.get(allocation.orderId);
    if (!previewDates) {
      continue;
    }
    const dateKey = dateOnly(allocation.date);
    if (previewDates.has(dateKey)) {
      continue;
    }
    movedFrom.push({
      ...allocation,
      movedFromPreview: true,
    });
  }
  return movedFrom;
}

function previewCalendarAllocationsForMode(mode, pendingAllocations) {
  if (mode === "scheduled") {
    return state.calendarAllocations;
  }
  if (mode === "all") {
    return [...state.calendarAllocations, ...pendingAllocations];
  }
  return pendingAllocations;
}

function renderConflictItem(conflict, index = 0, withAcknowledgement = false) {
  const salesDraft = state.preview?.kind === "sales-draft";
  const affected = conflict.affectedOrderIds?.length ? `影響：${conflict.affectedOrderIds.join(", ")}` : "無已知受影響訂單";
  const finishDate = dateOnly(conflict.earliestFinishDate);
  const canAcknowledge = !salesDraft && withAcknowledgement && conflict.reason === "existing allocations require manual review or reschedule";
  const acknowledgement = canAcknowledge ? `
    <label class="check-option conflict-ack">
      <input type="checkbox" data-conflict-ack="${index}">
      <span>確認以人工介入處理此衝突</span>
    </label>
  ` : "";
  if (salesDraft) {
    const explanation = conflict.orderId === "PREVIEW-DRAFT"
      ? conflictExplanation(conflict)
      : "這張待排程訂單由於新訂單的影響，在目前開始日期與交期之間沒有足夠產能。需要提前開始、延後交期、拆單，或調整訂單數量。";
    return `
      <div class="preview-item high">
        <strong>${escapeHtml(conflict.orderId)}</strong>
        <span>${escapeHtml(explanation)}</span>
        <span>最早完成：${finishDate}。</span>
      </div>
    `;
  }
  return `
    <div class="preview-item high">
      <strong>${escapeHtml(conflict.orderId)}</strong>
      <span>${escapeHtml(conflictExplanation(conflict))}</span>
      <span>最早完成：${finishDate}。可在下方選取衝突訂單與可移動訂單，產生最早完成解法。</span>
      <span>${escapeHtml(affected)}</span>
      <button class="secondary-button" data-preview-action="unselect-conflict-order" data-order-id="${escapeHtml(conflict.orderId)}" type="button">取消選取 ${escapeHtml(conflict.orderId)}</button>
      ${acknowledgement}
    </div>
  `;
}

function renderConflictActions(conflicts, manualForce) {
  if (state.preview?.kind !== "schedule" || conflicts.length === 0) {
    return "";
  }
  return `
    <div class="conflict-actions">
      <h3>衝突修改</h3>
      <p class="conflict-note">可以選取衝突訂單與可移動的既有排程，先預覽最早完成解法；確認接受後才會更新正式日曆。</p>
      ${renderConflictSolutionPicker(conflicts, state.preview?.request?.orderIds ?? [])}
      ${renderConflictDueDateEditors(conflicts)}
      <button class="danger-button" data-preview-action="reject-preview-orders" type="button">駁回此次選取</button>
      <button class="secondary-button" data-preview-action="return-workstation" type="button">回工作站調整訂單</button>
    </div>
  `;
}

function renderConflictSolutionPicker(conflicts, selectedOrderIds) {
  const conflictOrderIds = Array.from(new Set(conflicts.map((conflict) => conflict.orderId)));
  const solutionOrderIds = Array.from(new Set([...(selectedOrderIds ?? []), ...conflictOrderIds])).sort(compareOrderIds);
  const selectedSet = new Set(selectedOrderIds ?? []);
  const hasSelection = selectedSet.size > 0;
  const affectedOrderIds = Array.from(new Set(conflicts.flatMap((conflict) => conflict.affectedOrderIds ?? []))).sort(compareOrderIds);
  const movableAffected = affectedOrderIds.filter(canMoveOrder);
  const blockedAffected = affectedOrderIds.filter((orderId) => !canMoveOrder(orderId));
  return `
    <div class="solution-picker">
      <h4>最早完成解法</h4>
      <div class="solution-choice-list">
        ${solutionOrderIds.map((orderId) => `
          <label class="check-option">
            <input type="checkbox" data-conflict-solution-order value="${escapeHtml(orderId)}" ${!hasSelection || selectedSet.has(orderId) ? "checked" : ""}>
            <span>排入 ${escapeHtml(orderId)}</span>
          </label>
        `).join("")}
        ${movableAffected.map((orderId) => `
          <label class="check-option">
            <input type="checkbox" data-conflict-resolution-order value="${escapeHtml(orderId)}" checked>
            <span>允許移動 ${escapeHtml(orderId)}</span>
          </label>
        `).join("")}
        ${blockedAffected.map((orderId) => `
          <label class="check-option muted-option">
            <input type="checkbox" disabled>
            <span>${escapeHtml(orderId)} 已鎖定或不可移動</span>
          </label>
        `).join("")}
      </div>
      <button data-preview-action="preview-conflict-solution" type="button">預覽最早完成解法</button>
    </div>
  `;
}

function renderSolutionNotice(allocations) {
  if (!state.preview?.request?.allowLateCompletion) {
    return "";
  }
  const lateAllocations = allocations.filter((allocation) => {
    const order = state.orders.find((item) => item.id === allocation.orderId);
    return order && dateOnly(allocation.date) > dateOnly(order.dueDate);
  });
  if (lateAllocations.length === 0) {
    return `
      <div class="preview-item solution-notice">
        <strong>最早完成解法</strong>
        <span>此解法可在交期內完成所有已選訂單與可移動訂單。</span>
      </div>
    `;
  }
  const lateOrderIds = Array.from(new Set(lateAllocations.map((allocation) => allocation.orderId))).sort(compareOrderIds);
  return `
    <div class="preview-item solution-notice high">
      <strong>最早完成解法</strong>
      <span>${escapeHtml(lateOrderIds.join(", "))} 會晚於原交期完成；接受後會依下方預覽更新正式日曆。</span>
    </div>
  `;
}

function canMoveOrder(orderId) {
  const order = state.orders.find((item) => item.id === orderId);
  if (!order || order.status !== statuses[1] || order.priority !== "low") {
    return false;
  }
  const allocations = state.calendarAllocations.filter((allocation) => allocation.orderId === orderId);
  return allocations.length > 0 && allocations.every((allocation) => !allocation.locked && allocation.status === statuses[1]);
}

function renderWaterline(allocations) {
  const metrics = waterlineMetrics(allocations);
  const remainingLabel = metrics.overloaded ? "已超載" : "剩餘";
  const remainingValue = metrics.overloaded ? (metrics.total - metrics.capacity) : metrics.remaining;
  return `
    <div class="waterline" title="已排 ${metrics.total}/${metrics.capacity}，${remainingLabel} ${remainingValue}">
      <div class="waterline-meta">
        <span>${remainingLabel}</span>
        <strong>${remainingValue.toLocaleString()} 片</strong>
      </div>
      <progress class="waterline-meter ${metrics.tone}" value="${Math.min(metrics.total, metrics.capacity)}" max="${metrics.capacity}"></progress>
    </div>
  `;
}

function renderCalendarItem(allocation) {
  const actionable = !allocation.preview
    && !allocation.movedFromPreview
    && (allocation.status === statuses[1] || allocation.status === statuses[2])
    && state.user?.role === "scheduler";
  const tag = actionable ? "button" : "div";
  const movedClass = allocation.movedPreview ? "moved-preview" : "";
  const movedFromClass = allocation.movedFromPreview ? "moved-from-preview" : "";
  const quantityChangedClass = allocation.quantityChangedPreview ? "quantity-changed-preview" : "";
  const childOrderClass = isNewChildScheduledAllocation(allocation) ? "child-order-preview" : "";
  const attrs = actionable
    ? `type="button" data-calendar-order-id="${escapeHtml(allocation.orderId)}" data-calendar-date="${dateOnly(allocation.date)}"`
    : "";
  const movedNote = allocation.movedFromPreview ? "<span class=\"calendar-item-note\">已移出</span>" : "";
  const quantityNote = allocation.quantityChangedPreview ? "<span class=\"calendar-item-note\">數量調整</span>" : "";
  const childNote = isNewChildScheduledAllocation(allocation) ? "<span class=\"calendar-item-note\">子訂單</span>" : "";
  return `
    <${tag} class="calendar-item ${priorityClass(allocation.priority)} ${allocation.preview ? "preview-item-inline" : ""} ${movedClass} ${movedFromClass} ${quantityChangedClass} ${childOrderClass}" ${attrs}>
      <strong>${escapeHtml(allocation.orderId)}</strong>
      <span>${escapeHtml(allocation.customer ?? "Preview")} · ${calendarDisplayQuantity(allocation).toLocaleString()} 片</span>
      <span>${priorityLabel(allocation.priority)} · ${escapeHtml(allocation.status ?? "試排")}</span>
      ${movedNote}
      ${quantityNote}
      ${childNote}
    </${tag}>
  `;
}

function isNewChildScheduledAllocation(allocation) {
  if (!allocation.preview || allocation.status !== statuses[1]) {
    return false;
  }
  const sourceOrderId = allocation.sourceOrderId || allocation.sourceOrderID;
  if (!sourceOrderId && !isChildOrderId(allocation.orderId)) {
    return false;
  }
  return !state.calendarAllocations.some((item) => item.orderId === allocation.orderId);
}

function calendarDisplayQuantity(allocation) {
  if (allocation.status === statuses[3] && Number(allocation.completedQuantity ?? 0) > 0) {
    return Number(allocation.completedQuantity);
  }
  return Number(allocation.quantity ?? 0);
}

function handleCalendarOrderClick(orderId, productionDate = "") {
  if (state.user?.role !== "scheduler") {
    showMessage("權限不足", "只有排程工程師可以進行生產回報。", "warn");
    return;
  }
  const order = state.orders.find((item) => item.id === orderId);
  if (!order) {
    showMessage("找不到訂單", `${orderId} 不在目前工作站訂單清單內。`, "warn");
    return;
  }
  if (order.status === statuses[1]) {
    handleOrderAction("start-production", orderId);
    return;
  }
  if (order.status === statuses[2]) {
    handleOrderAction("confirm-production", orderId, productionDate);
  }
}

function renderOrderAction(order) {
  if (state.user?.role === "sales" && order.status === "需業務處理") {
    return renderSalesCorrectionForm(order, {
      deleteLabel: "取消訂單",
    });
  }
  if (canSalesEditPendingOrder(order)) {
    const expanded = state.expandedSalesPendingOrderIds.has(order.id);
    return `
      ${expanded ? renderSalesCorrectionForm(order, {
        deleteLabel: "刪除訂單",
        contextLabel: "修改：業務修改",
      }) : ""}
      <button
        class="sales-pending-toggle"
        data-order-action="toggle-sales-pending-edit"
        data-order-id="${escapeHtml(order.id)}"
        type="button"
        aria-expanded="${expanded ? "true" : "false"}"
        aria-label="${expanded ? "收合修改訂單" : "展開修改訂單"}"
        title="${expanded ? "收合修改訂單" : "展開修改訂單"}"
      >訂單修改</button>
    `;
  }
  if (state.user?.role !== "scheduler") {
    return "";
  }
  if (order.status === "已排程") {
    return `<button class="row-action" data-order-action="start-production" data-order-id="${escapeHtml(order.id)}" type="button">開始生產</button>`;
  }
  if (order.status === "生產中") {
    return `<button class="row-action" data-order-action="confirm-production" data-order-id="${escapeHtml(order.id)}" type="button">回報生產</button>`;
  }
  if (order.status === "待排程") {
    return `<span class="row-hint">可拖曳到月曆</span>`;
  }
  return "";
}

function canSalesEditPendingOrder(order) {
  return state.user?.role === "sales" && order?.status === "待排程" && order.createdBy === state.user.id;
}

function renderSalesCorrectionForm(order, options = {}) {
  const contextLabel = options.contextLabel ? `<p class="modification-reason">${escapeHtml(options.contextLabel)}</p>` : "";
  const deleteLabel = options.deleteLabel ?? "取消訂單";
  return `
    <div class="drawer-actions">
      ${contextLabel}
      <label>
        <span>交期</span>
        <input data-resubmit-field="dueDate" type="date" onclick="this.showPicker()" min="${tomorrowDateInputValue()}" value="${dateOnly(order.dueDate)}">
      </label>
      <label>
        <span>數量</span>
        <input data-resubmit-field="quantity" type="number" min="25" max="2500" step="25" value="${order.quantity}">
      </label>
      <label>
        <span>原備註</span>
        <span class="drawer-note">${escapeHtml(order.note || "未填寫")}</span>
      </label>
      <button class="row-action" data-order-action="resubmit-order" data-order-id="${escapeHtml(order.id)}" type="button">重新送出</button>
      <button class="row-action danger-button" data-order-action="cancel-order" data-order-id="${escapeHtml(order.id)}" type="button">${escapeHtml(deleteLabel)}</button>
    </div>
  `;
}

async function handleOrderAction(action, orderId, productionDate = "") {
  try {
    if (action === "toggle-sales-pending-edit") {
      if (state.expandedSalesPendingOrderIds.has(orderId)) {
        state.expandedSalesPendingOrderIds.delete(orderId);
      } else {
        state.expandedSalesPendingOrderIds.add(orderId);
      }
      renderOrders();
      return;
    }
    if (action === "resubmit-order") {
      const card = document.querySelector(`[data-order-id="${cssEscape(orderId)}"]`);
      const dueDate = card?.querySelector('[data-resubmit-field="dueDate"]')?.value;
      const quantity = Number(card?.querySelector('[data-resubmit-field="quantity"]')?.value);
      assertFutureDueDate(dueDate);
      await request("/api/orders/resubmit", {
        method: "POST",
        body: JSON.stringify({ orderId, dueDate, quantity }),
      });
      state.expandedSalesPendingOrderIds.delete(orderId);
      showMessage("已重新送出", `${orderId} 已回到待排程。`);
      await refreshWorkspace();
      return;
    }
    if (action === "cancel-order") {
      const payload = await request("/api/orders", {
        method: "DELETE",
        body: JSON.stringify({ orderIds: [orderId] }),
      });
      state.expandedSalesPendingOrderIds.delete(orderId);
      showMessage("取消完成", `已取消 ${payload.cancelledOrderIds?.length ?? 0} 張訂單。`);
      await refreshWorkspace();
      return;
    }
    if (action === "start-production") {
      await request("/api/production/start", {
        method: "POST",
        body: JSON.stringify({ orderId }),
      });
      showMessage("已開始生產", `${orderId} 已轉為生產中，排程日期已鎖定。`);
      await refreshWorkspace();
      return;
    }
    if (action === "confirm-production") {
      const order = state.orders.find((item) => item.id === orderId);
      openProductionReport(order, productionDate);
    }
  } catch (error) {
    showMessage("操作失敗", error.message, "warn");
  }
}

async function scheduleDroppedOrders(orderIds, targetDate) {
  try {
    const preview = await createPreview({
      lineId: activeLine(),
      startDate: targetDate,
      orderIds,
      manualForce: false,
      reason: "",
    }, "schedule");
    openPreviewDialog(preview);
  } catch (error) {
    showMessage("拖曳排程失敗", error.message, "warn");
  }
}

async function updateOrderDueDate(orderId, dueDate) {
  return request(`/api/orders/${encodeURIComponent(orderId)}`, {
    method: "PATCH",
    body: JSON.stringify({ dueDate }),
  });
}

function renderConflictDueDateEditors(conflicts) {
  const orderIds = Array.from(new Set(conflicts.map((conflict) => conflict.orderId))).filter((orderId) => {
    const order = state.orders.find((item) => item.id === orderId);
    return order?.status === "待排程";
  });
  if (orderIds.length === 0) {
    return "";
  }
  return `
    <div class="conflict-due-date-editors">
      <h4>修改單筆交期後重試</h4>
      ${orderIds.map((orderId) => {
    const order = state.orders.find((item) => item.id === orderId);
    return `
          <label>
            <span>${escapeHtml(orderId)}</span>
            <input data-conflict-due-date="${escapeHtml(orderId)}" type="date" onclick="this.showPicker()" min="${tomorrowDateInputValue()}" value="${dateOnly(order.dueDate)}">
          </label>
          <button data-preview-action="update-conflict-due-date" data-order-id="${escapeHtml(orderId)}" type="button">更新 ${escapeHtml(orderId)} 交期並重試</button>
        `;
  }).join("")}
    </div>
  `;
}

function openProductionReport(order, productionDate = "") {
  if (!order) {
    showMessage("找不到訂單", "請重新整理後再試一次。", "warn");
    return;
  }
  const allocation = productionAllocationForOrder(order.id, productionDate);
  if (!allocation) {
    showMessage("找不到排程", "請從日曆上的排程日期回報生產。", "warn");
    return;
  }
  state.productionOrderId = order.id;
  state.mobileView = "actions";
  renderMobileView();
  const form = document.getElementById("production-form");
  form.elements.orderId.value = order.id;
  form.elements.productionDate.value = dateOnly(allocation.date);
  form.elements.producedQuantity.value = allocation.quantity;
  form.elements.producedQuantity.max = allocation.quantity;
  document.getElementById("production-title").textContent = `回報 ${order.id}`;
  document.getElementById("production-context").textContent = `生產日期 ${dateOnly(allocation.date)}，本日排程 ${allocation.quantity.toLocaleString()} 片，交期 ${dateOnly(order.dueDate)}。完成片數不可大於本日排程量，未生產部分會保留同一訂單編號並回到待排程。`;
  const dialog = document.getElementById("production-dialog");
  if (typeof dialog.showModal === "function" && !dialog.open) {
    dialog.showModal();
  } else {
    dialog.setAttribute("open", "");
  }
}

function closeProductionReport() {
  state.productionOrderId = "";
  const form = document.getElementById("production-form");
  if (form) {
    form.reset();
  }
  const dialog = document.getElementById("production-dialog");
  if (dialog?.open && typeof dialog.close === "function") {
    dialog.close();
  } else {
    dialog?.removeAttribute("open");
  }
}

function productionAllocationForOrder(orderId, productionDate = "") {
  const allocations = state.calendarAllocations
    .filter((allocation) => allocation.orderId === orderId && allocation.status !== statuses[3])
    .sort((a, b) => dateOnly(a.date).localeCompare(dateOnly(b.date)));
  if (productionDate) {
    return allocations.find((allocation) => dateOnly(allocation.date) === productionDate);
  }
  return allocations[0];
}

async function submitProductionReport(orderId, productionDate, producedQuantity) {
  const order = state.orders.find((item) => item.id === orderId);
  if (!order) {
    showMessage("找不到訂單", "請重新整理後再試一次。", "warn");
    return;
  }
  if (!Number.isInteger(producedQuantity) || producedQuantity <= 0) {
    showMessage("片數不正確", "已完成片數必須是大於 0 的整數。", "warn");
    return;
  }
  const allocation = productionAllocationForOrder(orderId, productionDate);
  if (!allocation) {
    showMessage("找不到排程", "請從日曆上的排程日期回報生產。", "warn");
    return;
  }
  if (producedQuantity > allocation.quantity) {
    showMessage("片數超過本日排程", `完成片數不可大於 ${allocation.quantity.toLocaleString()} 片。`, "warn");
    return;
  }
  try {
    const payload = await request("/api/production/confirm", {
      method: "POST",
      body: JSON.stringify({ orderId, productionDate, producedQuantity }),
    });
    closeProductionReport();
    const suffix = payload.remainder ? `，${payload.remainder.id} 剩餘 ${payload.remainder.quantity.toLocaleString()} 片已回到待排程` : "，已全數完成";
    showMessage("生產回報完成", `${orderId} 已更新${suffix}。`);
    await refreshWorkspace();
  } catch (error) {
    showMessage("生產回報失敗", error.message, "warn");
  }
}

function suggestedStartDate(preview) {
  const orderIds = preview?.request?.orderIds ?? [];
  const selected = state.orders.filter((order) => orderIds.includes(order.id));
  if (selected.length === 0) {
    return "";
  }
  const earliestDue = selected
    .map((order) => new Date(dateOnly(order.dueDate)))
    .sort((a, b) => a - b)[0];
  const total = selected.reduce((sum, order) => sum + Number(order.quantity ?? 0), 0);
  const daysNeeded = Math.max(1, Math.ceil(total / 10000));
  earliestDue.setUTCDate(earliestDue.getUTCDate() - daysNeeded + 1);
  const suggested = dateInputValue(earliestDue);
  const todayValue = todayDateInputValue();
  return suggested <= todayValue ? tomorrowDateInputValue() : suggested;
}

function cssEscape(value) {
  if (globalThis.CSS?.escape) {
    return CSS.escape(value);
  }
  return String(value).replaceAll('"', String.raw`\"`);
}

function checkedValues(selector) {
  return Array.from(document.querySelectorAll(selector))
    .filter((node) => node.checked)
    .map((node) => node.value);
}

function updateSelectedCount() {
  document.getElementById("selected-count").textContent = `已選取 ${state.selectedOrderIds.size} 張訂單`;
}

function renderMobileView() {
  document.body.dataset.mobileView = state.mobileView;
  document.querySelectorAll("[data-mobile-view]").forEach((button) => {
    button.classList.toggle("active", button.dataset.mobileView === state.mobileView);
  });
  if (state.user?.role === "scheduler" && state.mobileView === "actions") {
    setSchedulerPanelOpen(true);
  }
}

function setSchedulerPanelOpen(open) {
  state.schedulerPanelOpen = Boolean(open);
  renderSchedulerPanelState();
}

function renderSchedulerPanelState() {
  const panel = document.getElementById("scheduler-panel");
  const toggle = document.getElementById("scheduler-panel-toggle");
  const isScheduler = state.user?.role === "scheduler";
  const isOpen = isScheduler && state.schedulerPanelOpen;
  document.body.dataset.schedulerPanelOpen = isOpen ? "true" : "false";
  if (panel) {
    panel.hidden = !isOpen;
  }
  if (toggle) {
    toggle.hidden = !isScheduler;
    toggle.setAttribute("aria-expanded", String(isOpen));
    toggle.textContent = isOpen ? "收合排程紀錄" : "展開排程紀錄";
  }
}

function renderScheduleHistory() {
  const list = document.getElementById("schedule-history-list");
  if (!list) {
    return;
  }
  if (state.scheduleHistory.length === 0) {
    list.textContent = "尚無排程紀錄";
    return;
  }
  list.innerHTML = state.scheduleHistory.map((item) => `
    <div class="preview-item">
      <strong>${escapeHtml(scheduleHistoryTitle(item.action))}</strong>
      <span>${escapeHtml(scheduleHistoryBody(item))}</span>
      <span>${escapeHtml(formatDateTime(item.createdAt))}</span>
    </div>
  `).join("");
}

function scheduleHistoryTitle(action) {
  return {
    "schedule.job.create": "排程成功",
    "schedule.job.manual_force": "人工介入",
    "order.reject": "延後處理紀錄",
    "order.cancel": "訂單取消",
    "production.start": "開始生產",
    "production.confirm.complete": "生產回報",
    "production.confirm.partial": "生產回報",
  }[action] ?? action;
}

function scheduleHistoryBody(item) {
  const reason = item.reason ? `：${item.reason}` : "";
  return `${item.resource}${reason}`;
}

function openRejectDialog(orderIds) {
  state.rejectOrderIds = orderIds;
  const textarea = document.getElementById("reject-reason");
  textarea.value = "";
  document.getElementById("reject-title").textContent = `駁回 ${orderIds.length} 張訂單`;
  const dialog = document.getElementById("reject-dialog");
  if (typeof dialog.showModal === "function") {
    dialog.showModal();
  } else {
    dialog.setAttribute("open", "");
  }
}

async function submitRejectOrders() {
  const reason = document.getElementById("reject-reason").value.trim();
  if (!reason) {
    showMessage("請填寫駁回理由", "駁回訂單前必須讓 Sales 知道需要處理什麼。", "warn");
    return;
  }
  try {
    const payload = await request("/api/orders/reject", {
      method: "POST",
      body: JSON.stringify({ orderIds: state.rejectOrderIds, reason }),
    });
    const dialog = document.getElementById("reject-dialog");
    if (dialog.open && typeof dialog.close === "function") {
      dialog.close();
    }
    const rejectedCount = payload.orders?.length ?? 0;
    state.rejectOrderIds.forEach((orderId) => state.selectedOrderIds.delete(orderId));
    closePreviewPage();
    showMessage("已駁回訂單", `${rejectedCount} 張訂單已移交 Sales 處理。`);
    await refreshWorkspace();
  } catch (error) {
    showMessage("駁回失敗", error.message, "warn");
  }
}

function scheduleFormData() {
  const data = Object.fromEntries(new FormData(document.getElementById("schedule-form")));
  data.lineId = activeLine();
  data.currentDate = todayDateInputValue();
  data.startDate = tomorrowDateInputValue();
  data.manualForce = data.manualForce === "on";
  data.allowLateCompletion = false;
  data.orderIds = Array.from(state.selectedOrderIds);
  data.resolutionOrderIds = [];
  return data;
}

function orderFormData() {
  syncLineInputs();
  const data = Object.fromEntries(new FormData(document.getElementById("order-form")));
  data.lineId = activeLine();
  data.quantity = Number(data.quantity);
  return data;
}

function assertFutureDueDate(dueDate) {
  if (!isFutureDateKey(dueDate, todayDateInputValue())) {
    throw new Error(unacceptableDueDateMessage);
  }
}

function syncDueDateMinimums() {
  const min = tomorrowDateInputValue();
  document.querySelectorAll('input[name="dueDate"], [data-resubmit-field="dueDate"], [data-conflict-due-date]').forEach((input) => {
    input.min = min;
  });
}

async function retryPreview(overrides) {
  const request = {
    ...state.preview.request,
    ...overrides,
  };
  const result = await createPreview(request, state.preview.kind);
  openPreviewDialog(result);
}

async function waitForScheduleJob(jobId, attempts = 20) {
  for (let attempt = 0; attempt < attempts; attempt += 1) {
    await delay(1000);
    const job = await request(`/api/schedules/jobs/${encodeURIComponent(jobId)}`);
    if (job.status === "completed") {
      showMessage("排程完成", `任務 ${job.id} 已完成，日曆已更新。`);
      await refreshWorkspace();
      return job;
    }
    if (job.status === "failed" || job.status === "cancelled") {
      showMessage("排程未完成", job.message ?? "排程任務未完成。", "warn");
      await refreshWorkspace();
      return job;
    }
  }
  showMessage("排程仍在背景執行", "排程任務尚未完成，請稍後重新整理狀態。");
  await refreshWorkspace();
  return null;
}

function delay(ms) {
  return new Promise((resolve) => {
    globalThis.setTimeout(resolve, ms);
  });
}

async function request(path, options = {}, needsAuth = true) {
  const headers = {
    "Content-Type": "application/json",
    ...(options.headers),
  };
  if (needsAuth) {
    if (!state.token) {
      throw new Error("請先登入");
    }
    headers.Authorization = `Bearer ${state.token}`;
  }
  const response = await fetch(path, { ...options, headers });
  const payload = await response.json().catch(() => ({}));
  if (!response.ok) {
    if (response.status === 401) {
      clearSession();
      renderAuthState();
    }
    throw new RequestError(payload.error ?? "請求失敗，請稍後再試。", response.status);
  }
  return payload;
}

class RequestError extends Error {
  constructor(message, status) {
    super(message);
    this.name = "RequestError";
    this.status = status;
  }
}

function saveSession(token, user) {
  state.token = token;
  state.user = user;
  if (user.role !== "scheduler") {
    state.selectedLine = defaultLine(state.lines);
    localStorage.setItem("woms.selectedLine", state.selectedLine);
  }
  localStorage.setItem("woms.token", token);
  localStorage.setItem("woms.user", JSON.stringify(user));
  renderAuthState();
}

function clearSession() {
  state.token = "";
  state.user = null;
  state.lines = fallbackLines;
  state.orders = [];
  state.calendarAllocations = [];
  state.pendingCalendarAllocations = [];
  state.preview = null;
  state.hpaPeak = null;
  state.hpaPeakPollingEnabled = false;
  state.productionOrderId = "";
  state.selectedOrderIds.clear();
  state.expandedSalesPendingOrderIds.clear();
  localStorage.removeItem("woms.token");
  localStorage.removeItem("woms.user");
  syncHPAPeakPolling();
}

function focusCreatedOrder(order) {
  if (order?.lineId) {
    state.selectedLine = order.lineId;
    localStorage.setItem("woms.selectedLine", state.selectedLine);
  }
  state.filters = filtersForCreatedOrder(order);
  state.selectedOrderIds.clear();
}

function showMessage(title, body, type = "info", details = "") {
  const dialog = document.getElementById("message-dialog");
  document.getElementById("message-title").textContent = title;
  document.getElementById("message-body").textContent = body;
  const detailsNode = document.getElementById("message-details");
  detailsNode.hidden = !details;
  detailsNode.textContent = details;
  dialog.dataset.type = type;
  if (dialog.open) {
    dialog.close();
  }
  if (typeof dialog.showModal === "function") {
    dialog.showModal();
  } else {
    dialog.setAttribute("open", "");
  }
}

function configureLineForUser() {
  if (state.user?.role === "scheduler" && state.user.lineId) {
    state.selectedLine = state.user.lineId;
  }
  if (state.user?.role === "sales" && !state.filters.status) {
    state.filters.status = "待排程";
  }
  if (!lineIds().includes(state.selectedLine)) {
    state.selectedLine = defaultLine(state.lines);
  }
  syncLineInputs();
}

function syncLineInputs() {
  renderLineOptions();
  const line = activeLine();
  const activeSelect = document.getElementById("active-line-select");
  activeSelect.value = line;
  document.querySelector('#order-form input[name="lineId"]').value = line;
  document.querySelector('#schedule-form input[name="lineId"]').value = line;
}

function syncDateDefaults() {
  const start = document.querySelector('input[name="startDate"]');
  const due = document.querySelector('input[name="dueDate"]');
  if (start) {
    start.value = tomorrowDateInputValue();
  }
  if (due && !isFutureDateKey(due.value, todayDateInputValue())) {
    due.value = addDaysToDateKey(todayDateInputValue(), 3);
  }
  syncDueDateMinimums();
}

function activeLine() {
  if (state.user?.role === "scheduler" && state.user.lineId) {
    return state.user.lineId;
  }
  return state.selectedLine || defaultLine(state.lines);
}

function lineIds() {
  return state.lines.map((line) => line.id);
}

function activeLineTimezone() {
  return state.lines.find((line) => line.id === activeLine())?.timezone || defaultTimezone;
}

function visibleLineOrders() {
  return lineScopedOrders(state.orders, activeLine());
}

function selectableOrders() {
  return visibleLineOrders().filter((order) => order.status === "待排程");
}

function allConflictAcknowledgementsChecked() {
  const boxes = Array.from(document.querySelectorAll("[data-conflict-ack]"));
  return boxes.every((box) => box.checked);
}

function canScheduleOnDate(dateKey) {
  return state.user?.role === "scheduler" && dateKey > todayDateInputValue();
}

function conflictsCanBeManuallyForced(conflicts) {
  return conflicts.every((conflict) => conflict.reason === "existing allocations require manual review or reschedule");
}

function firstPreviewDate(allocations) {
  if (!allocations.length) {
    return null;
  }
  return new Date(allocations[0].date);
}

function dateOnly(value) {
  return new Date(value).toISOString().slice(0, 10);
}

function formatDateTime(value) {
  return new Date(value).toLocaleString("zh-TW", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  });
}

function dateInputValue(value) {
  return new Date(value).toISOString().slice(0, 10);
}

function todayDateInputValue() {
  return dateKeyInTimeZone(new Date(), activeLineTimezone());
}

function tomorrowDateInputValue() {
  return tomorrowDateKey(todayDateInputValue());
}

function addDaysToDateKey(dateKey, days) {
  const date = new Date(`${dateKey}T00:00:00Z`);
  date.setUTCDate(date.getUTCDate() + days);
  return date.toISOString().slice(0, 10);
}

function calendarDateFromDateKey(dateKey) {
  return new Date(`${dateKey}T00:00:00Z`);
}

function monthKey(value) {
  return `${value.getUTCFullYear()}-${String(value.getUTCMonth() + 1).padStart(2, "0")}`;
}
