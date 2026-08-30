// ── app store: route, language, connection ──
// Svelte 5 runes module — reactive from anywhere, import { app } directly.
import type { Route } from "./types";

export type NavMode = "top" | "side";

export const app = $state({
  route: "dashboard" as Route,
  navMode: "top" as NavMode,
  connected: false,
  connecting: false,
  syncing: true,
  // cross-page hints
  shortcut: {} as Record<string, unknown>,
});

export function setConnected(state: { connected: boolean; syncing: boolean }): void {
  app.connected = state.connected;
  app.syncing = state.syncing;
}

export function navigate(route: Route): void {
  app.route = route;
  // clear stale shortcuts on navigation
  app.shortcut = {};
  try {
    history.replaceState(null, "", `#${route}`);
  } catch {
    /* ignore */
  }
}

const NAV_KEY = "ini-node.nav";

/** persist the nav layout the user picked in Settings → Appearance. */
export function setNavMode(mode: NavMode): void {
  app.navMode = mode;
  try {
    localStorage.setItem(NAV_KEY, mode);
  } catch {
    /* ignore */
  }
}

// init route from location.hash (deep-linking, also enables headless screenshots)
function initRoute(): void {
  const h = (location.hash || "").replace("#", "") as Route;
  const valid: Route[] = ["dashboard", "internals", "wallet", "wallet-settings", "send", "create", "settings", "console"];
  if (valid.includes(h)) app.route = h;
}
if (typeof window !== "undefined") initRoute();

// restore nav layout; top bar is the default (keeps the content column wide, and
// leaves room for a frameless wails3 titlebar drag region later).
if (typeof window !== "undefined") {
  try {
    const saved = localStorage.getItem(NAV_KEY) as NavMode | null;
    if (saved === "top" || saved === "side") app.navMode = saved;
  } catch {
    /* keep default */
  }
}