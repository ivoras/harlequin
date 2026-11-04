// Desktop colour theme: "system" follows the OS; light/dark pin it. The
// choice is applied as <html data-theme> (the inline script in index.html
// applies it before first paint) and remembered per browser.
const KEY = "harlequin.theme";
export type ThemePref = "system" | "light" | "dark";

function read(): ThemePref {
  try {
    const v = localStorage.getItem(KEY);
    return v === "light" || v === "dark" ? v : "system";
  } catch {
    return "system";
  }
}

export const theme = $state<{ pref: ThemePref }>({ pref: read() });

export function setTheme(p: ThemePref): void {
  theme.pref = p;
  try {
    if (p === "system") localStorage.removeItem(KEY);
    else localStorage.setItem(KEY, p);
  } catch {
    /* storage blocked: applies for this page only */
  }
  if (p === "system") delete document.documentElement.dataset.theme;
  else document.documentElement.dataset.theme = p;
}
