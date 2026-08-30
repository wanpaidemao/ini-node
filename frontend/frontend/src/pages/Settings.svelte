<script lang="ts">
  import { onMount } from "svelte";
  import { LANG_NAMES, LANGS, setLang, t } from "../lib/i18n";
  import { Services } from "../lib/services";
  import { app, setNavMode, type NavMode } from "../lib/store.svelte";
  import type { AppConfig } from "../lib/types";

  // Settings page — every control now maps to a REAL backend: node ini keys
  // (datadir/maxpeers/upnp/debuglevel) go through /api/node-config to
  // btcd-runtime.ini and apply on the next node start; frontend-only keys
  // (runnodeonstart) persist in frontend.ini; language/nav layout stay in
  // localStorage. The old mock controls (fake addrType radio, fake migrate
  // progress, unbound "open dir" button, a foreign machine's hardcoded
  // datadir) are gone.
  // 设置页——每个控件都对接真实后端:节点 ini 键(datadir/maxpeers/upnp/
  // debuglevel)经 /api/node-config 写入 btcd-runtime.ini,节点下次启动
  // 生效;前端专属键(runnodeonstart)存 frontend.ini;语言/导航布局存
  // localStorage。旧版 mock 控件(假地址类型单选、假迁移进度、未绑事件的
  // "打开目录"按钮、硬编码的他人机器 datadir)已全部移除。
  type Category = "connection" | "node" | "wallet" | "data" | "appearance";

  const categories: { id: Category; label: string }[] = [
    { id: "connection", label: t("set.connection") },
    { id: "node", label: t("set.sync") },
    { id: "wallet", label: t("set.wallet_display") },
    { id: "data", label: t("set.data_logs") },
    { id: "appearance", label: t("set.appearance") },
  ];

  let cat = $state<Category>("connection");

  let cfg = $state<AppConfig | null>(null);
  let test = $state<{ ok: boolean; ms: number } | null>(null);
  let testing = $state(false);
  let saved = $state(false);
  let savedTimer: ReturnType<typeof setTimeout> | undefined;

  // flash() — "saved" feedback shared by all save actions.
  // flash() — 所有保存动作共用的"已保存"反馈。
  function flash() {
    saved = true;
    clearTimeout(savedTimer);
    savedTimer = setTimeout(() => (saved = false), 1800);
  }

  onMount(async () => {
    cfg = await Services.getConfig();
  });

  function pickCat(c: Category) {
    cat = c;
  }

  function pickNav(mode: NavMode) {
    setNavMode(mode);
  }

  function connBadge() {
    if (app.connecting) return { cls: "busy", key: "shell.connecting" };
    if (!app.connected) return { cls: "off", key: "shell.offline" };
    if (app.syncing) return { cls: "sync", key: "shell.syncing" };
    return { cls: "on", key: "shell.connected" };
  }

  async function doTest() {
    testing = true;
    test = null;
    try {
      test = await Services.testNode();
    } finally {
      testing = false;
    }
  }

  // ── connection save: rpc endpoint + credentials → runtime ini ──
  // ── 连接保存:RPC 地址 + 凭据 → runtime ini ──
  let savingConn = $state(false);

  async function saveConnection() {
    if (!cfg || savingConn) return;
    savingConn = true;
    try {
      await Services.saveConfig({
        rpcEndpoint: cfg.rpcEndpoint,
        rpcUser: cfg.rpcUser,
        ...(cfg.rpcPass ? { rpcPass: cfg.rpcPass } : {}),
      });
      flash();
    } finally {
      savingConn = false;
    }
  }

  // ── node save: maxpeers/upnp/debuglevel → runtime ini (next start) ──
  // debuglevel additionally applies live via the debuglevel RPC when the
  // node is running, so log verbosity changes take effect immediately.
  // ── 节点保存:maxpeers/upnp/debuglevel → runtime ini(下次启动生效)。
  // 节点运行时 debuglevel 还会经 debuglevel RPC 即时生效,日志级别立刻改变。
  let savingNode = $state(false);

  async function saveNode() {
    if (!cfg || savingNode) return;
    savingNode = true;
    try {
      await Services.saveConfig({
        maxPeers: cfg.maxPeers,
        upnp: cfg.upnp,
        ...(cfg.debugLevel ? { debugLevel: cfg.debugLevel } : {}),
      });
      if (cfg.debugLevel) {
        await Services.setDebugLevel(cfg.debugLevel).catch(() => {
          /* node not running — ini value applies at next start */
          /* 节点未运行 — ini 值下次启动生效 */
        });
      }
      flash();
    } finally {
      savingNode = false;
    }
  }

  // runNodeOnStart lives in frontend.ini (not btcd options).
  // runNodeOnStart 存 frontend.ini(不属于 btcd 选项)。
  async function saveRunOnStart() {
    if (!cfg) return;
    await Services.saveConfig({ runNodeOnStart: cfg.runNodeOnStart });
    flash();
  }

  // ── data dir: edit → runtime ini; open → OS file manager ──
  // ── 数据目录:编辑 → runtime ini;打开 → 系统文件管理器 ──
  let savingDir = $state(false);

  async function saveDataDir() {
    if (!cfg || savingDir) return;
    savingDir = true;
    try {
      await Services.saveConfig({ dataDir: cfg.dataDir });
      flash();
    } finally {
      savingDir = false;
    }
  }

  async function openDir() {
    if (!cfg?.dataDir) return;
    await Services.openDataDir(cfg.dataDir);
  }

  function peerChange(ev: Event) {
    const v = parseInt((ev.target as HTMLSelectElement).value, 10);
    if (cfg && v >= 1 && v <= 125) cfg.maxPeers = v;
  }

  function pickLang(l: string) {
    setLang(l as never);
  }

  function currentLang() {
    return (localStorage.getItem("ini-node.lang") || "en") as string;
  }
</script>

<section class="set">
  <div class="head">
    <div>
      <p class="eyebrow">configuration</p>
      <h1 class="h-page">{t("set.title")}</h1>
    </div>
    {#if saved}
      <span class="save-chip" role="status">✓ {t("wal.set.saved")}</span>
    {/if}
  </div>

  <div class="cat-nav" role="tablist" aria-label={t("set.sections")}>
    {#each categories as c}
      <button
        role="tab"
        class="cat-btn"
        class:active={cat === c.id}
        aria-selected={cat === c.id}
        onclick={() => pickCat(c.id)}
      >
        {c.label}
      </button>
    {/each}
  </div>

  {#if cfg}
    <!-- ── Connection / node ─────────────────────────── -->
    {#if cat === "connection"}
    <div class="card">
      <div class="conn-head">
        <h2 class="h-card">{t("set.connection")}</h2>
        <span class="conn-chip {connBadge().cls}" role="status">
          <span class="dot" aria-hidden="true"></span>
          <span>{t(connBadge().key)}</span>
        </span>
      </div>
      <div class="field">
        <label class="field-label" for="rpc">{t("set.rpc_endpoint")}</label>
        <div class="field-row">
          <input id="rpc" class="mono" bind:value={cfg.rpcEndpoint} autocomplete="off" spellcheck="false" />
          <button class="btn" onclick={doTest} disabled={testing}>
            {#if testing}<span class="spin" aria-hidden="true"></span>{/if}
            {t("set.test")}
          </button>
        </div>
        {#if test}
          <p class="hint mono" class:ok={test.ok} class:bad={!test.ok} role="status">
            {test.ok ? t("set.test_ok", { ms: test.ms }) : t("set.test_fail")}
          </p>
        {/if}
      </div>

      <div class="field">
        <span class="field-label">{t("set.cred_source")}</span>
        <div class="cred-row">
          <label class="check">
            <input type="radio" name="cred" checked={cfg.credFromIni} onclick={() => (cfg!.credFromIni = true)} />
            <span>{t("set.from_ini")}</span>
          </label>
          <label class="check">
            <input type="radio" name="cred" checked={!cfg.credFromIni} onclick={() => (cfg!.credFromIni = false)} />
            <span>{t("set.manual")}</span>
          </label>
        </div>
        {#if !cfg.credFromIni}
          <div class="field-row">
            <input bind:value={cfg.rpcUser} placeholder={t("set.user")} aria-label={t("set.user")} autocomplete="off" />
            <input bind:value={cfg.rpcPass} type="password" placeholder={t("set.pass")} aria-label={t("set.pass")} autocomplete="off" />
          </div>
        {/if}
      </div>

      <div class="field">
        <button class="btn btn-primary" onclick={saveConnection} disabled={savingConn}>
          {#if savingConn}<span class="spin" aria-hidden="true"></span>{/if}
          {t("g.apply")}
        </button>
        <p class="hint">{t("set.ini_path")}: <span class="mono">{cfg.iniPath || "—"}</span></p>
      </div>
    </div>
    {/if}

    <!-- ── Node / sync ──────────────────────────────── -->
    {#if cat === "node"}
    <div class="card">
      <h2 class="h-card">{t("set.sync")}</h2>
      <div class="field-row sync-row">
        <div class="field">
          <label class="field-label" for="peers">{t("set.max_peers")}</label>
          <select id="peers" value={cfg.maxPeers} onchange={peerChange}>
            {#each [4, 8, 16, 32, 64, 125] as v}
              <option value={v}>{v}</option>
            {/each}
          </select>
        </div>
        <div class="field">
          <label class="field-label" for="dbg">{t("set.debug_level")}</label>
          <select id="dbg" bind:value={cfg.debugLevel}>
            <option>trace</option>
            <option>debug</option>
            <option>info</option>
            <option>warn</option>
            <option>error</option>
          </select>
        </div>
        <div class="field apply-col">
          <button class="btn btn-primary" onclick={saveNode} disabled={savingNode}>
            {#if savingNode}<span class="spin" aria-hidden="true"></span>{/if}
            {t("g.apply")}
          </button>
        </div>
      </div>
      <p class="hint">{t("set.peer_hint")}</p>

      <div class="field-row flags">
        <label class="check">
          <input type="checkbox" bind:checked={cfg.upnp} />
          <span>{t("set.upnp")}</span>
        </label>
        <label class="check">
          <!-- frontend.ini client setting / frontend.ini 客户端设置 -->
          <input type="checkbox" bind:checked={cfg.runNodeOnStart} onchange={saveRunOnStart} />
          <span>{t("set.run_at_start")}</span>
        </label>
      </div>
    </div>
    {/if}

    <!-- ── Wallet / indexes — read-only node state ──── -->
    {#if cat === "wallet"}
    <div class="card">
      <h2 class="h-card">{t("set.wallet_display")}</h2>
      <p class="hint">{t("set.index_hint")}</p>
      <div class="field-row sync-row">
        <div class="field">
          <span class="field-label">sugarindex</span>
          <p class="unit-val mono">
            {cfg.sugarIndex ? "1 (on)" : "0 (off)"}
            <span class="dim">· {t("set.sugar_hint")}</span>
          </p>
        </div>
      </div>
    </div>
    {/if}

    <!-- ── Data / logs ─────────────────────────────── -->
    {#if cat === "data"}
    <div class="card">
      <h2 class="h-card">{t("set.data_logs")}</h2>
      <div class="field">
        <label class="field-label" for="datadir2">{t("set.data_dir")}</label>
        <div class="field-row">
          <input id="datadir2" class="mono" bind:value={cfg.dataDir} autocomplete="off" spellcheck="false" />
          <button class="btn" onclick={saveDataDir} disabled={savingDir}>
            {#if savingDir}<span class="spin" aria-hidden="true"></span>{/if}
            {t("g.apply")}
          </button>
          <button class="btn btn-ghost" onclick={openDir}>{t("set.open_dir")}</button>
        </div>
        <p class="hint">{t("set.data_dir_hint")}</p>
      </div>
    </div>
    {/if}

    <!-- ── Appearance / theme ──────────────────────── -->
    {#if cat === "appearance"}
    <div class="card">
      <h2 class="h-card">{t("set.appearance")}</h2>

      <div class="field">
        <span class="field-label">{t("set.language")}</span>
        <select id="langsel" onchange={(e) => pickLang((e.target as HTMLSelectElement).value)}>
          {#each LANGS as l}
            <option value={l} selected={currentLang() === l}>{LANG_NAMES[l]}</option>
          {/each}
        </select>
      </div>

      <div class="field">
        <span class="field-label" id="nav-layout-label">{t("set.nav_layout")}</span>
        <div class="nav-layout" role="radiogroup" aria-labelledby="nav-layout-label">
          <label class="layout-card {app.navMode === 'top' ? 'active' : ''}">
            <input type="radio" name="navlayout" checked={app.navMode === "top"} onchange={() => pickNav("top")} />
            <span class="layout-preview" aria-hidden="true">
              <span class="lp-bar"><i></i><i></i><i></i></span>
              <span class="lp-body"></span>
            </span>
            <span class="layout-name">{t("set.nav_top")}</span>
            <span class="layout-hint">{t("set.nav_top_hint")}</span>
          </label>

          <label class="layout-card {app.navMode === 'side' ? 'active' : ''}">
            <input type="radio" name="navlayout" checked={app.navMode === "side"} onchange={() => pickNav("side")} />
            <span class="layout-preview" aria-hidden="true">
              <span class="lp-rail"></span>
              <span class="lp-body"></span>
            </span>
            <span class="layout-name">{t("set.nav_side")}</span>
            <span class="layout-hint">{t("set.nav_side_hint")}</span>
          </label>
        </div>
      </div>
    </div>
    {/if}
  {:else}
    <div class="card"><p>…</p></div>
  {/if}
</section>

<style>
  .set {
    display: flex;
    flex-direction: column;
    gap: 16px;
    max-width: 880px;
    margin: 0 auto;
  }
  .head {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
  }
  .h-page {
    font-family: var(--font-display);
    font-size: 24px;
    margin: 2px 0 0;
  }
  .h-card {
    margin: 0 0 16px;
    font-size: 14px;
  }
  .save-chip {
    font-size: 12px;
    font-weight: 700;
    color: var(--mint);
  }

  /* ── category tabs ─────────────────────────────── */
  .cat-nav {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    border-bottom: 1px solid var(--line);
    padding-bottom: 6px;
  }
  .cat-btn {
    appearance: none;
    border: none;
    background: none;
    color: var(--ink-dim);
    font-family: var(--font-display);
    font-size: 13px;
    font-weight: 600;
    padding: 7px 12px;
    border-radius: var(--r-8);
    cursor: pointer;
    transition: background 0.12s ease, color 0.12s ease;
    touch-action: manipulation;
  }
  .cat-btn:hover {
    background: var(--violet);
    color: var(--ink-fg);
  }
  .cat-btn:focus-visible {
    outline: none;
    box-shadow: var(--focus);
  }
  .cat-btn.active {
    background: var(--violet-2);
    color: var(--ink-fg);
    box-shadow: inset 0 -2px 0 var(--straw);
  }

  /* ── appearance: nav-layout picker ─────────────── */
  .nav-layout {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
    gap: 12px;
  }
  .layout-card {
    display: flex;
    flex-direction: column;
    gap: 6px;
    border: 1px solid var(--line);
    border-radius: var(--r-12);
    padding: 12px;
    cursor: pointer;
    transition: border-color 0.12s ease, background 0.12s ease;
    position: relative;
  }
  .layout-card:hover {
    border-color: var(--mist);
  }
  .layout-card.active {
    border-color: var(--straw);
    background: var(--straw-soft);
  }
  .layout-card input {
    position: absolute;
    opacity: 0;
    pointer-events: none;
  }
  .layout-card:has(input:focus-visible) {
    box-shadow: var(--focus);
  }
  .layout-preview {
    display: block;
    width: 100%;
    height: 60px;
    border-radius: var(--r-8);
    background: var(--ink);
    border: 1px solid var(--line);
    overflow: hidden;
    position: relative;
  }
  .lp-bar {
    display: flex;
    gap: 3px;
    align-items: center;
    height: 16px;
    padding: 0 8px;
    background: var(--violet);
    border-bottom: 1px solid var(--line);
  }
  .lp-bar i {
    width: 14px;
    height: 5px;
    border-radius: 2px;
    background: var(--violet-2);
  }
  .lp-body {
    position: absolute;
    inset: 22px 8px 8px;
    border-radius: 3px;
    background: var(--violet);
  }
  .lp-rail {
    position: absolute;
    top: 8px;
    left: 8px;
    bottom: 8px;
    width: 12px;
    border-radius: 3px;
    background: var(--violet-2);
    border: 1px solid var(--line);
  }
  .layout-card.active .lp-bar i:first-child {
    background: var(--straw);
  }
  .layout-card.active .lp-rail {
    background: var(--straw);
  }
  .layout-name {
    font-family: var(--font-display);
    font-weight: 800;
    font-size: 13px;
  }
  .layout-hint {
    font-size: 11px;
    color: var(--ink-dim);
  }

  .conn-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
    margin-bottom: 16px;
  }
  .conn-head .h-card {
    margin: 0;
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
  .field {
    display: flex;
    flex-direction: column;
    gap: 5px;
    margin-bottom: 14px;
    min-width: 0;
  }
  .field-row {
    display: flex;
    gap: 8px;
    align-items: center;
  }
  .field-row input[class="mono"] {
    flex: 1;
    min-width: 0;
    font-size: 12px;
  }
  .cred-row {
    display: flex;
    gap: 16px;
    flex-wrap: wrap;
  }
  .check {
    display: flex;
    align-items: center;
    gap: 7px;
    font-size: 13px;
    cursor: pointer;
  }
  .check input {
    accent-color: var(--straw);
  }
  .hint {
    font-size: 11px;
    color: var(--ink-dim);
    margin: 2px 0 0;
  }
  .hint.ok {
    color: var(--mint);
  }
  .hint.bad {
    color: var(--straw);
  }
  .sync-row {
    flex-wrap: wrap;
  }
  .sync-row .field {
    flex: 1;
    min-width: 170px;
  }
  .apply-col {
    flex: none;
    display: flex;
    flex-direction: column;
    justify-content: flex-end;
    margin-bottom: 14px;
  }
  .flags {
    flex-wrap: wrap;
  }
  .unit-val {
    margin: 0;
    font-size: 15px;
  }
  .dim {
    color: var(--mist);
    font-size: 12px;
    font-weight: 400;
  }
</style>
