import { defineConfig, type Plugin } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import wails from "@wailsio/runtime/plugins/vite";
import { request as httpRequest } from "http";
import { readFileSync, writeFileSync, existsSync } from "fs";
import { resolve, dirname, isAbsolute } from "path";

// ── node RPC proxy ──────────────────────────────────────────────
// btcd listens on 127.0.0.1:8334 (HTTP, --notls). Credentials live only in
// backend/btcd-runtime.ini; this dev proxy reads them once and injects the
// Basic auth header, so no secrets are baked into frontend code.
const INI_PATH = resolve(import.meta.dirname, "../../backend/btcd-runtime.ini");
const RPC_TARGET = "http://127.0.0.1:8334/";

interface IniOpts {
  rpcuser?: string;
  rpcpass?: string;
  rpclisten?: string;
  logdir?: string;
  debuglevel?: string;
  loglevel?: string;
}

function parseIni(path: string): IniOpts {
  const out: IniOpts = {};
  try {
    for (const line of readFileSync(path, "utf8").split(/\r?\n/)) {
      const m = line.match(/^\s*([a-zA-Z0-9]+)\s*=\s*(.*)\s*$/);
      if (m && m[1] in out === false) (out as Record<string, string>)[m[1]] = m[2];
    }
  } catch {
    /* ini missing → leave defaults; UI will show not-connected */
  }
  return out;
}

function rpcTarget() {
  const opts = parseIni(INI_PATH);
  const user = opts.rpcuser ?? "ini";
  const pass = opts.rpcpass ?? "ini";
  const auth = "Basic " + Buffer.from(`${user}:${pass}`).toString("base64");
  const target = new URL(/^https?:\/\//.test(opts.rpclisten ?? "") ? opts.rpclisten! : `http://${opts.rpclisten ?? "127.0.0.1:8334"}`);
  return {
    hostname: target.hostname || "127.0.0.1",
    port: target.port || "8334",
    auth,
  };
}

function rpcProxyPlugin(): Plugin {
  return {
    name: "ini-node-rpc-proxy",
    configureServer(server) {
      server.middlewares.use("/rpc", (req, res) => {
        const { hostname, port, auth } = rpcTarget();
        const chunks: Buffer[] = [];
        req.on("data", (c) => chunks.push(c as Buffer));
        req.on("end", () => {
          const body = Buffer.concat(chunks);
          const preq = httpRequest(
            {
              hostname,
              port: Number(port),
              path: "/",
              method: req.method ?? "POST",
              headers: {
                ...req.headers,
                authorization: auth,
                "content-type": "application/json",
                host: `${hostname}:${port}`,
              },
            },
            (pres) => {
              res.writeHead(pres.statusCode ?? 500, pres.headers);
              pres.pipe(res);
            },
          );
          preq.on("error", (e) => {
            res.writeHead(502, { "content-type": "application/json" });
            res.end(JSON.stringify({ jsonrpc: "1.0", error: { code: -32603, message: String(e.message) }, result: null, id: null }));
          });
          preq.write(body);
          preq.end();
        });
      });
      // expose parsed creds/endpoint for Settings without leaking the password
      server.middlewares.use("/api/node-config", (req, res) => {
        const o = parseIni(INI_PATH);
        res.setHeader("content-type", "application/json");
        // Frontend-only client settings (logredirect, future UI flags) live
        // in frontend.ini NEXT TO the runtime ini — never inside it, because
        // the ini doubles as btcd's --configfile and btcd strictly rejects
        // unknown keys (which would kill the node at startup).
        // 前端客户端专属设置(logredirect 及未来 UI 开关)统一存 runtime ini
        // 旁边的 frontend.ini——绝不写进 ini 本身(ini 是 btcd 的
        // --configfile,未知键会导致节点拒启)。
        const FRONTEND_INI = resolve(dirname(INI_PATH), "frontend.ini");
        // mergeIniKey writes key=value into the given ini, preserving other
        // lines; the key is appended when absent. / 合并写入 key=value,保留
        // 其他行;键不存在则追加。
        const mergeIniKey = (path: string, key: string, value: string) => {
          const existing = existsSync(path) ? readFileSync(path, "utf8") : "";
          const keyRe = new RegExp(`^${key}\\s*=`, "m");
          const out = existing.split(/\r?\n/).map((l) => (keyRe.test(l) ? `${key}=${value}` : l));
          const has = out.some((l) => keyRe.test(l));
          const final = (has ? out.join("\n") : [...out, `${key}=${value}`].join("\n")).replace(/\n+$/, "") + "\n";
          writeFileSync(path, final);
        };
        if (req.method === "POST") {
          const chunks: Buffer[] = [];
          req.on("data", (c) => chunks.push(c as Buffer));
          req.on("end", () => {
            try {
              const body = JSON.parse(Buffer.concat(chunks).toString("utf8"));
              const lines: string[] = [];
              if (typeof body.rpcuser === "string") lines.push(`rpcuser=${body.rpcuser}`);
              if (typeof body.rpcpass === "string") lines.push(`rpcpass=${body.rpcpass}`);
              if (typeof body.logredirect === "string") {
                try { mergeIniKey(FRONTEND_INI, "logredirect", body.logredirect.trim()); } catch { /* ignore */ }
              }
              if (typeof body.rpcendpoint === "string" && /^https?:\/\/.+/.test(body.rpcendpoint)) {
                const u = new URL(body.rpcendpoint);
                lines.push(`rpclisten=${u.host}`);
              }
              if (lines.length) {
                const existing = existsSync(INI_PATH) ? readFileSync(INI_PATH, "utf8") : "";
                const keyRe = /^([a-zA-Z0-9]+)\s*=/;
                const updates = new Map(lines.map((l) => {
                  const m = l.match(keyRe);
                  return [m ? m[1] : "", l] as const;
                }));
                const out = existing
                  .split(/\r?\n/)
                  .map((l) => {
                    const m = l.match(keyRe);
                    if (m && updates.has(m[1])) {
                      const v = updates.get(m[1])!;
                      updates.delete(m[1]);
                      return v;
                    }
                    return l;
                  })
                  .concat([...updates.values()]);
                writeFileSync(INI_PATH, out.join("\n").replace(/\n+$/, "") + "\n");
              }
            } catch {
              /* ignore malformed body */
            }
            res.end(JSON.stringify({ ok: true }));
          });
          return;
        }
        res.end(
          JSON.stringify({
            rpcEndpoint: o.rpclisten ? `http://${o.rpclisten}` : "http://127.0.0.1:8334",
            rpcUser: o.rpcuser ?? "",
            rpcPass: o.rpcpass ?? "",
            credFromIni: true,
            // Frontend-only client settings from frontend.ini (not the node
            // ini, which btcd parses strictly). / 前端客户端专属设置读
            // frontend.ini(不读节点 ini,btcd 严格解析)。
            ...parseIni(FRONTEND_INI),
          }),
        );
      });
      // /api/logs — tail the node stdout log (GET ?lines=N) or clear it (POST).
      // Mirrors frontend/rpcproxy.go handleLogs so the dev panel matches the
      // packaged build.  /api/logs 与 rpcproxy 的 handleLogs 行为一致。
      server.middlewares.use("/api/logs", (req, res) => {
        const o = parseIni(INI_PATH);
        let logDir = resolve(dirname(INI_PATH), "logs");
        if (o.logdir) logDir = isAbsolute(o.logdir) ? o.logdir : resolve(dirname(INI_PATH), o.logdir);
        const logPath = resolve(logDir, "node.stdout.log");
        if (req.method === "POST") {
          try {
            writeFileSync(logPath, "");
          } catch {
            /* ignore */
          }
          res.setHeader("content-type", "application/json");
          res.end(JSON.stringify({ ok: true }));
          return;
        }
        const q = new URL(req.url ?? "/", "http://localhost").searchParams.get("lines");
        let lines = 200;
        if (q && /^\d+$/.test(q)) lines = Math.max(1, parseInt(q, 10));
        let all: string[] = [];
        try {
          const data = readFileSync(logPath, "utf8");
          all = data.replace(/\n$/, "").split("\n");
        } catch {
          /* missing file → empty list */
        }
        if (all.length > lines) all = all.slice(all.length - lines);
        res.setHeader("content-type", "application/json");
        res.end(JSON.stringify({ ok: true, lines: all }));
      });
      // /api/loglevel — read (GET) or set (POST) the node log level.  GET asks
      // btcd's debuglevel get RPC for the live level; POST applies the level
      // and persists it into the runtime ini (debuglevel=).  Same semantics as
      // frontend/rpcproxy.go handleLoglevel.  /api/loglevel 与 rpcproxy 的
      // handleLoglevel 语义一致：GET 查实际级别,POST 设置并回写 ini。
      server.middlewares.use("/api/loglevel", (req, res) => {
        const { hostname, port, auth } = rpcTarget();
        const rpcCall = (spec: string) =>
          new Promise<string>((resolveRpc, rejectRpc) => {
            const body = JSON.stringify({
              jsonrpc: "1.0",
              id: "loglevel",
              method: "debuglevel",
              params: [spec],
            });
            const preq = httpRequest(
              {
                hostname,
                port: Number(port),
                path: "/",
                method: "POST",
                headers: {
                  "content-type": "application/json",
                  authorization: auth,
                  host: `${hostname}:${port}`,
                },
              },
              (pres) => {
                const chunks: Buffer[] = [];
                pres.on("data", (c) => chunks.push(c as Buffer));
                pres.on("end", () => {
                  try {
                    const out = JSON.parse(Buffer.concat(chunks).toString("utf8"));
                    if (out.error) rejectRpc(new Error(String((out.error as any).message ?? out.error)));
                    else resolveRpc(String(out.result ?? ""));
                  } catch (e) {
                    rejectRpc(e as Error);
                  }
                });
              },
            );
            preq.on("error", rejectRpc);
            preq.write(body);
            preq.end();
          });
        const persist = (level: string) => {
          try {
            const existing = existsSync(INI_PATH) ? readFileSync(INI_PATH, "utf8") : "";
            const keyRe = /^([a-zA-Z0-9]+)\s*=/;
            const out = existing
              .split(/\r?\n/)
              .map((l) => (/^debuglevel\s*=/.test(l) ? `debuglevel=${level}` : l))
              .join("\n")
              .replace(/\n+$/, "");
            const has = existing.split(/\r?\n/).some((l) => /^debuglevel\s*=/.test(l));
            const final = (has ? out : out + "\ndebuglevel=" + level) + "\n";
            writeFileSync(INI_PATH, final);
          } catch {
            /* ignore persist failure */
          }
        };
        res.setHeader("content-type", "application/json");
        if (req.method === "POST") {
          const chunks: Buffer[] = [];
          req.on("data", (c) => chunks.push(c as Buffer));
          req.on("end", () => {
            try {
              const body = JSON.parse(Buffer.concat(chunks).toString("utf8") || "{}");
              const level = String(body.level ?? "").trim();
              if (!level) {
                res.end(JSON.stringify({ ok: false, error: "level required / 缺少 level" }));
                return;
              }
              rpcCall(level)
                .then(() => {
                  persist(level);
                  res.end(JSON.stringify({ ok: true, level }));
                })
                .catch((e: Error) => res.end(JSON.stringify({ ok: false, error: e.message })));
            } catch {
              res.end(JSON.stringify({ ok: false, error: "bad body / 请求体错误" }));
            }
          });
          return;
        }
        rpcCall("get")
          .then((level) => res.end(JSON.stringify({ ok: true, level: level || "info" })))
          .catch(() => {
            const o = parseIni(INI_PATH);
            res.end(JSON.stringify({ ok: true, level: o.debuglevel || o.loglevel || "info" }));
          });
      });
    },
  };
}

// https://vitejs.dev/config/
export default defineConfig({
  server: {
    host: "127.0.0.1",
    port: Number(process.env.WAILS_VITE_PORT) || 9245,
    strictPort: true,
  },
  plugins: [svelte(), wails("./bindings"), rpcProxyPlugin()],
});
