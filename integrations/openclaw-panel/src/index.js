// gemba-panel: an OpenClaw plugin that exposes this Gemba instance's
// work summary to a dashboard widget.
//
// The widget never talks to Gemba. It asks the Gateway for a declared
// data binding, the Gateway calls this plugin's read-scoped method, and
// this plugin fetches the summary. That indirection is the point:
//
//   - Gemba binds to 127.0.0.1. A browser on a phone or another machine
//     cannot reach it, and the Gateway can.
//   - A widget granted a binding cannot ask for anything else. Giving it
//     network reach instead would let it fetch any origin its CSP
//     allowed, for as long as the grant lived.
//   - The read stays read-only by construction. There is one method, it
//     takes no parameters, and it issues one GET.
//
// See README.md for install, and for why this plugin is shipped here
// rather than installed by the task that wrote it.

import { definePluginEntry } from 'openclaw/plugin-sdk/plugin-entry';

import { normaliseBaseUrl, projectSummary } from './summary.js';

// FETCH_TIMEOUT_MS bounds one read. A widget poll that hangs would hold
// a Gateway request open; a summary that cannot answer in this long is
// not a summary worth waiting for.
const FETCH_TIMEOUT_MS = 5000;

// MAX_BODY_BYTES bounds what the plugin will decode. The summary is a
// fixed-size document by design, so anything near this is a misconfigured
// base URL pointing at something else.
const MAX_BODY_BYTES = 256 * 1024;

export default definePluginEntry({
  id: 'gemba-panel',
  name: 'Gemba panel',
  description: "Reads a Gemba instance's work summary for a dashboard widget.",
  register(api) {
    // Discovery and CLI-metadata loads must not open sockets. The method
    // registration itself is inert, so it is safe in every mode; the
    // fetch only happens when a granted widget asks.
    api.registerGatewayMethod(
      'gemba.summary.read',
      async () => {
        const baseUrl = normaliseBaseUrl(api.config?.base_url);
        const body = await readSummary(baseUrl);
        return projectSummary(body);
      },
      { scope: 'operator.read' }
    );
  },
});

async function readSummary(baseUrl) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), FETCH_TIMEOUT_MS);
  try {
    const res = await fetch(`${baseUrl}/api/work-summary`, {
      method: 'GET',
      headers: { accept: 'application/json' },
      // A redirect would take the read somewhere the operator did not
      // configure, so it is refused rather than followed.
      redirect: 'error',
      signal: controller.signal,
    });
    if (!res.ok) {
      // The upstream body is not echoed. It belongs to a service the
      // widget's audience may not be entitled to read.
      throw new Error(`gemba-panel: the Gemba API answered ${res.status}`);
    }
    const text = await res.text();
    if (text.length > MAX_BODY_BYTES) {
      throw new Error('gemba-panel: the Gemba API returned more than a summary');
    }
    return JSON.parse(text);
  } finally {
    clearTimeout(timer);
  }
}
