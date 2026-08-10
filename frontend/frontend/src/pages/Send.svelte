<script lang="ts">
  import { t } from "../lib/i18n";
  import { Services } from "../lib/services";

  let to = $state("");
  let amount = $state("0.50000000");
  let fee = $state("0.00000100");
  let feeMode = $state<"low" | "med" | "high" | "custom">("med");
  let inputs = $state<"auto" | "manual">("auto");
  let change = $state<"new" | "spec">("new");
  let avoidReuse = $state(true);
  let raw = $state("");
  let psbt = $state<{ psbt: string; hex: string; size: number; feeS: number } | null>(null);
  let sending = $state(false);
  let txid = $state<string | null>(null);
  let broadcastErr = $state<string | null>(null);
  let available = 1234.0;

  const amountS = () => parseFloat(amount) || 0;
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

  function validAddr() {
    return to === "" || /^(sugar1q|bc1q|3|1)[a-zA-Z0-9]{10,}$/.test(to.trim());
  }
  function blocked() {
    return amountS() <= 0 || amountS() > available || feeS() >= amountS() || !validAddr() || to.trim() === "";
  }

  async function buildPreview() {
    const r = await Services.buildPsbt(to.trim(), amountS(), feeS());
    psbt = r;
    txid = null;
    broadcastErr = null;
  }

  async function broadcast() {
    sending = true;
    broadcastErr = null;
    try {
      const id = await Services.broadcast(psbt?.hex ?? raw);
      txid = id;
    } catch (e) {
      broadcastErr = String(e);
    } finally {
      sending = false;
    }
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

  <div class="card form">
    <div class="field">
      <label class="field-label" for="to">{t("send.to")}</label>
      <div class="field-row">
        <input
          id="to"
          class:invalid={!validAddr() && to !== ""}
          bind:value={to}
          placeholder="sugar1…"
          autocomplete="off"
          spellcheck="false"
          aria-describedby="to-hint"
        />
        <button class="btn" type="button">{t("g.paste")}</button>
      </div>
      {#if !validAddr() && to !== ""}
        <p class="err" id="to-hint"><span class="dot" aria-hidden="true"></span> invalid address</p>
      {/if}
    </div>

    <div class="field-row split">
      <div class="field">
        <label class="field-label" for="amt">{t("send.amount")}</label>
        <div class="field-row">
          <input id="amt" bind:value={amount} inputmode="decimal" autocomplete="off" />
          <span class="suffix mono">S</span>
        </div>
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
        </div>
      </div>
      {#if amountS() > available}
        <p class="err">{t("send.blocked_insufficient")}</p>
      {/if}
      {#if feeS() >= amountS() && amountS() > 0}
        <p class="err">{t("send.blocked_fee")}</p>
      {/if}
    </div>

    <details class="coin">
      <summary class="coin-sum">
        <span>{t("send.coin_control")}</span>
        <svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true"><polyline points="6 9 12 15 18 9" /></svg>
      </summary>
      <div class="coin-body">
        <div class="field-row">
          <label class="field-label" for="inputs">{t("send.inputs")}</label>
          <select id="inputs" bind:value={inputs}>
            <option value="auto">{t("send.auto_select")}</option>
            <option value="manual">{t("send.manual_utxo")}</option>
          </select>
        </div>
        <div class="field-row">
          <label class="field-label" for="change">{t("send.change_addr")}</label>
          <select id="change" bind:value={change}>
            <option value="new">{t("send.new_addr")}</option>
            <option value="spec">{t("send.custom")}</option>
          </select>
        </div>
        <label class="check">
          <input type="checkbox" bind:checked={avoidReuse} />
          <span>{t("send.avoid_reuse")}</span>
        </label>
      </div>
    </details>

    <div class="submit-row">
      <button class="btn btn-ghost" type="button" onclick={buildPreview}>{t("send.preview")}</button>
      <button class="btn btn-primary" type="button" disabled={blocked() || !txid && false} onclick={broadcast}>{t("send.broadcast")}</button>
    </div>
  </div>

  {#if psbt || txid}
    <div class="card preview">
      <div class="card-head">
        <span class="h-card">{t("send.preview_title")}</span>
        <span class="chip mint"><span class="dot mint" aria-hidden="true"></span> {t("send.sign_state")}</span>
      </div>
      <dl class="kv">
        <div><dt>{t("send.inputs_count")}</dt><dd class="mono">2</dd></div>
        <div><dt>{t("send.outputs_count")}</dt><dd class="mono">2</dd></div>
        <div><dt>{t("send.fee")}</dt><dd class="mono" translate="no">{psbt?.feeS.toFixed(8)} S</dd></div>
        <div><dt>{t("send.size")}</dt><dd class="mono" translate="no">{psbt?.size} B</dd></div>
      </dl>
      {#if txid}
        <p class="txid mono" translate="no">✓ {txid}</p>
      {/if}
      {#if broadcastErr}
        <div class="errbox" role="alert">
          <span>{broadcastErr}</span>
          <button class="mini" onclick={broadcast}>{t("send.resolve")}</button>
        </div>
      {/if}
      <div class="preview-actions">
        <button class="btn" disabled={sending} onclick={broadcast}>
          {#if sending}<span class="spin" aria-hidden="true"></span>{/if}
          {t("send.broadcast")}
        </button>
        <button class="btn btn-ghost">{t("send.copy_hex")}</button>
      </div>
    </div>
  {/if}

  <div class="card raw-row">
    <label class="field-label" for="raw">{t("send.raw_broadcast")}</label>
    <div class="field-row">
      <input id="raw" bind:value={raw} placeholder="02000000…" autocomplete="off" spellcheck="false" class="mono" />
      <button class="btn" onclick={broadcast} disabled={raw.trim() === "" || sending}>{t("send.parse")}</button>
    </div>
  </div>
</section>

<style>
  .send {
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
  .form {
    display: flex;
    flex-direction: column;
    gap: 14px;
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
    color: var(--straw);
    font-size: 12px;
    margin: 0;
  }
  .err .dot {
    background: var(--straw);
    width: 6px;
    height: 6px;
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
  .coin {
    border: 1px solid var(--line);
    border-radius: var(--r-12);
    background: #f0f0f0;
  }
  .coin-sum {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 12px 14px;
    cursor: pointer;
    font-weight: 600;
    font-size: 13px;
    color: var(--ink-fg);
  }
  .coin-sum::-webkit-details-marker {
    display: none;
  }
  .coin-sum svg {
    color: var(--mist);
    transition: transform 0.15s ease;
    margin-left: auto;
  }
  .coin[open] .coin-sum svg {
    transform: rotate(180deg);
  }
  .coin-body {
    display: grid;
    gap: 12px;
    padding: 0 14px 14px;
  }
  .coin-body .field-row {
    display: grid;
    grid-template-columns: 120px 1fr;
  }
  .check {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 13px;
    cursor: pointer;
  }
  .check input {
    accent-color: var(--straw);
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
  }
  .errbox {
    margin-top: 10px;
    display: flex;
    align-items: center;
    gap: 10px;
    color: var(--straw);
    font-size: 12px;
    border: 1px solid var(--straw);
    border-radius: var(--r-8);
    padding: 8px 12px;
  }
  .mini {
    background: none;
    border: 1px solid var(--straw);
    color: var(--straw);
    border-radius: 6px;
    padding: 2px 8px;
    font-size: 11px;
    cursor: pointer;
    margin-left: auto;
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
</style>