// Calendar bucketing of per-day usage (GET /usage/daily) into the day, week and
// month series the Usage view charts. Days are "YYYY-MM-DD" in the viewer's
// local time (the server groups by it); all arithmetic is on those calendar
// strings via UTC dates, so DST and timezones can't shift a bucket.
import type { UsageDay } from "./types";

export interface Bucket {
  key: string; // first day of the bucket, YYYY-MM-DD
  label: string; // full name for the readout, e.g. "Mon 3 Mar 2026", "Week of 3 Mar 2026", "March 2026"
  tick: string; // short axis label, or "" for none
  cost: number;
  calls: number;
  prompt: number;
  cached: number;
  completion: number;
}

const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
const LONG_MONTHS = ["January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"];
const WEEKDAYS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];

const parse = (key: string) => new Date(key + "T00:00:00Z");
const fmt = (d: Date) => d.toISOString().slice(0, 10);
const addDays = (key: string, n: number) => {
  const d = parse(key);
  d.setUTCDate(d.getUTCDate() + n);
  return fmt(d);
};

// localToday is the viewer's current calendar day.
export function localToday(now = new Date()): string {
  const y = now.getFullYear(), m = now.getMonth() + 1, d = now.getDate();
  return `${y}-${String(m).padStart(2, "0")}-${String(d).padStart(2, "0")}`;
}

// weekStart: the Monday on or before key.
export function weekStart(key: string): string {
  const wd = parse(key).getUTCDay(); // 0 = Sunday
  return addDays(key, -((wd + 6) % 7));
}

const monthStart = (key: string) => key.slice(0, 8) + "01";

function empty(key: string, label: string, tick: string): Bucket {
  return { key, label, tick, cost: 0, calls: 0, prompt: 0, cached: 0, completion: 0 };
}

function fill(buckets: Bucket[], days: UsageDay[], keyOf: (day: string) => string) {
  const byKey = new Map(buckets.map((b) => [b.key, b]));
  for (const d of days) {
    const b = byKey.get(keyOf(d.day));
    if (!b) continue;
    b.cost += d.cost_usd;
    b.calls += d.calls;
    b.prompt += d.prompt_tokens;
    b.cached += d.cached_tokens;
    b.completion += d.completion_tokens;
  }
  return buckets;
}

// daily: the last n days up to today (inclusive), oldest first.
export function daily(days: UsageDay[], today: string, n = 90): Bucket[] {
  const out: Bucket[] = [];
  for (let i = n - 1; i >= 0; i--) {
    const key = addDays(today, -i);
    const d = parse(key);
    const dom = d.getUTCDate();
    const tick = dom === 1 ? MONTHS[d.getUTCMonth()] : d.getUTCDay() === 1 ? String(dom) : "";
    out.push(empty(key, `${WEEKDAYS[d.getUTCDay()]} ${dom} ${MONTHS[d.getUTCMonth()]} ${d.getUTCFullYear()}`, tick));
  }
  return fill(out, days, (k) => k);
}

// weekly: the last n Monday-based weeks up to the current one, oldest first.
export function weekly(days: UsageDay[], today: string, n = 52): Bucket[] {
  const out: Bucket[] = [];
  const last = weekStart(today);
  let prevMonth = -1;
  for (let i = n - 1; i >= 0; i--) {
    const key = addDays(last, -7 * i);
    const d = parse(key);
    const m = d.getUTCMonth();
    const tick = m !== prevMonth ? (m === 0 ? String(d.getUTCFullYear()) : MONTHS[m]) : "";
    prevMonth = m;
    out.push(empty(key, `Week of ${d.getUTCDate()} ${MONTHS[m]} ${d.getUTCFullYear()}`, tick));
  }
  return fill(out, days, weekStart);
}

// monthly: every month from the first recorded day's month to the current one.
export function monthly(days: UsageDay[], today: string): Bucket[] {
  const first = days.length ? monthStart(days.reduce((a, d) => (d.day < a ? d.day : a), days[0].day)) : monthStart(today);
  const out: Bucket[] = [];
  const end = monthStart(today);
  for (let key = first; key <= end; ) {
    const d = parse(key);
    const m = d.getUTCMonth(), y = d.getUTCFullYear();
    out.push(empty(key, `${LONG_MONTHS[m]} ${y}`, m === 0 || key === first ? `${MONTHS[m]} ${y}` : MONTHS[m]));
    key = `${m === 11 ? y + 1 : y}-${String(m === 11 ? 1 : m + 2).padStart(2, "0")}-01`;
  }
  return fill(out, days, monthStart);
}
