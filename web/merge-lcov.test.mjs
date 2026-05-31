import test from "node:test";
import assert from "node:assert/strict";
import { writeFileSync, readFileSync, unlinkSync } from "node:fs";
import { join } from "node:path";
import { mergeLcovFile, mergeLcovText } from "../scripts/merge-lcov.mjs";

test("merge-lcov.mjs merges duplicate LCOV records correctly", () => {
  const tempFile = join(process.cwd(), "temp-test-lcov.info");
  
  // Create sample duplicate lcov records with FN, FNDA, BRDA, DA
  const content = `
SF:web/ui.js
FN:1,foo
FNDA:2,foo
DA:1,2
DA:2,0
BRDA:1,1,1,2
end_of_record
SF:web/ui.js
FN:1,foo
FN:3,bar
FNDA:1,foo
FNDA:5,bar
DA:1,1
DA:3,5
BRDA:1,1,1,1
BRDA:1,1,2,-
end_of_record
some junk text without SF line
end_of_record
  `.trim();
  
  writeFileSync(tempFile, content, "utf8");
  
  try {
    mergeLcovFile(tempFile);
    
    // Read and verify the merged content
    const merged = readFileSync(tempFile, "utf8");
    assert.match(merged, /SF:web\/ui\.js/);
    assert.match(merged, /FN:1,foo/);
    assert.match(merged, /FN:3,bar/);
    assert.match(merged, /FNDA:3,foo/); // 2 + 1 = 3
    assert.match(merged, /FNDA:5,bar/);
    assert.match(merged, /DA:1,3/); // 2 + 1 = 3
    assert.match(merged, /DA:2,0/);
    assert.match(merged, /DA:3,5/);
    assert.match(merged, /BRDA:1,1,1,3/); // 2 + 1 = 3
    assert.match(merged, /BRDA:1,1,2,0/);
    assert.match(merged, /FNF:2/);
    assert.match(merged, /FNH:2/);
    assert.match(merged, /BRF:2/);
    assert.match(merged, /BRH:1/);
    assert.match(merged, /LF:3/);
    assert.match(merged, /LH:2/); // line 1 and 3 are hit, line 2 is 0 hits
    assert.match(merged, /end_of_record/);
  } finally {
    try {
      unlinkSync(tempFile);
    } catch {}
  }
});

test("merge-lcov.mjs ignores records without a source file", () => {
  assert.equal(mergeLcovText("some junk text without SF line\nend_of_record"), "\n");
});
