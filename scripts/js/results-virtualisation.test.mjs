// The Results table windows its rows: only the slice covering the scroll
// viewport plus an overscan buffer is ever in the document. The decision is
// pure arithmetic over a prefix sum of row heights, which is exactly why it can
// be checked here, without a browser. These tests load the shipped script,
// lift the two functions that choose the window out of it, and prove the window
// stays bounded however large the page grows.
//
// Run with: node --test scripts/js/results-virtualisation.test.mjs

import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const repositoryRoot = join(dirname(fileURLToPath(import.meta.url)), "..", "..");
const scriptPath = join(repositoryRoot, "web", "static", "js", "app-results.js");
const source = await readFile(scriptPath, "utf8");

// declaredNumber reads one of the script's own tuning constants so this test
// always runs against the values that ship, not a copy of them.
function declaredNumber(name) {
  const match = new RegExp("const\\s+" + name + "\\s*=\\s*(-?\\d+)\\s*;").exec(source);
  assert.ok(match, `app-results.js no longer declares ${name}`);

  return Number(match[1]);
}

// extractFunction slices one top-level function declaration out of the script by
// counting braces from its body. The two functions it is used on contain no
// strings, comments, or template literals, so plain counting is exact.
function extractFunction(name) {
  const start = source.indexOf("function " + name + "(");
  assert.ok(start >= 0, `app-results.js no longer declares ${name}()`);

  let index = source.indexOf("{", start);
  let depth = 0;
  for (; index < source.length; index += 1) {
    if (source[index] === "{") depth += 1;
    else if (source[index] === "}") {
      depth -= 1;
      if (depth === 0) return source.slice(start, index + 1);
    }
  }

  throw new Error(`${name}() in app-results.js is not brace balanced`);
}

const { computeRowWindow, rowIndexAtOffset } = new Function(
  extractFunction("rowIndexAtOffset") +
    "\n" +
    extractFunction("computeRowWindow") +
    "\nreturn { computeRowWindow: computeRowWindow, rowIndexAtOffset: rowIndexAtOffset };",
)();

const overscan = declaredNumber("virtualOverscanRows");
const windowLimit = declaredNumber("virtualWindowLimit");
const threshold = declaredNumber("virtualRowThreshold");

// uniformOffsets is the prefix sum the renderer builds for a page whose rows are
// all one line tall, which is what the Results table's CSS guarantees.
function uniformOffsets(rows, height) {
  const offsets = new Array(rows + 1);
  offsets[0] = 0;
  for (let index = 0; index < rows; index += 1) offsets[index + 1] = offsets[index] + height;

  return offsets;
}

test("the window never grows with the page", () => {
  const rowHeight = 34;
  const viewport = 900;
  const sizes = [threshold, 500, 5_000, 200_000];
  const widths = new Set();

  for (const rows of sizes) {
    const offsets = uniformOffsets(rows, rowHeight);
    const total = offsets[rows];
    for (let scrollTop = 0; scrollTop <= total; scrollTop += Math.max(1, Math.floor(total / 97))) {
      const chosen = computeRowWindow(offsets, scrollTop, viewport, overscan, windowLimit);
      assert.ok(chosen.start >= 0 && chosen.start <= chosen.end && chosen.end <= rows,
        `window ${JSON.stringify(chosen)} left the row model of ${rows} rows`);
      widths.add(chosen.end - chosen.start);
    }
  }

  // The widest window a 200,000-row page produces is the same as the widest a
  // 500-row page produces: it is a function of the viewport, not of the page.
  const widest = Math.max(...widths);
  const viewportRows = Math.ceil(viewport / rowHeight);
  assert.ok(widest <= viewportRows + 2 * overscan + 2,
    `widest window was ${widest} rows for a ${viewportRows}-row viewport`);
  assert.ok(widest <= windowLimit, `widest window ${widest} exceeded the hard cap ${windowLimit}`);
});

test("the hard cap bounds the window even for an absurd viewport", () => {
  const offsets = uniformOffsets(50_000, 34);
  const chosen = computeRowWindow(offsets, 0, 10_000_000, overscan, windowLimit);

  assert.equal(chosen.start, 0);
  assert.equal(chosen.end - chosen.start, windowLimit);
});

test("the window covers every row the viewport can show", () => {
  const rowHeight = 28;
  const rows = 4_000;
  const offsets = uniformOffsets(rows, rowHeight);
  const viewport = 700;

  for (const scrollTop of [0, 1, rowHeight - 1, 12_345, offsets[rows] - viewport, offsets[rows]]) {
    const chosen = computeRowWindow(offsets, scrollTop, viewport, overscan, windowLimit);
    const first = rowIndexAtOffset(offsets, Math.min(Math.max(0, scrollTop), offsets[rows]));
    const last = rowIndexAtOffset(offsets, Math.min(Math.max(0, scrollTop), offsets[rows]) + viewport);

    assert.ok(chosen.start <= first, `window started at ${chosen.start}, below it row ${first} is visible`);
    assert.ok(chosen.end > last, `window ended at ${chosen.end}, above it row ${last} is visible`);
    // The buffer is real: rows are kept beyond the viewport in both directions
    // wherever the page has room for them.
    if (first >= overscan) assert.equal(chosen.start, first - overscan);
  }
});

test("a short or empty page asks for no window at all", () => {
  assert.deepEqual(computeRowWindow([0], 0, 800, overscan, windowLimit), { start: 0, end: 0 });
  assert.deepEqual(computeRowWindow([], 0, 800, overscan, windowLimit), { start: 0, end: 0 });
  assert.deepEqual(computeRowWindow(null, 0, 800, overscan, windowLimit), { start: 0, end: 0 });

  // One row still yields one row rather than an empty document.
  const single = computeRowWindow(uniformOffsets(1, 34), 0, 800, overscan, windowLimit);
  assert.deepEqual(single, { start: 0, end: 1 });
});

test("nonsense geometry degrades to a valid window instead of throwing", () => {
  const offsets = uniformOffsets(1_000, 34);

  for (const [scrollTop, viewport] of [[-500, 600], [NaN, 600], [1e12, 600], [0, NaN], [0, -1]]) {
    const chosen = computeRowWindow(offsets, scrollTop, viewport, overscan, windowLimit);
    assert.ok(Number.isInteger(chosen.start) && Number.isInteger(chosen.end),
      `window ${JSON.stringify(chosen)} was not a pair of row indexes`);
    assert.ok(chosen.start >= 0 && chosen.start < chosen.end && chosen.end <= 1_000,
      `window ${JSON.stringify(chosen)} left the row model`);
  }
});

test("rowIndexAtOffset finds the row containing a pixel offset", () => {
  const offsets = uniformOffsets(100, 20);

  assert.equal(rowIndexAtOffset(offsets, 0), 0);
  assert.equal(rowIndexAtOffset(offsets, 19), 0);
  assert.equal(rowIndexAtOffset(offsets, 20), 1);
  assert.equal(rowIndexAtOffset(offsets, 1_999), 99);
  // Past the end it clamps to the last row rather than running off the model.
  assert.equal(rowIndexAtOffset(offsets, 10_000), 99);
});

test("the shipped script keeps the machinery the window depends on", () => {
  for (const symbol of [
    "paintRowWindow", "renderEveryRow", "scheduleRowRender", "requestAnimationFrame",
    "syncSelectionMirror", "applyRowIndexes", "revealRow", "aria-rowindex", "aria-rowcount",
  ]) {
    assert.ok(source.includes(symbol), `app-results.js no longer references ${symbol}`);
  }

  assert.ok(threshold > 0, "the safe-degradation threshold must be positive");
});
