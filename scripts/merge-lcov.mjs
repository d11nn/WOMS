#!/usr/bin/env node
import { readFileSync, writeFileSync } from "node:fs";

export function mergeLcovText(input) {
  const records = input
    .split("end_of_record")
    .map((record) => record.trim())
    .filter(Boolean);

  const bySource = new Map();

  function sourceRecord(source) {
    if (!bySource.has(source)) {
      bySource.set(source, {
        source,
        functions: new Map(),
        functionHits: new Map(),
        branches: new Map(),
        lines: new Map(),
      });
    }
    return bySource.get(source);
  }

  for (const recordText of records) {
    const lines = recordText.split(/\r?\n/);
    const sourceLine = lines.find((line) => line.startsWith("SF:"));
    if (!sourceLine) {
      continue;
    }
    const record = sourceRecord(sourceLine.slice(3));
    for (const line of lines) {
      if (line.startsWith("FN:")) {
        const [lineNo, name] = line.slice(3).split(",", 2);
        record.functions.set(name, lineNo);
        continue;
      }
      if (line.startsWith("FNDA:")) {
        const [hits, name] = line.slice(5).split(",", 2);
        record.functionHits.set(name, (record.functionHits.get(name) ?? 0) + Number(hits));
        continue;
      }
      if (line.startsWith("BRDA:")) {
        const parts = line.slice(5).split(",");
        const key = parts.slice(0, 3).join(",");
        const hits = parts[3] === "-" ? 0 : Number(parts[3]);
        record.branches.set(key, (record.branches.get(key) ?? 0) + hits);
        continue;
      }
      if (line.startsWith("DA:")) {
        const [lineNo, hits] = line.slice(3).split(",", 2);
        record.lines.set(lineNo, (record.lines.get(lineNo) ?? 0) + Number(hits));
      }
    }
  }

  const output = [];
  for (const record of bySource.values()) {
    const fns = [...record.functions.entries()]
      .sort((a, b) => Number(a[1]) - Number(b[1]) || a[0].localeCompare(b[0]))
      .map(([name, lineNo]) => `FN:${lineNo},${name}`);

    const fnHits = [...record.functionHits.entries()]
      .sort((a, b) => a[0].localeCompare(b[0]))
      .map(([name, hits]) => `FNDA:${hits},${name}`);

    const branches = [...record.branches.entries()]
      .sort((a, b) => {
        const left = a[0].split(",").map(Number);
        const right = b[0].split(",").map(Number);
        return left[0] - right[0] || left[1] - right[1] || left[2] - right[2];
      })
      .map(([key, hits]) => `BRDA:${key},${hits}`);

    const lines = [...record.lines.entries()]
      .sort((a, b) => Number(a[0]) - Number(b[0]))
      .map(([lineNo, hits]) => `DA:${lineNo},${hits}`);

    output.push(
      `SF:${record.source}`,
      ...fns,
      ...fnHits,
      `FNF:${record.functions.size}`,
      `FNH:${[...record.functionHits.values()].filter((hits) => hits > 0).length}`,
      ...branches,
      `BRF:${record.branches.size}`,
      `BRH:${[...record.branches.values()].filter((hits) => hits > 0).length}`,
      ...lines,
      `LF:${record.lines.size}`,
      `LH:${[...record.lines.values()].filter((hits) => hits > 0).length}`,
      "end_of_record"
    );
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
