// mission-control bridge for OpenCode.
//
// A plain `opencode` runs its server in-process with no TCP port, so
// mission-control's discovery (which looks for listening opencode
// processes) can't see it. This plugin opens one: a loopback-only port that
// forwards every request to the in-process server. The daemon then finds and
// drives the session exactly like `opencode --port 0`, with no daemon
// changes. All the logic stays in Go; this is only a door.
//
// Install globally: copy or symlink into ~/.config/opencode/plugins/.
//
// Caveats:
// - Uses the SDK client's internal fetch (client._client.getConfig().fetch),
//   not a public API. If an OpenCode upgrade moves it, the plugin logs and
//   does nothing; `opencode --port 0` still works.
// - Like `opencode --port`, the port is unauthenticated: any local process
//   can drive the agent. It only binds 127.0.0.1.
// - Never call the server during init: it waits on plugins to finish loading.
// - A plain `opencode` exposes a placeholder serverUrl (localhost:4096) with
//   nothing listening; requests must go through the client's fetch.

const server = async ({ serverUrl, client }) => {
  // OpenCode may initialize plugins more than once per process; one door is
  // enough. (The daemon also watches only one port per process, so a
  // duplicate, or the native port of `opencode --port N`, is harmless.)
  if (globalThis.__missionControlBridge) return {}
  const inner = client && client._client && client._client.getConfig ? client._client.getConfig() : null
  if (!inner || typeof inner.fetch !== "function") {
    console.error("mission-control-bridge: SDK internals changed; not bridging (use opencode --port 0)")
    return {}
  }

  const bridge = (globalThis.__missionControlBridge = Bun.serve({
    hostname: "127.0.0.1",
    port: 0,
    idleTimeout: 0, // /event is a long-lived SSE stream
    fetch(req) {
      const url = new URL(req.url)
      return inner.fetch(new Request(new URL(url.pathname + url.search, serverUrl), req))
    },
  }))

  return {
    dispose: async () => {
      bridge.stop(true)
      globalThis.__missionControlBridge = undefined
    },
  }
}

export default { id: "mission-control-bridge", server }
