#!/usr/bin/env node
import { readFileSync } from "node:fs";
import { pathToFileURL } from "node:url";

const imageSections = new Set(["api", "worker", "web"]);

function isImageKey(line) {
  return /^ {2}image:\s*$/.test(line);
}

function isSectionKey(line) {
  return line.startsWith("  ") && !line.startsWith("    ") && /^[A-Za-z][A-Za-z0-9_-]*:/.test(line.slice(2));
}

export function parseImageTags(valuesText) {
  const tags = {};
  let section = "";
  let inImage = false;

  for (const line of valuesText.split(/\r?\n/)) {
    const topLevel = line.match(/^([A-Za-z][A-Za-z0-9_-]*):\s*$/);
    if (topLevel) {
      section = topLevel[1];
      inImage = false;
      continue;
    }

    if (!imageSections.has(section)) {
      continue;
    }

    if (isImageKey(line)) {
      inImage = true;
      continue;
    }

    if (isSectionKey(line)) {
      inImage = false;
      continue;
    }

    if (inImage) {
      const tag = line.match(/^ {4}tag:\s*"?([^"\s]+)"?\s*$/);
      if (tag) {
        tags[section] = tag[1];
      }
    }
  }

  return tags;
}

export function verifyReleaseTag(valuesText, expectedTag) {
  const tags = parseImageTags(valuesText);
  const missing = [...imageSections].filter((section) => !tags[section]);
  if (missing.length > 0) {
    throw new Error(`Missing image tag for: ${missing.join(", ")}`);
  }

  const mismatches = Object.entries(tags).filter(([, tag]) => tag !== expectedTag);
  if (mismatches.length > 0) {
    const rendered = mismatches.map(([section, tag]) => `${section}=${tag}`).join(", ");
    throw new Error(`Helm image tags do not match ${expectedTag}: ${rendered}`);
  }

  return tags;
}

export function runCli(argv, io = {}) {
  const readFile = io.readFile ?? readFileSync;
  const log = io.log ?? console.log;
  const logError = io.error ?? console.error;
  const [, , valuesPath, expectedTag] = argv;

  if (!valuesPath || !expectedTag) {
    logError("Usage: node scripts/verify-release-tag.mjs <values.yaml> <expected-tag>");
    return 2;
  }

  try {
    const tags = verifyReleaseTag(readFile(valuesPath, "utf8"), expectedTag);
    log(`Helm image tags match ${expectedTag}: ${JSON.stringify(tags)}`);
    return 0;
  } catch (error) {
    logError(error instanceof Error ? error.message : String(error));
    return 1;
  }
}

/* node:coverage ignore next 3 */
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  process.exitCode = runCli(process.argv);
}
