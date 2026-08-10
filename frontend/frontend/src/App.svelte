<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { app, navigate, setConnected } from "./lib/store.svelte";
  import { Services } from "./lib/services";
  import { t, setLang, LANG_NAMES, LANGS } from "./lib/i18n";
  import type { Route } from "./lib/types";
  import Dashboard from "./pages/Dashboard.svelte";
  import Internals from "./pages/Internals.svelte";
  import Wallet from "./pages/Wallet.svelte";
  import Send from "./pages/Send.svelte";
  import Create from "./pages/Create.svelte";
  import Settings from "./pages/Settings.svelte";
  import Console from "./pages/Console.svelte";

  const sections: { title: string; items: { route: Route; label: string }[] }[] = [
    {
      title: "nav.section.node",
      items: [
        { route: "dashboard", label: "nav.dashboard" },
        { route: "internals", label: "nav.internals" },
      ],
    },
    {
      title: "nav.section.wallet",
      items: [
        { route: "wallet", label: "nav.wallet" },
        { route: "create", label: "create.title" },
        { route: "send", label: "nav.send" },
      ],
    },
    {
      title: "nav.section.system",
      items: [
        { route: "settings", label: "nav.settings" },
        { route: "console", label: "nav.console" },
      ],
    },
  ];

  const titles: Record<Route, string> = {
    dashboard: "nav.dashboard",
    internals: "nav.internals",
    wallet: "nav.wallet",
    send: "nav.send",
    create: "create.title",
    settings: "nav.settings",
    console: "nav.console",
  };

  let collapsible = $state(false);
  let langOpen = $state(false);
  let lang = $state<keyof typeof LANG_NAMES>("en");
  let connAddr = $state("—");
  let debugLevel = $state("info");

  async function loadConnAddr() {
    try {
      const cfg = await Services.getConfig();
      connAddr = cfg.rpcEndpoint.replace(/^https?:\/\//, "");
    } catch {
      /* keep "—" */
    }
  }

  function go(r: Route) {
    navigate(r);
    langOpen = false;
  }

  function setLangUI(l: keyof typeof LANG_NAMES) {
    setLang(l);
    lang = l;
    langOpen = false;
  }

  function connBadge() {
    if (app.connecting) return { cls: "busy", key: "shell.connecting" };
    if (!app.connected) return { cls: "off", key: "shell.offline" };
    if (app.syncing) return { cls: "sync", key: "shell.syncing" };
    return { cls: "on", key: "shell.connected" };
  }

  function jumpDebug() {
    if (app.route !== "internals") {
      app.shortcut.focusDebug = true;
      navigate("internals");
    }
  }

  let healthTimer: ReturnType<typeof setInterval> | undefined;

  async function checkHealth() {
    if (!app.connected) app.connecting = true;
    try {
      const [s, d] = await Promise.all([Services.getSyncStatus(), Services.getDebugLevel()]);
      debugLevel = d;
      setConnected({ connected: true, syncing: s.blocks < s.headers });
    } catch {
      setConnected({ connected: false, syncing: false });
    } finally {
      app.connecting = false;
    }
  }

  onMount(() => {
    checkHealth();
    loadConnAddr();
    healthTimer = setInterval(checkHealth, 5000);
  });
  onDestroy(() => clearInterval(healthTimer));
</script>

<svelte:head>
  <title>{t(titles[app.route])}</title>
</svelte:head>

<div class="shell" class:collapsed={collapsible}>
  <a class="skip-link" href="#main">Skip to content</a>

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

  <div class="main-col">
    <header class="topbar">
      <span class="topbar-title">{t(titles[app.route])}</span>
      <div class="topbar-right">
        <div class="lang-wrap">
          <button class="chip lang-chip" aria-haspopup="true" aria-expanded={langOpen} onclick={() => (langOpen = !langOpen)}>
            {LANG_NAMES[lang] || "English"}
            <svg viewBox="0 0 24 24" width="11" height="11" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><circle cx="12" cy="12" r="9" /><path d="M3 12h18M12 3a15 15 0 0 1 0 18M12 3a15 15 0 0 0 0 18" /></svg>
          </button>
          {#if langOpen}
            <div class="lang-panel" role="listbox" aria-label="Language">
              {#each LANGS as l}
                <button
                  class="lang-opt"
                  class:active={lang === l}
                  role="option"
                  aria-selected={lang === l}
                  onclick={() => setLangUI(l)}
                >
                  {LANG_NAMES[l]}
                </button>
              {/each}
            </div>
          {/if}
        </div>

        <button class="chip debug-chip" onclick={jumpDebug} title={t("shell.debug")}>
          <span class="dot" aria-hidden="true"></span> debug · {debugLevel}
        </button>

        <div class="conn-chip {connBadge().cls}" role="status">
          <span class="dot" aria-hidden="true"></span>
          <span>{t(connBadge().key)}</span>
          <span class="conn-addr mono">{app.connected ? connAddr : "—"}</span>
        </div>
      </div>
    </header>

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
      {/if}
    </main>
  </div>

  {#if langOpen}
    <div class="lang-overlay" onclick={() => (langOpen = false)} aria-hidden="true"></div>
  {/if}
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
  .shell.collapsed {
    grid-template-columns: 48px 1fr;
  }

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
    grid-template-rows: var(--top-h) 1fr;
    min-width: 0;
  }

  .topbar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0 16px;
    border-bottom: 1px solid var(--line);
    background: var(--ink);
    z-index: 5;
  }
  .topbar-title {
    font-family: var(--font-display);
    font-weight: 800;
    font-size: 15px;
  }
  .topbar-right {
    display: flex;
    align-items: center;
    gap: 10px;
  }

  .lang-wrap {
    position: relative;
  }
  .lang-chip,
  .debug-chip {
    cursor: pointer;
    transition: color 0.12s ease, border-color 0.12s ease;
  }
  .lang-chip:hover,
  .debug-chip:hover {
    color: var(--ink-fg);
    border-color: rgba(0, 0, 0, 0.2);
  }
  .lang-chip:focus-visible,
  .debug-chip:focus-visible {
    outline: none;
    box-shadow: var(--focus);
  }

  .lang-panel {
    position: absolute;
    top: calc(100% + 8px);
    right: 0;
    background: var(--ink);
    border: 1px solid var(--line);
    border-radius: var(--r-12);
    box-shadow: var(--shadow);
    padding: 6px;
    min-width: 150px;
    z-index: 30;
  }
  .lang-panel button {
    display: block;
    width: 100%;
    text-align: left;
    background: none;
    border: none;
    color: var(--ink-dim);
    padding: 8px 10px;
    border-radius: var(--r-8);
    cursor: pointer;
    font-size: 13px;
  }
  .lang-panel button:hover {
    background: rgba(0, 0, 0, 0.07);
    color: var(--ink-fg);
  }
  .lang-panel button.active {
    color: var(--straw);
  }
  .lang-overlay {
    position: fixed;
    inset: 0;
    z-index: 20;
  }

  .debug-chip .dot {
    background: var(--honey);
  }

  .conn-chip {
    display: inline-flex;
    align-items: center;
    gap: 7px;
    border-radius: 999px;
    padding: 4px 12px;
    font-size: 12px;
    border: 1px solid var(--line);
    color: var(--ink-dim);
    transition: border-color 0.12s ease, color 0.12s ease;
  }
  .conn-chip .dot {
    background: var(--mist);
    flex: none;
  }
  .conn-chip.on {
    border-color: var(--mint);
    color: var(--mint);
  }
  .conn-chip.on .dot {
    background: var(--mint);
  }
  .conn-chip.sync {
    border-color: var(--honey);
    color: var(--honey);
  }
  .conn-chip.sync .dot {
    background: var(--honey);
  }
  .conn-chip.off {
    border-color: rgba(0, 0, 0, 0.25);
    color: var(--ink-dim);
  }
  .conn-addr {
    opacity: 0.65;
    font-size: 11px;
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
    .brand,
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
  }
</style>