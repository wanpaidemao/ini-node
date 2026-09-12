<script lang="ts">
  // PeerStatus: standalone node-connection-status page (same nav level as
  // the console).  Extracted/expanded from the console's connection card:
  // adds the per-peer latency column, an addnode input, and full metrics.
  // 节点连接状态页:与控制台同级的独立导航页。从控制台的连接卡片
  // 抽出并扩展:新增每 peer 延迟列、addnode 输入框与完整指标。
  import { onMount } from "svelte";
  import { fmt, t } from "../lib/i18n";
  import { Services } from "../lib/services";
  import { navigate } from "../lib/store.svelte";
  import type { Peer } from "../lib/types";

  let peers = $state<Peer[]>([]);
  let connected = $state(true);
  let busy = $state(false);
  let addAddr = $state("");
  let addMsg = $state("");
  let copied = false;

  // 8 is the configured maxpeers (runtime.ini); used for the dynamic-peers
  // and net-EQ metrics, matching the console card.
  // 8 是配置的 maxpeers(runtime.ini);用于动态 peer 与净 EQ 指标,
  // 与控制台卡片保持一致。
  const peerCap = 8;

  onMount(() => {
    refresh();
  });

  async function refresh() {
    try {
      peers = await Services.getPeers();
      connected = true;
    } catch {
      connected = false;
    }
  }

  const sumOutbound = () => peers.filter((p) => p.dir === "outbound").length;
  const sumInbound = () => peers.filter((p) => p.dir === "inbound").length;
  const medianLatency = () => {
    if (peers.length === 0) return 0;
    const lats = peers.map((p) => p.latencyMs).sort((a, b) => a - b);
    const mid = Math.floor(lats.length / 2);
    return lats.length % 2 ? lats[mid] : Math.round((lats[mid - 1] + lats[mid]) / 2);
  };

  async function addNode() {
    const addr = addAddr.trim();
    if (!addr || busy) return;
    busy = true;
    addMsg = "";
    try {
      await Services.rpcCall("addnode", [addr, "add"]);
      addMsg = `✓ addnode ${addr}`;
      addAddr = "";
      // Give the handshake a beat, then refresh so the new peer shows up.
      // 稍等握手完成后再刷新,让新 peer 出现在列表里。
      setTimeout(refresh, 1200);
    } catch (e) {
      addMsg = `✗ ${e instanceof Error ? e.message : String(e)}`;
    } finally {
      busy = false;
    }
  }

  async function drop(p: Peer) {
    peers = peers.filter((x) => x.id !== p.id);
    await Services.disconnectPeer(p.id);
    setTimeout(refresh, 800);
  }

  async function dropAll() {
    if (confirm(t("con.confirm_disconnect_all"))) {
      peers = [];
      await Services.resetPeers();
      connected = true;
      setTimeout(refresh, 800);
    }
  }

  async function resetDefaults() {
    await Services.resetPeers();
    await refresh();
  }

  async function copyList() {
    const text = peers
      .map((p) => `${p.id}\t${p.dir}\t${p.addr}\t${p.version}\t${p.height}\t${p.syncBlPerSec ?? "—"}\t${p.latencyMs}ms`)
      .join("\n");
    navigator.clipboard?.writeText(text);
    copied = true;
    setTimeout(() => (copied = false), 1500);
  }
</script>

<section class="con">
  <div class="head">
    <div>
      <p class="eyebrow">P2P connections</p>
      <h1 class="h-page">{t("pe.title")}</h1>
    </div>
    <span class="live"><span class="dot" aria-hidden="true"></span>
      <button class="btn btn-ghost se" onclick={refresh}>{t("pe.refresh")}</button>
    </span>
  </div>

  {#if !connected}
    <div class="card banner" role="alert">
      <span class="dot off" aria-hidden="true"></span>
      <span>{t("con.not_connected")}</span>
    </div>
  {/if}

  <!-- metrics -->
  <div class="card">
    <div class="card-head">
      <span class="h-card">{t("pe.metrics")}</span>
      <span class="chip" class:offline={!connected}>
        <span class="dot mint" aria-hidden="true"></span> {connected ? t("g.connected") : t("con.not_connected")}
      </span>
    </div>
    <div class="conn-metrics">
      <span class="metric">{t("con.dynamic_peers", { used: peers.length, cap: peerCap })}</span>
      <span class="metric">{t("dash.outbound", { n: sumOutbound() })}</span>
      <span class="metric">{t("dash.inbound", { n: sumInbound() })}</span>
      <span class="metric mono">{t("con.net_eq", { n: peers.length, m: peerCap })}</span>
      <span class="metric mono">{t("con.latency_median", { n: medianLatency() })}</span>
    </div>
  </div>

  <!-- addnode -->
  <div class="card">
    <h2 class="h-card">{t("pe.addnode")}</h2>
    <div class="cmd-row">
      <span class="prompt mono" aria-hidden="true">+</span>
      <input
        class="cmd mono"
        placeholder="111.17.69.95:34230"
        bind:value={addAddr}
        autocomplete="off"
        spellcheck="false"
        disabled={!connected || busy}
        onkeydown={(e) => { if (e.key === "Enter") addNode(); }}
        aria-label={t("pe.addnode")}
      />
      <button class="btn btn-primary" onclick={addNode} disabled={!connected || busy || !addAddr.trim()}>
        {t("pe.addnode_go")}
      </button>
    </div>
    {#if addMsg}<p class="hint mono" translate="no">{addMsg}</p>{/if}
    <p class="hint">{t("pe.addnode_hint")}</p>
  </div>

  <!-- peer table -->
  <div class="card">
    <div class="card-head">
      <span class="h-card">{t("pe.table")}</span>
      <button class="btn btn-ghost se" onclick={copyList}>{copied ? "✓" : t("g.copy")}</button>
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
          <th scope="col">{t("pe.col_latency")}</th>
          <th scope="col">{t("con.col_action")}</th>
        </tr>
      </thead>
      <tbody>
        {#each peers as p (p.id)}
          <tr>
            <td class="mono" translate="no">{p.id}</td>
            <td>{p.dir === "outbound" ? t("pe.dir_out") : t("pe.dir_in")}</td>
            <td class="mono addr" translate="no">{p.addr}</td>
            <td class="mono" translate="no">{p.version}</td>
            <td class="mono" translate="no">{fmt(p.height)}</td>
            <td class="mono" class:sync={p.syncBlPerSec != null} translate="no">{p.syncBlPerSec != null ? `+${p.syncBlPerSec}` : "—"}</td>
            <td class="mono" translate="no">{p.latencyMs}ms</td>
            <td>
              <button
                class="mini danger"
                onclick={() => drop(p)}
                aria-label={`${t("con.disconnect")} ${p.id}`}
              >
                {t("con.disconnect")}
              </button>
            </td>
          </tr>
        {:else}
          <tr><td colspan="8" class="hint">{t("pe.empty")}</td></tr>
        {/each}
      </tbody>
    </table>
    <div class="conn-actions">
      <button class="btn btn-danger" onclick={dropAll}>{t("con.disconnect_all")}</button>
      <button class="btn" onclick={resetDefaults}>{t("con.reset_peers")}</button>
      <span class="spacer"></span>
      <button class="btn btn-ghost" onclick={() => navigate("console")}>{t("nav.console")} →</button>
      <button class="btn btn-ghost" onclick={() => navigate("internals")}>{t("nav.internals")} →</button>
    </div>
  </div>
</section>

<style>
  /* Component-scoped styles: Svelte does NOT share styles across
     components, so the classes borrowed from Console.svelte must be
     declared here too.  Mirrors Console.svelte's stylesheet for the
     classes this page uses (.con/.head/.card layout, metrics pills,
     peer table, action buttons), plus page-specific bits.
     组件作用域样式:Svelte 的样式不跨组件共享,从 Console.svelte
     借用的 class 必须在本组件内声明。以下镜像 Console.svelte 中
     本页用到的类(布局/指标胶囊/peer 表格/操作按钮),外加本页特有样式。 */
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
  .banner .dot.off {
    background: var(--straw);
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
  .hint {
    font-size: 11px;
    color: var(--ink-dim);
    margin: 8px 0 0;
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
  td[colspan] {
    text-align: center;
    padding: 1rem 0;
  }
  @media (max-width: 720px) {
    .cmd-row .cmd {
      flex: 1 1 100%;
    }
  }
</style>
