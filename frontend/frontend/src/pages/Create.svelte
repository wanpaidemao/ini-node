<script lang="ts">
  import { t } from "../lib/i18n";
  import { navigate } from "../lib/store.svelte";

  let name = $state("main");
  let type = $state<"hd" | "wif">("hd");
  let lang = $state("en");
  let words = $state<12 | 24>(12);
  let mnemonic = $state<string[]>([]);
  let backedUp = $state(false);
  let openTab = $state<"wif" | "mnemonic" | "file">("wif");
  let wif = $state("");
  let pass = $state("");
  let saved = $state(["main", "savings.wallet", "watch-only"]);
  let error = $state<string | null>(null);

  const wordBank = [
    "abandon", "ability", "able", "about", "above", "absent", "absorb", "abstract", "absurd", "abuse",
    "access", "accident", "account", "accuse", "achieve", "acid", "acoustic", "acquire", "across", "act",
    "action", "actor", "actress", "actual", "adapt", "add", "addict", "address", "adjust", "admit",
    "adult", "advance", "advice", "aerobic", "affair", "afford", "afraid", "again", "age", "agent",
    "agree", "ahead", "aim", "air", "airport", "aisle", "alarm", "album", "alcohol", "alert",
    "alien", "all", "alley", "allow", "almost", "alone", "alpha", "already", "also", "alter",
    "always", "amateur", "amazing", "among", "amount", "amused", "analyst", "anchor", "ancient", "anger",
    "angle", "angry", "animal", "ankle", "announce", "annual", "another", "answer", "antenna", "antique",
    "anxiety", "any", "apart", "apology", "appear", "apple", "approve", "april", "arch", "arctic",
    "area", "arena", "argue", "arm", "armed", "armor", "army", "around", "arrange", "arrest",
    "arrive", "arrow", "art", "artefact", "artist", "artwork", "ask", "aspect", "assault", "asset",
  ];

  function gen() {
    const shuffled = [...wordBank].sort(() => 0.5 - Math.random());
    mnemonic = shuffled.slice(0, words);
    backedUp = false;
    error = null;
  }

  function openWallet() {
    if (openTab === "wif" && !/^([Ss]|5)[a-zA-Z0-9]{20,}$/.test(wif.trim())) {
      error = t("create.bad_passphrase");
      return;
    }
    if (openTab === "mnemonic" && pass.trim().length < 4) {
      error = t("create.bad_passphrase");
      return;
    }
    error = null;
    navigate("wallet");
  }

  const langs = ["en", "zh-Hans", "zh-Hant", "es", "de", "fr", "it", "pt", "ru"];
</script>

<section class="create">
  <div class="head">
    <div>
      <p class="eyebrow">identity</p>
      <h1 class="h-page">{t("create.title")}</h1>
    </div>
  </div>

  <div class="two">
    <!-- create -->
    <div class="card">
      <h2 class="h-card">{t("create.new_wallet")}</h2>
      <div class="field">
        <label class="field-label" for="wname">{t("create.name")}</label>
        <input id="wname" bind:value={name} placeholder="main" autocomplete="off" />
      </div>
      <div class="field">
        <label class="field-label" for="wtype">{t("create.type")}</label>
        <select id="wtype" bind:value={type}>
          <option value="hd">{t("create.hd_seed")}</option>
          <option value="wif">WIF</option>
        </select>
      </div>
      {#if type === "hd"}
        <div class="field-row">
          <div class="field">
            <label class="field-label" for="wlang">{t("create.language")}</label>
            <select id="wlang" bind:value={lang}>
              {#each langs as l}<option value={l}>{l}</option>{/each}
            </select>
          </div>
          <div class="field">
            <label class="field-label" for="wcount">{t("create.word_count")}</label>
            <select id="wcount" bind:value={words}>
              <option value={12}>12</option>
              <option value={24}>24</option>
            </select>
          </div>
        </div>
        <button class="btn btn-primary" onclick={gen}>{t("create.generate")}</button>

        {#if mnemonic.length > 0}
          <div class="mnemonic glass">
            <p class="mnemonic-title">{t("create.mnemonic_panel")}</p>
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
            <button class="btn btn-primary" disabled={!backedUp} onclick={() => navigate("wallet")}>
              {t("create.open_btn")} →
            </button>
          </div>
        {/if}
      {/if}
    </div>

    <!-- open -->
    <div class="card">
      <h2 class="h-card">{t("create.open_wallet")}</h2>
      <div class="tabs" role="tablist">
        <button class="tab" class:active={openTab === "wif"} role="tab" aria-selected={openTab === "wif"} onclick={() => (openTab = "wif")}>{t("create.tab_wif")}</button>
        <button class="tab" class:active={openTab === "mnemonic"} role="tab" aria-selected={openTab === "mnemonic"} onclick={() => (openTab = "mnemonic")}>{t("create.tab_mnemonic")}</button>
        <button class="tab" class:active={openTab === "file"} role="tab" aria-selected={openTab === "file"} onclick={() => (openTab = "file")}>{t("create.tab_file")}</button>
      </div>

      {#if openTab === "wif"}
        <div class="field">
          <label class="field-label" for="wif">{t("create.wif")}</label>
          <input id="wif" bind:value={wif} placeholder="S… 或 5…" autocomplete="off" spellcheck="false" class="mono" />
        </div>
      {:else if openTab === "mnemonic"}
        <div class="field">
          <label class="field-label" for="npass">{t("g.password")}</label>
          <input id="npass" type="password" bind:value={pass} autocomplete="off" />
        </div>
      {:else}
        <div class="field">
          <label class="field-label" for="fpass">{t("g.password")}</label>
          <input id="fpass" type="password" bind:value={pass} autocomplete="off" />
        </div>
      {/if}

      {#if error}
        <p class="err" role="alert"><span class="dot" aria-hidden="true"></span>{error}</p>
      {/if}

      <button class="btn btn-primary" onclick={openWallet}>{t("create.open")}</button>
      {#if openTab === "mnemonic"}
        <p class="hint">{t("create.language")}: {lang}</p>
      {/if}

      <hr class="divider" />
      <p class="eyebrow">{t("create.saved_list")}</p>
      <ul class="wallet-list">
        {#each saved as s}
          <li class="wallet-li">
            <span class="mono">{s}</span>
            <span class="li-actions">
              <button class="mini" onclick={() => navigate("wallet")}>{t("create.open_btn")}</button>
              <button class="mini danger" onclick={() => (saved = saved.filter((x) => x !== s))}>{t("create.delete")}</button>
            </span>
          </li>
        {/each}
      </ul>
    </div>
  </div>
</section>

<style>
  .create {
    display: flex;
    flex-direction: column;
    gap: 16px;
    max-width: 1080px;
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
    margin: 0 0 14px;
  }
  .two {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 14px;
    align-items: start;
  }
  .field {
    display: flex;
    flex-direction: column;
    gap: 5px;
    margin-bottom: 12px;
  }
  .field-row {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 12px;
    margin-bottom: 12px;
  }

  .mnemonic {
    margin-top: 14px;
    background: #f0f0f0;
    border: 1px solid var(--line);
    border-radius: var(--r-12);
    padding: 14px;
    display: flex;
    flex-direction: column;
    gap: 12px;
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
    background: #e5e5e5;
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

  .tabs {
    display: flex;
    gap: 4px;
    margin-bottom: 12px;
    border-bottom: 1px solid var(--line);
    flex-wrap: wrap;
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
    background: #eee;
  }
  .tab.active {
    color: var(--straw);
    box-shadow: inset 0 -2px 0 var(--straw);
  }

  .err {
    display: flex;
    align-items: center;
    gap: 7px;
    color: var(--straw);
    font-size: 12px;
    margin: 0 0 10px;
  }
  .err .dot {
    background: var(--straw);
    width: 6px;
    height: 6px;
    border-radius: 50%;
  }
  .hint {
    font-size: 11px;
    color: var(--ink-dim);
    margin: 8px 0 0;
  }
  .divider {
    border: none;
    border-top: 1px dashed var(--line);
    margin: 16px 0;
  }
  .wallet-list {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .wallet-li {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    padding: 8px 2px;
    font-size: 13px;
  }
  .wallet-li + .wallet-li {
    border-top: 1px dashed var(--line);
  }
  .li-actions {
    display: flex;
    gap: 6px;
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
  .mini.danger {
    color: var(--straw);
    border-color: var(--straw);
  }

  @media (max-width: 880px) {
    .two {
      grid-template-columns: 1fr;
    }
  }
</style>