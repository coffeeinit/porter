import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
const html = readFileSync(join(here, 'index.html'), 'utf8');
const inlineScript = [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)].at(-1)?.[1];
assert.ok(inlineScript, 'dashboard inline script exists');

class ElementStub {
  constructor(id = '') {
    this.id = id;
    this.innerHTML = '';
    this.textContent = '';
    this.value = '';
    this.disabled = false;
    this.style = {};
    this.dataset = {};
    this.attributes = {};
    this.className = '';
    this.classList = { toggle() {} };
    this.options = [];
    this.selectedIndex = 0;
  }
  setAttribute(name, value) { this.attributes[name] = value; }
  addEventListener() {}
  focus() {}
}

function page(items = [], nextCursor = '') {
  return { items, ...(nextCursor ? { next_cursor: nextCursor } : {}) };
}

function jsonResponse(status, value) {
  return {
    status,
    ok: status >= 200 && status < 300,
    async text() { return typeof value === 'string' ? value : JSON.stringify(value); },
  };
}

function createDashboard({ override = () => null, promptValue = null, confirmValue = false } = {}) {
  const elements = new Map();
  const calls = [];
  const eventHandlers = new Map();
  const document = {
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, new ElementStub(id));
      return elements.get(id);
    },
    querySelectorAll() { return []; },
    querySelector(selector) {
      const key = `selector:${selector}`;
      if (!elements.has(key)) elements.set(key, new ElementStub(key));
      return elements.get(key);
    },
  };
  const location = { href: 'https://porter.test/', origin: 'https://porter.test', hash: '#/overview' };
  const window = {
    addEventListener(name, handler) { eventHandlers.set(name, handler); },
  };
  const fixtures = new Map([
    ['/api/v1/fleet/capabilities', { join_token_issuance: true, agent_enrollment: true, node_credential_rotation: true }],
    ['/api/v1/projects', page([{ id: 'prj_alpha', name: 'Alpha', state: 'active', created_at: '2026-11-01T00:00:00Z' }])],
    ['/api/v1/projects/prj_alpha/services', page([{ id: 'svc_api', project_id: 'prj_alpha', name: 'api', state: 'active', runtime: 'firecracker', desired_instances: 2, created_at: '2026-11-02T00:00:00Z' }])],
    ['/api/v1/fleet/nodes', page([{ id: 'node_a', name: 'Node A', state: 'ready', enrollment_target: 'same_host', created_at: '2026-11-03T00:00:00Z', updated_at: '2026-11-04T00:00:00Z' }])],
    ['/api/v1/operations', page([{ id: 'op_a', kind: 'vm.apply', state: 'queued', resource_id: 'svc_api', created_at: '2026-11-04T00:00:00Z' }], 'ops-next')],
    ['/api/v1/domain-claims', page([{ id: 'claim_a', project_id: 'prj_alpha', service_id: 'svc_api', hostname: 'api.example.test', state: 'pending_proof', verification_txt_name: '_porter.api.example.test', verification_txt_value: 'proof-value', created_at: '2026-11-04T00:00:00Z' }])],
    ['/api/v1/events', page([{ id: 'event_a', type: 'service.created', occurred_at: '2026-11-04T00:00:00Z', project_id: 'prj_alpha', service_id: 'svc_api' }])],
    ['/api/v1/projects/prj_alpha/volumes', page([])],
    ['/api/v1/projects/prj_alpha/services/svc_api/volumes', page([])],
    ['/api/v1/services/svc_api/releases', page([{ id: 'rel_a', service_id: 'svc_api', sequence: 1, state: 'blocked', intent: 'preview', source: { kind: 'git', repository: 'https://example.test/repo.git', ref: 'main' }, build: { kind: 'dockerfile' }, created_at: '2026-11-05T00:00:00Z', updated_at: '2026-11-05T00:00:00Z' }])],
    ['/api/v1/releases/rel_a/instances', page([{ id: 'instance_a', service_id: 'svc_api', release_id: 'rel_a', ordinal: 1, runtime: 'firecracker', state: 'running', health: 'healthy', created_at: '2026-11-05T00:00:00Z', updated_at: '2026-11-05T00:00:00Z' }])],
  ]);
  const fetch = async (href, options = {}) => {
    const url = new URL(href);
    calls.push({ url, options });
    const forced = override(url, options);
    if (forced) {
      if (forced instanceof Error) throw forced;
      return forced;
    }
    let data = fixtures.get(url.pathname);
    if (url.pathname === '/api/v1/operations' && url.searchParams.get('cursor') === 'ops-next') {
      data = page([{ id: 'op_b', kind: 'vm.apply', state: 'succeeded', resource_id: 'node_a' }]);
    }
    if (data === undefined) return jsonResponse(404, { error: { message: 'route not found' } });
    return jsonResponse(200, data);
  };
  const context = vm.createContext({
    document, location, window, fetch, URL, URLSearchParams, AbortController,
    setTimeout() { return 1; }, setInterval() { return 1; },
    prompt() { return promptValue; }, confirm() { return confirmValue; },
    navigator: { clipboard: { async writeText() {} } },
  });
  new vm.Script(inlineScript, { filename: 'porter-dashboard-inline.js' }).runInContext(context);
  return { context, elements, calls, eventHandlers, location };
}

async function settle() {
  await new Promise(resolve => setImmediate(resolve));
}

async function authenticate(dashboard) {
  dashboard.context.document.getElementById('fleetAdminToken').value = 'operator-secret';
  await vm.runInContext('setFleetAdminToken()', dashboard.context);
}

function requestPath(call) {
  return `${call.options.method || 'GET'} ${call.url.pathname}`;
}

const mountedPageRoutes = new Set([
  // Mounted by backend commit 3738acb: each list returns contract/v1.Page[T].
  'GET /api/v1/projects',
  'GET /api/v1/operations',
  'GET /api/v1/fleet/nodes',
  'GET /api/v1/domain-claims',
  'GET /api/v1/events',
  'GET /api/v1/projects/prj_alpha/services',
]);

 test('page load stays read-only and refresh consumes mounted Page DTOs', async () => {
  const dashboard = createDashboard();
  await settle();
  assert.equal(dashboard.calls.length, 0, 'unauthenticated startup sends no API requests');
  await vm.runInContext('createFleetJoinToken()', dashboard.context);
  await assert.rejects(vm.runInContext("api('/api/v1/fleet/join-tokens', {method:'POST', body:'{}'})", dashboard.context), /Protected actions/);
  assert.equal(dashboard.calls.length, 0, 'unauthorized Fleet actions send no write request');

  await authenticate(dashboard);
  assert.ok(dashboard.calls.length > 0);
  assert.ok(dashboard.calls.every(call => (call.options.method || 'GET') === 'GET'), 'startup and refresh make no writes');
  assert.ok(dashboard.calls.every(call => call.options.credentials === 'omit'));
  assert.ok(dashboard.calls.every(call => call.options.redirect === 'error'));
  assert.ok(dashboard.calls.every(call => call.options.headers.Authorization === 'Bearer operator-secret'));

  for (const call of dashboard.calls) {
    const path = requestPath(call);
    if (path === 'GET /api/v1/fleet/capabilities') continue;
    assert.ok(mountedPageRoutes.has(path), `request matches mounted backend route: ${path}`);
    assert.equal(call.url.searchParams.get('page_size'), '100', `${path} uses paginated list DTO`);
  }
  assert.equal(vm.runInContext('token', dashboard.context), 'operator-secret');
  assert.equal(vm.runInContext('fleetActionsEnabled', dashboard.context), true);
  assert.match(dashboard.elements.get('projectsBody').innerHTML, /Alpha/);
  assert.match(dashboard.elements.get('projectsBody').innerHTML, />1</);
  assert.match(dashboard.elements.get('opsBody').innerHTML, /svc_api/);
  assert.match(dashboard.elements.get('opsBody').innerHTML, /node_a/);
  assert.ok(dashboard.calls.some(call => call.url.pathname === '/api/v1/operations' && call.url.searchParams.get('cursor') === 'ops-next'));
  assert.match(dashboard.elements.get('nodesBody').innerHTML, /Node A/);
  assert.match(dashboard.elements.get('domainsBody').innerHTML, /api\.example\.test/);
  assert.match(dashboard.elements.get('eventsBody').innerHTML, /service\.created/);
  assert.match(html, /porter-api-contract" content="postgres-only"/);
  assert.doesNotMatch(html, /location\.search|fetch\([^)]*['"]\/v1\//);
  assert.doesNotMatch(html, /sample row|demo row/i);
  assert.doesNotMatch(html, /\b(?:localStorage|sessionStorage)\b/);
  assert.match(html, /target\.origin!==trustedAPIOrigin/);
  assert.match(html, /target\.protocol!=='https:'/);
});

test('a panel 404 fails closed without clearing verified auth', async () => {
  const dashboard = createDashboard({
    override(url) {
      if (url.pathname === '/api/v1/domain-claims') {
        return jsonResponse(404, { error: { message: 'route not found' } });
      }
      return null;
    },
  });
  await authenticate(dashboard);
  assert.equal(vm.runInContext('token', dashboard.context), 'operator-secret');
  assert.equal(vm.runInContext('fleetActionsEnabled', dashboard.context), true);
  assert.match(dashboard.elements.get('domainsBody').innerHTML, /Domain claims unavailable/);
  assert.match(dashboard.elements.get('projectsBody').innerHTML, /Alpha/);
  assert.match(dashboard.elements.get('statusLine').textContent, /panels? unavailable/);
  dashboard.location.hash = '#/projects/prj_alpha';
  await vm.runInContext("loadProject('prj_alpha')", dashboard.context);
  assert.match(dashboard.elements.get('pdDomains').innerHTML, /Domain claims unavailable/);

  await assert.rejects(vm.runInContext("api('/api/v1/unmounted-panel')", dashboard.context), /route not found/);
  assert.equal(vm.runInContext('token', dashboard.context), 'operator-secret', 'unsupported-route 404 does not clear a valid token');
  assert.equal(vm.runInContext('fleetActionsEnabled', dashboard.context), true);
  assert.ok(dashboard.calls.every(call => (call.options.method || 'GET') === 'GET'));
});

test('unmounted dashboard features are labeled unavailable and are not requested', async () => {
  const dashboard = createDashboard();
  await authenticate(dashboard);
  for (const unsupported of ['/api/v1/apps', '/api/v1/images', '/api/v1/traces', '/api/v1/gateway/requests', '/api/v1/gateway/stats', '/api/v1/mcp/tokens']) {
    assert.ok(!dashboard.calls.some(call => call.url.pathname.startsWith(unsupported)), `${unsupported} is not requested`);
  }
  assert.match(html, /Images<\/div><div class="h1"[^>]*>Unavailable in the mounted API/);
  assert.match(html, /MCP credential management routes are not exposed/);
  assert.match(html, /Gateway traffic statistics, request logs, and active routing are not exposed/);
});

test('project details and user actions use mounted paths and request/response DTOs', async () => {
  const dashboard = createDashboard({
    promptValue: 'New project',
    override(url, options) {
      const method = options.method || 'GET';
      if (method === 'POST' && url.pathname === '/api/v1/projects') return jsonResponse(201, { id: 'prj_new', name: 'New project', state: 'active' });
      if (method === 'POST' && url.pathname === '/api/v1/projects/prj_alpha/services') return jsonResponse(201, { id: 'svc_new', project_id: 'prj_alpha', name: 'worker', runtime: 'firecracker', desired_instances: 3 });
      if (method === 'POST' && url.pathname === '/api/v1/projects/prj_alpha/services/svc_api/domain-claims') return jsonResponse(201, { id: 'claim_new', project_id: 'prj_alpha', service_id: 'svc_api', hostname: 'worker.example.test', state: 'pending_proof', verification_txt_name: '_porter.worker.example.test', verification_txt_value: 'proof-new' });
      if (method === 'POST' && url.pathname === '/api/v1/domain-claims/claim_a/verify-txt') return jsonResponse(200, { id: 'claim_a', state: 'verified', hostname: 'api.example.test' });
      if (method === 'POST' && url.pathname === '/api/v1/fleet/join-tokens') return jsonResponse(201, { id: 'join_a', token: 'one-time-join-token', target: 'same_host', control_plane_url: 'https://porter.test', expires_at: '2026-11-12T00:00:00Z' });
      return null;
    },
  });
  await authenticate(dashboard);

  dashboard.location.hash = '#/projects/prj_alpha';
  await vm.runInContext("loadProject('prj_alpha')", dashboard.context);
  assert.match(dashboard.elements.get('pdServices').innerHTML, /firecracker/);
  assert.match(dashboard.elements.get('pdReleases').innerHTML, /main/);
  assert.match(dashboard.elements.get('pdInstances').innerHTML, /healthy/);
  for (const route of [
    'GET /api/v1/projects/prj_alpha/services',
    'GET /api/v1/projects/prj_alpha/volumes',
    'GET /api/v1/services/svc_api/releases',
    'GET /api/v1/releases/rel_a/instances',
    'GET /api/v1/projects/prj_alpha/services/svc_api/volumes',
  ]) assert.ok(dashboard.calls.some(call => requestPath(call) === route), `${route} is mounted`);

  dashboard.context.document.getElementById('selProject').value = 'prj_alpha';
  dashboard.context.document.getElementById('serviceName').value = 'worker';
  dashboard.context.document.getElementById('servicePort').value = '9090';
  dashboard.context.document.getElementById('serviceVcpu').value = '2';
  dashboard.context.document.getElementById('serviceMem').value = '512';
  dashboard.context.document.getElementById('serviceInstances').value = '3';
  dashboard.context.document.getElementById('serviceRuntime').value = 'firecracker';
  await vm.runInContext('createProject()', dashboard.context);
  await vm.runInContext('createService()', dashboard.context);

  const domainSelect = dashboard.context.document.getElementById('domainApp');
  domainSelect.value = 'svc_api';
  domainSelect.options = [{ dataset: { project: 'prj_alpha' } }];
  dashboard.context.document.getElementById('domainHost').value = 'worker.example.test';
  await vm.runInContext('addDomain()', dashboard.context);
  dashboard.context.window.pendingDomainID = 'claim_a';
  await vm.runInContext('verifyPendingDomain()', dashboard.context);

  dashboard.context.document.getElementById('fleetTarget').value = 'same_host';
  dashboard.context.document.getElementById('fleetControlPlaneURL').value = 'https://porter.test';
  await vm.runInContext('createFleetJoinToken()', dashboard.context);

  const writes = dashboard.calls.filter(call => call.options.method && call.options.method !== 'GET');
  const byPath = pathname => writes.find(call => call.url.pathname === pathname);
  assert.equal(JSON.parse(byPath('/api/v1/projects').options.body).name, 'New project');
  assert.deepEqual(JSON.parse(byPath('/api/v1/projects/prj_alpha/services').options.body), {
    name: 'worker', port: 9090, vcpus: 2, memory_mb: 512, desired_instances: 3, runtime: 'firecracker',
  });
  assert.deepEqual(JSON.parse(byPath('/api/v1/projects/prj_alpha/services/svc_api/domain-claims').options.body), { hostname: 'worker.example.test' });
  assert.deepEqual(JSON.parse(byPath('/api/v1/domain-claims/claim_a/verify-txt').options.body), {});
  assert.deepEqual(JSON.parse(byPath('/api/v1/fleet/join-tokens').options.body), { target: 'same_host', control_plane_url: 'https://porter.test' });
  assert.ok(writes.every(call => call.options.headers.Authorization === 'Bearer operator-secret'));
});

test('offline-node credential rotation uses the mounted Fleet route and one-time DTO', async () => {
  const dashboard = createDashboard({
    confirmValue: true,
    override(url, options) {
      if (options.method === 'POST' && url.pathname === '/api/v1/fleet/nodes/node_a/credentials') {
        return jsonResponse(201, { node_id: 'node_a', token: 'rotated-once', generation: 2 });
      }
      return null;
    },
  });
  await authenticate(dashboard);
  dashboard.context.window.currentFleetNodes = [{ id: 'node_a', name: 'Node A', state: 'offline' }];
  await vm.runInContext("rotateFleetNodeCredentials('node_a', 'Node A')", dashboard.context);
  const rotation = dashboard.calls.find(call => call.url.pathname === '/api/v1/fleet/nodes/node_a/credentials' && call.options.method === 'POST');
  assert.ok(rotation, 'credential rotation targets the mounted per-node route');
  assert.equal(rotation.options.body, '{}');
  assert.equal(rotation.options.headers.Authorization, 'Bearer operator-secret');
  assert.equal(dashboard.elements.get('fleetCredentialToken').textContent, 'rotated-once');
});

test('capability authorization requires literal HTTP 200 and every true flag', async t => {
  for (const scenario of [
    { name: '201 is rejected', status: 201, body: { join_token_issuance: true, agent_enrollment: true, node_credential_rotation: true } },
    { name: 'a missing capability is rejected', status: 200, body: { join_token_issuance: true, agent_enrollment: true, node_credential_rotation: false } },
  ]) {
    await t.test(scenario.name, async () => {
      const dashboard = createDashboard({
        override(url) {
          if (url.pathname === '/api/v1/fleet/capabilities') return jsonResponse(scenario.status, scenario.body);
          return null;
        },
      });
      await authenticate(dashboard);
      assert.equal(vm.runInContext('token', dashboard.context), '');
      assert.equal(vm.runInContext('fleetActionsEnabled', dashboard.context), false);
      assert.equal(dashboard.calls.length, 1, 'no dashboard data is fetched without verified capability flags');
      assert.equal(dashboard.calls[0].options.method || 'GET', 'GET');
    });
  }
});

test('401, 403, 503, and network failures still clear verified auth', async t => {
  const scenarios = [
    { name: '401', override: url => url.pathname === '/api/v1/domain-claims' ? jsonResponse(401, { error: { message: 'unauthorized' } }) : null },
    { name: '403', override: url => url.pathname === '/api/v1/domain-claims' ? jsonResponse(403, { error: { message: 'forbidden' } }) : null },
    { name: '503', override: url => url.pathname === '/api/v1/domain-claims' ? jsonResponse(503, { error: { message: 'unavailable' } }) : null },
    { name: 'network failure', override: url => url.pathname === '/api/v1/domain-claims' ? new Error('offline') : null },
  ];
  for (const scenario of scenarios) {
    await t.test(scenario.name, async () => {
      const dashboard = createDashboard({ override: scenario.override });
      await authenticate(dashboard);
      await vm.runInContext("api('/api/v1/domain-claims')", dashboard.context).catch(() => {});
      assert.equal(vm.runInContext('token', dashboard.context), '');
      assert.equal(vm.runInContext('fleetActionsEnabled', dashboard.context), false);
    });
  }
});

test('logout and pagehide clear verified auth from memory', async t => {
  for (const mode of ['logout', 'pagehide']) {
    await t.test(mode, async () => {
      const dashboard = createDashboard();
      await authenticate(dashboard);
      assert.equal(vm.runInContext('token', dashboard.context), 'operator-secret');
      if (mode === 'logout') await vm.runInContext('clearFleetAdminToken()', dashboard.context);
      else dashboard.eventHandlers.get('pagehide')();
      assert.equal(vm.runInContext('token', dashboard.context), '');
      assert.equal(vm.runInContext('fleetActionsEnabled', dashboard.context), false);
      assert.equal(dashboard.context.document.getElementById('fleetAdminToken').value, '');
    });
  }
});
