<script lang="ts">
  import { t } from "../lib/i18n";
  import { navigate } from "../lib/store.svelte";
  import { Services } from "../lib/services";

  // ── state ──────────────────────────────────────────────────────
  // Open-wallet tabs: legacy email/password login (in-memory) or BIP39
  // wallet.db passphrase unlock. WIF/file mock tabs were removed — only
  // methods backed by real RPCs are offered.
  // 打开钱包两个入口:邮箱密码登录(纯内存)或 BIP39 wallet.db 口令解锁。
  // 移除了 WIF/文件 mock 标签页——只提供真实 RPC 支持的方式。
  let openTab = $state<"email" | "pass">("email");
  let email = $state("");
  let emailPass = $state("");
  let pass = $state("");
  let error = $state<string | null>(null);
  let busy = $state(false);

  // Create panel (collapsed by default, like before).
  // 创建面板(默认折叠,与之前一致)。
  let createOpen = $state(false);
  let createPass = $state("");
  let createPass2 = $state("");
  let mnemonic = $state<string[]>([]);
  let backedUp = $state(false);
  let creating = $state(false);
  let newAddr = $state("");
  let mnemonicCopied = $state(false);

  // ── actions ────────────────────────────────────────────────────
  // openWallet: dispatch by tab — walletlogin (legacy KDF, in-memory) or
  // walletpassphrase (BIP39 wallet.db). Both navigate to the wallet page.
  // openWallet: 按标签页分发——walletlogin(传统 KDF,纯内存)或
  // walletpassphrase(BIP39 wallet.db)。成功后进入钱包页。
  async function openWallet() {
    error = null;
    if (openTab === "email") {
      if (!email.includes("@") || emailPass.length < 10) {
        error = t("login.fail");
        return;
      }
    } else if (pass.length < 1) {
      error = t("create.bad_passphrase");
      return;
    }
    busy = true;
    try {
      const ok =
        openTab === "email"
          ? !!(await Services.login(email.trim(), emailPass)).address
          : await Services.unlock(pass);
      if (!ok) {
        error = openTab === "email" ? t("login.fail") : t("create.bad_passphrase");
        return;
      }
      navigate("wallet");
    } catch {
      error = openTab === "email" ? t("login.fail") : t("create.bad_passphrase");
    } finally {
      busy = false;
    }
  }

  // createWallet: real createwallet RPC → the backend generates the BIP39
  // mnemonic (English wordlist) and returns it once. The fake client-side
  // word bank is gone. / createWallet: 真实 createwallet RPC——后端生成
  // BIP39 助记词(英文词表)且仅返回一次。客户端假词库已移除。
  async function createWallet() {
    error = null;
    if (createPass.length < 6 || createPass !== createPass2) {
      error = t("create.pass_mismatch");
      return;
    }
    creating = true;
    try {
      const r = await Services.createWallet(createPass);
      mnemonic = r.mnemonic.trim().split(/\s+/);
      newAddr = r.address;
    } catch (e) {
      error = String(e).replace(/^Error:\s*/, "");
    } finally {
      creating = false;
    }
  }

  function copyMnemonic() {
    if (!mnemonic.length) return;
    navigator.clipboard?.writeText(mnemonic.join(" "));
    mnemonicCopied = true;
    setTimeout(() => (mnemonicCopied = false), 1500);
  }
</script>

<section class="create">
  <div class="head">
    <div>
      <p class="eyebrow">identity</p>
      <h1 class="h-page">{t("create.title")}</h1>
    </div>
    <button class="btn" onclick={() => (createOpen = !createOpen)} aria-expanded={createOpen}>
      <span class="chev" aria-hidden="true">{createOpen ? "▾" : "▸"}</span>
      {t("create.new_wallet")}
    </button>
  </div>

  {#if createOpen}
    <!-- ── create new wallet (real createwallet RPC) ── -->
    <!-- ── 创建新钱包(真实 createwallet RPC) ── -->
    <div class="card auth-card">
      <h2 class="h-card">{t("create.new_wallet")}</h2>
      <form onsubmit={(e) => { e.preventDefault(); createWallet(); }}>
        <div class="field">
          <label class="field-label" for="cp1">{t("g.password")}</label>
          <input id="cp1" type="password" bind:value={createPass} autocomplete="new-password" />
        </div>
        <div class="field">
          <label class="field-label" for="cp2">{t("create.pass_repeat")}</label>
          <input id="cp2" type="password" bind:value={createPass2} autocomplete="new-password" />
        </div>
        {#if error}
          <p class="err" role="alert"><span class="dot" aria-hidden="true"></span>{error}</p>
        {/if}
        <button class="btn btn-primary wide" type="submit" disabled={creating}>
          {#if creating}<span class="spin" aria-hidden="true"></span>{/if}
          {t("create.generate")}
        </button>
      </form>

      {#if mnemonic.length > 0}
        <div class="mnemonic">
          <div class="mnemonic-head">
            <p class="mnemonic-title">{t("create.mnemonic_panel")}</p>
            <button class="mini" onclick={copyMnemonic}>
              {mnemonicCopied ? "✓" : t("g.copy")}
            </button>
          </div>
          <div class="words">
            {#each mnemonic as w, i}
              <span class="word" title={w}>
                <span class="idx mono">{i + 1}</span>
                {w}
              </span>
            {/each}
          </div>
          <label class="check">
            <input type="checkbox" bind:checked={backedUp} />
            <span>{t("create.backup_confirm")}</span>
          </label>
          <button class="btn btn-primary wide" disabled={!backedUp} onclick={() => navigate("wallet")}>
            {t("create.open_btn")} →
          </button>
        </div>
      {/if}
    </div>
  {:else}
    <!-- ── open existing wallet ── -->
    <!-- ── 打开已有钱包 ── -->
    <div class="card auth-card">
      <div class="tabs" role="tablist">
        <button class="tab" class:active={openTab === "email"} role="tab" aria-selected={openTab === "email"} onclick={() => (openTab = "email")}>{t("create.tab_email")}</button>
        <button class="tab" class:active={openTab === "pass"} role="tab" aria-selected={openTab === "pass"} onclick={() => (openTab = "pass")}>{t("create.tab_passphrase")}</button>
      </div>

      <form onsubmit={(e) => { e.preventDefault(); openWallet(); }}>
        {#if openTab === "email"}
          <div class="field">
            <label class="field-label" for="lemail">{t("login.email")}</label>
            <input id="lemail" type="email" bind:value={email} placeholder="name@example.com" autocomplete="username" spellcheck="false" />
          </div>
          <div class="field">
            <label class="field-label" for="lpass">{t("login.password")}</label>
            <input id="lpass" type="password" bind:value={emailPass} autocomplete="current-password" />
          </div>
          <p class="hint">{t("login.hint")}</p>
        {:else}
          <div class="field">
            <label class="field-label" for="wpass">{t("g.password")}</label>
            <input id="wpass" type="password" bind:value={pass} autocomplete="off" />
          </div>
          <p class="hint">{t("create.pass_hint")}</p>
        {/if}

        {#if error}
          <p class="err" role="alert"><span class="dot" aria-hidden="true"></span>{error}</p>
        {/if}

        <button class="btn btn-primary wide" type="submit" disabled={busy}>
          {#if busy}<span class="spin" aria-hidden="true"></span>{/if}
          {openTab === "email" ? t("login.btn") : t("create.open")}
        </button>
      </form>
    </div>
  {/if}
</section>

<style>
  .create {
    display: flex;
    flex-direction: column;
    gap: 16px;
    /* Narrow centered column: this page is an identity form, not a dashboard.
       狭窄居中布局:本页是身份表单而非仪表盘。 */
    max-width: 440px;
    margin: 24px auto 0;
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
    .create {
      animation: none;
    }
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
  .chev {
    display: inline-block;
    width: 12px;
  }
  .auth-card {
    display: flex;
    flex-direction: column;
    gap: 12px;
    padding: 18px;
  }
  .field {
    display: flex;
    flex-direction: column;
    gap: 5px;
  }
  .wide {
    width: 100%;
    justify-content: center;
  }

  .mnemonic {
    margin-top: 6px;
    background: #fff;
    border: 1px solid var(--line);
    border-radius: var(--r-12);
    padding: 14px;
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .mnemonic-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
  }
  .mnemonic-title {
    margin: 0;
    font-size: 12px;
    color: var(--honey);
    font-weight: 700;
  }
  .words {
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    gap: 7px;
    user-select: none;
  }
  .word {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 12px;
    padding: 5px 7px;
    border-radius: 6px;
    background: var(--violet);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .idx {
    color: var(--mist);
    font-size: 10px;
    flex: none;
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

  .tabs {
    display: flex;
    gap: 4px;
    border-bottom: 1px solid var(--line);
    flex-wrap: wrap;
    margin-bottom: 4px;
  }
  .tab {
    background: none;
    border: none;
    color: var(--ink-dim);
    padding: 7px 12px;
    font-size: 12px;
    font-weight: 600;
    cursor: pointer;
    border-radius: var(--r-8) var(--r-8) 0 0;
  }
  .tab:hover {
    color: var(--ink-fg);
    background: var(--violet-2);
  }
  .tab.active {
    color: var(--straw);
    box-shadow: inset 0 -2px 0 var(--straw);
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
  .hint {
    font-size: 11px;
    color: var(--ink-dim);
    margin: 0;
    line-height: 1.45;
  }
</style>
