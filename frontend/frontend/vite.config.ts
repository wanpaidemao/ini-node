import { defineConfig, type Plugin } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import wails from "@wailsio/runtime/plugins/vite";
import { request as httpRequest } from "http";
import { readFileSync, writeFileSync, existsSync } from "fs";
import { resolve } from "path";

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
        if (req.method === "POST") {
          const chunks: Buffer[] = [];
          req.on("data", (c) => chunks.push(c as Buffer));
          req.on("end", () => {
            try {
              const body = JSON.parse(Buffer.concat(chunks).toString("utf8"));
              const lines: string[] = [];
              if (typeof body.rpcuser === "string") lines.push(`rpcuser=${body.rpcuser}`);
              if (typeof body.rpcpass === "string") lines.push(`rpcpass=${body.rpcpass}`);
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
          }),
        );
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
