# Gemba companion panel for OpenClaw

An OpenClaw plugin that puts this Gemba instance's work summary on an
OpenClaw session dashboard: totals, project boards, readiness, held work,
and whether any source is degraded or stale.

**This plugin is shipped here, not installed.** Installing OpenClaw plugin
code needs `operator.admin` and a Gateway restart, which is outside the
authority of the task that wrote it. The blocker and the alternatives are
spelled out at the bottom, along with what was verified and what was not.

## What it is, and why it is shaped this way

The widget never talks to Gemba. It asks the Gateway for a declared data
binding, the Gateway calls this plugin's read-scoped method, and this
plugin fetches `GET /api/work-summary`.

```
board widget  ──openclaw.data.read──▶  Gateway  ──gemba.summary.read──▶  plugin  ──GET──▶  Gemba
```

Three reasons for the indirection, in the order they bite:

1. **Gemba binds to `127.0.0.1`.** A browser on a phone, or on any other
   machine, cannot reach it. The Gateway can.
2. **A binding is narrower than network reach.** A widget granted
   `gemba-panel.summary` can ask for that and nothing else. Granting it
   `net` instead would let it fetch any origin its CSP allowed, for as
   long as the grant lived.
3. **It stays read-only by construction.** One method, no parameters, one
   GET. There is no shape of call it could make that writes.

`src/summary.js` bounds what reaches the widget: at most 12 projects and
10 held items, each cap reported, so the panel says "and 4 more" rather
than implying it is showing everything. Gemba's summary is already a
fixed-size document; these cap the two lists that are not.

## Install

The Gemba server must already be running with `--cors-allowed-origins` set
if anything other than the Gateway will read it. This plugin does not need
CORS: the fetch is server to server.

```bash
# 1. Point the Gateway at this directory and enable it.
openclaw plugins install /path/to/gemba-core/integrations/openclaw-panel
openclaw plugins enable gemba-panel

# 2. Tell it where Gemba is.
#    In the Gateway config, under plugins.entries:
#      "gemba-panel": { "enabled": true, "config": { "base_url": "http://127.0.0.1:7676" } }

# 3. Restart the Gateway. Installing plugin code requires it.
#    Keep the restart outside the Gateway's own cgroup.

# 4. Confirm the plugin loaded and its binding registered.
openclaw plugins list --json | jq '.plugins[] | select(.id == "gemba-panel")'
```

Then author the panel from an agent session that has `show_widget`:

```
show_widget({
  title: "Gemba",
  name: "gemba",
  pin: true,
  size: "md",
  capabilities: { data: ["gemba-panel.summary"] },
  widget_code: <contents of widget/panel.html>
})
```

The declared capability needs an approval under the session's permission
mode. Under **Guarded** that is an Allow prompt; under **Read only** it is
refused.

## Uninstall

```bash
openclaw plugins disable gemba-panel
openclaw plugins uninstall gemba-panel
```

Disabling removes the binding from the capability registry. A widget still
pinned to the board renders as unavailable rather than silently showing
stale numbers.

## Test

```bash
node --test test/*.test.js
```

Nine tests over the projection and the base-URL guard. They cover the
truncation counts, the unfiled-project bucket, degraded and stale
reporting, and the refusal of a non-http `base_url`.

## The capability blocker, exactly

A rendering panel on an OpenClaw dashboard needs one of two things, and
neither was available to the task that wrote this plugin:

- **`show_widget`**, which authors an `html`, `a2ui`, or plugin-registered
  widget. It is exposed to a session with an inline-widget-capable client
  or a matching channel presenter. It was not in this session's tool set.
- **A plugin registering a widget kind or a data binding**, which is what
  this directory is. Enabling a plugin already in the startup inventory
  can sometimes skip a restart, but *installing* plugin code cannot:
  "Installing, updating, or removing plugin code requires a Gateway
  restart" (`docs/plugins/manage-plugins.md`). That plus `operator.admin`
  puts it outside a code-change task's authority.

One thing worth knowing, because it looks like a third option and is not.
`dashboard widget_put` accepts a `pluginKind` it has never heard of and
returns success. Putting `gemba:summary` on a board succeeded at
revision 1 on an install where no plugin registers that kind, and the
cell then renders as **"Widget from disabled plugin gemba"** with a
Delete button and no content, checked in a browser against the Control
UI. The Control UI resolves plugin widget kinds against a registry
bundled with the client, so an unregistered kind stores fine and cannot
draw anything. **A successful `widget_put` is not evidence of a rendering
panel.**

The other extension point, for a panel that owns its own rendering rather
than being agent-authored HTML, is `api.registerBoardWidgetContentKind`.
It is a documented SDK surface and it would remove the `show_widget` step
entirely. It is not used here because it could not be exercised without
installing the plugin, and shipping a renderer nobody has run is worse
than shipping the binding that a verified `show_widget` call can consume.

## What was verified, and what was not

Verified: the projection and its bounds, by unit test; the shape of
`/api/work-summary`, against the live endpoint; that the manifest matches
the documented `openclaw.plugin.json` dashboard contract; and, in a
browser, that an unregistered `pluginKind` stores and then refuses to
render.

Not verified: this plugin loading in a Gateway, the binding appearing in
the capability registry, and the panel rendering on a board. All three
need the install above.
