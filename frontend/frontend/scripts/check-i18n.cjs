const fs = require('fs');
const path = require('path');
const p = path.join(__dirname, '..', 'src', 'i18n');
const list = ['en','zh-Hans','zh-Hant','es','de','fr','it','pt','ru'];
let baseKeys = null;
for (const f of list) {
  const fp = path.join(p, f + '.json');
  try {
    const j = JSON.parse(fs.readFileSync(fp, 'utf8'));
    const keys = Object.keys(j);
    if (!baseKeys) baseKeys = keys;
    const missing = baseKeys.filter(k => !(k in j));
    const extra = keys.filter(k => !baseKeys.includes(k));
    console.log(`${f}: ${keys.length} keys | lang=${j['lang.name']} | missing=${missing.length} extra=${extra.length}`);
  } catch (e) {
    console.log(`${f}: ERROR ${e.message}`);
  }
}