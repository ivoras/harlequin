// Which web UI (desktop at /, mobile at /m/) this browser prefers. Unset means
// "pick by screen width" (see the redirect script in web/index.html); an
// explicit switch from either UI is remembered.
const KEY = "harlequin.ui";

export type UIKind = "desktop" | "mobile";

export function switchUI(to: UIKind): void {
  try {
    localStorage.setItem(KEY, to);
  } catch {
    /* storage blocked: the switch still works for this navigation */
  }
  // Carry ?s=&p= so the same session opens in the other UI.
  location.assign((to === "mobile" ? "/m/" : "/") + location.search);
}
