#!/usr/bin/env node
import { readFileSync, writeFileSync } from "node:fs";

function createCoverageRecord(source) {
  return {
    source,
    functions: new Map(),
    functionHits: new Map(),
    branches: new Map(),
    lines: new Map(),
  };
}

function sourceRecord(bySource, source) {
  if (!bySource.has(source)) {
    bySource.set(source, createCoverageRecord(source));
  }
  return bySource.get(source);
}

function addNumber(map, key, value) {
  map.set(key, (map.get(key) ?? 0) + value);
}

function mergeLcovLine(record, line) {
  if (line.startsWith("FN:")) {
    const [lineNo, name] = line.slice(3).split(",", 2);
    record.functions.set(name, lineNo);
    return;
  }

  if (line.startsWith("FNDA:")) {
    const [hits, name] = line.slice(5).split(",", 2);
    addNumber(record.functionHits, name, Number(hits));
    return;
  }

  if (line.startsWith("BRDA:")) {
    const parts = line.slice(5).split(",");
    const key = parts.slice(0, 3).join(",");
    const hits = parts[3] === "-" ? 0 : Number(parts[3]);
    addNumber(record.branches, key, hits);
    return;
  }

  if (line.startsWith("DA:")) {
    const [lineNo, hits] = line.slice(3).split(",", 2);
    addNumber(record.lines, lineNo, Number(hits));
  }
}

function mergeLcovRecord(bySource, recordText) {
  const lines = recordText.split(/\r?\n/);
  const sourceLine = lines.find((line) => line.startsWith("SF:"));
  if (!sourceLine) {
    return;
  }

  const record = sourceRecord(bySource, sourceLine.slice(3));
  for (const line of lines) {
    mergeLcovLine(record, line);
  }
}

function sortBranchEntries(leftEntry, rightEntry) {
  const left = leftEntry[0].split(",").map(Number);
  const right = rightEntry[0].split(",").map(Number);
  return left[0] - right[0] || left[1] - right[1] || left[2] - right[2];
}

function hitCount(values) {
  return [...values].filter((hits) => hits > 0).length;
}

function renderCoverageRecord(record) {
  const fns = [...record.functions.entries()]
    .sort((a, b) => Number(a[1]) - Number(b[1]) || a[0].localeCompare(b[0]))
    .map(([name, lineNo]) => `FN:${lineNo},${name}`);

  const fnHits = [...record.functionHits.entries()]
    .sort((a, b) => a[0].localeCompare(b[0]))
    .map(([name, hits]) => `FNDA:${hits},${name}`);

  const branches = [...record.branches.entries()]
    .sort(sortBranchEntries)
    .map(([key, hits]) => `BRDA:${key},${hits}`);

  const lines = [...record.lines.entries()]
    .sort((a, b) => Number(a[0]) - Number(b[0]))
    .map(([lineNo, hits]) => `DA:${lineNo},${hits}`);

  return [
    `SF:${record.source}`,
    ...fns,
    ...fnHits,
    `FNF:${record.functions.size}`,
    `FNH:${hitCount(record.functionHits.values())}`,
    ...branches,
    `BRF:${record.branches.size}`,
    `BRH:${hitCount(record.branches.values())}`,
    ...lines,
    `LF:${record.lines.size}`,
    `LH:${hitCount(record.lines.values())}`,
    "end_of_record",
  ];
}

export function mergeLcovText(input) {
  const records = input
    .split("end_of_record")
    .map((record) => record.trim())
    .filter(Boolean);

  const bySource = new Map();

  for (const recordText of records) {
    mergeLcovRecord(bySource, recordText);
  }

  const output = [];
  for (const record of bySource.values()) {
    output.push(...renderCoverageRecord(record));
  }

  return `${output.join("\n")}\n`;
}

export function mergeLcovFile(file) {
  writeFileSync(file, mergeLcovText(readFileSync(file, "utf8")));
}

if (import.meta.url === `file://${process.argv[1]}`) {
  const file = process.argv[2];
  if (!file) {
    console.error("Usage: merge-lcov.mjs <lcov.info>");
    process.exit(1);
  }

  mergeLcovFile(file);
}
