<script lang="ts">
  import { onMount } from "svelte";
  import { fmt, fmtBytes, LANG_NAMES, LANGS, setLang, t } from "../lib/i18n";
  import { Services } from "../lib/services";
  import type { AppConfig } from "../lib/types";

  let cfg = $state<AppConfig | null>(null);
  let test = $state<{ ok: boolean; ms: number } | null>(null);
  let testing = $state(false);
  let applied = $state(false);
  let migratePct = $state<number | null>(null);
  let migrateBusy = $state(false);

  onMount(async () => {
    cfg = await Services.getConfig();
  });

  async function doTest() {
    testing = true;
    test = null;
    try {
      test = await Services.testNode();
    } finally {
      testing = false;
    }
  }

  async function applyPeers() {
    if (!cfg) return;
    const n = await Services.setSyncPeers(cfg.parallelPeers);
    cfg.parallelPeers = n;
    applied = true;
    setTimeout(() => (applied = false), 2000);
  }

  async function chooseDir() {
    if (!cfg) return;
    const dir = await Services.pickDataDir();
    if (dir) cfg.dataDir = dir;
  }

  let savingConn = $state(false);

  async function saveConnection() {
    if (!cfg || savingConn) return;
    savingConn = true;
    try {
      await Services.saveConfig(cfg);
    } finally {
      savingConn = false;
    }
  }

  async function migrate() {
    if (!cfg || migrateBusy) return;
    migrateBusy = true;
    migratePct = await Services.migrateDataDir(cfg.dataDir, cfg.dataDir + "-new");
    migrateBusy = false;
    setTimeout(() => (migratePct = null), 3000);
  }

  function peerChange(ev: Event) {
    const v = parseInt((ev.target as HTMLSelectElement).value, 10);
    if (cfg && v >= 4 && v <= 16) cfg.parallelPeers = v;
    else if (cfg) cfg.parallelPeers = 8;
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
  </div>

  {#if cfg}
    <div class="card">
      <h2 class="h-card">{t("set.connection")}</h2>
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
        <label class="field-label" for="wapi">{t("set.wallet_api")}</label>
        <input id="wapi" class="mono" bind:value={cfg.walletApi} autocomplete="off" spellcheck="false" />
      </div>

      <div class="field">
        <button class="btn btn-primary" onclick={saveConnection} disabled={savingConn}>
          {#if savingConn}<span class="spin" aria-hidden="true"></span>{/if}
          {t("g.apply")}
        </button>
      </div>
    </div>

    <div class="card">
      <h2 class="h-card">{t("set.sync")}</h2>
      <div class="field-row sync-row">
        <div class="field">
          <label class="field-label" for="peers">{t("set.parallel_peers")}</label>
          <select id="peers" value={cfg.parallelPeers} onchange={peerChange}>
            {#each Array.from({ length: 13 }, (_, i) => i + 4) as v}
              <option value={v}>{v}</option>
            {/each}
          </select>
        </div>
        <div class="field">
          <label class="field-label" for="dbg">{t("set.debug_level")}</label>
          <select id="dbg" bind:value={cfg.debugLevel}>
            <option>off</option>
            <option>warn</option>
            <option>info</option>
            <option>debug</option>
            <option>trace</option>
          </select>
        </div>
        <div class="field apply-col">
          <span class="hint omitted">&nbsp;</span>
          <button class="btn btn-primary" onclick={applyPeers} disabled={!cfg}>
            {applied ? "✓" : t("g.apply")}
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
          <input type="checkbox" checked={cfg.proxy != null} />
          <span>{t("set.proxy")} {cfg.proxy ? `:${cfg.proxy}` : ""}</span>
        </label>
      </div>
    </div>

    <div class="card">
      <h2 class="h-card">{t("set.wallet_display")}</h2>
      <div class="field">
        <span class="field-label">{t("set.addr_type")}</span>
        <div class="cred-row">
          <label class="check">
            <input type="radio" name="addr" checked={cfg.addrType === "bech32"} onclick={() => (cfg!.addrType = "bech32")} />
            <span>{t("set.bech32")}</span>
          </label>
          <label class="check">
            <input type="radio" name="addr" checked={cfg.addrType === "segwit"} onclick={() => (cfg!.addrType = "segwit")} />
            <span>{t("set.segwit")}</span>
          </label>
          <label class="check">
            <input type="radio" name="addr" checked={cfg.addrType === "legacy"} onclick={() => (cfg!.addrType = "legacy")} />
            <span>{t("set.legacy")}</span>
          </label>
        </div>
      </div>

      <div class="field-row sync-row">
        <div class="field">
          <label class="field-label" for="langsel">{t("set.language")}</label>
          <select id="langsel" onchange={(e) => pickLang((e.target as HTMLSelectElement).value)}>
            {#each LANGS as l}
              <option value={l} selected={currentLang() === l}>{LANG_NAMES[l]}</option>
            {/each}
          </select>
        </div>
        <div class="field">
          <span class="field-label">{t("set.unit")}</span>
          <p class="unit-val mono">S <span class="dim">{t("set.unit_fixed")}</span></p>
        </div>
        <div class="field">
          <span class="field-label">{t("set.default_wallet")}</span>
          <p class="unit-val mono">{cfg.defaultWallet}</p>
        </div>
      </div>
    </div>

    <div class="card">
      <h2 class="h-card">{t("set.data_logs")}</h2>
      <div class="field">
        <label class="field-label" for="datadir">{t("set.data_dir")}</label>
        <div class="field-row">
          <input id="datadir" class="mono" bind:value={cfg.dataDir} autocomplete="off" spellcheck="false" />
          <button class="btn" onclick={chooseDir}>{t("set.choose")}</button>
          <button class="btn btn-ghost">{t("set.open_dir")}</button>
        </div>
        <p class="hint">● {t("set.disk_free", { n: fmtBytes(cfg.diskFree) })} · {t("set.migrate_hint")}</p>
        <div class="migrate-row">
          <button class="btn btn-danger" onclick={migrate} disabled={migrateBusy}>
            {#if migrateBusy}<span class="spin" aria-hidden="true"></span>{/if}
            migrate →
          </button>
          {#if migratePct != null}
            <div class="migrate-bar" aria-hidden="true"><span style:width={`${migratePct}%`}></span></div>
            <span class="hint mono">{migratePct}%</span>
          {/if}
        </div>
      </div>

      <div class="field-row flags">
        <label class="check">
          <input type="checkbox" bind:checked={cfg.runNodeOnStart} />
          <span>
            {t("set.run_at_start")}
            <span class="dim mono"> sugarchain-node</span>
          </span>
        </label>
      </div>
    </div>
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
  .migrate-row {
    display: flex;
    align-items: center;
    gap: 10px;
    margin-top: 4px;
    flex-wrap: wrap;
  }
  .migrate-bar {
    flex: 1;
    min-width: 120px;
    height: 8px;
    border-radius: 4px;
    background: #e0e0e0;
    overflow: hidden;
  }
  .migrate-bar span {
    display: block;
    height: 100%;
    background: var(--straw);
  }
</style>