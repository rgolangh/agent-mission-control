// agent-mission-control bridge for OpenCode.
//
// A plain `opencode` runs its server in-process with no TCP port, so
// agent-mission-control's discovery (which looks for listening opencode
// processes) can't see it. This plugin opens one: a loopback-only port that
// forwards requests to the in-process server. The daemon then finds and
// drives the session like `opencode --port 0`. All the logic stays in Go;
// this is only a door, and it is locked:
//
// - Every request needs `Authorization: Bearer <token>`. The token is random
//   per process and written, with the port, to
//   $XDG_RUNTIME_DIR/agent-mission-control/opencode/<pid>.token (dir 0700, file
//   0600): only this user can read it. Exception: GET /global/health, which
//   discovery probes and which reveals only the version.
// - The Host header must name the loopback port (defeats DNS rebinding), and
//   requests carrying an Origin header are refused (browsers always send one
//   cross-origin; the daemon never does). OpenCode's own server checks
//   neither, so without these a web page could drive the agent.
//
// Install globally: copy or symlink into ~/.config/opencode/plugins/.
//
// Caveats:
// - Uses the SDK client's internal fetch (client._client.getConfig().fetch),
//   not a public API. If an OpenCode upgrade moves it, the plugin logs and
//   does nothing; `opencode --port 0` still works (without these protections).
// - Never call the server during init: it waits on plugins to finish loading.
// - A plain `opencode` exposes a placeholder serverUrl (localhost:4096) with
//   nothing listening; requests must go through the client's fetch.

import { randomBytes, timingSafeEqual } from "node:crypto"
import fs from "node:fs"
import path from "node:path"

const deny = (status, error) =>
  new Response(JSON.stringify({ error }), { status, headers: { "content-type": "application/json" } })

const server = async ({ serverUrl, client }) => {
  // OpenCode may initialize plugins more than once per process; one door is
  // enough. (The daemon also watches only one port per process.)
  if (globalThis.__agentMissionControlBridge) return {}
  const inner = client && client._client && client._client.getConfig ? client._client.getConfig() : null
  if (!inner || typeof inner.fetch !== "function") {
    console.error("agent-mission-control-bridge: SDK internals changed; not bridging (use opencode --port 0)")
    return {}
  }
  const runtime = process.env.XDG_RUNTIME_DIR
  if (!runtime) {
    console.error("agent-mission-control-bridge: XDG_RUNTIME_DIR is not set; not bridging")
    return {}
  }

  const token = randomBytes(32).toString("hex")
  const expected = Buffer.from("Bearer " + token)
  const authorized = (req) => {
    const got = Buffer.from(req.headers.get("authorization") || "")
    return got.length === expected.length && timingSafeEqual(got, expected)
  }

  let port = 0
  const bridge = (globalThis.__agentMissionControlBridge = Bun.serve({
    hostname: "127.0.0.1",
    port: 0,
    idleTimeout: 0, // /event is a long-lived SSE stream
    fetch(req) {
      const host = req.headers.get("host") || ""
      if (host !== `127.0.0.1:${port}` && host !== `localhost:${port}`) return deny(403, "bad host")
      if (req.headers.has("origin")) return deny(403, "cross-origin requests are not allowed")
      const url = new URL(req.url)
      const health = req.method === "GET" && url.pathname === "/global/health"
      if (!health && !authorized(req)) return deny(401, "missing or wrong token")

      // Concatenate onto the inner server's origin rather than resolve the
      // path against it: a path like "//other.host/x" would resolve to that
      // other host.
      const fwd = new Request(new URL(serverUrl).origin + url.pathname + url.search, req)
      fwd.headers.delete("authorization") // ours, not the inner server's
      return inner.fetch(fwd)
    },
  }))
  port = bridge.port

  // Publish the credential where only this user can read it.
  const dir = path.join(runtime, "agent-mission-control", "opencode")
  const file = path.join(dir, `${process.pid}.token`)
  try {
    fs.mkdirSync(dir, { recursive: true, mode: 0o700 })
    fs.chmodSync(dir, 0o700)
    fs.writeFileSync(file, JSON.stringify({ port, token }) + "\n", { mode: 0o600 })
    fs.chmodSync(file, 0o600)
  } catch (e) {
    console.error("agent-mission-control-bridge: cannot write token, closing the bridge: " + e.message)
    bridge.stop(true)
    globalThis.__agentMissionControlBridge = undefined
    return {}
  }

  return {
    dispose: async () => {
      bridge.stop(true)
      globalThis.__agentMissionControlBridge = undefined
      try { fs.unlinkSync(file) } catch {}
    },
  }
}

export default { id: "agent-mission-control-bridge", server }
