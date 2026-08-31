<script lang="ts">
  import { onMount } from "svelte";
  import { t } from "../lib/i18n";
  import { navigate } from "../lib/store.svelte";
  import { Services } from "../lib/services";
  import { registerWallet, removeWallet, walletRegistry } from "../lib/wallet-registry.svelte";

  // ── state ──────────────────────────────────────────────────────
  // Open-wallet tabs: legacy email/password login (in-memory), BIP39
  // wallet.db passphrase unlock, or WIF private key import (Step 11,
  // in-memory hybrid — index 0 is the imported key's web-wallet address).
  // 打开钱包三个入口:邮箱密码登录(纯内存)、BIP39 wallet.db 口令解锁、
  // 或 WIF 私钥导入(第 11 步,纯内存混合——index 0 即导入私钥的
  // web 钱包地址)。
  let openTab = $state<"email" | "pass" | "wif" | "mnemonic">("email");
  let email = $state("");
  let emailPass = $state("");
  let pass = $state("");
  let wifKey = $state("");
  let walletName = $state("");
  let restoreMnemonic = $state("");
  let restorePass = $state("");
  let walletNames = $state<string[]>([]);
  let error = $state<string | null>(null);
  let busy = $state(false);

  // Selected saved profile (one-click reopen): clicking a card fills the
  // email field and switches to the matching tab so only the password
  // remains to type. HD profiles switch to the passphrase tab.
  // 选中的已保存档案(一键重开):点击卡片自动填充邮箱并切到对应标签页,
  // 只剩密码需要输入。HD 档案则切到口令标签页。
  let selectedId = $state<string | null>(null);

  function pickProfile(id: string) {
    selectedId = id;
    const p = walletRegistry.find((w) => w.id === id);
    if (!p) return;
    if (p.type === "web" && p.email) {
      openTab = "email";
      email = p.email;
    } else if (p.type === "web") {
      // WIF import profile (web type, no email) → WIF tab.
      // WIF 导入档案(web 类,无邮箱)→ WIF 标签页。
      openTab = "wif";
    } else {
      openTab = "pass";
      walletName = p.walletName ?? p.name ?? "default";
    }
  }

  // Create panel (collapsed by default, like before).
  // 创建面板(默认折叠,与之前一致)。
  let createOpen = $state(false);
  let createName = $state("");
  let createPass = $state("");
  let createPass2 = $state("");
  let mnemonic = $state<string[]>([]);
  let backedUp = $state(false);
  let creating = $state(false);
  let newAddr = $state("");
  let mnemonicCopied = $state(false);

  // Load the existing BIP39 wallet names for the open/restore picker hint.
  // 加载已有 BIP39 钱包名,供打开/恢复时提示。
  onMount(async () => {
    try {
      walletNames = await Services.listWallets();
    } catch {
      walletNames = [];
    }
  });

  // ── actions ────────────────────────────────────────────────────
  // openWallet: dispatch by tab — legacy KDF login (in-memory), BIP39
  // wallet.db passphrase unlock, or WIF import (Step 11). All navigate to
  // the wallet page on success.
  // NOTE: legacy login is deterministic derivation — any email/password
  // derives a wallet, so there is no "wrong password" at the KDF level.
  // Real failures (e.g. "already unlocked") are shown verbatim instead of
  // the old misleading generic message.
  // openWallet: 按标签页分发——传统 KDF 登录(纯内存)、BIP39 wallet.db
  // 口令解锁或 WIF 导入(第 11 步)。成功后进入钱包页。注意:传统登录是
  // 确定性派生——任意邮箱密码都能派生钱包,KDF 层面不存在"密码错误"。
  // 真实错误(如"已解锁")原样展示,不再显示误导性的笼统文案。
  async function openWallet() {
    error = null;
    // Only basic sanity here: the web wallet has NO minimum password
    // length, so a short (but non-empty) password must not be rejected.
    // 此处仅做基本检查:web 钱包没有密码最小长度限制,短(但非空)密码
    // 不应被拒绝。
    if (openTab === "email") {
      if (!email.includes("@") || emailPass.length < 1) {
        error = t("login.fail");
        return;
      }
    } else if (openTab === "wif") {
      if (wifKey.trim().length < 20) {
        error = t("login.bad_wif");
        return;
      }
    } else if (openTab === "mnemonic") {
      if (restoreMnemonic.trim().length < 11 || restorePass.length < 1) {
        error = t("create.bad_passphrase");
        return;
      }
    } else if (pass.length < 1) {
      error = t("create.bad_passphrase");
      return;
    }
    busy = true;
    try {
      if (openTab === "email") {
        const ok = !!(await Services.login(email.trim(), emailPass)).address;
        if (!ok) {
          error = t("login.fail");
          return;
        }
      } else if (openTab === "wif") {
        // WIF import: the binding validates the key and derives the
        // hybrid wallet in memory; errors (bad checksum etc.) surface
        // verbatim. / WIF 导入:binding 校验私钥并在内存中派生混合
        // 钱包;错误(校验和错误等)原样上浮。
        await Services.loginWIF(wifKey.trim());
      } else if (openTab === "mnemonic") {
        // Restore from mnemonic: the binding validates the phrase, derives
        // and persists the wallet under walletName, then unlocks it.
        // 助记词恢复:binding 校验助记词,按 walletName 派生并持久化钱包并解锁。
        await Services.restoreWallet(walletName, restoreMnemonic.trim(), restorePass);
      } else {
        const ok = await Services.unlock(walletName, pass);
        if (!ok) {
          error = t("create.bad_passphrase");
          return;
        }
      }
      // Register the opened wallet in the saved-profiles list (metadata
      // only — password never stored). / 登记到已保存档案列表(仅元数据,
      // 密码永不存储)。
      const st = await Services.getWallet();
      if (openTab === "email") {
        registerWallet({ type: "web", email: email.trim(), address: st.address ?? undefined });
      } else if (openTab === "wif") {
        registerWallet({ type: "web", name: "WIF import", address: st.address ?? undefined });
      } else {
        const nm = walletName.trim() || "default";
        registerWallet({ type: "hd", walletName: nm, name: nm, address: st.address ?? undefined });
      }
      wifKey = ""; // never keep the key in the field / 字段绝不保留私钥
      restoreMnemonic = ""; // never keep the phrase in the field / 字段绝不保留助记词
      restorePass = "";
      navigate("wallet");
    } catch (e) {
      // Surface the real cause: "wallet is already unlocked", binding
      // unavailable (plain browser instead of the Wails window), etc.
      // 显示真实原因:"钱包已解锁"、binding 不可用(普通浏览器而非
      // Wails 窗口)等。
      error = String(e).replace(/^Error:\s*/, "");
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
      const r = await Services.createWallet(createName, createPass);
      mnemonic = r.mnemonic.trim().split(/\s+/);
      newAddr = r.address;
      // New HD wallet goes straight into the saved list. / 新建 HD 钱包
      // 直接进入已保存列表。
      registerWallet({ type: "hd", walletName: r.name, name: r.name, address: r.address });
      walletNames = await Services.listWallets().catch(() => walletNames);
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
          <label class="field-label" for="cname">{t("create.wallet_name")}</label>
          <input id="cname" type="text" bind:value={createName} placeholder="default" autocomplete="off" spellcheck="false" />
        </div>
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
    <!-- ── saved wallet profiles: one-click reopen (metadata only) ── -->
    <!-- ── 已保存钱包档案:一键重开(仅元数据,密码永不存储) ── -->
    {#if walletRegistry.length > 0}
      <div class="card profile-card" aria-label={t("create.saved_wallets")}>
        <p class="profile-title">{t("create.saved_wallets")}</p>
        <div class="profiles">
          {#each walletRegistry as p (p.id)}
            <button
              class="profile"
              class:selected={selectedId === p.id}
              onclick={() => pickProfile(p.id)}
              title={p.address ?? p.email ?? p.name}
            >
              <span class="badge" class:hd={p.type === "hd"}>{p.type === "web" ? "web" : "HD"}</span>
              <span class="pname">{p.name}</span>
              <span class="paddr mono" translate="no">{p.address ?? "—"}</span>
              <span
                class="del"
                role="button"
                tabindex="-1"
                aria-label={t("create.forget")}
                onclick={(e) => { e.stopPropagation(); removeWallet(p.id); if (selectedId === p.id) selectedId = null; }}
                onkeydown={(e) => { if (e.key === "Enter") { e.stopPropagation(); removeWallet(p.id); } }}
              >✕</span>
            </button>
          {/each}
        </div>
      </div>
    {/if}

    <!-- ── open existing wallet ── -->
    <!-- ── 打开已有钱包 ── -->
    <div class="card auth-card">
      <div class="tabs" role="tablist">
        <button class="tab" class:active={openTab === "email"} role="tab" aria-selected={openTab === "email"} onclick={() => (openTab = "email")}>{t("create.tab_email")}</button>
        <button class="tab" class:active={openTab === "pass"} role="tab" aria-selected={openTab === "pass"} onclick={() => (openTab = "pass")}>{t("create.tab_passphrase")}</button>
        <button class="tab" class:active={openTab === "mnemonic"} role="tab" aria-selected={openTab === "mnemonic"} onclick={() => (openTab = "mnemonic")}>{t("create.tab_mnemonic")}</button>
        <!-- Step 11: WIF private key import / 第 11 步:WIF 私钥导入 -->
        <button class="tab" class:active={openTab === "wif"} role="tab" aria-selected={openTab === "wif"} onclick={() => (openTab = "wif")}>{t("create.tab_wif")}</button>
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
        {:else if openTab === "wif"}
          <!-- Step 11: import a WIF private key — in-memory hybrid wallet,
               index 0 is the imported key's web-wallet address so funds
               restore exactly. Never written to disk. -->
          <!-- 第 11 步:导入 WIF 私钥——纯内存混合钱包,index 0 即导入
               私钥的 web 钱包地址,资产精确恢复。绝不写入磁盘。 -->
          <div class="field">
            <label class="field-label" for="wifkey">{t("create.wif_label")}</label>
            <input
              id="wifkey"
              class="mono-input"
              type="password"
              bind:value={wifKey}
              placeholder="WIF private key / WIF 私钥"
              autocomplete="off"
              spellcheck="false"
              translate="no"
            />
          </div>
          <p class="hint">{t("create.wif_hint")}</p>
        {:else if openTab === "mnemonic"}
          <div class="field">
            <label class="field-label" for="mname">{t("create.wallet_name")}</label>
            <input id="mname" type="text" bind:value={walletName} placeholder="default" autocomplete="off" spellcheck="false" />
          </div>
          <div class="field">
            <label class="field-label" for="mnemonic">{t("create.mnemonic_label")}</label>
            <textarea id="mnemonic" class="mono-input" bind:value={restoreMnemonic} rows="3" placeholder="word1 word2 … word12" autocomplete="off" spellcheck="false" translate="no"></textarea>
          </div>
          <div class="field">
            <label class="field-label" for="mrestorepass">{t("g.password")}</label>
            <input id="mrestorepass" type="password" bind:value={restorePass} autocomplete="new-password" />
          </div>
        {:else}
          <div class="field">
            <label class="field-label" for="wname">{t("create.wallet_name")}</label>
            <input id="wname" type="text" bind:value={walletName} placeholder="default" autocomplete="off" spellcheck="false" />
          </div>
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
          {openTab === "email" ? t("login.btn") : openTab === "wif" ? t("create.wif_btn") : openTab === "mnemonic" ? t("create.restore_btn") : t("create.open")}
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

  /* Saved wallet profiles: compact rows, original web-wallet list style.
     已保存钱包档案:紧凑行,原版 web 钱包列表风格。 */
  .profile-card {
    padding: 14px 16px;
  }
  .profile-title {
    margin: 0 0 10px;
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--ink-dim);
    font-weight: 700;
  }
  .profiles {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .profile {
    display: grid;
    grid-template-columns: auto 1fr auto auto;
    align-items: center;
    gap: 10px;
    width: 100%;
    text-align: left;
    background: var(--violet);
    border: 1px solid var(--line);
    border-radius: var(--r-8);
    padding: 8px 10px;
    cursor: pointer;
    font-size: 12px;
  }
  .profile:hover {
    border-color: var(--straw);
  }
  .profile.selected {
    border-color: var(--straw);
    box-shadow: 0 0 0 1px var(--straw);
  }
  .badge {
    font-size: 9px;
    font-weight: 800;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    padding: 2px 6px;
    border-radius: 5px;
    background: var(--straw);
    color: #fff;
    flex: none;
  }
  .badge.hd {
    background: var(--honey);
  }
  .pname {
    font-weight: 600;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .paddr {
    color: var(--ink-dim);
    font-size: 11px;
    max-width: 130px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .del {
    color: var(--ink-dim);
    font-size: 11px;
    padding: 2px 5px;
    border-radius: 5px;
    flex: none;
  }
  .del:hover {
    color: var(--honey);
    background: #fff;
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
  /* WIF key field: monospace, no wrap / WIF 私钥输入:等宽字体 */
  .mono-input {
    font-family: var(--font-mono);
    font-size: 12px;
  }
</style>
