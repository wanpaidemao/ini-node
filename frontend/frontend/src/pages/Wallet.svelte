<script lang="ts">
  import { onMount } from "svelte";
  import QRCode from "qrcode";
  import { fmtDateTime, t } from "../lib/i18n";
  import { Services } from "../lib/services";
  import { navigate } from "../lib/store.svelte";
  import { walletAutoLock, walletLocked, walletSettings, walletUnlocked } from "../lib/wallet-settings.svelte";
  import type { TokenBalance, Tx, WalletState } from "../lib/types";

  // Token op result (same shape the send pipeline returns): txid + rawHex
  // kept for a broadcast retry, plus the fee actually paid.
  // 代币操作结果(与发送链路同形):txid + 保留 rawHex 供广播重试,
  // 另附实付手续费。
  interface TokenResult {
    txid: string;
    rawHex: string;
    broadcastErr: string;
    fee: number;
    inputCount: number;
  }

  // Page state: w is the real getwalletinfo snapshot; locked mirrors
  // w.locked so the unlock form replaces the old click-to-unlock mock.
  // 页面状态:w 为真实 getwalletinfo 快照;locked 镜像 w.locked,
  // 解锁表单取代原先"点一下即解锁"的 mock 行为。
  let w = $state<WalletState | null>(null);
  let txs = $state<Tx[]>([]);
  let tab = $state<"history" | "tokens" | "keys" | "consolidate">("history");
  let loadErr = $state<string | null>(null);

  // Unlock form state. / 解锁表单状态。
  let pass = $state("");
  let unlockErr = $state<string | null>(null);
  let busy = $state(false);
  let copyOk = $state(false);

  // Keys tab: read-only derived-address list (local binding, node not
  // required). Loaded lazily on first tab visit, cached for the session.
  // Keys 标签页:只读已派生地址列表(本地 binding,无需节点)。首次切到
  // 该标签时懒加载,会话内缓存。
  let addrs = $state<{ index: number; address: string }[] | null>(null);
  let addrCopied = $state(-1);
  $effect(() => {
    if (tab === "keys" && addrs === null && w && !w.locked) {
      Services.getAddresses()
        .then((a) => (addrs = a))
        .catch(() => (addrs = []));
    }
  });

  function copyKeyAddr(i: number, a: string) {
    navigator.clipboard?.writeText(a);
    addrCopied = i;
    setTimeout(() => (addrCopied = -1), 1500);
  }

  // Step 11: per-address WIF export (Keys tab). The key is copied straight
  // to the clipboard and never rendered on screen.
  // 第 11 步:按地址导出 WIF(Keys 标签页)。私钥直接复制到剪贴板,
  // 绝不在屏幕上渲染。
  let wifCopied = $state(-1);
  async function exportKeyWIF(i: number) {
    try {
      const wif = await Services.exportWIF(i);
      navigator.clipboard?.writeText(wif);
      wifCopied = i;
      setTimeout(() => (wifCopied = -1), 2000);
    } catch {
      /* locked mid-call — ignore / 调用间隙锁定 — 忽略 */
    }
  }

  // ── Tokens tab (Step 6/6b): real token layer over Wails bindings ──
  // Balances load lazily on the first tab visit (like Keys); the ops panel
  // covers transfer / create / issue / burn with a shared form. Amounts are
  // display units — services converts with each token's own decimals.
  // 代币标签页(第 6/6b 步):Wails bindings 对接真实代币层。余额在首次
  // 切入时懒加载(与 Keys 一致);操作面板以共用表单覆盖转账/创建/
  // 增发/销毁。金额为显示单位——services 按各代币自身 decimals 换算。
  let tokens = $state<TokenBalance[] | null>(null);
  let tokensErr = $state<string | null>(null);
  let tokenBusy = $state(false);
  $effect(() => {
    if (tab === "tokens" && tokens === null && w && !w.locked) {
      tokensErr = null;
      Services.getTokenBalances()
        .then((bs) => (tokens = bs))
        .catch((e) => {
          tokensErr = String(e);
          tokens = [];
        });
    }
  });

  // fmtToken renders a base-unit balance in display units (per decimals).
  // fmtToken 按精度把基本单位余额渲染为显示单位。
  function fmtToken(b: TokenBalance): string {
    const v = b.value / Math.pow(10, b.decimals);
    return v.toLocaleString("en-US", { maximumFractionDigits: Math.max(2, b.decimals) });
  }

  // op form state: one mode switcher, shared fields. / 表单状态:单一
  // 模式切换 + 共用字段。
  let opMode = $state<"transfer" | "create" | "issue" | "burn">("transfer");
  let opTicker = $state("");
  let opTo = $state("");
  let opValue = $state(0);
  let opDecimals = $state(8);
  let opReissuable = $state(true);
  let opMarker = $state(0.001);
  let opFee = $state(0.001);
  let opResult = $state<TokenResult | null>(null);
  let opErr = $state<string | null>(null);

  // picking a balance row pre-fills ticker + switches to transfer.
  // 点击余额行预填 ticker 并切到转账模式。
  function pickToken(b: TokenBalance) {
    opMode = "transfer";
    opTicker = b.ticker;
  }

  async function runTokenOp() {
    opErr = null;
    opResult = null;
    tokenBusy = true;
    try {
      const r =
        opMode === "transfer"
          ? await Services.tokenTransfer(opTicker.trim(), opTo.trim(), opValue, opMarker, opFee)
          : opMode === "create"
            ? await Services.tokenCreate(opTicker.trim(), opValue, opDecimals, opReissuable, opFee)
            : opMode === "issue"
              ? await Services.tokenIssue(opTicker.trim(), opValue, opFee)
              : await Services.tokenBurn(opTicker.trim(), opValue, opFee);
      opResult = r as TokenResult;
      // refresh the balances after a successful op / 操作成功后刷新余额
      if (!r.broadcastErr) {
        Services.getTokenBalances()
          .then((bs) => (tokens = bs))
          .catch(() => {});
      }
    } catch (e) {
      opErr = String(e).replace(/^Error:\s*/, "");
    } finally {
      tokenBusy = false;
    }
  }

  // Broadcast retry for a signed token tx whose broadcast failed.
  // 已签名代币交易广播失败后的重试。
  async function retryTokenBroadcast() {
    if (!opResult?.rawHex) return;
    opErr = null;
    tokenBusy = true;
    try {
      const txid = await Services.broadcastRaw(opResult.rawHex);
      opResult = { ...opResult, txid, broadcastErr: "" };
      Services.getTokenBalances()
        .then((bs) => (tokens = bs))
        .catch(() => {});
    } catch (e) {
      opErr = String(e).replace(/^Error:\s*/, "");
    } finally {
      tokenBusy = false;
    }
  }

  // Real QR of the receive address (replaces the old decorative pseudo
  // pattern): regenerated whenever the unlocked address changes. SVG output
  // so it stays crisp at any size and needs no canvas.
  // 收款地址的真实二维码(取代原先的装饰性假图案):解锁地址变化时重新
  // 生成。输出 SVG,任意缩放都清晰且无需 canvas。
  let qrSvg = $state("");
  $effect(() => {
    const addr = w?.address;
    if (!addr) {
      qrSvg = "";
      return;
    }
    QRCode.toString(addr, {
      type: "svg",
      margin: 1,
      width: 184,
      color: { dark: "#1a1a1a", light: "#ffffff" },
    })
      .then((s) => (qrSvg = s))
      .catch(() => (qrSvg = ""));
  });

  async function refresh() {
    try {
      w = await Services.getWallet();
      loadErr = null;
    } catch (e) {
      loadErr = String(e);
      w = null;
    }
    if (w && !w.locked) {
      // Found unlocked (page load / email login elsewhere): start the
      // auto-lock countdown and fetch history with the configured row count.
      // 发现已解锁(页面加载/他处邮箱登录):启动自动锁定倒计时,
      // 并按设置的条数拉取历史。
      walletUnlocked();
      txs = await Services.getHistory(walletSettings.historyCount);
    }
  }

  onMount(refresh);

  // React to the auto-lock firing while this page is open: refresh into the
  // locked state. / 页面停留期间自动锁定触发时:刷新为锁定态。
  $effect(() => {
    if (walletAutoLock.lockedAt > 0) refresh();
  });

  // unlock: real walletpassphrase RPC; on success reload balance + history.
  // unlock: 真实 walletpassphrase RPC;成功后重载余额与历史。
  async function unlock() {
    unlockErr = null;
    busy = true;
    try {
      const ok = await Services.unlock(pass);
      if (!ok) {
        unlockErr = t("wal.unlock_failed");
        return;
      }
      pass = "";
      walletUnlocked();
      await refresh();
    } catch {
      unlockErr = t("wal.unlock_failed");
    } finally {
      busy = false;
    }
  }

  // lock: walletlock RPC; drops the in-memory keys.
  // lock: walletlock RPC;丢弃内存密钥。
  async function lock() {
    walletLocked();
    await Services.lockWallet().catch(() => {});
    await refresh();
  }

  function copyAddr() {
    if (!w?.address) return;
    navigator.clipboard?.writeText(w.address);
    copyOk = true;
    setTimeout(() => (copyOk = false), 1500);
  }

  const tabs: { id: typeof tab; label: string }[] = [
    { id: "history", label: "wal.tab_history" },
    { id: "tokens", label: "wal.tab_tokens" },
    { id: "keys", label: "wal.tab_keys" },
    { id: "consolidate", label: "wal.tab_consolidate" },
  ];
</script>

<section class="wal">
  <div class="head">
    <div>
      <p class="eyebrow">wallet · {w?.defaultWalletName ?? "main"}</p>
      <h1 class="h-page">{t("wal.title")}</h1>
    </div>
    <div class="head-actions">
      <!-- secondary page entry: wallet settings / 二级页面入口:钱包设置 -->
      <button
        class="chip btn-chip"
        onclick={() => navigate("wallet-settings")}
        title={t("wal.set.title")}
        aria-label={t("wal.set.title")}
      >
        <svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true">
          <circle cx="12" cy="12" r="3" />
          <path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 1 1-4 0v-.09a1.65 1.65 0 0 0-1-1.51 1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 1 1 0-4h.09a1.65 1.65 0 0 0 1.51-1 1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33h.01a1.65 1.65 0 0 0 1-1.51V3a2 2 0 1 1 4 0v.09a1.65 1.65 0 0 0 1 1.51h.01a1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82v.01a1.65 1.65 0 0 0 1.51 1H21a2 2 0 1 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z" />
        </svg>
        {t("wal.set.entry")}
      </button>
      {#if w && !w.locked}
        <button class="chip btn-chip" onclick={lock} title={t("wal.lock")}>
          <span class="dot ok" aria-hidden="true"></span> {t("wal.lock")}
        </button>
      {/if}
    </div>
  </div>

  {#if loadErr}
    <div class="card err-card" role="alert">
      <p class="err"><span class="dot" aria-hidden="true"></span>{loadErr}</p>
    </div>
  {:else if w && w.locked}
    <!-- ── locked: real unlock form (BIP39 passphrase) + login shortcut ── -->
    <!-- ── 锁定:真实解锁表单(BIP39 口令)+ 登录入口 ── -->
    <div class="card unlock-card">
      <h2 class="h-card">{t("wal.unlock")}</h2>
      <form class="unlock-form" onsubmit={(e) => { e.preventDefault(); unlock(); }}>
        <input
          type="password"
          bind:value={pass}
          placeholder={t("g.password")}
          aria-label={t("g.password")}
          autocomplete="off"
        />
        <button class="btn btn-primary" type="submit" disabled={busy}>
          {#if busy}<span class="spin" aria-hidden="true"></span>{/if}
          {t("wal.unlock")}
        </button>
      </form>
      {#if unlockErr}
        <p class="err" role="alert"><span class="dot" aria-hidden="true"></span>{unlockErr}</p>
      {/if}
      <p class="hint">{t("wal.login_hint")}</p>
      <button class="btn btn-ghost" onclick={() => navigate("create")}>{t("wal.go_login")} →</button>
    </div>
  {:else if w}
    <div class="top-grid">
      <!-- ── balance card ── -->
      <!-- ── 余额卡片 ── -->
      <div class="card balance">
        <p class="eyebrow">{t("wal.total")}</p>
        <!-- privacy mode: mask all balance figures (wallet settings) -->
        <!-- 隐私模式:遮蔽全部余额数字(钱包设置) -->
        {#if walletSettings.hideBalance}
          <p class="bal mono" translate="no">{t("wal.hidden_balance")}</p>
        {:else if !w.chainOnline}
          <!-- node offline: wallet itself works; chain figures unknown -->
          <!-- 节点离线:钱包本身可用;链上数字未知 -->
          <p class="bal mono" translate="no">— <span class="unit">S</span></p>
        {:else}
          <p class="bal mono" translate="no">{(w.total).toFixed(8)} <span class="unit">S</span></p>
        {/if}
        <div class="bal-sub">
          {#if walletSettings.hideBalance}
            <span class="part"><span class="dot ok" aria-hidden="true"></span>{t("wal.confirmed", { n: t("wal.hidden_balance") })}</span>
          {:else if !w.chainOnline}
            <span class="part"><span class="dot wait" aria-hidden="true"></span>{t("wal.offline_balance")}</span>
          {:else}
            <span class="part"><span class="dot ok" aria-hidden="true"></span>{t("wal.confirmed", { n: w.confirmed.toFixed(4) })}</span>
            <!-- external REST source badge: figures not from the local node -->
            <!-- 外部 REST 数据源徽章:数字并非来自本节点 -->
            {#if w.chainExternal}
              <span class="part ext"><span class="dot wait" aria-hidden="true"></span>{t("wal.ext_source")}</span>
            {/if}
          {/if}
          {#if w.chainOnline && !walletSettings.hideBalance && w.pending > 0}
            <span class="part"><span class="dot wait" aria-hidden="true"></span>{t("wal.pending", { n: w.pending.toFixed(4) })}</span>
          {/if}
          {#if w.chainOnline && !walletSettings.hideBalance && w.immature > 0}
            <span class="part"><span class="dot lock" aria-hidden="true"></span>{t("wal.immature", { n: w.immature.toFixed(4) })}</span>
          {/if}
        </div>
        <div class="bal-actions">
          <button class="btn btn-primary" onclick={() => navigate("send")}>{t("wal.send")}</button>
          <button class="btn btn-ghost" onclick={copyAddr}>{copyOk ? "✓" : t("wal.receive")}</button>
        </div>
      </div>

      <!-- ── receive address card ── -->
      <!-- ── 收款地址卡片 ── -->
      <div class="card receive">
        <!-- Step 9: label follows the configured address type / 标签跟随
             配置的地址类型 -->
        <p class="eyebrow">{t("wal.receive_addr")} ({walletSettings.addressType})</p>
        <!-- real QR encoding the receive address / 编码收款地址的真实二维码 -->
        {#if qrSvg}
          <div class="qr" aria-hidden="true">{@html qrSvg}</div>
        {/if}
        <p class="addr mono" translate="no">{w.address}</p>
        <div class="addr-actions">
          <button class="btn btn-ghost" aria-label={t("g.copy")} onclick={copyAddr}>
            <svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true"><rect x="9" y="9" width="13" height="13" rx="2" /><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1" /></svg>
            {copyOk ? "✓" : t("g.copy")}
          </button>
          <button class="btn btn-ghost" aria-label={t("dash.retry")} onclick={refresh}>{t("dash.retry")}</button>
        </div>
        <p class="reuse-warn mono">{t("wal.address_reuse_warning")}</p>
      </div>
    </div>

    <!-- ── tabs ── -->
    <!-- ── 标签页 ── -->
    <div class="card tabled-card">
      <div class="tabs" role="tablist" aria-label={t("wal.title")}>
        {#each tabs as tb}
          <button
            class="tab"
            class:active={tab === tb.id}
            role="tab"
            aria-selected={tab === tb.id}
            onclick={() => (tab = tb.id)}
          >
            {t(tb.label)}
          </button>
        {/each}
      </div>

      {#if tab === "history"}
        {#if txs.length === 0}
          <!-- node offline: distinguish "no history" from "node not reachable" -->
          <!-- 节点离线:区分"无历史记录"与"节点不可达" -->
          <p class="empty">{w && !w.chainOnline ? t("wal.offline_balance") : t("wal.empty")}</p>
        {:else}
          <table class="tx-table">
            <thead>
              <tr>
                <th scope="col">{t("wal.col_time")}</th>
                <th scope="col">{t("wal.col_dir")}</th>
                <th scope="col">{t("wal.col_amount")}</th>
                <th scope="col">{t("wal.col_status")}</th>
              </tr>
            </thead>
            <tbody>
              {#each txs as x}
                <tr>
                  <td class="mono" translate="no">{fmtDateTime(x.time)}</td>
                  <td>{x.dir === "out" ? t("wal.out") : t("wal.in")}</td>
                  <td class={`mono amount ${x.dir}`} translate="no">{x.dir === "out" ? "−" : "+"}{(x.amount < 0 ? -x.amount : x.amount).toFixed(8)} S</td>
                  <td>
                    <span class={`st ${x.status}`}>
                      <span class="dot" aria-hidden="true"></span>
                      {x.status === "confirmed" ? t("wal.confirmed_status") : t("wal.pending_status")}
                    </span>
                    <!-- txid deep-links into the Explorer tx view (Step 10) -->
                    <!-- txid 内链到浏览器交易视图(第 10 步) -->
                    <span
                      class="mono hash link"
                      translate="no"
                      title={x.hash}
                      role="button"
                      tabindex="0"
                      onclick={() => navigate("explorer", { txid: x.hash })}
                      onkeydown={(e) => e.key === "Enter" && navigate("explorer", { txid: x.hash })}
                    >({x.hash})</span>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        {/if}
      {:else if tab === "tokens"}
        <!-- ── Tokens tab (Step 6/6b): balances + four ops ── -->
        <!-- ── 代币标签页(第 6/6b 步):余额 + 四操作 ── -->
        {#if tokens === null}
          <p class="empty">{t("wal.loading")}</p>
        {:else if tokensErr}
          <p class="empty">{tokensErr}</p>
        {:else if tokens.length === 0}
          <p class="empty">{t("wal.tab_tokens_empty")}</p>
        {:else}
          <table class="key-table token-table">
            <thead>
              <tr>
                <th scope="col">{t("wal.tok_col_ticker")}</th>
                <th scope="col">{t("wal.tok_col_balance")}</th>
                <th scope="col">{t("con.col_action")}</th>
              </tr>
            </thead>
            <tbody>
              {#each tokens as b (b.ticker)}
                <tr>
                  <td class="mono" translate="no">{b.ticker}</td>
                  <td class="mono" translate="no">{fmtToken(b)}</td>
                  <td>
                    <button class="mini" onclick={() => pickToken(b)}>{t("wal.tok_transfer")}</button>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        {/if}

        <!-- ops panel: one form, mode switcher / 操作面板:单表单 + 模式切换 -->
        <div class="token-ops">
          <div class="op-tabs" role="tablist">
            {#each ["transfer", "create", "issue", "burn"] as m}
              <button class="op-tab" class:active={opMode === m} role="tab" aria-selected={opMode === m} onclick={() => (opMode = m as typeof opMode)}>
                {t(`wal.tok_${m}`)}
              </button>
            {/each}
          </div>
          <form
            class="op-form"
            onsubmit={(e) => {
              e.preventDefault();
              runTokenOp();
            }}
          >
            <div class="op-row">
              <label>
                <span>{t("wal.tok_ticker")}</span>
                <input type="text" bind:value={opTicker} spellcheck="false" translate="no" required />
              </label>
              {#if opMode === "transfer"}
                <label>
                  <span>{t("send.to")}</span>
                  <input type="text" bind:value={opTo} spellcheck="false" translate="no" required />
                </label>
              {/if}
              <label>
                <span>{t("wal.tok_value")}</span>
                <input type="number" bind:value={opValue} min="0" step="any" required />
              </label>
              {#if opMode === "transfer"}
                <label>
                  <span>{t("wal.tok_marker")}</span>
                  <input type="number" bind:value={opMarker} min="0.00000001" step="any" />
                </label>
              {/if}
              {#if opMode === "create"}
                <label>
                  <span>{t("wal.tok_decimals")}</span>
                  <input type="number" bind:value={opDecimals} min="0" max="18" />
                </label>
                <label class="check-inline">
                  <input type="checkbox" bind:checked={opReissuable} />
                  <span>{t("wal.tok_reissuable")}</span>
                </label>
              {/if}
              <label>
                <span>{t("wal.tok_fee")}</span>
                <input type="number" bind:value={opFee} min="0.00000001" step="any" />
              </label>
            </div>
            <button class="btn btn-primary" type="submit" disabled={tokenBusy}>
              {#if tokenBusy}<span class="spin" aria-hidden="true"></span>{/if}
              {t(`wal.tok_${opMode}`)}
            </button>
          </form>
          {#if opErr}
            <p class="err" role="alert"><span class="dot" aria-hidden="true"></span>{opErr}</p>
          {/if}
          {#if opResult}
            <div class="op-result" role="status">
              {#if opResult.broadcastErr}
                <p class="err"><span class="dot" aria-hidden="true"></span>{t("wal.tok_broadcast_failed", { e: opResult.broadcastErr })}</p>
                <button class="btn btn-ghost" onclick={retryTokenBroadcast} disabled={tokenBusy}>{t("wal.tok_retry")}</button>
              {:else}
                <p class="ok mono" translate="no">
                  {t("wal.tok_done")} <span class="hash" title={opResult.txid} role="button" tabindex="0"
                    onclick={() => navigate("explorer", { txid: opResult!.txid })}
                    onkeydown={(e) => e.key === "Enter" && navigate("explorer", { txid: opResult!.txid })}
                  >{opResult.txid}</span>
                </p>
              {/if}
            </div>
          {/if}
        </div>
      {:else if tab === "keys"}
        <!-- derived-address list: local, works without the node -->
        <!-- 已派生地址列表:纯本地,无需节点 -->
        {#if addrs === null}
          <p class="empty">{t("wal.loading")}</p>
        {:else if addrs.length === 0}
          <p class="empty">{t("wal.tab_keys_hint")}</p>
        {:else}
          <table class="key-table">
            <thead>
              <tr>
                <th scope="col">{t("wal.col_index")}</th>
                <th scope="col">{t("con.col_addr")}</th>
                <th scope="col">{t("con.col_action")}</th>
              </tr>
            </thead>
            <tbody>
              {#each addrs as a}
                <tr>
                  <td class="mono" translate="no">#{a.index}</td>
                  <td class="mono key-addr" translate="no" title={a.address}>{a.address}</td>
                  <td>
                    <button class="mini" onclick={() => copyKeyAddr(a.index, a.address)}>
                      {addrCopied === a.index ? "✓" : t("g.copy")}
                    </button>
                    <!-- Step 11: WIF export → clipboard only / WIF 导出 → 仅剪贴板 -->
                    <button class="mini" title={t("wal.wif_export_hint")} onclick={() => exportKeyWIF(a.index)}>
                      {wifCopied === a.index ? "✓ WIF" : "WIF"}
                    </button>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        {/if}
      {:else}
        <!-- consolidation: needs sending (Step 5), placeholder for now -->
        <!-- 合并功能依赖发送(Step 5),暂为占位 -->
        <p class="empty">{t("wal.tab_consolidate_hint")}</p>
      {/if}
    </div>
  {/if}
</section>

<style>
  .wal {
    display: flex;
    flex-direction: column;
    gap: 16px;
    max-width: 1080px;
    margin: 0 auto;
    animation: rise 0.28s ease both;
  }
  @keyframes rise {
    from {
      opacity: 0;
      transform: translateY(6px);
    }
    to {
      opacity: 1;
      transform: none;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .wal {
      animation: none;
    }
  }
  .head {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
    gap: 12px;
  }
  .h-page {
    font-family: var(--font-display);
    font-size: 24px;
    margin: 2px 0 0;
  }
  .head-actions {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
  }
  .btn-chip {
    cursor: pointer;
  }
  .btn-chip:hover {
    background: var(--violet-2);
    color: var(--ink-fg);
  }
  .dot.ok {
    background: var(--mint);
  }
  .dot.wait {
    background: var(--honey);
  }
  .dot.lock {
    background: var(--mist);
  }

  /* ── locked state: centered unlock form ── */
  .unlock-card {
    max-width: 440px;
    margin: 12px auto 0;
    width: 100%;
    display: flex;
    flex-direction: column;
    gap: 12px;
    padding: 18px;
  }
  .unlock-form {
    display: flex;
    gap: 8px;
  }
  .unlock-form input {
    flex: 1;
    min-width: 0;
  }
  .hint {
    font-size: 11px;
    color: var(--ink-dim);
    margin: 0;
    line-height: 1.45;
  }
  .err {
    display: flex;
    align-items: center;
    gap: 7px;
    color: var(--honey);
    font-size: 12px;
    margin: 0;
  }
  .err .dot {
    background: var(--honey);
    width: 6px;
    height: 6px;
    border-radius: 50%;
    flex: none;
  }
  .err-card {
    max-width: 640px;
    margin: 12px auto 0;
    width: 100%;
  }

  .top-grid {
    display: grid;
    grid-template-columns: 1.2fr 1fr;
    gap: 14px;
  }
  .balance {
    display: flex;
    flex-direction: column;
  }
  .bal {
    font-size: 30px;
    font-family: var(--font-display);
    margin: 6px 0 0;
    letter-spacing: -0.5px;
  }
  .unit {
    font-size: 18px;
    color: var(--straw);
    margin-left: 2px;
  }
  .bal-sub {
    display: flex;
    gap: 12px;
    flex-wrap: wrap;
    margin: 6px 0 14px;
  }
  .part {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    font-size: 12px;
    color: var(--ink-dim);
  }
  .part .dot {
    width: 6px;
    height: 6px;
  }
  .part.ext {
    color: var(--honey);
  }
  .bal-actions {
    display: flex;
    gap: 8px;
    margin-top: auto;
  }

  .receive {
    display: flex;
    flex-direction: column;
    align-items: center;
    text-align: center;
    gap: 8px;
  }
  .qr {
    border-radius: var(--r-8);
    padding: 6px;
    background: #fff;
    border: 1px solid var(--line);
    display: flex;
    /* injected QR SVG fills the box regardless of its width attribute */
    /* 注入的二维码 SVG 填满容器,无视其 width 属性 */
  }
  .qr :global(svg) {
    display: block;
    width: 184px;
    height: auto;
  }
  .addr {
    max-width: 100%;
    word-break: break-all;
    font-size: 12px;
    font-variant-numeric: tabular-nums;
    margin: 0;
  }
  .addr-actions {
    display: flex;
    gap: 8px;
  }
  .reuse-warn {
    font-size: 11px;
    color: var(--honey);
    margin: 0;
  }

  .tabled-card {
    padding: 0;
    overflow: hidden;
  }
  .tabs {
    display: flex;
    gap: 4px;
    padding: 10px 12px 0;
    border-bottom: 1px solid var(--line);
  }
  .tab {
    background: none;
    border: none;
    color: var(--ink-dim);
    padding: 8px 14px;
    font-size: 13px;
    font-weight: 600;
    cursor: pointer;
    border-radius: var(--r-8) var(--r-8) 0 0;
    transition: color 0.12s ease, background 0.12s ease;
  }
  .tab:hover {
    color: var(--ink-fg);
    background: var(--violet-2);
  }
  .tab.active {
    color: var(--straw);
    box-shadow: inset 0 -2px 0 var(--straw);
  }

  .tx-table {
    width: 100%;
    border-collapse: collapse;
    font-size: 13px;
  }
  .tx-table th {
    text-align: left;
    font-size: 11px;
    font-weight: 700;
    letter-spacing: 0.8px;
    text-transform: uppercase;
    color: var(--mist);
    padding: 10px 14px;
  }
  .tx-table td {
    padding: 9px 14px;
    border-top: 1px solid var(--line);
    font-variant-numeric: tabular-nums;
  }
  .amount.in {
    color: var(--mint);
  }
  .amount.out {
    color: var(--straw);
  }
  .st {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 12px;
  }
  .st .dot {
    width: 7px;
    height: 7px;
  }
  .st.confirmed .dot {
    background: var(--mint);
  }
  .st.pending .dot {
    background: var(--honey);
  }
  .st.pending {
    color: var(--honey);
  }
  .hash {
    color: var(--ink-dim);
    font-size: 11px;
    margin-left: 6px;
    /* full txid is 64 chars — truncate to keep the table readable, full
       value on hover via title / 完整 txid 64 字符——截断保持表格可读,
       title 提供悬停全文 */
    max-width: 120px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    display: inline-block;
    vertical-align: bottom;
  }
  .tx-table td .hash {
    display: inline-block;
  }

  /* Keys tab address table / Keys 标签页地址表 */
  .key-table {
    width: 100%;
    border-collapse: collapse;
    font-size: 12px;
  }
  .key-table th {
    text-align: left;
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--ink-dim);
    padding: 10px 10px 8px;
    border-bottom: 1px solid var(--line);
  }
  .key-table td {
    padding: 8px 10px;
    border-bottom: 1px solid var(--violet);
  }
  .key-table tr:last-child td {
    border-bottom: none;
  }
  .key-addr {
    max-width: 340px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .mini {
    background: none;
    border: 1px solid var(--line);
    color: var(--ink-fg);
    border-radius: 6px;
    padding: 3px 9px;
    font-size: 11px;
    cursor: pointer;
  }
  .mini:hover {
    border-color: var(--straw);
  }
  .empty {
    color: var(--ink-dim);
    padding: 28px;
    margin: 0;
    text-align: center;
    font-size: 13px;
  }

  /* ── Tokens tab (Step 6/6b): ops panel + result ── */
  /* ── 代币标签页(第 6/6b 步):操作面板 + 结果 ── */
  .token-ops {
    border-top: 1px solid var(--line);
    padding: 14px;
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .op-tabs {
    display: flex;
    gap: 4px;
    flex-wrap: wrap;
  }
  .op-tab {
    background: none;
    border: 1px solid var(--line);
    color: var(--ink-dim);
    padding: 5px 12px;
    font-size: 12px;
    font-weight: 600;
    cursor: pointer;
    border-radius: 999px;
  }
  .op-tab:hover {
    color: var(--ink-fg);
    border-color: var(--straw);
  }
  .op-tab.active {
    color: var(--straw);
    border-color: var(--straw);
    background: var(--violet-2);
  }
  .op-form {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .op-row {
    display: flex;
    gap: 10px;
    flex-wrap: wrap;
  }
  .op-row label {
    display: flex;
    flex-direction: column;
    gap: 4px;
    font-size: 11px;
    color: var(--ink-dim);
    min-width: 120px;
    flex: 1;
  }
  .op-row label.check-inline {
    flex-direction: row;
    align-items: center;
    gap: 6px;
    min-width: 0;
    flex: none;
    align-self: end;
    padding-bottom: 7px;
  }
  .op-row input[type="number"] {
    font-variant-numeric: tabular-nums;
  }
  .op-result {
    display: flex;
    align-items: center;
    gap: 10px;
    flex-wrap: wrap;
  }
  .ok {
    color: var(--mint);
    font-size: 12px;
    margin: 0;
    display: inline-flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
  }
  .hash.link,
  .kv-v.link {
    color: var(--straw);
    cursor: pointer;
  }
  .hash.link:hover {
    text-decoration: underline;
  }
  .err {
    display: flex;
    align-items: center;
    gap: 7px;
    color: var(--honey);
    font-size: 12px;
    margin: 0;
  }
  .err .dot {
    background: var(--honey);
    width: 6px;
    height: 6px;
    border-radius: 50%;
    flex: none;
  }

  @media (max-width: 760px) {
    .top-grid {
      grid-template-columns: 1fr;
    }
  }
</style>
