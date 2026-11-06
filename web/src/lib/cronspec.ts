// Common schedules for the cron form, and their conversion to/from the raw
// cron spec the server stores (5-field or "@every <dur>"). Times are the
// server's clock. parseSpec only recognises what buildSpec produces (plus
// "@hourly"), so anything else opens in the form's "Custom" mode untouched.

export type PresetID =
  | "every15m" | "every30m" | "hourly" | "every6h" | "every12h"
  | "daily" | "weekdays" | "weekly" | "monthly";

export interface Sched {
  preset: PresetID;
  time: string; // "HH:MM", used by the presets that run at a time of day
  dow: number; // 0 = Sunday … 6, used by "weekly"
}

export const PRESETS: { id: PresetID; label: string; time?: boolean; dow?: boolean }[] = [
  { id: "every15m", label: "Every 15 minutes" },
  { id: "every30m", label: "Every 30 minutes" },
  { id: "hourly", label: "Every hour" },
  { id: "every6h", label: "Every 6 hours" },
  { id: "every12h", label: "Every 12 hours" },
  { id: "daily", label: "Every day", time: true },
  { id: "weekdays", label: "Every weekday (Mon–Fri)", time: true },
  { id: "weekly", label: "Every week", time: true, dow: true },
  { id: "monthly", label: "Every month (on the 1st)", time: true },
];

export const WEEKDAYS = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];

export const defaultSched = (): Sched => ({ preset: "daily", time: "09:00", dow: 1 });

const FIXED: Partial<Record<PresetID, string>> = {
  every15m: "@every 15m",
  every30m: "@every 30m",
  hourly: "0 * * * *",
  every6h: "@every 6h",
  every12h: "@every 12h",
};

export function buildSpec(s: Sched): string {
  const fixed = FIXED[s.preset];
  if (fixed) return fixed;
  const [h, m] = s.time.split(":").map((x) => parseInt(x, 10));
  const hm = `${Number.isFinite(m) ? m : 0} ${Number.isFinite(h) ? h : 9}`;
  switch (s.preset) {
    case "weekdays": return `${hm} * * 1-5`;
    case "weekly": return `${hm} * * ${s.dow}`;
    case "monthly": return `${hm} 1 * *`;
    default: return `${hm} * * *`;
  }
}

export function parseSpec(spec: string): Sched | null {
  const t = spec.trim().replace(/\s+/g, " ");
  if (t === "@hourly") return { ...defaultSched(), preset: "hourly" };
  for (const [id, s] of Object.entries(FIXED)) if (s === t) return { ...defaultSched(), preset: id as PresetID };
  const m = /^(\d{1,2}) (\d{1,2}) (\*|1) \* (\*|1-5|[0-6])$/.exec(t);
  if (!m) return null;
  const [, min, hour, dom, dow] = m;
  if (+min > 59 || +hour > 23) return null;
  const time = `${hour.padStart(2, "0")}:${min.padStart(2, "0")}`;
  const base = { ...defaultSched(), time };
  if (dom === "1") return dow === "*" ? { ...base, preset: "monthly" } : null;
  if (dow === "*") return { ...base, preset: "daily" };
  if (dow === "1-5") return { ...base, preset: "weekdays" };
  return { ...base, preset: "weekly", dow: +dow };
}

// describe renders a spec for people: "Every day at 09:00", or the raw spec
// when it isn't one of the presets.
export function describe(spec: string): string {
  const s = parseSpec(spec);
  if (!s) return spec;
  const p = PRESETS.find((x) => x.id === s.preset)!;
  const day = p.dow ? ` on ${WEEKDAYS[s.dow]}` : "";
  return p.time ? `${p.label}${day} at ${s.time}` : p.label;
}
