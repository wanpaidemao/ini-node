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

// navigate with optional cross-page params: they land in app.shortcut so the
// target page can pick them up (e.g. wallet history → explorer tx detail).
// navigate 附带跨页参数:参数写入 app.shortcut 供目标页读取(例如钱包
// 历史 → 浏览器交易详情)。
export function navigate(route: Route, params?: Record<string, unknown>): void {
  app.route = route;
  // clear stale shortcuts on navigation (or set the new ones)
  app.shortcut = params ?? {};
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
  const valid: Route[] = ["dashboard", "internals", "explorer", "wallet", "wallet-settings", "send", "create", "settings", "console", "control"];
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