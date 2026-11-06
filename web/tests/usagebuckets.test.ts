import { test } from "node:test";
import assert from "node:assert/strict";
import { daily, weekly, monthly, weekStart } from "../src/lib/usagebuckets";

const day = (d: string, cost: number) => ({ day: d, cost_usd: cost, calls: 1, prompt_tokens: 10, cached_tokens: 4, completion_tokens: 2 });
const data = [day("2025-11-30", 5), day("2026-02-27", 1), day("2026-03-01", 2), day("2026-03-02", 4), day("2026-03-03", 8)];
const today = "2026-03-03"; // a Tuesday

test("weekStart is the Monday on or before", () => {
  assert.equal(weekStart("2026-03-03"), "2026-03-02");
  assert.equal(weekStart("2026-03-02"), "2026-03-02");
  assert.equal(weekStart("2026-03-01"), "2026-02-23"); // Sunday belongs to the previous week
});

test("daily: 90 days ending today, zero-filled, ticks on Mondays and month starts", () => {
  const b = daily(data, today);
  assert.equal(b.length, 90);
  assert.equal(b[89].key, today);
  assert.equal(b[89].cost, 8);
  assert.equal(b.find((x) => x.key === "2026-03-01")!.tick, "Mar");
  assert.equal(b.find((x) => x.key === "2026-03-02")!.tick, "2");
  assert.equal(b.reduce((s, x) => s + x.cost, 0), 15); // 2025-11-30 is outside the window
  assert.equal(b[0].key, "2025-12-04");
});

test("weekly: 52 Monday weeks, sums across month boundaries", () => {
  const b = weekly(data, today);
  assert.equal(b.length, 52);
  assert.equal(b[51].key, "2026-03-02");
  assert.equal(b[51].cost, 12); // Mar 2 + Mar 3
  assert.equal(b[50].cost, 3); // Feb 27 + Sunday Mar 1
  assert.equal(b.reduce((s, x) => s + x.cost, 0), 20);
});

test("monthly: from the first record's month to now, across a year boundary", () => {
  const b = monthly(data, today);
  assert.deepEqual(b.map((x) => x.key), ["2025-11-01", "2025-12-01", "2026-01-01", "2026-02-01", "2026-03-01"]);
  assert.deepEqual(b.map((x) => x.cost), [5, 0, 0, 1, 14]);
  assert.equal(b[2].tick, "Jan 2026");
  assert.equal(b[0].tick, "Nov 2025");
  assert.equal(monthly([], today).length, 1);
});
