// The pure half of the panel: turning a Gemba work summary into the
// fixed-size record a dashboard widget renders.
//
// It is separate from the plugin entry so it can be tested without an
// OpenClaw host, and so the projection is reviewable on its own. Nothing
// here touches the network or the filesystem.

/**
 * Bounds on what reaches a widget.
 *
 * A dashboard widget is a small cell polled on a timer. Its response has
 * to stay a fixed size, or a board that grows turns a status panel into
 * a second copy of itself. Gemba's own summary is already bounded; these
 * cap the two lists that are not.
 */
export const MAX_PROJECTS = 12;
export const MAX_CLAIMS = 10;

/**
 * projectSummary reduces GET /api/work-summary to the panel's record.
 *
 * Every list is capped and every cap is reported, so the panel can say
 * "and 4 more" rather than implying it is showing everything. Empty
 * groups are arrays rather than null: a renderer that has to handle both
 * will eventually handle only one of them.
 *
 * @param {unknown} raw decoded /api/work-summary body
 * @returns {{
 *   generated_at: string,
 *   total: number,
 *   healthy: boolean,
 *   degraded: string[],
 *   projects: Array<{id: string, title: string, items: number}>,
 *   projects_truncated: number,
 *   by_readiness: Record<string, number>,
 *   claimed: Array<{id: string, title: string, holder: string, state: string}>,
 *   claimed_truncated: number,
 *   stale_sources: string[]
 * }}
 */
export function projectSummary(raw) {
  const body = isRecord(raw) ? raw : {};
  const adaptors = Array.isArray(body.adaptors) ? body.adaptors : [];
  const sources = Array.isArray(body.sources) ? body.sources : [];
  const projects = Array.isArray(body.projects) ? body.projects : [];
  const claimed = Array.isArray(body.claimed) ? body.claimed : [];

  // A panel that reports a board as healthy while a source is
  // unreadable is worse than one that reports nothing: the numbers it
  // shows would be a subset presented as the whole.
  const degraded = adaptors
    .filter((a) => isRecord(a) && a.healthy === false)
    .map((a) => String(a.reason || a.name || 'unknown'));

  const staleSources = sources
    .filter((s) => isRecord(s) && s.freshness && s.freshness !== 'fresh')
    .map((s) => `${String(s.id)} (${String(s.freshness)})`);

  const namedProjects = projects
    .filter(isRecord)
    .map((p) => ({
      id: String(p.id ?? ''),
      // The unfiled bucket has no id and no title. Naming it here keeps
      // the panel from rendering a blank row for the largest group.
      title: String(p.title || p.id || 'No project'),
      items: Number(p.items ?? 0),
    }));

  const heldWork = claimed.filter(isRecord).map((c) => ({
    id: String(c.id ?? ''),
    title: String(c.title ?? ''),
    holder: String(c.holder ?? 'unknown'),
    state: String(c.state ?? 'unknown'),
  }));

  return {
    generated_at: String(body.generated_at ?? ''),
    total: Number(body.total ?? 0),
    healthy: degraded.length === 0,
    degraded,
    projects: namedProjects.slice(0, MAX_PROJECTS),
    projects_truncated: Math.max(0, namedProjects.length - MAX_PROJECTS),
    by_readiness: isRecord(body.by_readiness) ? body.by_readiness : {},
    claimed: heldWork.slice(0, MAX_CLAIMS),
    claimed_truncated:
      Math.max(0, heldWork.length - MAX_CLAIMS) + Number(body.claimed_truncated ?? 0),
    stale_sources: staleSources,
  };
}

function isRecord(value) {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

/**
 * normaliseBaseUrl rejects anything that is not an http(s) origin and
 * strips a trailing slash.
 *
 * The base URL is operator config, and the plugin fetches it with the
 * Gateway's own network reach. A config value that could name a
 * non-http scheme would turn a dashboard binding into a general fetch.
 */
export function normaliseBaseUrl(value) {
  const raw = String(value ?? '').trim();
  if (!raw) throw new Error('gemba-panel: base_url is required');
  let url;
  try {
    url = new URL(raw);
  } catch {
    throw new Error(`gemba-panel: base_url is not a URL: ${raw}`);
  }
  if (url.protocol !== 'http:' && url.protocol !== 'https:') {
    throw new Error(`gemba-panel: base_url must be http or https, got ${url.protocol}`);
  }
  return url.origin + url.pathname.replace(/\/+$/, '');
}
