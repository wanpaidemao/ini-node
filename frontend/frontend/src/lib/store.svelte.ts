// ── ini-node app store: route, language, connection ──
// Svelte 5 runes module — reactive from anywhere, import { app } directly.
import type { Route } from "./types";

export const app = $state({
  route: "dashboard" as Route,
  connected: true,
  connecting: false,
  syncing: true,
  // cross-page hints
  shortcut: {} as Record<string, unknown>,
});

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

// init route from location.hash (deep-linking, also enables headless screenshots)
function initRoute(): void {
  const h = (location.hash || "").replace("#", "") as Route;
  const valid: Route[] = ["dashboard", "internals", "wallet", "send", "create", "settings", "console"];
  if (valid.includes(h)) app.route = h;
}
if (typeof window !== "undefined") initRoute();