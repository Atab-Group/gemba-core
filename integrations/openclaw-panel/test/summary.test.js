import assert from 'node:assert/strict';
import { test } from 'node:test';

import { MAX_CLAIMS, MAX_PROJECTS, normaliseBaseUrl, projectSummary } from '../src/summary.js';

const summary = {
  instance_id: 'abc',
  generated_at: '2026-09-08T12:00:00Z',
  total: 2785,
  adaptors: [{ name: 'atab-github', healthy: true }],
  sources: [
    { id: 'atab-group', items: 2539, freshness: 'fresh' },
    { id: 'hadedahealth', items: 246, freshness: 'fresh' },
  ],
  projects: [
    { id: 'atab-group#1', title: 'Atab Group Tasks', source: 'atab-group', items: 1599 },
    { id: '', items: 940 },
  ],
  by_readiness: { ready: 233, blocked: 19 },
  claimed: [{ id: 'a/1', title: 'held', holder: 'bot', state: 'active' }],
};

test('projects the summary into a fixed-size record', () => {
  const out = projectSummary(summary);
  assert.equal(out.total, 2785);
  assert.equal(out.healthy, true);
  assert.deepEqual(out.degraded, []);
  assert.equal(out.projects.length, 2);
  assert.equal(out.by_readiness.ready, 233);
  assert.equal(out.claimed[0].holder, 'bot');
});

// The unfiled bucket has no id and no title. Rendering a blank row for
// the largest group on the board would be the panel's worst failure.
test('names the unfiled bucket', () => {
  const out = projectSummary(summary);
  assert.equal(out.projects[1].title, 'No project');
  assert.equal(out.projects[1].items, 940);
});

// A panel reporting a board as healthy while a source is unreadable is
// worse than one reporting nothing: its numbers would be a subset shown
// as the whole.
test('reports a degraded adaptor rather than swallowing it', () => {
  const out = projectSummary({
    ...summary,
    adaptors: [{ name: 'atab-github', healthy: false, reason: 'degraded sources: atab-group' }],
  });
  assert.equal(out.healthy, false);
  assert.deepEqual(out.degraded, ['degraded sources: atab-group']);
});

test('names a stale source', () => {
  const out = projectSummary({
    ...summary,
    sources: [{ id: 'atab-group', items: 1, freshness: 'stale' }],
  });
  assert.deepEqual(out.stale_sources, ['atab-group (stale)']);
});

// The response has to stay a fixed size, or a board that grows turns a
// status panel into a second copy of itself.
test('caps the lists and says by how much', () => {
  const projects = Array.from({ length: MAX_PROJECTS + 4 }, (_, i) => ({
    id: `s#${i}`,
    title: `board ${i}`,
    items: i,
  }));
  const claimed = Array.from({ length: MAX_CLAIMS + 3 }, (_, i) => ({
    id: `a/${i}`,
    title: `held ${i}`,
    holder: 'bot',
    state: 'active',
  }));
  const out = projectSummary({ ...summary, projects, claimed });
  assert.equal(out.projects.length, MAX_PROJECTS);
  assert.equal(out.projects_truncated, 4);
  assert.equal(out.claimed.length, MAX_CLAIMS);
  assert.equal(out.claimed_truncated, 3);
});

// The server's own truncation count travels too, or a widget would
// report "showing everything" for a board the server already cut.
test('adds the server-side truncation count', () => {
  const out = projectSummary({ ...summary, claimed_truncated: 7 });
  assert.equal(out.claimed_truncated, 7);
});

// Empty groups are arrays. A renderer that has to handle both null and
// [] will eventually handle only one of them.
test('an empty body still yields the full shape', () => {
  const out = projectSummary(undefined);
  assert.deepEqual(out.projects, []);
  assert.deepEqual(out.claimed, []);
  assert.deepEqual(out.degraded, []);
  assert.deepEqual(out.stale_sources, []);
  assert.equal(out.total, 0);
});

test('normaliseBaseUrl accepts an http origin and strips the trailing slash', () => {
  assert.equal(normaliseBaseUrl('http://127.0.0.1:7676/'), 'http://127.0.0.1:7676');
  assert.equal(normaliseBaseUrl('https://board.example/gemba/'), 'https://board.example/gemba');
});

// The base URL is operator config fetched with the Gateway's own reach.
// A non-http scheme would turn a dashboard binding into a general fetch.
test('normaliseBaseUrl refuses anything that is not http or https', () => {
  assert.throws(() => normaliseBaseUrl('file:///etc/passwd'), /http or https/);
  assert.throws(() => normaliseBaseUrl(''), /required/);
  assert.throws(() => normaliseBaseUrl('not a url'), /not a URL/);
});
