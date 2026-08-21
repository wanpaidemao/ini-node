<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { app, navigate, setConnected } from "./lib/store.svelte";
  import { Services } from "./lib/services";
  import { t, fmt } from "./lib/i18n";
  import type { Route } from "./lib/types";
  import Dashboard from "./pages/Dashboard.svelte";
  import Internals from "./pages/Internals.svelte";
  import Wallet from "./pages/Wallet.svelte";
  import Send from "./pages/Send.svelte";
  import Create from "./pages/Create.svelte";
  import Settings from "./pages/Settings.svelte";
  import Console from "./pages/Console.svelte";
  import ControlCenter from "./pages/ControlCenter.svelte";

  // Page kinds: "rpc" pages go through the RPC proxy and can also be
  // served standalone as a plain frontend; "wails" pages need the desktop
  // (Wails) backend (node control / local settings / console).
  // 页面类型:"rpc" 页面经 RPC 代理访问,也可独立作为纯前端;"wails" 页面
  // 依赖桌面(Wails)后端(节点控制/本地设置/控制台)。
  type PageKind = "rpc" | "wails";
  const sections: { title: string; items: { route: Route; label: string; kind: PageKind }[] }[] = [
    {
      title: "nav.section.node",
      items: [
        { route: "dashboard", label: "nav.dashboard", kind: "rpc" },
        { route: "internals", label: "nav.internals", kind: "rpc" },
        { route: "control", label: "nav.control", kind: "wails" },
      ],
    },
    {
      title: "nav.section.wallet",
      items: [
        { route: "wallet", label: "nav.wallet", kind: "rpc" },
        { route: "create", label: "create.title", kind: "rpc" },
        { route: "send", label: "nav.send", kind: "rpc" },
      ],
    },
    {
      title: "nav.section.system",
      items: [
        { route: "settings", label: "nav.settings", kind: "wails" },
        { route: "console", label: "nav.console", kind: "wails" },
      ],
    },
  ];

  const titles: Record<Route, string> = {
    dashboard: "nav.dashboard",
    internals: "nav.internals",
    control: "nav.control",
    wallet: "nav.wallet",
    send: "nav.send",
    create: "create.title",
    settings: "nav.settings",
    console: "nav.console",
  };

  const menus = sections.map((s) => ({
    title: s.title,
    main: s.items[0],
    items: s.items.slice(1),
  }));

  function isOnMenu(m: { main: { route: Route }; items: { route: Route }[] }): boolean {
    return app.route === m.main.route || m.items.some((i) => i.route === app.route);
  }

  let openMenu: Route | null = $state(null);
  let collapsible = $state(false);

  function go(r: Route) {
    navigate(r);
  }

  function connBadge() {
    if (app.connecting) return { cls: "busy", key: "shell.connecting" };
    if (!app.connected) return { cls: "off", key: "shell.offline" };
    if (app.syncing) return { cls: "sync", key: "shell.syncing" };
    return { cls: "on", key: "shell.connected" };
  }

  let healthTimer: ReturnType<typeof setInterval> | undefined;
  let navSync = $state<{ blocks: number; headers: number; rate: number } | null>(null);

  async function checkHealth() {
    if (!app.connected) app.connecting = true;
    try {
      const s = await Services.getSyncStatus();
      setConnected({ connected: true, syncing: s.blocks < s.headers });
      navSync = { blocks: s.blocks, headers: s.headers, rate: s.rateBlPerSec };
    } catch {
      setConnected({ connected: false, syncing: false });
    } finally {
      app.connecting = false;
    }
  }

  onMount(() => {
    checkHealth();
    healthTimer = setInterval(checkHealth, 5000);
  });
  onDestroy(() => clearInterval(healthTimer));
</script>

<svelte:head>
  <title>{t(titles[app.route])}</title>
</svelte:head>

<div class="shell" class:nav-top={app.navMode === "top"} class:nav-side={app.navMode !== "top"} class:collapsed={collapsible}>
  <a class="skip-link" href="#main">Skip to content</a>

  <!-- top navigation (default): main menus with hover submenus · live node sync rail -->
  {#if app.navMode === "top"}
    <header class="topnav" aria-label="Main navigation">
      <nav class="topnav-menus">
        {#each menus as m}
          <div
            class="menu-wrap"
            role="group"
            onmouseenter={() => (openMenu = m.main.route)}
            onmouseleave={() => (openMenu = null)}
            onfocusin={() => (openMenu = m.main.route)}
            onfocusout={(e) => {
              if (!e.currentTarget.contains(e.relatedTarget as Node)) openMenu = null;
            }}
          >
            <button
              class="menu-btn"
              class:active={isOnMenu(m)}
              aria-current={isOnMenu(m) ? "page" : undefined}
              aria-haspopup={m.items.length ? "menu" : undefined}
              aria-expanded={m.items.length ? openMenu === m.main.route : undefined}
              onclick={() => go(m.main.route)}
              title={t(m.main.label)}
            >
              {t(m.main.label)}
              {#if m.items.length}
                <svg class="chev" viewBox="0 0 24 24" width="12" height="12" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" aria-hidden="true">
                  <polyline points="6 9 12 15 18 9" />
                </svg>
              {/if}
            </button>
            {#if m.items.length}
              <div class="menu-drop" class:open={openMenu === m.main.route} role="menu">
                {#each m.items as item}
                  <button
                    class="menu-drop-item"
                    class:active={app.route === item.route}
                    role="menuitem"
                    onclick={() => go(item.route)}
                    title={t(item.label)}
                  >
                    {t(item.label)}
                  </button>
                {/each}
              </div>
            {/if}
          </div>
        {/each}
      </nav>

      <div
        class="nav-sync"
        class:on={connBadge().cls === "on"}
        class:sync={connBadge().cls === "sync"}
        class:off={connBadge().cls === "off"}
        class:busy={connBadge().cls === "busy"}
        role="status"
        title={t(connBadge().key)}
      >
        <span class="nav-sync-dot" aria-hidden="true"></span>
        <span class="nav-sync-state">{t(connBadge().key)}</span>
        <span class="nav-sync-num mono" translate="no">
          {#if navSync}
            {navSync.headers > 0 ? Math.min(100, (navSync.blocks / navSync.headers) * 100).toFixed(1) : "—"}%
          {:else}
            —%
          {/if}
        </span>
        <span class="nav-sync-blocks mono" translate="no" title={navSync ? `#${fmt(navSync.blocks)} / #${fmt(navSync.headers)}` : undefined}>
          {navSync ? fmt(navSync.blocks) : "…"}
        </span>
        <span class="nav-sync-rate mono" translate="no">{navSync ? `${navSync.rate.toFixed(0)} bl/s` : "—"}</span>
      </div>

      <!-- signature: the chain fill — the nav is a live instrument, not a menu -->
      <div class="chain-fill" aria-hidden="true">
        <span
          class="chain-fill-bar"
          style:width={`${navSync && navSync.headers > 0 ? Math.min(100, (navSync.blocks / navSync.headers) * 100) : 0}%`}
        ></span>
      </div>
    </header>
  {/if}

  <!-- side rail (optional): the classic collapsed sidebar -->
  {#if app.navMode !== "top"}
    <aside class="rail" class:collapsed={collapsible}>
      <div class="rail-top">
        <button
          class="collapse-btn"
          aria-label={collapsible ? "Expand navigation" : "Collapse navigation"}
          title={collapsible ? "Expand navigation" : "Collapse navigation"}
          onclick={() => (collapsible = !collapsible)}
        >
          <svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true">
            {#if collapsible}
              <polyline points="9 6 15 12 9 18" />
            {:else}
              <polyline points="15 6 9 12 15 18" />
            {/if}
          </svg>
        </button>
      </div>
      <nav>
        {#each sections as section}
          <p class="section-head">{t(section.title)}</p>
          <ul>
            {#each section.items as item}
              <li>
                <button
                  class="nav-item"
                  class:active={app.route === item.route}
                  aria-current={app.route === item.route ? "page" : undefined}
                  onclick={() => go(item.route)}
                  title={t(item.label)}
                >
                  <span class="nav-dot" aria-hidden="true"></span>
                  <span class="nav-text">{t(item.label)}</span>
                </button>
              </li>
            {/each}
          </ul>
        {/each}
      </nav>
    </aside>
  {/if}

  <div class="main-col">
    {#if !app.connected && app.route === "dashboard"}
      <div style="display:flex;justify-content:space-between;align-items:center;gap:8px;margin:10px 16px;padding:10px 14px;border:1px solid #d97706;border-radius:8px;background:#fef3c7;font-size:13px">
        <span>Node RPC not reachable / 节点 RPC 不可达</span>
        <button class="btn btn-primary" onclick={() => navigate("control")} style="font-size:12px">
          Open Control Center / 打开控制中心
        </button>
      </div>
    {/if}

    <main id="main" class="page">
      {#if app.route === "dashboard"}
        <Dashboard />
      {:else if app.route === "internals"}
        <Internals />
      {:else if app.route === "wallet"}
        <Wallet />
      {:else if app.route === "send"}
        <Send />
      {:else if app.route === "create"}
        <Create />
      {:else if app.route === "settings"}
        <Settings />
      {:else if app.route === "console"}
        <Console />
      {:else if app.route === "control"}
        <ControlCenter />
      {/if}
    </main>
  </div>
</div>

<style>
  .shell {
    display: grid;
    grid-template-columns: var(--rail-w) 1fr;
    grid-template-rows: 100vh;
    height: 100vh;
    overflow: hidden;
    background: var(--ink);
    color: var(--ink-fg);
    transition: grid-template-columns 0.15s ease;
  }
  .shell.nav-top {
    grid-template-columns: 1fr;
    grid-template-rows: var(--nav-h) minmax(0, 1fr);
  }
  .shell.nav-top.collapsed {
    grid-template-columns: 1fr;
  }
  .shell.collapsed {
    grid-template-columns: 48px 1fr;
  }

  /* ── top navigation bar ────────────────────────── */
  .topnav {
    grid-row: 1;
    position: relative;
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 0 12px 0 16px;
    background: var(--violet);
    border-bottom: 1px solid var(--line);
    min-width: 0;
  }
  .topnav-menus {
    display: flex;
    align-items: stretch;
    gap: 4px;
    min-width: 0;
    flex: 1;
  }
  .menu-wrap {
    position: relative;
    display: flex;
    align-items: center;
  }
  .menu-btn {
    appearance: none;
    border: none;
    background: none;
    color: var(--ink-dim);
    font-family: var(--font-body);
    font-size: 13px;
    font-weight: 600;
    padding: 7px 11px;
    border-radius: var(--r-8);
    cursor: pointer;
    white-space: nowrap;
    display: inline-flex;
    align-items: center;
    gap: 5px;
    transition: background 0.12s ease, color 0.12s ease;
    touch-action: manipulation;
  }
  .menu-btn .chev {
    transition: transform 0.12s ease;
  }
  .menu-btn:hover {
    background: rgba(0, 0, 0, 0.06);
    color: var(--ink-fg);
  }
  .menu-btn:focus-visible {
    outline: none;
    box-shadow: var(--focus);
  }
  .menu-btn.active {
    background: var(--violet-2);
    color: var(--ink-fg);
    box-shadow: inset 0 -2px 0 var(--straw);
  }
  .menu-btn.active .chev {
    transform: rotate(180deg);
  }

  .menu-drop {
    position: absolute;
    /* No gap between the button and the dropdown: a gap triggers the
       wrapper's onmouseleave while the pointer travels to the submenu,
       closing it before it can be reached.  Keep the visual spacing via
       inner padding instead.
       按钮与下拉之间不留空隙:空隙会在指针移向子菜单时触发外层
       onmouseleave 提前关闭子菜单。视觉间距改用内边距实现。*/
    top: 100%;
    left: 0;
    min-width: 172px;
    display: none;
    flex-direction: column;
    gap: 2px;
    padding: 6px;
    padding-top: 10px;
    background: var(--ink);
    border: 1px solid var(--line);
    border-radius: var(--r-8);
    box-shadow: var(--shadow);
    z-index: 60;
  }
  .menu-drop.open {
    display: flex;
  }
  .menu-drop-item {
    appearance: none;
    border: none;
    background: none;
    color: var(--ink-fg);
    font-family: var(--font-body);
    font-size: 13px;
    font-weight: 500;
    text-align: left;
    padding: 8px 10px;
    border-radius: var(--r-8);
    cursor: pointer;
    white-space: nowrap;
    transition: background 0.12s ease, color 0.12s ease;
  }
  .menu-drop-item:hover {
    background: var(--violet-2);
  }
  .menu-drop-item:focus-visible {
    outline: none;
    box-shadow: var(--focus);
  }
  .menu-drop-item.active {
    color: var(--straw);
  }

  /* live sync readout */
  .nav-sync {
    display: inline-flex;
    align-items: center;
    gap: 7px;
    flex: none;
    padding: 5px 10px;
    border-radius: 999px;
    font-size: 12px;
    font-weight: 700;
    color: var(--ink-dim);
    border: 1px solid var(--line);
    background: var(--ink);
  }
  .nav-sync-dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--mist);
    flex: none;
  }
  .nav-sync-num {
    font-variant-numeric: tabular-nums;
  }
  .nav-sync-blocks {
    border-left: 1px solid var(--line);
    padding-left: 7px;
    font-variant-numeric: tabular-nums;
  }
  .nav-sync-rate {
    font-variant-numeric: tabular-nums;
  }
  .nav-sync.on {
    color: var(--mint);
    border-color: var(--mint);
  }
  .nav-sync.on .nav-sync-dot {
    background: var(--mint);
  }
  .nav-sync.sync {
    color: var(--honey);
    border-color: var(--honey);
  }
  .nav-sync.sync .nav-sync-dot {
    background: var(--honey);
    animation: pulse 1.6s ease-in-out infinite;
  }
  .nav-sync.off .nav-sync-dot {
    background: var(--mist);
  }
  .nav-sync.busy .nav-sync-dot {
    background: var(--straw);
  }

  @keyframes pulse {
    0%,
    100% {
      opacity: 1;
    }
    50% {
      opacity: 0.35;
    }
  }

  /* sync progress strip along the bottom edge */
  .chain-fill {
    position: absolute;
    left: 0;
    right: 0;
    bottom: -1px;
    height: 3px;
    background: rgba(0, 0, 0, 0.1);
    pointer-events: none;
  }
  .chain-fill-bar {
    display: block;
    height: 100%;
    background: var(--straw);
    transition: width 0.6s ease;
  }

  /* ── side rail ────────────────────────────────── */
  .rail {
    grid-row: 1;
    background: var(--violet);
    border-right: 1px solid var(--line);
    display: flex;
    flex-direction: column;
    padding: 12px 10px;
    overflow-y: auto;
    min-width: 0;
  }
  .rail-top {
    display: flex;
    align-items: center;
    padding: 2px 4px 8px;
    flex: none;
  }
  .collapse-btn {
    background: none;
    border: none;
    color: var(--mist);
    padding: 5px;
    cursor: pointer;
    border-radius: var(--r-8);
    display: inline-flex;
    align-items: center;
    justify-content: center;
  }
  .collapse-btn:hover {
    color: var(--ink-fg);
    background: rgba(0, 0, 0, 0.07);
  }
  .collapse-btn:focus-visible {
    outline: none;
    box-shadow: var(--focus);
  }
  .rail.collapsed .section-head,
  .rail.collapsed .nav-text {
    display: none;
  }
  .rail.collapsed nav ul {
    display: flex;
    flex-direction: column;
    align-items: center;
  }
  .rail.collapsed .nav-item {
    justify-content: center;
    padding: 9px 0;
  }
  .rail.collapsed .rail-top {
    justify-content: center;
    padding: 2px 0 8px;
  }

  .section-head {
    font-size: 11px;
    font-weight: 800;
    letter-spacing: 1.4px;
    text-transform: uppercase;
    color: var(--mist);
    margin: 16px 8px 6px;
    font-family: var(--font-display);
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .nav-item {
    display: flex;
    align-items: center;
    gap: 10px;
    width: 100%;
    background: none;
    border: none;
    color: var(--ink-dim);
    padding: 9px 12px;
    border-radius: var(--r-8);
    cursor: pointer;
    font-size: 13px;
    font-weight: 600;
    transition: background 0.12s ease, color 0.12s ease;
    touch-action: manipulation;
  }
  .nav-item:hover {
    background: rgba(0, 0, 0, 0.07);
    color: var(--ink-fg);
  }
  .nav-item:focus-visible {
    outline: none;
    box-shadow: var(--focus);
  }
  .nav-item.active {
    color: var(--ink-fg);
    background: var(--violet-2);
  }
  .nav-dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--mist);
    flex: none;
  }
  .nav-item.active .nav-dot {
    background: var(--straw);
  }

  .main-col {
    grid-row: 1;
    display: grid;
    /* The topbar row was removed with the topbar itself; a single
       minmax(0,1fr) row keeps the page content filling the full column
       height (previously it was squeezed into the 48px topbar row). */
    grid-template-rows: minmax(0, 1fr);
    min-width: 0;
    min-height: 0;
  }
  .shell.nav-top .main-col {
    grid-row: 2;
  }

  .page {
    overflow-y: auto;
    padding: 20px 24px 40px;
    scroll-margin-top: 8px;
  }

  .skip-link {
    position: absolute;
    left: -9999px;
    top: 0;
    z-index: 100;
    padding: 8px 14px;
    background: var(--straw);
    border-radius: 0 0 var(--r-8) 0;
  }
  .skip-link:focus-visible {
    left: 0;
  }

  @media (max-width: 760px) {
    .shell {
      grid-template-columns: 1fr;
      grid-template-rows: auto 1fr;
    }
    .shell.collapsed {
      grid-template-columns: 1fr;
    }
    .shell.nav-top {
      grid-template-rows: auto minmax(0, 1fr);
    }
    .rail-top,
    .collapse-btn {
      display: none;
    }
    .rail {
      flex-direction: row;
      align-items: flex-start;
      border-right: none;
      border-bottom: 1px solid var(--line);
      padding: 8px;
      overflow-x: auto;
    }
    .section-head {
      display: none;
    }
    .rail nav {
      display: flex;
      align-items: center;
      gap: 4px;
    }
    .rail nav ul {
      display: contents;
    }
    .nav-item {
      width: auto;
      white-space: nowrap;
      padding: 7px 12px;
    }
    .nav-dot {
      display: none;
    }
    .main-col {
      grid-row: 2;
    }
    .topnav {
      flex-wrap: wrap;
      gap: 6px;
      padding: 8px 10px;
    }
    .topnav-menus {
      order: 3;
      flex-basis: 100%;
      padding-bottom: 4px;
    }
  }
</style>