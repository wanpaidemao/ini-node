<script lang="ts">
  import { onMount } from "svelte";
  import { fmt, t } from "../lib/i18n";
  import { Services } from "../lib/services";
  import { navigate } from "../lib/store.svelte";
  import type { Peer, RpcResult } from "../lib/types";

  let cmd = $state("");
  let argsRaw = $state("");
  let history = $state<RpcResult[]>([]);
  let connected = $state(true);
  let peers = $state<Peer[]>([]);
  let busy = $state(false);
  let format = $state(true);
  let copied = $state(false);

  const methods = [
    "getblockchaininfo", "getblockcount", "getbestblockhash", "getblock", "getblockhash",
    "getrawtransaction", "gettxout", "getconnectioncount", "getnetworkinfo", "getpeerinfo",
    "getnettotals", "getmempoolinfo", "getrawmempool", "getblocktemplate", "getdifficulty",
    "getmininginfo", "uptime", "ping",
  ];

  const suites: { label: string; cmd: string }[] = [
    { label: "con.chain_state", cmd: "getblockchaininfo" },
    { label: "con.network_info", cmd: "getnetworkinfo" },
    { label: "con.node_peers", cmd: "getpeerinfo" },
    { label: "con.mining", cmd: "getmininginfo" },
  ];

  onMount(() => {
    refreshPeers();
  });

  async function refreshPeers() {
    try {
      peers = await Services.getPeers();
      connected = true;
    } catch {
      connected = false;
    }
  }

  async function copyOutput() {
    const text = history.map((r) => r.output).join("\n\n");
    if (!text) return;
    navigator.clipboard?.writeText(text);
    copied = true;
    setTimeout(() => (copied = false), 1500);
  }

  function matchedMethods() {
    if (!cmd.trim()) return methods.slice(0, 5);
    return methods.filter((m) => m.startsWith(cmd.trim().toLowerCase())).slice(0, 6);
  }

  async function run(custom?: string) {
    const method = (custom ?? cmd).trim();
    if (!method || busy) return;
    busy = true;
    try {
      let params: unknown[] = [];
      if (argsRaw.trim()) {
        try {
          const parsed = JSON.parse(argsRaw.trim());
          params = Array.isArray(parsed) ? parsed : [parsed];
        } catch {
          params = argsRaw.trim().split(/\s+/);
        }
      }
      const res = await Services.rpcCall(method, params);
      history = [res, ...history].slice(0, 50);
    } finally {
      busy = false;
    }
  }

  const sumOutbound = () => peers.filter((p) => p.dir === "outbound").length;
  const sumInbound = () => peers.filter((p) => p.dir === "inbound").length;
  const syncing = () => peers.filter((p) => p.syncBlPerSec != null).length;
  const medianLatency = () => {
    if (peers.length === 0) return 0;
    const lats = peers.map((p) => p.latencyMs).sort((a, b) => a - b);
    const mid = Math.floor(lats.length / 2);
    return lats.length % 2 ? lats[mid] : Math.round((lats[mid - 1] + lats[mid]) / 2);
  };
</script>

<section class="con">
  <div class="head">
    <div>
      <p class="eyebrow">RPC console</p>
      <h1 class="h-page">{t("con.title")}</h1>
    </div>
    <span class="live"><span class="dot" aria-hidden="true"></span> 4s</span>
  </div>

  {#if !connected}
    <div class="card banner" role="alert">
      <span class="dot off" aria-hidden="true"></span>
      <span>{t("con.not_connected")}</span>
      <button class="btn" onclick={() => (connected = true)}>{t("con.reconnect")}</button>
    </div>
  {/if}

  <div class="card">
    <h2 class="h-card">{t("con.rpc_input")}</h2>
    <div class="cmd-row">
      <span class="prompt mono" aria-hidden="true">$</span>
      <input
        id="rpc-cmd"
        class="cmd mono"
        list="method-list"
        bind:value={cmd}
        placeholder="getblockchaininfo"
        autocomplete="off"
        spellcheck="false"
        disabled={!connected}
        aria-describedby="cmd-hint"
      />
      <datalist id="method-list">
        {#each methods as m}<option value={m}></option>{/each}
      </datalist>
      <input
        class="args mono"
        placeholder="params"
        bind:value={argsRaw}
        autocomplete="off"
        spellcheck="false"
        disabled={!connected}
        aria-label="params"
      />
      <button class="btn btn-primary" onclick={() => run()} disabled={!connected || busy || !cmd.trim()}>
        {#if busy}<span class="spin" aria-hidden="true"></span>{/if}
        ⏎ {t("con.execute")}
      </button>
    </div>
    {#if matchedMethods().length > 0 && !busy}
      <div class="sug" role="listbox">
        {#each matchedMethods() as m}
          <button class="sug-item mono" onclick={() => { cmd = m; run(m); }}>{m}</button>
        {/each}
      </div>
    {/if}
    <p class="hint" id="cmd-hint">{t("con.disabled_hint")}</p>

    <div class="suite-row">
      <span class="eyebrow mini">{t("con.suites")}</span>
      {#each suites as s}
        <button class="btn btn-ghost se" onclick={() => run(s.cmd)} disabled={!connected}>{t(s.label)}</button>
      {/each}
    </div>
  </div>

  <!-- output -->
  <div class="card output">
    <div class="card-head">
      <span class="h-card">{t("con.output")}</span>
      <div class="out-opts">
        <label class="check">
          <input type="checkbox" bind:checked={format} />
          <span>{t("con.format")}</span>
        </label>
        <button class="btn btn-ghost se" onclick={copyOutput}>{copied ? "✓" : t("g.copy")}</button>
      </div>
    </div>
    {#if history.length === 0}
      <pre class="mono out-empty">$ — awaiting command…</pre>
    {:else}
      <div class="out-scroll">
        {#each history as r}
          <div class="out-entry">
            <p class="mono out-cmd" translate="no">$ {r.method}</p>
            <pre class="mono out-pre">{r.output}</pre>
            <p class="out-meta mono" translate="no">elapsed {r.elapsedMs}ms ✓</p>
          </div>
        {/each}
      </div>
    {/if}
  </div>

  <!-- node connection -->
  <div class="card">
    <div class="card-head">
      <span class="h-card">{t("con.conn_state")}</span>
      <span class="chip" class:offline={!connected}>
        <span class="dot mint" aria-hidden="true"></span> {connected ? t("g.connected") : t("con.not_connected")}
      </span>
    </div>
    <div class="conn-metrics">
      <span class="metric">{t("con.dynamic_peers", { used: peers.length, cap: 8 })}</span>
      <span class="metric">{t("dash.outbound", { n: sumOutbound() })}</span>
      <span class="metric">{t("dash.inbound", { n: sumInbound() })}</span>
      <span class="metric mono">{t("con.net_eq", { n: 8, m: 8 })}</span>
      <span class="metric mono">{t("con.latency_median", { n: medianLatency() })}</span>
    </div>
    <table class="peer-t">
      <thead>
        <tr>
          <th scope="col">{t("con.col_id")}</th>
          <th scope="col">{t("con.col_dir")}</th>
          <th scope="col">{t("con.col_addr")}</th>
          <th scope="col">{t("con.col_version")}</th>
          <th scope="col">{t("con.col_height")}</th>
          <th scope="col">{t("con.col_sync")}</th>
          <th scope="col">{t("con.col_action")}</th>
        </tr>
      </thead>
      <tbody>
        {#each peers as p}
          <tr>
            <td class="mono" translate="no">{p.id}</td>
            <td>{p.dir === "outbound" ? "出" : "入"}</td>
            <td class="mono addr" translate="no">{p.addr}</td>
            <td class="mono" translate="no">{p.version}</td>
            <td class="mono" translate="no">{fmt(p.height)}</td>
            <td class="mono" class:sync={p.syncBlPerSec != null} translate="no">{p.syncBlPerSec != null ? `+${p.syncBlPerSec}` : "—"}</td>
            <td>
              <button
                class="mini danger"
                onclick={() => { peers = peers.filter((x) => x.id !== p.id); Services.disconnectPeer(p.id); }}
                aria-label={`${t("con.disconnect")} ${p.id}`}
              >
                {t("con.disconnect")}
              </button>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
    <div class="conn-actions">
      <button
        class="btn btn-danger"
        onclick={() => {
          if (confirm(t("con.confirm_disconnect_all"))) {
            peers = [];
            Services.resetPeers();
            connected = true;
          }
        }}
      >
        {t("con.disconnect_all")}
      </button>
      <button class="btn" onclick={() => { Services.resetPeers(); Services.getPeers().then((p) => (peers = p)); }}>
        {t("con.reset_peers")}
      </button>
      <span class="spacer"></span>
      <button class="btn btn-ghost" onclick={() => navigate("internals")}>{t("nav.internals")} →</button>
    </div>
  </div>
</section>

<style>
  .con {
    display: flex;
    flex-direction: column;
    gap: 16px;
    max-width: 1000px;
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
  .live {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 12px;
    color: var(--mint);
  }
  .live .dot {
    background: var(--mint);
  }
  .banner {
    display: flex;
    align-items: center;
    gap: 10px;
    border-color: var(--straw);
  }
  .dot.off {
    background: var(--straw);
  }
  .h-card {
    margin: 0 0 12px;
  }
  .card-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 10px;
  }
  .cmd-row {
    display: flex;
    gap: 8px;
    align-items: center;
    flex-wrap: wrap;
  }
  .prompt {
    color: var(--straw);
    font-weight: 700;
  }
  .cmd {
    flex: 1;
    min-width: 200px;
  }
  .args {
    width: 140px;
    flex: none;
  }
  .sug {
    display: flex;
    gap: 6px;
    flex-wrap: wrap;
    margin-top: 8px;
  }
  .sug-item {
    background: #fff;
    border: 1px solid var(--line);
    border-radius: 6px;
    color: var(--ink-dim);
    padding: 3px 9px;
    font-size: 11px;
    cursor: pointer;
  }
  .sug-item:hover {
    color: var(--straw);
    border-color: var(--straw);
  }
  .hint {
    font-size: 11px;
    color: var(--ink-dim);
    margin: 8px 0 0;
  }
  .suite-row {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-top: 12px;
    flex-wrap: wrap;
    border-top: 1px dashed var(--line);
    padding-top: 10px;
  }
  .eyebrow.mini {
    font-size: 10px;
  }
  .btn.se {
    padding: 4px 10px;
    min-height: 28px;
    font-size: 12px;
  }
  .out-opts {
    display: flex;
    align-items: center;
    gap: 10px;
  }
  .check {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 12px;
    cursor: pointer;
  }
  .check input {
    accent-color: var(--straw);
  }
  .output {
    padding: 0;
    overflow: hidden;
  }
  .output .card-head {
    padding: 16px 16px 0;
  }
  .out-scroll {
    max-height: 320px;
    overflow-y: auto;
    padding: 0 16px 16px;
  }
  .out-entry + .out-entry {
    border-top: 1px dashed var(--line);
    margin-top: 10px;
    padding-top: 10px;
  }
  .out-cmd {
    margin: 0 0 6px;
    color: var(--straw);
    font-size: 12px;
  }
  .out-pre {
    margin: 0;
    white-space: pre-wrap;
    word-break: break-word;
    font-size: 12px;
    line-height: 1.55;
    background: #f4f4f4;
    border-radius: var(--r-8);
    padding: 10px 12px;
    border: 1px solid var(--line);
    color: var(--ink-fg);
  }
  .out-meta {
    margin: 6px 0 0;
    font-size: 11px;
    color: var(--mint);
  }
  .out-empty {
    margin: 0;
    padding: 16px;
    color: var(--ink-dim);
    font-size: 12px;
  }

  .conn-metrics {
    display: flex;
    gap: 8px;
    flex-wrap: wrap;
    margin-bottom: 12px;
  }
  .chip.offline {
    border-color: var(--straw);
    color: var(--straw);
  }
  .dot.mint {
    background: var(--mint);
  }
  .metric {
    font-size: 12px;
    color: var(--ink-dim);
    border: 1px solid var(--line);
    border-radius: 999px;
    padding: 3px 10px;
  }
  .peer-t {
    width: 100%;
    border-collapse: collapse;
    font-size: 12px;
  }
  .peer-t th {
    text-align: left;
    font-size: 11px;
    color: var(--mist);
    padding: 8px 10px;
    border-bottom: 1px solid var(--line);
    font-weight: 700;
    letter-spacing: 0.6px;
    text-transform: uppercase;
  }
  .peer-t td {
    padding: 8px 10px;
    border-bottom: 1px dashed var(--line);
    font-variant-numeric: tabular-nums;
  }
  .addr {
    min-width: 0;
    max-width: 220px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .sync {
    color: var(--mint);
  }
  .mini.danger {
    background: #fff;
    border: 1px solid var(--straw);
    color: var(--straw);
    border-radius: 6px;
    padding: 2px 8px;
    font-size: 11px;
    cursor: pointer;
  }
  .mini.danger:hover {
    background: rgba(37, 99, 235, 0.06);
  }
  .conn-actions {
    display: flex;
    gap: 8px;
    padding-top: 12px;
    margin-top: 4px;
    border-top: 1px solid var(--line);
    flex-wrap: wrap;
  }
  .spacer {
    flex: 1;
  }

  @media (max-width: 720px) {
    .cmd-row .cmd {
      flex: 1 1 100%;
    }
  }
</style>