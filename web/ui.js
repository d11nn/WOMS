export function matchesOrder(order, query) {
  if (!query) {
    return true;
  }
  return [order.id, order.customer, order.lineId, order.status, order.priority]
    .some((value) => String(value).toLowerCase().includes(query));
}

export function exactFilterOrders(orders, filters) {
  return orders.filter((order) => {
    return matchesSet(order.customer, filters.customers)
      && matchesStatus(order.status, filters.status)
      && matchesSet(order.priority, filters.priorities);
  });
}

export function filtersForCreatedOrder(order, fallbackStatus = "待排程") {
  return {
    customers: new Set(),
    status: order?.status || fallbackStatus,
    priorities: new Set(),
  };
}

export function customerFilterValues(orders, filters) {
  const candidateFilters = {
    ...filters,
    customers: new Set(),
  };
  return uniqueValues(exactFilterOrders(orders, candidateFilters), "customer");
}

export function sortOrdersForWorkstation(orders) {
  return [...orders].sort((a, b) => {
    if (a.priority !== b.priority) {
      return a.priority === "high" ? -1 : 1;
    }
    const dueDelta = dateValue(a.dueDate) - dateValue(b.dueDate);
    if (dueDelta !== 0) {
      return dueDelta;
    }
    return naturalOrderNumber(a.id) - naturalOrderNumber(b.id) || String(a.id).localeCompare(String(b.id));
  });
}

export function trailingAsciiDigits(value) {
  const text = String(value);
  let start = text.length;
  while (start > 0) {
    const code = text.codePointAt(start - 1);
    if (code < 48 || code > 57) {
      break;
    }
    start -= 1;
  }
  return start === text.length ? "" : text.slice(start);
}

export function isChildOrderId(orderId) {
  const value = String(orderId);
  if (!value.startsWith("ORD-")) {
    return false;
  }
  const childSuffix = trailingAsciiDigits(value);
  if (!childSuffix) {
    return false;
  }
  const separatorIndex = value.length - childSuffix.length - 1;
  if (separatorIndex < 4 || value[separatorIndex] !== "-") {
    return false;
  }
  const parentSegment = value.slice(4, separatorIndex);
  if (!parentSegment || parentSegment.includes("-")) {
    return false;
  }
  return Number.isFinite(Number(childSuffix));
}

export function defaultLine(lines) {
  return [...lines].map((line) => typeof line === "string" ? line : line.id).sort(compareNatural)[0] ?? "";
}

export function lineScopedOrders(orders, lineId) {
  return orders.filter((order) => order.lineId === lineId);
}

export function waterlineMetrics(allocations, capacity = 10000) {
  const total = allocations.reduce((sum, allocation) => sum + Number(allocation.quantity ?? 0), 0);
  const ratio = capacity > 0 ? Math.min(total / capacity, 1) : 0;
  const remaining = Math.max(capacity - total, 0);
  const remainingRatio = capacity > 0 ? remaining / capacity : 0;
  return {
    total,
    capacity,
    remaining,
    overloaded: total > capacity,
    ratio,
    remainingPercent: Math.round(remainingRatio * 100),
    percent: Math.round(ratio * 100),
    tone: waterlineTone(ratio),
    color: waterlineColor(ratio),
  };
}

export function conflictExplanation(conflict) {
  if (conflict.reason === "capacity cannot satisfy order before due date") {
    return "這張訂單在目前開始日期與交期之間沒有足夠產能。需要提前開始、延後交期、拆單，或調整訂單數量。";
  }
  if (conflict.reason === "existing allocations require manual review or reschedule") {
    return "這次試排會碰到既有排程。若客戶或高優先級需求已核准，可以填寫原因後用人工強制介入重新試排。";
  }
  if (conflict.reason === "preview selection includes order") {
    return "此訂單已包含在本次試排清單中，若不想排入可取消選取。";
  }
  return "這筆衝突需要排程工程師檢查產能、交期與受影響訂單後再處理。";
}

export function uniqueValues(items, key) {
  return Array.from(new Set(items.map((item) => item[key]).filter(Boolean))).sort(compareNatural);
}

export function statusCounts(orders) {
  const counts = {
    "待排程": 0,
    "已排程": 0,
    "生產中": 0,
    "已完成": 0,
    "需業務處理": 0,
    "已取消": 0,
  };
  for (const order of orders) {
    if (Object.hasOwn(counts, order.status)) {
      counts[order.status] += 1;
    }
  }
  return counts;
}

export function monthGrid(year, monthIndex) {
  const firstOfMonth = new Date(Date.UTC(year, monthIndex, 1));
  const startOffset = firstOfMonth.getUTCDay();
  const start = new Date(firstOfMonth);
  start.setUTCDate(firstOfMonth.getUTCDate() - startOffset);

  const days = [];
  for (let index = 0; index < 42; index += 1) {
    const date = new Date(start);
    date.setUTCDate(start.getUTCDate() + index);
    days.push({
      date,
      key: date.toISOString().slice(0, 10),
      inMonth: date.getUTCMonth() === monthIndex,
    });
  }
  return days;
}

export function groupAllocationsByDate(allocations) {
	return sortCalendarAllocations(allocations).reduce((groups, allocation) => {
    const key = new Date(allocation.date).toISOString().slice(0, 10);
    groups[key] = groups[key] ?? [];
    groups[key].push(allocation);
    return groups;
	}, {});
}

export function sortCalendarAllocations(allocations) {
  return [...allocations].sort(compareCalendarAllocations);
}

function compareCalendarAllocations(a, b) {
  if (a.priority !== b.priority) {
    return a.priority === "high" ? -1 : 1;
  }
  const dueDelta = dateValue(a.dueDate) - dateValue(b.dueDate);
  if (dueDelta !== 0) {
    return dueDelta;
  }
  const createdDelta = createdAtTimestampValue(a) - createdAtTimestampValue(b);
  if (createdDelta !== 0) {
    return createdDelta;
  }
  return compareOrderIds(a.orderId ?? a.orderID ?? "", b.orderId ?? b.orderID ?? "");
}

export function mergePreviewCalendarAllocations(previewAllocations, calendarAllocations, resolutionOrderIds = []) {
  const normalize = (value) => String(value ?? "").trim().toLowerCase();
  const previewOrderIds = new Set(
    previewAllocations.map((allocation) => normalize(allocation.orderId ?? allocation.orderID)).filter(Boolean),
  );
  const resolutionIds = new Set(resolutionOrderIds.map(normalize).filter(Boolean));
  const replacedOrderIds = new Set([...previewOrderIds, ...resolutionIds]);

  const merged = [];
  for (const allocation of calendarAllocations) {
    const orderId = normalize(allocation.orderId ?? allocation.orderID);
    if (!orderId || replacedOrderIds.has(orderId)) {
      continue;
    }
    merged.push(allocation);
  }
  for (const allocation of previewAllocations) {
    merged.push({ ...allocation, preview: true });
  }
  return merged;
}

export const unacceptableDueDateMessage = "無法被接受的交期";
export const defaultTimezone = "Asia/Taipei";

export function isFutureDateKey(dateKey, todayKey) {
	return Boolean(dateKey) && dateKey > todayKey;
}

export function dateKeyInTimeZone(value = new Date(), timezone = defaultTimezone) {
  const date = value instanceof Date ? value : new Date(value);
  if (Number.isNaN(date.getTime())) {
    return "";
  }
  try {
    const parts = new Intl.DateTimeFormat("en-US", {
      timeZone: timezone || defaultTimezone,
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
    }).formatToParts(date);
    const values = Object.fromEntries(parts.map((part) => [part.type, part.value]));
    return `${values.year}-${values.month}-${values.day}`;
  } catch {
    return date.toISOString().slice(0, 10);
  }
}

export function tomorrowDateKey(todayKey) {
	const tomorrow = new Date(`${todayKey}T00:00:00Z`);
	tomorrow.setUTCDate(tomorrow.getUTCDate() + 1);
	return tomorrow.toISOString().slice(0, 10);
}

export function statusClass(status) {
  return {
    "待排程": "status-pending",
    "已排程": "status-scheduled",
    "生產中": "status-running",
    "已完成": "status-completed",
    "需業務處理": "status-rejected",
    "已取消": "status-cancelled",
  }[status] ?? "";
}

export function priorityLabel(priority) {
  return priority === "high" ? "高" : "低";
}

export function priorityClass(priority) {
  return priority === "high" ? "high" : "";
}

export function escapeHtml(value) {
  return String(value)
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");
}

function matchesSet(value, selected) {
  return selected.size === 0 || selected.has(String(value));
}

function matchesStatus(value, selected) {
  return !selected || String(value) === selected;
}

function orderStatusRank(status) {
  return {
    "待排程": 0,
    "已排程": 1,
    "生產中": 2,
    "已完成": 3,
    "需業務處理": 4,
    "已取消": 5,
  }[status] ?? 99;
}

function dateValue(value) {
  const timestamp = new Date(value).getTime();
  return Number.isNaN(timestamp) ? Number.MAX_SAFE_INTEGER : timestamp;
}

function createdAtTimestampValue(allocation) {
  const explicit = Number(allocation.createdAtTimestamp);
  if (Number.isFinite(explicit)) {
    return explicit;
  }
  return dateValue(allocation.createdAt);
}

function naturalOrderNumber(value) {
  const suffix = trailingAsciiDigits(value);
  return suffix ? Number(suffix) : Number.MAX_SAFE_INTEGER;
}

const naturalCollator = new Intl.Collator("und", { numeric: true });

export function compareOrderIds(a, b) {
  return compareNatural(a, b);
}

export function compareNatural(a, b) {
  return naturalCollator.compare(String(a), String(b));
}

function waterlineColor(ratio) {
  const clamped = Math.max(0, Math.min(ratio, 1));
  if (clamped < 0.8) {
    const progress = clamped / 0.8;
    const hue = Math.round(210 - progress * 178);
    return `hsl(${hue} 88% 48%)`;
  }
  const progress = (clamped - 0.8) / 0.2;
  const hue = Math.round(32 - progress * 32);
  return `hsl(${hue} 88% 48%)`;
}

function waterlineTone(ratio) {
  if (ratio >= 0.9) {
    return "danger";
  }
  if (ratio >= 0.7) {
    return "warning";
  }
  return "safe";
}
