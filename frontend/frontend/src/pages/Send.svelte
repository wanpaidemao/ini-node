<script lang="ts">
  import { onMount } from "svelte";
  import { t } from "../lib/i18n";
  import { Services } from "../lib/services";

  // Step 8 real send pipeline (in-process bindings, §5.11):
  //   multi-output rows (≤10) → validation → confirm modal → Send →
  //   result card (txid / rawHex / retry) → recent recipients saved.
  // The old PSBT-preview mock flow is gone.
  // 第 8 步真实发送链路（进程内 bindings，方案 §5.11）：
  //   多输出行（≤10）→ 校验 → 确认弹窗 → Send →
  //   结果卡（txid / rawHex / 重试）→ 记住近期收款人。
  // 旧的 PSBT 预览 mock 流程已移除。

  // One payment row. / 一个收款行。
  interface Row {
    to: string;
    amount: string;
  }

  let rows = $state<Row[]>([{ to: "", amount: "0.50000000" }]);
  let fee = $state("0.00000100");
  let feeMode = $state<"low" | "med" | "high" | "custom">("med");
  let raw = $state("");

  let sending = $state(false);
  let confirming = $state(false);
  let txid = $state<string | null>(null);
  let broadcastErr = $state<string | null>(null);
  let rawHex = $state<string | null>(null);
  let sendErr = $state<string | null>(null);
  let hexCopied = $state(false);
  let retrying = $state(false);

  // Result summary from the last Send (satoshis → SUGAR for display).
  // 最近一次 Send 的结果摘要（聪 → SUGAR 显示）。
  let result = $state<{
    totalIn: number;
    amount: number;
    fee: number;
    change: number;
    inputCount: number;
  } | null>(null);

  // available: real confirmed balance (getwalletinfo / external source).
  // available: 真实已确认余额（getwalletinfo / 外部数据源）。
  let available = $state(0);
  let walletLocked = $state(false);

  // Recent recipients (localStorage, mirrors web-wallet's saved-recipients
  // dropdown; the node-less equivalent of its disk-side list).
  // 近期收款人（localStorage，对应 web-wallet 的已存收款人下拉；
  // 等价于其磁盘侧列表，但不需要节点）。
  let recents = $state<string[]>([]);

  const RECENTS_KEY = "ini.recipients";

  function loadRecents() {
    try {
      recents = JSON.parse(localStorage.getItem(RECENTS_KEY) ?? "[]");
      if (!Array.isArray(recents)) recents = [];
    } catch {
      recents = [];
    }
  }

  function saveRecipients(addrs: string[]) {
    const uniq = Array.from(new Set([...addrs, ...recents])).filter((a) => a.trim() !== "");
    recents = uniq.slice(0, 10);
    try {
      localStorage.setItem(RECENTS_KEY, JSON.stringify(recents));
    } catch {
      /* storage full/blocked → keep in-memory only / 存储受限 → 仅保留内存 */
    }
  }

  onMount(async () => {
    loadRecents();
    try {
      const w = await Services.getWallet();
      walletLocked = w.locked;
      if (!w.locked) available = w.confirmed;
    } catch {
      /* node offline → available stays 0 / 节点离线 → 余额保持 0 */
    }
  });

  const satToSugar = (sat: number) => sat / 1e8;
  const rowAmount = (r: Row) => parseFloat(r.amount) || 0;
  const totalAmount = () => rows.reduce((s, r) => s + rowAmount(r), 0);
  const feeS = () => parseFloat(fee) || 0;

  const feeRates: Record<string, number> = { low: 0.0000002, med: 0.000001, high: 0.000003 };

  function feeChanged(ev: Event) {
    const name = (ev.target as HTMLSelectElement).value;
    if (name !== "custom") {
      feeMode = name as typeof feeMode;
      fee = feeRates[name].toFixed(8);
    } else {
      feeMode = "custom";
    }
  }

  // estimateFee: suggestion from the external REST /fee (external source
  // mode only; manual input stays available otherwise).
  // estimateFee: 外部 REST /fee 的建议（仅外部源模式；否则保留手动输入）。
  async function estimateFee() {
    try {
      const v = await Services.estimateFee();
      fee = (v > 0 ? v : feeS()).toFixed(8);
      feeMode = "custom";
    } catch {
      /* keep manual fee — surfaced implicitly by disabled state */
      /* 保留手动费用——禁用态已隐式提示 */
    }
  }

  function addRow() {
    if (rows.length < 10) rows.push({ to: "", amount: "0.10000000" });
  }
  function removeRow(i: number) {
    if (rows.length > 1) rows.splice(i, 1);
  }

  function validAddr(a: string) {
    return a === "" || /^(sugar1q|bc1q|3|1)[a-zA-Z0-9]{10,}$/.test(a.trim());
  }
  function blocked() {
    return (
      totalAmount() <= 0 ||
      totalAmount() + feeS() > available ||
      feeS() < 0 ||
      rows.some((r) => r.to.trim() === "" || !validAddr(r.to) || rowAmount(r) <= 0)
    );
  }

  async function pasteTo(r: Row) {
    try {
      r.to = (await navigator.clipboard.readText()).trim();
    } catch {
      /* clipboard permission denied → ignore / 剪贴板无权限 → 忽略 */
    }
  }

  // openConfirm shows the confirm modal (validation already passed).
  // openConfirm 展示确认弹窗（校验已通过）。
  function openConfirm() {
    confirming = true;
  }

  // doSend runs the real pipeline: Send → result card; broadcast failure
  // keeps rawHex for the retry button. Recipients are saved on success.
  // doSend 执行真实流水线：Send → 结果卡；广播失败保留 rawHex 供重试按钮。
  // 成功后记录收款人。
  async function doSend() {
    confirming = false;
    sending = true;
    sendErr = null;
    txid = null;
    broadcastErr = null;
    result = null;
    try {
      const r = await Services.sendOutputs(
        rows.map((row) => ({ address: row.to.trim(), amount: rowAmount(row) })),
        feeS(),
      );
      rawHex = r.rawHex;
      result = {
        totalIn: satToSugar(r.totalIn),
        amount: satToSugar(r.amount),
        fee: satToSugar(r.fee),
        change: satToSugar(r.change),
        inputCount: r.inputCount,
      };
      if (r.broadcastErr) {
        broadcastErr = r.broadcastErr;
      } else {
        txid = r.txid;
        saveRecipients(rows.map((row) => row.to.trim()));
        // reset form after a successful broadcast / 广播成功后重置表单
        rows = [{ to: "", amount: "0.50000000" }];
      }
    } catch (e) {
      sendErr = String(e).replace(/^Error:\s*/, "");
    } finally {
      sending = false;
    }
  }

  // retryBroadcast pushes the kept rawHex again (node → external chain).
  // retryBroadcast 再次推送保留的 rawHex（节点 → 外部链）。
  async function retryBroadcast() {
    if (!rawHex) return;
    retrying = true;
    try {
      const id = await Services.broadcastRaw(rawHex);
      txid = id;
      broadcastErr = null;
      saveRecipients(rows.map((row) => row.to.trim()));
    } catch (e) {
      broadcastErr = String(e).replace(/^Error:\s*/, "");
    } finally {
      retrying = false;
    }
  }

  // broadcastRawRow: the raw-tx input row (Broadcast page equivalent).
  // broadcastRawRow: 裸交易输入行（对应 web-wallet 的 Broadcast 页）。
  async function broadcastRawRow() {
    retrying = true;
    broadcastErr = null;
    txid = null;
    try {
      txid = await Services.broadcastRaw(raw.trim());
    } catch (e) {
      broadcastErr = String(e).replace(/^Error:\s*/, "");
    } finally {
      retrying = false;
    }
  }

  function copyHex() {
    if (!rawHex) return;
    navigator.clipboard?.writeText(rawHex);
    hexCopied = true;
    setTimeout(() => (hexCopied = false), 1500);
  }
</script>

<section class="send">
  <div class="head">
    <div>
      <p class="eyebrow">transaction</p>
      <h1 class="h-page">{t("send.title")}</h1>
    </div>
    <span class="chip"><span class="dot mint" aria-hidden="true"></span> {t("send.target_mainnet")}</span>
  </div>

  {#if walletLocked}
    <div class="card locked-note">
      <p class="note">{t("send.locked_note")}</p>
    </div>
  {/if}

  <div class="card form">
    {#each rows as row, i (i)}
      <div class="field out-row">
        <label class="field-label" for="to-{i}">
          {t("send.to")}{#if rows.length > 1} #{i + 1}{/if}
        </label>
        <div class="field-row">
          <input
            id="to-{i}"
            class:invalid={!validAddr(row.to) && row.to !== ""}
            bind:value={row.to}
            placeholder="sugar1…"
            autocomplete="off"
            spellcheck="false"
            list="recent-recipients"
          />
          <button class="btn" type="button" onclick={() => pasteTo(row)}>{t("g.paste")}</button>
          {#if rows.length > 1}
            <button class="btn row-del" type="button" onclick={() => removeRow(i)} aria-label={t("send.remove_row")}>✕</button>
          {/if}
        </div>
        <div class="field-row amt-row">
          <div class="field">
            <label class="field-label" for="amt-{i}">{t("send.amount")}</label>
            <div class="field-row">
              <input id="amt-{i}" bind:value={row.amount} inputmode="decimal" autocomplete="off" />
              <span class="suffix mono">S</span>
            </div>
          </div>
        </div>
        {#if !validAddr(row.to) && row.to !== ""}
          <p class="err"><span class="dot" aria-hidden="true"></span> invalid address</p>
        {/if}
        {#if rowAmount(row) > available}
          <p class="err">{t("send.blocked_insufficient")}</p>
        {/if}
      </div>
    {/each}

    <!-- recent recipients datalist: native dropdown for the to fields -->
    <!-- 近期收款人 datalist：地址输入框的原生下拉 -->
    <datalist id="recent-recipients">
      {#each recents as a}
        <option value={a}></option>
      {/each}
    </datalist>

    <div class="add-row">
      <button class="btn btn-ghost" type="button" onclick={addRow} disabled={rows.length >= 10}>
        + {t("send.add_output")}
      </button>
      <span class="hint">{rows.length}/10</span>
    </div>

    <div class="field-row split">
      <div class="field">
        <span class="field-label">{t("send.total")}</span>
        <p class="total mono">{totalAmount().toFixed(8)} S</p>
        <p class="hint">{t("send.available", { n: available.toFixed(2) })} S</p>
      </div>
      <div class="field">
        <label class="field-label" for="fee">{t("send.fee")}</label>
        <div class="field-row">
          <select id="fee" onchange={feeChanged}>
            <option value="low" selected={feeMode === "low"}>{t("send.fee_estimate", { size: "1 blk" })}</option>
            <option value="med" selected={feeMode === "med"}>{t("send.fee_estimate", { size: "3 blk" })}</option>
            <option value="high" selected={feeMode === "high"}>{t("send.fee_estimate", { size: "5 blk" })}</option>
            <option value="custom" selected={feeMode === "custom"}>{t("send.custom")}</option>
          </select>
          <input class="fee-input" bind:value={fee} aria-label="fee value" inputmode="decimal" />
          <button class="btn" type="button" onclick={estimateFee} title={t("send.fee_suggest_hint")}>
            {t("send.fee_suggest")}
          </button>
        </div>
      </div>
      {#if totalAmount() + feeS() > available}
        <p class="err">{t("send.blocked_insufficient")}</p>
      {/if}
    </div>

    <div class="submit-row">
      <button class="btn btn-primary" type="button" disabled={blocked() || sending} onclick={openConfirm}>
        {#if sending}<span class="spin" aria-hidden="true"></span>{/if}
        {t("send.send_btn")}
      </button>
    </div>

    {#if sendErr}
      <div class="errbox" role="alert">
        <span>{sendErr}</span>
      </div>
    {/if}
  </div>

  {#if result}
    <div class="card preview">
      <div class="card-head">
        <span class="h-card">{t("send.result_title")}</span>
        {#if txid}
          <span class="chip mint"><span class="dot mint" aria-hidden="true"></span> {t("send.sent_state")}</span>
        {:else}
          <span class="chip honey"><span class="dot honey" aria-hidden="true"></span> {t("send.not_broadcast")}</span>
        {/if}
      </div>
      <dl class="kv">
        <div><dt>{t("send.inputs_count")}</dt><dd class="mono">{result.inputCount}</dd></div>
        <div><dt>{t("send.outputs_count")}</dt><dd class="mono">{rows.length + (result.change > 0 ? 1 : 0)}</dd></div>
        <div><dt>{t("send.amount")}</dt><dd class="mono" translate="no">{result.amount.toFixed(8)} S</dd></div>
        <div><dt>{t("send.fee")}</dt><dd class="mono" translate="no">{result.fee.toFixed(8)} S</dd></div>
        <div><dt>{t("send.change")}</dt><dd class="mono" translate="no">{result.change.toFixed(8)} S</dd></div>
        <div><dt>{t("send.total_in")}</dt><dd class="mono" translate="no">{result.totalIn.toFixed(8)} S</dd></div>
      </dl>
      {#if txid}
        <p class="txid mono" translate="no">✓ {txid}</p>
      {/if}
      {#if broadcastErr}
        <div class="errbox" role="alert">
          <span>{broadcastErr}</span>
          <button class="mini" onclick={retryBroadcast} disabled={retrying}>{t("send.resolve")}</button>
        </div>
      {/if}
      <div class="preview-actions">
        <button class="btn" disabled={retrying || !broadcastErr} onclick={retryBroadcast}>
          {#if retrying}<span class="spin" aria-hidden="true"></span>{/if}
          {t("send.retry_broadcast")}
        </button>
        <button class="btn btn-ghost" onclick={copyHex}>{hexCopied ? "✓" : t("send.copy_hex")}</button>
      </div>
    </div>
  {/if}

  <div class="card raw-row">
    <label class="field-label" for="raw">{t("send.raw_broadcast")}</label>
    <div class="field-row">
      <input id="raw" bind:value={raw} placeholder="02000000…" autocomplete="off" spellcheck="false" class="mono" />
      <button class="btn" onclick={broadcastRawRow} disabled={raw.trim() === "" || retrying}>{t("send.parse")}</button>
    </div>
  </div>
</section>

<!-- confirm modal: mirrors web-wallet's send confirmation dialog -->
<!-- 确认弹窗：对应 web-wallet 的发送确认对话框 -->
{#if confirming}
  <div class="modal-veil" role="dialog" aria-modal="true" aria-label={t("send.confirm_title")}>
    <div class="card modal">
      <span class="h-card">{t("send.confirm_title")}</span>
      <dl class="kv">
        <div><dt>{t("send.recipients")}</dt><dd class="mono">{rows.filter((r) => r.to.trim() !== "").length}</dd></div>
        <div><dt>{t("send.amount")}</dt><dd class="mono" translate="no">{totalAmount().toFixed(8)} S</dd></div>
        <div><dt>{t("send.fee")}</dt><dd class="mono" translate="no">{feeS().toFixed(8)} S</dd></div>
        <div><dt>{t("send.total_plus_fee")}</dt><dd class="mono" translate="no">{(totalAmount() + feeS()).toFixed(8)} S</dd></div>
      </dl>
      <ul class="out-list">
        {#each rows as row, i (i)}
          {#if row.to.trim() !== ""}
            <li><span class="mono addr" title={row.to}>{row.to}</span><span class="mono">{rowAmount(row).toFixed(8)} S</span></li>
          {/if}
        {/each}
      </ul>
      <div class="modal-actions">
        <button class="btn btn-ghost" type="button" onclick={() => (confirming = false)}>{t("g.cancel")}</button>
        <button class="btn btn-primary" type="button" onclick={doSend}>{t("send.confirm_send")}</button>
      </div>
    </div>
  </div>
{/if}

<style>
  .send {
    display: flex;
    flex-direction: column;
    gap: 16px;
    max-width: 880px;
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
    .send {
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
  .dot.mint {
    background: var(--mint);
  }
  .dot.honey {
    background: var(--honey);
  }
  .chip.honey {
    border-color: var(--honey);
    color: var(--honey);
  }
  .locked-note {
    border-color: var(--honey);
    background: #fff;
  }
  .note {
    margin: 0;
    font-size: 12px;
    color: var(--honey);
  }
  .form {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .out-row {
    border: 1px solid var(--line);
    border-radius: var(--r-12);
    padding: 12px 14px;
    background: var(--violet);
  }
  .amt-row {
    margin-top: 4px;
    max-width: 320px;
  }
  .row-del {
    flex: none;
    color: var(--honey);
    border-color: var(--honey);
  }
  .add-row {
    display: flex;
    align-items: center;
    gap: 10px;
  }
  .add-row .hint {
    font-size: 11px;
    color: var(--ink-dim);
  }
  .field {
    display: flex;
    flex-direction: column;
    gap: 5px;
    flex: 1;
    min-width: 0;
  }
  .field-row {
    display: flex;
    gap: 8px;
    align-items: center;
  }
  .field-row input {
    flex: 1;
    min-width: 0;
  }
  .field-row.split {
    gap: 14px;
    flex-wrap: wrap;
    align-items: flex-start;
  }
  .total {
    margin: 0;
    font-size: 15px;
    font-weight: 700;
    color: var(--ink-fg);
  }
  .suffix {
    color: var(--straw);
    font-weight: 700;
    font-size: 13px;
  }
  input.invalid {
    border-color: var(--straw);
  }
  .err {
    display: flex;
    align-items: center;
    gap: 6px;
    color: var(--honey);
    font-size: 12px;
    margin: 0;
  }
  .err .dot {
    background: var(--honey);
    width: 6px;
    height: 6px;
    flex: none;
  }
  .hint {
    font-size: 11px;
    color: var(--ink-dim);
    margin: 0;
  }
  .fee-input {
    width: 110px;
    flex: none;
  }
  .submit-row {
    display: flex;
    gap: 10px;
    justify-content: flex-end;
  }
  .card-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
    margin-bottom: 10px;
  }
  .kv {
    margin: 0;
    display: grid;
    grid-template-columns: repeat(2, 1fr);
    gap: 8px 20px;
  }
  .kv > div {
    display: flex;
    justify-content: space-between;
  }
  .kv dt {
    font-size: 12px;
    color: var(--ink-dim);
  }
  .kv dd {
    margin: 0;
    font-size: 13px;
  }
  .txid {
    color: var(--mint);
    margin: 10px 0 0;
    word-break: break-all;
  }
  .errbox {
    margin-top: 10px;
    display: flex;
    align-items: center;
    gap: 10px;
    color: var(--honey);
    font-size: 12px;
    border: 1px solid var(--honey);
    border-radius: var(--r-8);
    padding: 8px 12px;
  }
  .form .errbox {
    margin-top: 0;
  }
  .mini {
    background: none;
    border: 1px solid var(--honey);
    color: var(--honey);
    border-radius: 6px;
    padding: 2px 8px;
    font-size: 11px;
    cursor: pointer;
    margin-left: auto;
    flex: none;
  }
  .preview-actions {
    display: flex;
    gap: 8px;
    margin-top: 14px;
  }
  .raw-row {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .raw-row input {
    font-size: 12px;
  }
  /* modal / 确认弹窗 */
  .modal-veil {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.35);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 50;
  }
  .modal {
    width: min(480px, 92vw);
    display: flex;
    flex-direction: column;
    gap: 12px;
    animation: rise 0.18s ease both;
  }
  .out-list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
    max-height: 160px;
    overflow-y: auto;
  }
  .out-list li {
    display: flex;
    justify-content: space-between;
    gap: 10px;
    font-size: 12px;
    border-bottom: 1px dashed var(--line);
    padding-bottom: 6px;
  }
  .out-list .addr {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    direction: rtl;
    text-align: left;
  }
  .modal-actions {
    display: flex;
    gap: 8px;
    justify-content: flex-end;
  }
</style>
