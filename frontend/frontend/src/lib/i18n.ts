// ── ini-node i18n: keyed t(), interpolate, Intl, reactive store ──
import en from "../i18n/en.json";
import zhHans from "../i18n/zh-Hans.json";
import zhHant from "../i18n/zh-Hant.json";
import es from "../i18n/es.json";
import de from "../i18n/de.json";
import fr from "../i18n/fr.json";
import it from "../i18n/it.json";
import pt from "../i18n/pt.json";
import ru from "../i18n/ru.json";

export const LANGS = [
  "zh-Hans",
  "zh-Hant",
  "en",
  "es",
  "de",
  "fr",
  "it",
  "pt",
  "ru",
] as const;
export type Lang = (typeof LANGS)[number];

export const LANG_NAMES: Record<Lang, string> = {
  "zh-Hans": "简体中文",
  "zh-Hant": "繁體中文",
  en: "English",
  es: "Español",
  de: "Deutsch",
  fr: "Français",
  it: "Italiano",
  pt: "Português",
  ru: "Русский",
};

type Dict = Record<string, string>;

const ALL: Record<Lang, Dict> = {
  "zh-Hans": zhHans as Dict,
  "zh-Hant": zhHant as Dict,
  en: en as Dict,
  es: es as Dict,
  de: de as Dict,
  fr: fr as Dict,
  it: it as Dict,
  pt: pt as Dict,
  ru: ru as Dict,
};

const cache = new Map<Lang, Dict>();
function load(lang: Lang): Dict {
  if (cache.has(lang)) return cache.get(lang)!;
  cache.set(lang, ALL[lang]);
  return ALL[lang];
}

let current: Lang = "en";
const listeners = new Set<() => void>();

export function getLang(): Lang {
  return current;
}

export function setLang(lang: Lang): void {
  if (lang === current || !LANG_NAMES[lang]) return;
  current = lang;
  for (const l of listeners) l();
  try {
    localStorage.setItem("ini-node.lang", lang);
  } catch {
    /* ignore */
  }
}

export function onLangChange(fn: () => void): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

export function loadLang(): Lang {
  try {
    const saved = localStorage.getItem("ini-node.lang") as Lang | null;
    if (saved && LANG_NAMES[saved]) {
      current = saved;
    } else {
      const nav = navigator.languages?.find((l): l is Lang =>
        LANGS.includes(l as Lang),
      );
      if (!nav) {
        const base = (navigator.language || "").toLowerCase().split("-")[0];
        const hit = LANGS.find((l) => l.toLowerCase().startsWith(base));
        if (hit) current = hit;
      }
    }
  } catch {
    /* keep en */
  }
  return current;
}

/** keyed translate with `{name}` interpolation. Missing key falls back to EN, tagged ⚠untranslated. */
export function t(key: string, vars?: Record<string, string | number>): string {
  const dict = load(current);
  let s: string | undefined;
  if (key in dict) s = dict[key];
  else if (key in en) s = (en as Dict)[key];
  else return `⚠${key}`;
  if (s === undefined) return `⚠${key}`;
  if (vars) {
    for (const [k, v] of Object.entries(vars)) {
      const fmt = typeof v === "number" ? new Intl.NumberFormat(current).format(v) : String(v);
      s = s.split(`{${k}}`).join(fmt);
    }
  }
  return s;
}

const nf = new Map<Lang, Intl.NumberFormat>();
function numberFmt(lang: Lang) {
  if (!nf.has(lang)) nf.set(lang, new Intl.NumberFormat(lang));
  return nf.get(lang)!;
}

/** format a number per locale (e.g. 3346323 → 3,346,323). */
export function fmt(n: number): string {
  return numberFmt(current).format(n);
}

/** bytes → compact (1.05GB). */
export function fmtBytes(n: number): string {
  const units = ["B", "KB", "MB", "GB", "TB"];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v >= 100 ? 0 : v >= 10 ? 1 : 2)}${units[i]}`;
}

/** connection uptime from a unix-seconds connect time → "2d 5h" / "3h 12m" / "4m". */
export function fmtUptime(connTimeUnixSec: number): string {
  if (!connTimeUnixSec) return "—";
  const secs = Math.max(0, Math.floor(Date.now() / 1000 - connTimeUnixSec));
  const d = Math.floor(secs / 86400);
  const h = Math.floor((secs % 86400) / 3600);
  const m = Math.floor((secs % 3600) / 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  return `${Math.max(1, m)}m`;
}

const dtf = new Map<Lang, Intl.DateTimeFormat>();
function dtFmt(lang: Lang) {
  if (!dtf.has(lang)) {
    dtf.set(lang, new Intl.DateTimeFormat(lang, {
      month: "2-digit",
      day: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
    }));
  }
  return dtf.get(lang)!;
}

/** compact date + time, e.g. 08-10 12:03 per locale. */
export function fmtDateTime(ts: number): string {
  return dtFmt(current).format(new Date(ts));
}

/** human relative "3 min ago" etc.; returns ~"x min" for gap text. */
export function fmtAgo(tsMs: number, suffix = true): string {
  const diff = Math.max(0, Date.now() - tsMs);
  const min = Math.floor(diff / 60000);
  if (min < 1) return t("g.seconds", { n: Math.max(1, Math.floor(diff / 1000)) });
  if (min < 60) return suffix ? `${t("g.minutes", { n: min })}` : t("g.minutes", { n: min });
  const h = Math.floor(min / 60);
  if (h < 24) return t("g.hours", { n: h });
  const d = Math.floor(h / 24);
  return t("g.days", { n: d });
}