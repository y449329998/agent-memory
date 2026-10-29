(function () {
  'use strict';

  const TOKEN_KEY = 'agent-memory-token';
  const TYPES = [
    ['preference', '偏好'], ['constraint', '约束'], ['convention', '约定'],
    ['lesson', '教训'], ['fact', '事实'], ['goal', '目标']
  ];
  const STATUS = { doing: '进行中', done: '已完成', blocked: '受阻', unknown: '未知' };
  const state = { token: readToken(), memory: null, mem: null };

  /* ---------- 基础 ---------- */
  function $(id) { return document.getElementById(id); }
  function el(tag, cls, text) {
    const n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text !== undefined && text !== null) n.textContent = String(text);
    return n;
  }
  function clear(n) { while (n.firstChild) n.removeChild(n.firstChild); }
  function enc(v) { return encodeURIComponent(v == null ? '' : v); }
  function readToken() {
    let token = '';
    try { token = sessionStorage.getItem(TOKEN_KEY) || ''; } catch (e) { /* 隐私模式 */ }
    try {
      const url = new URL(location.href);
      if (url.searchParams.has('token')) {
        const q = url.searchParams.get('token') || '';
        if (q) { token = q; try { sessionStorage.setItem(TOKEN_KEY, q); } catch (e) {} }
        url.searchParams.delete('token');
        history.replaceState(null, '', url.pathname + (url.search || '') + url.hash);
      }
    } catch (e) {}
    return token;
  }
  async function api(path, opts) {
    opts = opts || {};
    const headers = new Headers(opts.headers || {});
    headers.set('Authorization', 'Bearer ' + state.token);
    headers.set('Accept', 'application/json, text/plain;q=0.9');
    if (opts.body !== undefined && opts.body !== null) headers.set('Content-Type', 'application/json');
    let res;
    try {
      res = await fetch(path, { method: opts.method || 'GET', headers, body: opts.body, cache: 'no-store' });
    } catch (e) {
      throw new Error('无法连接服务，请确认 agent-memory 正在运行');
    }
    if (res.status === 401) throw new Error('访问令牌无效，请重新用 /?token=<令牌> 打开一次');
    const text = await res.text();
    if (!res.ok) throw new Error('HTTP ' + res.status + (text ? '：' + text.slice(0, 200) : ''));
    if (!text) return {};
    const ct = res.headers.get('content-type') || '';
    return ct.includes('application/json') ? JSON.parse(text) : text;
  }
  function toast(kind, text) {
    const box = $('toasts');
    if (!box) return;
    const t = el('div', 'toast ' + kind, text);
    t.title = '点击关闭';
    t.addEventListener('click', () => t.remove());
    box.appendChild(t);
    while (box.children.length > 4) box.removeChild(box.firstChild);
    if (kind !== 'error') setTimeout(() => t.remove(), 4200);
  }
  function fail(label, err) { toast('error', label + '：' + (err && err.message ? err.message : err)); }
  async function busy(btn, label, fn) {
    const old = btn.textContent;
    btn.disabled = true;
    btn.textContent = label + '中…';
    try { return await fn(); }
    catch (e) { fail(label, e); }
    finally { btn.disabled = false; btn.textContent = old; }
  }
  function fmtTime(v) {
    if (!v) return '—';
    const d = new Date(v);
    if (isNaN(d.getTime()) || d.getFullYear() <= 1) return '—';
    const p = (n) => (n < 10 ? '0' + n : '' + n);
    return d.getFullYear() + '-' + p(d.getMonth() + 1) + '-' + p(d.getDate()) + ' ' + p(d.getHours()) + ':' + p(d.getMinutes());
  }
  function typeLabel(t) { return TYPES.filter((x) => x[0] === t).map((x) => x[1])[0] || t || '其他'; }
  function typeStamp(t) { return el('span', 'stamp tiny type-' + (t || 'fact'), typeLabel(t)); }
  function statusStamp(s) { return el('span', 'stamp tiny status-' + (s || 'unknown'), STATUS[s] || STATUS.unknown); }
  function fillTypeSelect(sel, withAll, def) {
    if (!sel) return;
    clear(sel);
    if (withAll) { const o = el('option', '', '全部类型'); o.value = ''; sel.appendChild(o); }
    TYPES.forEach((t) => { const o = el('option', '', t[1]); o.value = t[0]; sel.appendChild(o); });
    sel.value = def || (withAll ? '' : 'fact');
  }
  function projectLabel(p) { return (p && (p.display_name || p.key)) || '—'; }
  function clip(text, n) { return text.length > n ? text.slice(0, n) + '…' : text; }
  function plainPreview(text) {
    return text
      .replace(/```[\s\S]*?```/g, ' ')
      .replace(/!\[[^\]]*\]\([^)]*\)/g, ' ')
      .replace(/\[([^\]]*)\]\([^)]*\)/g, '$1')
      .replace(/[*_`#>]+/g, '')
      .replace(/\s+/g, ' ')
      .trim();
  }
  function countUp(node, to) {
    if (!node) return;
    const from = parseInt(node.textContent, 10) || 0;
    const target = Number(to) || 0;
    if (window.matchMedia('(prefers-reduced-motion: reduce)').matches || document.hidden || from === target) {
      node.textContent = String(target);
      return;
    }
    const start = performance.now();
    const step = (now) => {
      const p = Math.min(1, (now - start) / 520);
      node.textContent = String(Math.round(from + (target - from) * p));
      if (p < 1) requestAnimationFrame(step); else node.textContent = String(target);
    };
    requestAnimationFrame(step);
    setTimeout(() => { node.textContent = String(target); }, 640);
  }

  /* ---------- 公共 UI ---------- */
  function showGate() {
    const main = $('main');
    if (main) main.hidden = true;
    const gate = $('token-gate');
    if (gate) gate.hidden = false;
    const h = $('health-text');
    if (h) h.textContent = '缺少令牌';
    const dot = $('health-dot');
    if (dot) dot.className = 'dot bad';
  }
  async function loadHealth() {
    const dot = $('health-dot'), text = $('health-text');
    if (!dot || !text) return;
    try {
      const d = await api('/v1/healthz');
      dot.className = 'dot ok';
      text.textContent = '正常' + (d && d.version ? ' · ' + d.version : '');
    } catch (e) {
      dot.className = 'dot bad';
      text.textContent = e.message.includes('令牌') ? '鉴权失败' : '不可达';
    }
  }
  function bindSync() {
    const btn = $('sync-btn');
    if (!btn) return;
    btn.addEventListener('click', () => busy(btn, '同步', async () => {
      const d = await api('/v1/scan', { method: 'POST' });
      const errors = (d.errors || []).length;
      toast(errors ? 'error' : 'info', (errors ? '同步完成，有 ' + errors + ' 个错误' : '同步完成') + '，正在刷新…');
      setTimeout(() => location.reload(), 700);
    }));
  }
  function revealSections() {
    document.documentElement.classList.add('js');
    if (!('IntersectionObserver' in window)) return;
    const sections = Array.from(document.querySelectorAll('.section'));
    sections.forEach((s) => s.classList.add('reveal'));
    const io = new IntersectionObserver((entries) => {
      entries.forEach((e) => {
        if (e.isIntersecting) { e.target.classList.add('in'); io.unobserve(e.target); }
      });
    }, { rootMargin: '0px 0px -40px 0px' });
    sections.forEach((s) => io.observe(s));
  }
  function entry(m, next) {
    const li = el('li', 'entry');
    li.dataset.type = m.Type || 'fact';
    const a = el('a', 'entry-link');
    a.href = '/m/' + enc(m.ID) + (next ? '?next=' + enc(next) : '');
    const head = el('div', 'entry-head');
    head.appendChild(typeStamp(m.Type));
    head.appendChild(el('span', 'meta', m.Scope === 'user' ? '用户级' : '项目级'));
    if (m.Stale) head.appendChild(el('span', 'stamp tiny stale-mark', '过时'));
    a.appendChild(head);
    a.appendChild(el('p', 'entry-text', clip(plainPreview(m.Text || ''), 240)));
    const meta = el('div', 'entry-meta');
    const agents = (m.Sources || []).map((s) => s.Agent).filter(Boolean);
    meta.appendChild(el('span', '', '来源：' + (agents.join(' · ') || '—')));
    meta.appendChild(el('span', '', '最后变更：' + fmtTime(m.LastSeenAt)));
    a.appendChild(meta);
    li.appendChild(a);
    return li;
  }
  function emptyRow(list, text) { list.appendChild(el('li', 'empty', text)); }
  function normalizeHit(h) {
    return { ID: h.id, Scope: h.scope, Type: h.type, Text: h.text, Stale: h.stale, LastSeenAt: h.last_seen_at, Sources: h.sources || [] };
  }

  /* ---------- 首页 ---------- */
  async function initHome() {
    fillTypeSelect($('home-add-type'), false, 'fact');
    $('home-add').addEventListener('submit', (e) => {
      e.preventDefault();
      const btn = e.currentTarget.querySelector('button');
      busy(btn, '保存', async () => {
        const text = $('home-add-text').value.trim();
        if (!text) { toast('error', '请先写正文'); return; }
        const d = await api('/v1/memory', { method: 'POST', body: JSON.stringify({
          text, type: $('home-add-type').value, scope: 'user', agent: 'web'
        }) });
        $('home-add-text').value = '';
        toast('info', d.created ? '已归档 #' + d.id : '相同内容已存在 #' + d.id);
        await Promise.all([loadHomeMemories(), loadHomeStats()]);
      });
    });
    await Promise.all([loadHomeStats(), loadHomeProjects(), loadHomeMemories()]);
  }
  async function loadHomeStats() {
    try {
      const d = await api('/v1/stats');
      countUp($('stat-projects'), d.projects);
      countUp($('stat-memories'), d.memories);
      countUp($('stat-stale'), d.stale);
      countUp($('stat-sources'), d.sources);
      countUp($('stat-failed'), d.failed_source);
    } catch (e) { fail('读取概览', e); }
  }
  async function loadHomeProjects() {
    const box = $('project-index');
    try {
      const d = await api('/v1/projects');
      const list = (d.projects || []).slice().sort((a, b) => {
        if (!!b.path_exists !== !!a.path_exists) return a.path_exists ? -1 : 1;
        const ta = a.last_memory_at || '', tb = b.last_memory_at || '';
        if (ta !== tb) return tb.localeCompare(ta);
        return projectLabel(a).localeCompare(projectLabel(b));
      });
      clear(box);
      list.forEach((p) => {
        const a = el('a', 'index-row');
        a.href = '/p/' + enc(p.key);
        a.appendChild(el('span', 'name', projectLabel(p)));
        a.appendChild(el('span', 'path mono', p.primary_path || p.key));
        const counts = el('span', 'count', (p.memory_count || 0) + ' 记忆 · ' + (p.task_count ? p.task_count + ' 任务' : '仅记忆'));
        a.appendChild(counts);
        a.appendChild(el('span', 'when', p.path_exists === false ? '路径不存在' : (p.last_memory_at ? fmtTime(p.last_memory_at) : '—')));
        a.appendChild(el('span', 'arrow', '→'));
        box.appendChild(a);
      });
      if (!list.length) emptyRow(box, '还没有项目。点「立即同步」开始归档。');
      $('project-count').textContent = list.length + ' 个项目';
      $('project-empty').hidden = list.length > 0;
    } catch (e) {
      emptyRow(box, '项目索引加载失败：' + e.message);
    }
  }
  async function loadHomeMemories() {
    const list = $('home-memories');
    clear(list);
    try {
      const d = await api('/v1/memory?scope=user&limit=10&stale=0');
      (d.memories || []).forEach((m) => list.appendChild(entry(m, '/')));
      if (!d.memories || !d.memories.length) emptyRow(list, '还没有用户级记忆。');
      $('home-mem-meta').textContent = '共 ' + (d.total || 0) + ' 条';
    } catch (e) {
      emptyRow(list, '用户级记忆加载失败：' + e.message);
    }
  }

  /* ---------- 项目页 ---------- */
  function railItem(label, id) {
    const div = el('div');
    div.appendChild(el('dt', '', label));
    const dd = el('dd', '');
    dd.id = id;
    dd.textContent = '—';
    div.appendChild(dd);
    return div;
  }
  async function initProject() {
    const key = document.body.dataset.key || '';
    if (!key) { $('main').textContent = '缺少项目标识。'; return; }
    fillTypeSelect($('project-add-type'), false, 'fact');
    const rail = $('rail-stats');
    clear(rail);
    rail.appendChild(railItem('项目记忆', 'rail-project'));
    rail.appendChild(railItem('用户级', 'rail-user'));
    rail.appendChild(railItem('任务卡', 'rail-tasks'));
    rail.appendChild(railItem('来源', 'rail-sources'));
    rail.appendChild(railItem('失败源', 'rail-failed'));
    bindHandoff();
    try {
      const d = await api('/v1/projects');
      const p = (d.projects || []).find((x) => x.key === key);
      if (!p) {
        $('project-title').textContent = '项目不存在';
        $('project-warning').hidden = false;
        $('project-warning').textContent = '这个项目已不在索引里，可能已被移除。';
        return;
      }
      state.project = p;
      document.title = projectLabel(p) + ' · agent-memory';
      $('project-title').textContent = projectLabel(p);
      $('project-path').textContent = p.primary_path || p.key;
      $('crumb').textContent = projectLabel(p);
      const writable = p.path_exists !== false && !!p.primary_path;
      $('project-warning').hidden = writable;
      if (!writable) $('project-warning').textContent = '项目路径不存在：项目级写入与生成简报已禁用。';
      $('project-add-box').hidden = !writable;
      $('handoff-write').disabled = !writable;
      $('rail-project').textContent = p.memory_count || 0;
      $('rail-tasks').textContent = p.task_count || 0;
      $('all-memories').href = '/memories?project=' + enc(p.key) + '&scope=project';
      renderOverview(p);
      bindProjectAdd(p);
      await Promise.all([
        loadProjectMemories(p.key),
        loadProjectUserMemories(p.key),
        loadTasks(p.key),
        loadHandoff(p.key),
        loadProjectSources(p.key),
        loadSessions(p.key)
      ]);
    } catch (e) {
      $('project-title').textContent = '加载失败';
      $('project-warning').hidden = false;
      $('project-warning').textContent = e.message;
    }
  }
  function renderOverview(p) {
    const box = $('overview-grid');
    clear(box);
    const cells = [
      ['路径', p.primary_path || '（未登记路径）'],
      ['最后活动', p.last_memory_at ? fmtTime(p.last_memory_at) : '—'],
      ['路径状态', p.path_exists === false ? '不存在' : '可写'],
      ['登记标识', p.key]
    ];
    cells.forEach(([k, v]) => {
      const c = el('div', 'cell');
      c.appendChild(el('span', 'k', k));
      c.appendChild(el('span', 'v', v));
      box.appendChild(c);
    });
  }
  async function loadProjectMemories(key) {
    const list = $('project-memories');
    clear(list);
    try {
      const d = await api('/v1/memory?project_key=' + enc(key) + '&scope=project&limit=10&stale=0');
      (d.memories || []).forEach((m) => list.appendChild(entry(m, '/p/' + enc(key))));
      if (!d.memories || !d.memories.length) emptyRow(list, '这个项目还没有记忆。');
      $('project-mem-meta').textContent = '共 ' + (d.total || 0) + ' 条';
      $('rail-project').textContent = d.total || 0;
    } catch (e) { emptyRow(list, '项目记忆加载失败：' + e.message); }
  }
  async function loadProjectUserMemories(key) {
    const list = $('project-user-memories');
    clear(list);
    try {
      const d = await api('/v1/memory?scope=user&limit=5&stale=0');
      (d.memories || []).forEach((m) => list.appendChild(entry(m, '/p/' + enc(key))));
      if (!d.memories || !d.memories.length) emptyRow(list, '还没有用户级记忆。');
      $('project-user-meta').textContent = '共 ' + (d.total || 0) + ' 条';
      $('rail-user').textContent = d.total || 0;
    } catch (e) { emptyRow(list, '用户级记忆加载失败：' + e.message); }
  }
  async function loadTasks(key) {
    const box = $('task-list');
    clear(box);
    try {
      const d = await api('/v1/task?project_key=' + enc(key) + '&limit=10');
      const cards = d.cards || [];
      cards.forEach((c) => {
        const card = el('article', 'card');
        const head = el('div', 'card-head');
        head.appendChild(el('strong', '', c.Goal || '（无目标）'));
        head.appendChild(statusStamp(c.Status));
        card.appendChild(head);
        if (c.NextSteps) {
          const p = el('p', '');
          p.appendChild(el('span', 'meta', '下一步：'));
          p.appendChild(document.createTextNode(c.NextSteps));
          card.appendChild(p);
        }
        card.appendChild(el('div', 'meta', '最近活动：' + fmtTime(c.UpdatedAt)));
        if (c.Files && c.Files.length) {
          const det = el('details');
          det.appendChild(el('summary', 'meta', '改动文件（' + c.Files.length + '）'));
          const ul = el('ul');
          c.Files.forEach((f) => ul.appendChild(el('li', '', f)));
          det.appendChild(ul);
          card.appendChild(det);
        }
        box.appendChild(card);
      });
      if (!cards.length) box.appendChild(el('p', 'empty', '还没有任务卡。Qoder 的会话会提炼成任务卡。'));
      $('task-meta').textContent = cards.length ? '共 ' + cards.length + ' 张' : '';
    } catch (e) {
      box.appendChild(el('p', 'empty', '任务卡加载失败：' + e.message));
    }
  }
  async function fetchHandoff(key) {
    const d = await api('/v1/handoff?project_key=' + enc(key) + '&format=json');
    const body = d.body || '';
    $('handoff-body').textContent = body || '无';
    $('handoff-meta').textContent = '约 ' + body.length + ' 字' + (d.truncated ? ' · 已按预算精简' : '');
  }
  async function loadHandoff(key) {
    $('handoff-meta').textContent = '载入中…';
    try { await fetchHandoff(key); }
    catch (e) { $('handoff-meta').textContent = '加载失败：' + e.message; }
  }
  function bindHandoff() {
    $('handoff-preview').addEventListener('click', function () {
      if (!state.project) return;
      busy(this, '预览', () => fetchHandoff(state.project.key));
    });
    $('handoff-write').addEventListener('click', function () {
      if (!state.project || !state.project.primary_path) { toast('error', '这个项目没有可写路径'); return; }
      busy(this, '生成', async () => {
        const d = await api('/v1/handoff', { method: 'POST', body: JSON.stringify({ project_path: state.project.primary_path, write: true }) });
        const body = d.body || '';
        $('handoff-body').textContent = body || '无';
        $('handoff-meta').textContent = '约 ' + body.length + ' 字' + (d.changed ? ' · 已更新' : ' · 内容无变化');
        toast('info', d.changed ? '已更新项目里的交接简报' : '内容无变化，未重写');
      });
    });
  }
  async function loadProjectSources(key) {
    const list = $('source-list');
    clear(list);
    try {
      const d = await api('/v1/sources?project_key=' + enc(key) + '&limit=10');
      const rows = d.sources || [];
      let latest = '';
      const agents = {};
      rows.forEach((r) => {
        if (r.UpdatedAt > latest) latest = r.UpdatedAt;
        agents[r.Agent] = 1;
        const li = el('li', 'src-row' + (r.LastError ? ' failed' : ''));
        li.appendChild(el('span', 'agent', r.Agent));
        li.appendChild(el('span', 'path', r.Path || ''));
        li.appendChild(el('span', 'when', fmtTime(r.UpdatedAt)));
        li.appendChild(el('span', 'size', (r.Size || 0) + ' B'));
        if (r.LastError) {
          const err = el('span', 'err', '原因：' + r.LastError);
          li.appendChild(err);
        }
        list.appendChild(li);
      });
      if (!rows.length) emptyRow(list, '还没有给这个项目贡献过记忆的来源文件。');
      $('source-meta').textContent = '最后同步：' + (latest ? fmtTime(latest) : '—') + ' · 共 ' + (d.total || 0) + ' 个 · 失败 ' + (d.failed || 0);
      $('rail-sources').textContent = d.total || 0;
      $('rail-failed').textContent = d.failed || 0;
      const ov = $('overview-grid');
      if (ov && agents) {
        const c = el('div', 'cell');
        c.appendChild(el('span', 'k', '记忆来源'));
        c.appendChild(el('span', 'v', Object.keys(agents).join(' · ') || '—'));
        ov.appendChild(c);
      }
    } catch (e) { emptyRow(list, '来源加载失败：' + e.message); }
  }
  async function loadSessions(key) {
    const list = $('session-list');
    clear(list);
    try {
      const d = await api('/v1/sessions?project_key=' + enc(key) + '&limit=5');
      const rows = d.sessions || [];
      rows.forEach((s) => {
        const li = el('li', '');
        li.appendChild(el('div', 't', s.title || '（无标题会话）'));
        li.appendChild(el('div', 'm', s.agent + ' · 最近活动 ' + fmtTime(s.updated_at) + ' · 事件 ' + (s.event_count || 0)));
        list.appendChild(li);
      });
      $('session-empty').hidden = rows.length > 0;
    } catch (e) { $('session-empty').hidden = false; $('session-empty').textContent = '会话加载失败：' + e.message; }
  }
  function bindProjectAdd(p) {
    $('project-add').addEventListener('submit', (e) => {
      e.preventDefault();
      const btn = e.currentTarget.querySelector('button');
      busy(btn, '保存', async () => {
        const text = $('project-add-text').value.trim();
        if (!text) { toast('error', '请先写正文'); return; }
        const d = await api('/v1/memory', { method: 'POST', body: JSON.stringify({
          text, type: $('project-add-type').value, scope: 'project', project_path: p.primary_path, agent: 'web'
        }) });
        $('project-add-text').value = '';
        toast('info', d.created ? '已归档 #' + d.id : '相同内容已存在 #' + d.id);
        await loadProjectMemories(p.key);
      });
    });
  }

  /* ---------- 记忆档案 ---------- */
  function initMemories() {
    const params = new URLSearchParams(location.search);
    state.mem = {
      offset: 0, query: params.get('q') || '', scope: params.get('scope') || '',
      project: params.get('project') || '', type: params.get('type') || '',
      stale: params.get('stale') === '1'
    };
    fillTypeSelect($('filter-type'), true);
    $('filter-q').value = state.mem.query;
    $('filter-scope').value = state.mem.scope;
    $('filter-type').value = state.mem.type;
    $('filter-stale').checked = state.mem.stale;
    fillProjectFilter(state.mem.project);
    loadMemories(true);
    $('more').addEventListener('click', () => { state.mem.offset += 50; loadMemories(false); });
  }
  async function fillProjectFilter(selected) {
    const sel = $('filter-project');
    const all = el('option', '', '全部项目');
    all.value = '';
    sel.appendChild(all);
    try {
      const d = await api('/v1/projects');
      (d.projects || []).forEach((p) => {
        const o = el('option', '', projectLabel(p));
        o.value = p.key;
        sel.appendChild(o);
      });
      sel.value = selected || '';
    } catch (e) { /* 过滤下拉失败不阻塞列表 */ }
  }
  async function loadMemories(reset) {
    const m = state.mem;
    const list = $('memory-list');
    if (reset) { m.offset = 0; clear(list); list.appendChild(el('li', 'empty', '载入中…')); }
    try {
      let items, total;
      if (m.query) {
        const d = await api('/v1/search', { method: 'POST', body: JSON.stringify({
          query: m.query, scope: m.scope, project_key: m.project, all: !m.project,
          type: m.type, include_stale: m.stale, limit: 50
        }) });
        items = (d.hits || []).map(normalizeHit);
        total = items.length;
        $('memories-summary').textContent = '找到 ' + total + ' 条（最多显示 50 条）';
      } else {
        const p = ['limit=50', 'offset=' + m.offset];
        if (!m.project) p.push('all=1');
        if (m.scope) p.push('scope=' + enc(m.scope));
        if (m.project) p.push('project_key=' + enc(m.project));
        if (m.type) p.push('type=' + enc(m.type));
        if (!m.stale) p.push('stale=0');
        const d = await api('/v1/memory?' + p.join('&'));
        items = d.memories || [];
        total = typeof d.total === 'number' ? d.total : items.length;
        $('memories-summary').textContent = '共 ' + total + ' 条' + (items.length < total ? '，已显示 ' + (m.offset + items.length) : '');
      }
      if (reset) clear(list);
      items.forEach((item) => list.appendChild(entry(item, location.pathname + location.search)));
      if (!list.children.length) emptyRow(list, '没有符合条件的记忆。');
      m.total = total;
      $('more').hidden = !!m.query || m.offset + items.length >= total;
    } catch (e) {
      clear(list);
      emptyRow(list, '加载失败：' + e.message);
    }
  }

  /* ---------- 来源 ---------- */
  function initSources() {
    const failed = new URLSearchParams(location.search).get('failed') === '1';
    $('filter-failed').checked = failed;
    loadSourcePage(failed);
  }
  async function loadSourcePage(failed) {
    const list = $('source-list');
    clear(list);
    list.appendChild(el('li', 'empty', '载入中…'));
    try {
      const d = await api('/v1/sources?limit=200' + (failed ? '&failed=1' : ''));
      const rows = d.sources || [];
      clear(list);
      rows.forEach((r) => {
        const li = el('li', 'src-row' + (r.LastError ? ' failed' : ''));
        li.appendChild(el('span', 'agent', r.Agent));
        li.appendChild(el('span', 'path', r.Path || ''));
        li.appendChild(el('span', 'when', fmtTime(r.UpdatedAt)));
        li.appendChild(el('span', 'size', (r.Size || 0) + ' B'));
        if (r.LastError) li.appendChild(el('span', 'err', '原因：' + r.LastError));
        list.appendChild(li);
      });
      if (!rows.length) emptyRow(list, failed ? '没有失败的来源。' : '还没有来源。');
      $('sources-summary').textContent = '共 ' + (d.total || 0) + ' 个来源 · 失败 ' + (d.failed || 0) + ' 个' + (failed ? ' · 仅显示失败' : '');
    } catch (e) {
      clear(list);
      emptyRow(list, '加载失败：' + e.message);
    }
  }

  /* ---------- 编辑页 ---------- */
  function safeNext(v) {
    if (!v || v.charAt(0) !== '/' || v.charAt(1) === '/' || v.includes('\\')) return '';
    return v;
  }
  function initEdit() {
    const id = document.body.dataset.memoryId || '';
    const params = new URLSearchParams(location.search);
    const next = safeNext(params.get('next')) || '/memories';
    $('back-link').href = next;
    $('edit-cancel').href = next;
    fillTypeSelect($('edit-type'), false, 'fact');
    api('/v1/memory/item?id=' + enc(id)).then((m) => {
      state.memory = m;
      document.title = '编辑记忆 #' + m.ID + ' · agent-memory';
      $('memory-id').textContent = '#' + m.ID;
      $('edit-text').value = m.Text || '';
      fillTypeSelect($('edit-type'), false, m.Type || 'fact');
      $('edit-scope').textContent = m.Scope === 'user' ? '用户级' : '项目级';
      const imported = (m.Sources || []).some((s) => s.Path && s.Path !== 'mcp');
      const sources = (m.Sources || []).map((s) => s.Agent + ' · ' + (s.Path === 'mcp' ? '手动写入' : (s.Path || '—')));
      $('edit-meta').textContent = '来源：' + (sources.join('；') || '—') + '  ·  首次：' + fmtTime(m.FirstSeenAt) + '  ·  最后变更：' + fmtTime(m.LastSeenAt);
      $('edit-warning').hidden = !imported;
      $('edit-warning').textContent = imported ? '这条记忆来自 agent 文件；源文件再变化时，编辑可能被覆盖。' : '';
      $('edit-stale').textContent = m.Stale ? '恢复使用' : '不再使用';
      $('edit-stale').dataset.stale = m.Stale ? '1' : '0';
    }).catch((e) => {
      $('edit-text').value = '';
      $('edit-meta').textContent = '读取失败：' + e.message;
    });
    $('edit-form').addEventListener('submit', (e) => {
      e.preventDefault();
      if (!state.memory) return;
      const btn = e.currentTarget.querySelector('button[type="submit"]');
      busy(btn, '保存', async () => {
        const d = await api('/v1/memory/update', { method: 'POST', body: JSON.stringify({
          id: state.memory.ID, text: $('edit-text').value, type: $('edit-type').value
        }) });
        if (d.merged_into) {
          toast('info', '内容与 #' + d.merged_into + ' 相同，已合并');
          setTimeout(() => { location.href = next; }, 900);
        } else {
          location.href = next;
        }
      });
    });
    $('edit-stale').addEventListener('click', async function () {
      if (!state.memory) return;
      const btn = this;
      const want = btn.dataset.stale !== '1';
      let ok = false;
      await busy(btn, want ? '恢复' : '标记', async () => {
        await api('/v1/memory/stale', { method: 'POST', body: JSON.stringify({ id: state.memory.ID, stale: want }) });
        state.memory.Stale = want;
        ok = true;
        toast('info', want ? '已恢复使用' : '已标记为不再使用');
      });
      if (ok) {
        btn.dataset.stale = want ? '1' : '0';
        btn.textContent = want ? '恢复使用' : '不再使用';
      }
    });
  }

  /* ---------- 启动 ---------- */
  function init() {
    bindSync();
    revealSections();
    if (!state.token) { showGate(); return; }
    loadHealth();
    const view = document.body.dataset.view;
    if (view === 'home') initHome();
    else if (view === 'project') initProject();
    else if (view === 'memories') initMemories();
    else if (view === 'sources') initSources();
    else if (view === 'edit') initEdit();
  }
  init();
})();
