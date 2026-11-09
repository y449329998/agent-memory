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
  function summaryOf(item) {
    if (!item) return '';
    const state = item.summary_state || item.SummaryState || '';
    return state === 'ready' ? (item.summary || item.Summary || '') : '';
  }
  function paintSummary(boxID, textID, item) {
    const box = $(boxID);
    if (!box) return;
    const summary = summaryOf(item);
    const state = item.summary_state || item.SummaryState || '';
    const pending = state === 'pending' || state === 'processing';
    const failed = state === 'failed';
    box.hidden = !summary && !pending && !failed;
    box.classList.toggle('pending', !summary && pending);
    const badge = box.querySelector('.ai-badge');
    if (badge) badge.textContent = summary ? 'AI 摘要' : (failed ? 'AI 摘要 · 失败' : 'AI 摘要 · 生成中');
    const text = $(textID);
    if (!text) return;
    if (summary) renderMd(text, summary);
    else if (failed) text.textContent = '摘要生成失败，原文与任务状态不受影响。';
    else text.textContent = '摘要生成中，原文仍可阅读和编辑。';
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
  function isSessionTask(t) { return (t.Kind || 'formal') === 'session'; }
  // taskCard 是任务卡的唯一构造入口：项目档案与任务页共用，避免两处排版各自漂移。
  function taskCard(t, opts) {
    const o = opts || {};
    const card = el('article', 'card task-card');
    card.dataset.status = t.Status || 'unknown';
    card.dataset.kind = isSessionTask(t) ? 'session' : 'formal';
    const head = el('div', 'card-head');
    const title = el('a', 'task-link', clipText(plainPreview(t.Goal || '（无目标）'), 100));
    title.href = '/t/' + t.ID;
    head.appendChild(title);
    head.appendChild(statusStamp(t.Status));
    head.appendChild(el('span', 'stamp tiny kind-mark', isSessionTask(t) ? (t.SourceAgent || 'agent') + ' 对话' : '合并任务'));
    head.appendChild(el('span', 'stamp tiny', t.OwnerAgent || '未认领'));
    card.appendChild(head);
    if (isRealTime(t.ArchivedAt)) head.appendChild(el('span', 'stamp tiny stale-mark', '已归档'));
    const bits = ['步骤 ' + taskProgress(t)];
    if (!isSessionTask(t)) bits.push(t.SourceAgent || '手动');
    if (o.when) bits.push('最近更新 ' + fmtTime(t.UpdatedAt));
    card.appendChild(el('div', 'meta', bits.join(' · ')));
    const summary = summaryOf(t);
    if (summary) {
      const wrap = el('div', 'task-summary');
      wrap.appendChild(el('span', 'stamp tiny ai-badge', 'AI 摘要'));
      const md = el('div', 'md md-clamp card-md');
      renderMd(md, summary);
      wrap.appendChild(md);
      card.appendChild(wrap);
    } else if (t.NextSteps) {
      card.appendChild(el('p', 'task-excerpt', clipText(plainPreview(t.NextSteps), 220)));
    }
    if (o.link) {
      const a = el('a', 'more', '打开任务 →');
      a.href = '/t/' + t.ID;
      card.appendChild(a);
    }
    return card;
  }
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
      .replace(/\|?[\s:|-]*-{2,}[\s:|-]*\|/g, ' ')
      .replace(/\|/g, ' ')
      .replace(/[*_`#>]+/g, '')
      .replace(/\s+/g, ' ')
      .trim();
  }
  /* ---------- Markdown 预览（本地渲染，先转义再生成标签） ---------- */
  function escapeHtml(s) {
    return String(s)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;');
  }
  function inlineMd(raw, noLinks) {
    const codes = [];
    let s = escapeHtml(raw);
    s = s.replace(/`([^`\n]+)`/g, function (_, code) {
      codes.push('<code>' + code + '</code>');
      return '\u0002' + (codes.length - 1) + '\u0002';
    });
    s = s.replace(/!\[([^\]]*)\]\(([^)\s]+)(?:\s+&quot;[^&]*&quot;)?\)/g, function (_, alt) {
      return '<span class="md-image">[图片] ' + alt + '</span>';
    });
    s = s.replace(/\[([^\]]+)\]\(([^)\s]+)\)/g, function (_, label, url) {
      if (noLinks || !/^(https?:|mailto:|#|\/)/i.test(url)) return '<span class="md-link">' + label + '</span>';
      return '<a href="' + url + '" target="_blank" rel="noopener noreferrer">' + label + '</a>';
    });
    s = s.replace(/\*\*([^*\n]+)\*\*/g, '<strong>$1</strong>');
    s = s.replace(/(^|[^*])\*([^*\n]+)\*/g, '$1<em>$2</em>');
    s = s.replace(/~~([^~\n]+)~~/g, '<del>$1</del>');
    s = s.replace(/\n/g, '<br>');
    return s.replace(/\u0002(\d+)\u0002/g, function (_, i) { return codes[+i] || ''; });
  }
  function normalizeMarkdown(text) {
    if (!text || text.indexOf('\n') >= 0) return text;
    const codes = [];
    let s = text.replace(/`[^`\n]+`/g, function (code) {
      codes.push(code);
      return '\u0003' + (codes.length - 1) + '\u0003';
    });
    const bullets = (s.match(/\s[-*+]\s/g) || []).length;
    const numbered = (s.match(/\s\d+\.\s/g) || []).length;
    if (!/##/.test(s) && bullets < 2 && numbered < 2) return text;
    s = s.replace(/\s+(#{1,6}\s+)/g, '\n\n$1');
    s = s.replace(/\s+([-*+]\s+)/g, '\n$1');
    s = s.replace(/\s+(\d+\.\s+)/g, '\n$1');
    return s.replace(/\u0003(\d+)\u0003/g, function (_, i) { return codes[+i] || ''; });
  }
  function mdToHtml(source, noLinks) {
    let src = normalizeMarkdown(String(source || '')).replace(/\r\n?/g, '\n');
    const fences = [];
    src = src.replace(/```([^\n`]*)\n([\s\S]*?)```/g, function (_, lang, code) {
      const cls = lang.trim() ? ' class="language-' + escapeHtml(lang.trim()) + '"' : '';
      fences.push('<pre class="md-code"><code' + cls + '>' + escapeHtml(code.replace(/\n$/, '')) + '</code></pre>');
      return '\n\u0001' + (fences.length - 1) + '\u0001\n';
    });
    const lines = src.split('\n');
    const out = [];
    const isBlockStart = function (line, next) {
      if (!line.trim()) return true;
      if (/^\u0001\d+\u0001$/.test(line.trim())) return true;
      if (/^\s{0,3}#{1,6}\s+/.test(line)) return true;
      if (/^\s*([-*_])(?:\s*\1){2,}\s*$/.test(line)) return true;
      if (/^\s*>/.test(line)) return true;
      if (/^\s*(?:[-*+]|\d+\.)\s+/.test(line)) return true;
      if (line.includes('|') && next !== undefined && /^\s*\|?[\s:|-]*-{2,}[\s:|-]*\|?\s*$/.test(next)) return true;
      return false;
    };
    const splitRow = function (row) {
      return row.trim().replace(/^\|/, '').replace(/\|$/, '').split('|').map(function (c) { return c.trim(); });
    };
    let i = 0;
    while (i < lines.length) {
      const line = lines[i];
      if (!line.trim()) { i++; continue; }
      const fence = line.trim().match(/^\u0001(\d+)\u0001$/);
      if (fence) { out.push(fences[+fence[1]]); i++; continue; }
      const heading = line.match(/^\s{0,3}(#{1,6})\s+(.*)$/);
      if (heading) {
        const level = heading[1].length;
        out.push('<h' + level + '>' + inlineMd(heading[2], noLinks) + '</h' + level + '>');
        i++; continue;
      }
      if (/^\s*([-*_])(?:\s*\1){2,}\s*$/.test(line)) { out.push('<hr>'); i++; continue; }
      if (/^\s*>/.test(line)) {
        const quote = [];
        while (i < lines.length && /^\s*>/.test(lines[i])) {
          quote.push(inlineMd(lines[i].replace(/^\s*>\s?/, ''), noLinks));
          i++;
        }
        out.push('<blockquote>' + quote.join('<br>') + '</blockquote>');
        continue;
      }
      if (line.includes('|') && i + 1 < lines.length && /^\s*\|?[\s:|-]*-{2,}[\s:|-]*\|?\s*$/.test(lines[i + 1])) {
        const head = splitRow(line);
        const rows = [];
        i += 2;
        while (i < lines.length && lines[i].trim() && lines[i].includes('|')) {
          rows.push(splitRow(lines[i]));
          i++;
        }
        let table = '<div class="md-table"><table><thead><tr>' +
          head.map(function (c) { return '<th>' + inlineMd(c, noLinks) + '</th>'; }).join('') +
          '</tr></thead><tbody>';
        rows.forEach(function (row) {
          table += '<tr>' + head.map(function (_, idx) { return '<td>' + inlineMd(row[idx] || '', noLinks) + '</td>'; }).join('') + '</tr>';
        });
        out.push(table + '</tbody></table></div>');
        continue;
      }
      if (/^\s*(?:[-*+]|\d+\.)\s+/.test(line)) {
        const ordered = /^\s*\d+\.\s+/.test(line);
        const items = [];
        while (i < lines.length) {
          const item = lines[i].match(/^\s*(?:[-*+]|\d+\.)\s+(.*)$/);
          if (!item) break;
          let text = item[1];
          i++;
          while (i < lines.length && lines[i].trim() && !/^\s*(?:[-*+]|\d+\.)\s+/.test(lines[i]) && !isBlockStart(lines[i], lines[i + 1])) {
            text += '\n' + lines[i].trim();
            i++;
          }
          items.push(inlineMd(text, noLinks));
        }
        out.push((ordered ? '<ol>' : '<ul>') + items.map(function (x) { return '<li>' + x + '</li>'; }).join('') + (ordered ? '</ol>' : '</ul>'));
        continue;
      }
      const para = [line];
      i++;
      while (i < lines.length && lines[i].trim() && !isBlockStart(lines[i], lines[i + 1])) {
        para.push(lines[i]);
        i++;
      }
      out.push('<p>' + inlineMd(para.join('\n'), noLinks) + '</p>');
    }
    return out.join('\n') || '<p class="md-empty">无内容</p>';
  }
  function renderMd(node, text, noLinks) {
    if (!node) return;
    const parsed = new DOMParser().parseFromString(mdToHtml(text, noLinks), 'text/html');
    clear(node);
    Array.from(parsed.body.childNodes).forEach(function (child) { node.appendChild(child); });
  }
  function attachExpand(block) {
    requestAnimationFrame(function () {
      if (!block.isConnected || block.scrollHeight <= block.clientHeight + 8) return;
      const btn = el('button', 'stamp small ghost md-more', '展开全文');
      btn.type = 'button';
      btn.addEventListener('click', function () {
        const open = block.classList.toggle('open');
        btn.textContent = open ? '收起' : '展开全文';
      });
      block.after(btn);
    });
  }
  function clipText(text, n) {
    const chars = Array.from(text);
    return chars.length > n ? chars.slice(0, n - 1).join('') + '…' : text;
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
    const sections = Array.from(document.querySelectorAll('.section'));
    if (!('IntersectionObserver' in window)) {
      sections.forEach((s) => s.classList.add('in'));
      return;
    }
    sections.forEach((s) => s.classList.add('reveal'));
    const io = new IntersectionObserver((entries) => {
      entries.forEach((e) => {
        if (e.isIntersecting) { e.target.classList.add('in'); io.unobserve(e.target); }
      });
    }, { rootMargin: '0px 0px -40px 0px' });
    sections.forEach((s) => io.observe(s));
    // 兜底：无头截图、后台标签页或不支持 IO 回调时，别让正文一直停在 opacity:0。
    setTimeout(() => sections.forEach((s) => s.classList.add('in')), 900);
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
    const summary = summaryOf(m);
    if (summary) head.appendChild(el('span', 'stamp tiny ai-badge', 'AI 摘要'));
    a.appendChild(head);
    const preview = el('div', 'entry-text md md-clamp entry-md');
    renderMd(preview, clip(summary || (m.Text || ''), 3000), true);
    a.appendChild(preview);
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
    return { ID: h.id, Scope: h.scope, Type: h.type, Text: h.text, Stale: h.stale, LastSeenAt: h.last_seen_at, Sources: h.sources || [], Summary: h.summary, SummaryState: h.summary_state };
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
      countUp($('stat-tasks'), d.tasks);
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
    rail.appendChild(railItem('任务', 'rail-tasks'));
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
      $('task-more').href = '/tasks?project_key=' + enc(p.key);
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
  function taskProgress(t) {
    const steps = t.Steps || t.steps || [];
    let done = 0;
    steps.forEach((s) => { if (s.Status === 'done' || s.Status === 'skipped' || s.status === 'done' || s.status === 'skipped') done++; });
    return steps.length ? done + '/' + steps.length : '无步骤';
  }
  async function loadTasks(key) {
    const box = $('task-list');
    clear(box);
    try {
      const d = await api('/v1/tasks?project_key=' + enc(key) + '&limit=20');
      const tasks = d.tasks || [];
      tasks.forEach((t) => box.appendChild(taskCard(t, { when: true })));
      if (!tasks.length) box.appendChild(el('p', 'empty', '还没有任务。'));
      $('task-meta').textContent = tasks.length ? '共 ' + tasks.length + ' 个任务' : '';
    } catch (e) {
      box.appendChild(el('p', 'empty', '任务加载失败：' + e.message));
    }
  }
  async function fetchHandoff(key) {
    const d = await api('/v1/handoff?project_key=' + enc(key) + '&format=json');
    const body = d.body || '';
    renderMd($('handoff-body'), body || '无');
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
        renderMd($('handoff-body'), body || '无');
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
    bindMemFilters();
  }
  // bindMemFilters 让筛选条件一改就出结果：地址栏用 replaceState 同步，不整页刷新。
  function bindMemFilters() {
    const form = $('filters');
    const text = $('filter-q');
    if (!form || !text) return;
    let timer = null;
    function apply() {
      state.mem.query = text.value.trim();
      state.mem.scope = $('filter-scope').value;
      state.mem.project = $('filter-project').value;
      state.mem.type = $('filter-type').value;
      state.mem.stale = $('filter-stale').checked;
      const qs = new URLSearchParams();
      if (state.mem.query) qs.set('q', state.mem.query);
      if (state.mem.scope) qs.set('scope', state.mem.scope);
      if (state.mem.project) qs.set('project', state.mem.project);
      if (state.mem.type) qs.set('type', state.mem.type);
      if (state.mem.stale) qs.set('stale', '1');
      history.replaceState(null, '', qs.toString() ? '/memories?' + qs.toString() : '/memories');
      loadMemories(true);
    }
    form.addEventListener('submit', (e) => { e.preventDefault(); apply(); });
    form.addEventListener('change', apply);
    text.addEventListener('input', () => { clearTimeout(timer); timer = setTimeout(apply, 420); });
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
    function run() {
      const on = $('filter-failed').checked;
      history.replaceState(null, '', on ? '/sources?failed=1' : '/sources');
      loadSourcePage(on);
    }
    const form = $('filter-failed').closest('form');
    if (form) form.addEventListener('submit', (e) => { e.preventDefault(); run(); });
    $('filter-failed').addEventListener('change', run);
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

  /* ---------- 设置 ---------- */
  async function initSettings() {
    let data = null;
    function showSection(name) {
      if (['service', 'llm', 'scan', 'data'].indexOf(name) < 0) name = 'service';
      ['service', 'llm', 'scan', 'data'].forEach((key) => {
        $('settings-' + key).hidden = key !== name;
        const btn = document.querySelector('.settings-nav button[data-section="' + key + '"]');
        if (!btn) return;
        btn.classList.toggle('active', key === name);
        if (key === name) btn.setAttribute('aria-current', 'true');
        else btn.removeAttribute('aria-current');
      });
      try { history.replaceState(null, '', '#' + name); } catch (e) {}
    }
    function fmtSize(n) {
      n = Number(n) || 0;
      if (n >= 1048576) return (n / 1048576).toFixed(1) + ' MB';
      return Math.round(n / 1024) + ' KB';
    }
    function paintQueue(l) {
      $('llm-pending').textContent = l.summary_pending || 0;
      $('llm-ready').textContent = l.summary_ready || 0;
      $('llm-failed').textContent = l.summary_failed || 0;
      const total = (l.summary_pending || 0) + (l.summary_ready || 0) + (l.summary_failed || 0);
      $('llm-counts').textContent = total ? '共 ' + total + ' 条摘要任务' : '还没有摘要任务';
    }
    Array.from(document.querySelectorAll('.settings-nav button')).forEach((b) => {
      b.addEventListener('click', () => showSection(b.dataset.section));
    });
    window.addEventListener('hashchange', () => showSection((location.hash || '').replace('#', '')));
    function fill() {
      const s = data.server, d = data.desktop, sc = data.scan, l = data.llm, da = data.data;
      const runningPort = s.running_port || s.port;
      $('sr-port').textContent = runningPort || '—';
      $('sr-port').title = '配置端口 ' + s.port + (s.running_port && s.running_port !== s.port ? ' · 当前运行 ' + s.running_port : '');
      $('sr-llm').textContent = l.enabled ? 'ON' : 'OFF';
      $('sr-llm').dataset.state = l.enabled ? 'on' : 'off';
      $('sr-llm').title = l.model || '';
      $('sr-tray').textContent = d.close_to_tray ? 'ON' : 'OFF';
      $('sr-tray').dataset.state = d.close_to_tray ? 'on' : 'off';
      $('sr-db').textContent = fmtSize(da.db_size);
      $('svc-port').value = s.port;
      $('svc-running').textContent = s.running_port && s.running_port !== s.port
        ? '当前实际运行端口 ' + s.running_port + '，保存后切换到 ' + s.port + '。'
        : '';
      $('svc-mcp-binary').value = s.mcp_binary || '';
      $('svc-autostart').checked = d.autostart;
      $('svc-close-tray').checked = d.close_to_tray;
      $('llm-enabled').checked = l.enabled;
      $('llm-base').value = l.base_url || '';
      $('llm-model').value = l.model || '';
      $('llm-timeout').value = l.timeout_seconds || 30;
      $('llm-maxchars').value = l.max_input_chars || 12000;
      $('llm-key').placeholder = l.api_key_configured ? '已保存，留空不修改' : '未配置';
      $('llm-status').textContent = l.enabled && (!l.base_url || !l.model || !l.api_key_configured)
        ? '配置不完整（需要 base_url、模型与 API Key），摘要不会启动'
        : '';
      paintQueue(l);
      $('scan-interval').value = sc.interval_seconds;
      $('scan-budget').value = sc.budget_tokens;
      $('scan-recent').value = sc.recent_cards;
      $('scan-settle').value = sc.settle_idle_seconds;
      $('scan-qoder').checked = !sc.qoder_disabled;
      $('scan-trae').checked = !sc.trae_disabled;
      $('scan-workbuddy').checked = !sc.workbuddy_disabled;
      const info = $('data-info');
      clear(info);
      const cell = (k, v) => {
        const c = el('div', 'cell');
        c.appendChild(el('span', 'k', k));
        c.appendChild(el('span', 'v', v || '—'));
        info.appendChild(c);
      };
      cell('数据目录', da.data_dir);
      cell('库大小', fmtSize(da.db_size));
      cell('最近备份', da.last_backup || '尚无');
      cell('摘要队列', '待处理 ' + (l.summary_pending || 0) + ' · 完成 ' + (l.summary_ready || 0) + ' · 失败 ' + (l.summary_failed || 0));
    }
    try {
      data = await api('/v1/settings');
      fill();
      showSection((location.hash || '').replace('#', '') || 'service');
    } catch (e) {
      toast('error', '设置加载失败：' + e.message);
    }
    $('form-service').addEventListener('submit', function (e) {
      e.preventDefault();
      const btn = this.querySelector('button[type="submit"]');
      busy(btn, '保存', async function () {
        const d = await api('/v1/settings/service', {
          method: 'POST', body: JSON.stringify({
            port: Number($('svc-port').value),
            mcp_binary_path: $('svc-mcp-binary').value.trim(),
            autostart: $('svc-autostart').checked,
            close_to_tray: $('svc-close-tray').checked
          })
        });
        if (d.warnings && d.warnings.length) toast('error', d.warnings.join('；'));
        if (d.port && String(d.port) !== location.port) {
          // sessionStorage 按 origin 隔离；换端口后要重新带上令牌，否则新页面会要求登录。
          const next = location.protocol + '//' + location.hostname + ':' + d.port +
            '/settings?token=' + enc(state.token);
          setTimeout(() => { location.href = next; }, d.warnings && d.warnings.length ? 1600 : 400);
          return;
        }
        data = await api('/v1/settings');
        fill();
        showSection('service');
        $('svc-status').textContent = '已保存';
        toast('info', '服务设置已保存');
      });
    });
    $('form-llm').addEventListener('submit', function (e) {
      e.preventDefault();
      const btn = this.querySelector('button[type="submit"]');
      busy(btn, '保存', async function () {
        const d = await api('/v1/settings/llm', {
          method: 'POST', body: JSON.stringify({
            enabled: $('llm-enabled').checked,
            base_url: $('llm-base').value.trim(),
            model: $('llm-model').value.trim(),
            timeout_seconds: Number($('llm-timeout').value),
            max_input_chars: Number($('llm-maxchars').value),
            api_key: $('llm-key').value,
            clear_api_key: $('llm-clear-key').checked
          })
        });
        $('llm-key').value = '';
        $('llm-clear-key').checked = false;
        toast(d.restart_required ? 'info' : 'info', d.restart_required ? '设置已保存，重启后生效' : 'LLM 设置已保存');
        data = await api('/v1/settings');
        fill();
      });
    });
    $('llm-test').addEventListener('click', function () {
      const btn = this;
      busy(btn, '测试', async function () {
        const d = await api('/v1/settings/llm/test', { method: 'POST', body: '{}' });
        $('llm-status').textContent = d.ok ? ('连接成功 · ' + d.model + ' · ' + d.elapsed_ms + 'ms') : ('连接失败：' + d.error);
        toast(d.ok ? 'info' : 'error', d.ok ? '模型连接成功' : '模型连接失败');
      });
    });
    $('llm-retry').addEventListener('click', function () {
      const btn = this;
      busy(btn, '重试', async function () {
        const d = await api('/v1/settings/llm/retry', { method: 'POST', body: '{}' });
        toast('info', d.retried ? ('已重新排队 ' + d.retried + ' 条摘要') : '没有失败的摘要需要重试');
        const latest = await api('/v1/settings');
        paintQueue(latest.llm);
      });
    });
    $('form-scan').addEventListener('submit', function (e) {
      e.preventDefault();
      const btn = this.querySelector('button[type="submit"]');
      busy(btn, '保存', async function () {
        const d = await api('/v1/settings/scan', {
          method: 'POST', body: JSON.stringify({
            interval_seconds: Number($('scan-interval').value),
            budget_tokens: Number($('scan-budget').value),
            recent_cards: Number($('scan-recent').value),
            settle_idle_seconds: Number($('scan-settle').value),
            qoder_disabled: !$('scan-qoder').checked,
            trae_disabled: !$('scan-trae').checked,
            workbuddy_disabled: !$('scan-workbuddy').checked
          })
        });
        toast(d.restart_required ? 'info' : 'info', d.restart_required ? '已保存，重启服务后生效' : '扫描设置已保存');
        $('scan-status').textContent = d.restart_required ? '需重启' : '已生效';
      });
    });
    $('data-backup').addEventListener('click', function () {
      const btn = this;
      busy(btn, '备份', async function () {
        const d = await api('/v1/data/backup', { method: 'POST', body: '{}' });
        $('data-status').textContent = '已备份：' + (d.path || '');
        toast('info', '备份完成');
        data = await api('/v1/settings');
        fill();
      });
    });
    $('data-open').addEventListener('click', async function () {
      try { await api('/v1/data/open', { method: 'POST', body: '{}' }); toast('info', '已打开数据目录'); }
      catch (e) { toast('error', e.message); }
    });
    $('data-export').addEventListener('click', async function (e) {
      e.preventDefault();
      try {
        const res = await fetch('/v1/data/export', { headers: { Authorization: 'Bearer ' + state.token } });
        if (!res.ok) throw new Error('HTTP ' + res.status);
        const blob = await res.blob();
        const a = el('a');
        a.href = URL.createObjectURL(blob);
        a.download = 'agent-memory-export.json';
        a.click();
        setTimeout(() => URL.revokeObjectURL(a.href), 2000);
      } catch (err) { toast('error', '导出失败：' + err.message); }
    });
    let importToken = '';
    $('data-preview').addEventListener('click', async function () {
      const file = $('data-import-file').files[0];
      if (!file) { toast('error', '请先选择 JSON 文件'); return; }
      try {
        const fd = new FormData();
        fd.append('file', file);
        const res = await fetch('/v1/data/import', {
          method: 'POST', headers: { Authorization: 'Bearer ' + state.token }, body: fd
        });
        const text = await res.text();
        let d = {};
        try { d = text ? JSON.parse(text) : {}; } catch (e) { d = { error: text.slice(0, 200) }; }
        if (!res.ok) { toast('error', d.error || '预览失败'); return; }
        importToken = d.import_token;
        $('data-apply').disabled = !importToken;
        const box = $('data-preview-box');
        box.textContent = '将处理：项目 ' + d.projects + '，记忆 ' + d.memories + '，任务 ' + d.tasks;
        if (d.errors && d.errors.length) box.textContent += '；跳过 ' + d.errors.length + ' 项：' + d.errors.slice(0, 3).join('；');
      } catch (err) {
        toast('error', '预览失败：' + err.message);
      }
    });
    $('data-apply').addEventListener('click', async function () {
      if (!importToken) return;
      const btn = this;
      busy(btn, '导入', async function () {
        const d = await api('/v1/data/import/apply', { method: 'POST', body: JSON.stringify({ import_token: importToken }) });
        toast('info', '导入完成：项目 ' + d.projects + '，记忆 ' + d.memories + '，任务 ' + d.tasks);
        importToken = '';
        $('data-apply').disabled = true;
        $('data-preview-box').textContent = '导入完成';
      });
    });
  }

  /* ---------- MCP 接驳台 ---------- */
  async function initMCP() {
    const CLIENTS = {
      qoder: { label: 'Qoder', path: '~/.qoder-cn/mcp.json' },
      opencode: { label: 'OpenCode', path: '~/.config/opencode/opencode.json' },
      claude: { label: 'Claude Code', path: '~/.claude.json' },
      generic: { label: '通用 stdio', path: '任意 MCP 客户端配置' }
    };
    let data = null;
    let tab = 'qoder';
    const code = $('mcp-code');
    const gutter = $('mcp-code-gutter');
    function paintTab(name) {
      tab = name;
      Array.from(document.querySelectorAll('#mcp-tabs button')).forEach((b) => {
        const active = b.dataset.tab === name;
        b.setAttribute('aria-selected', active ? 'true' : 'false');
      });
      const snippet = (data && data.snippets && data.snippets[name]) || '';
      code.textContent = snippet;
      const lines = snippet.replace(/\n$/, '').split('\n');
      gutter.textContent = lines.map((_, i) => String(i + 1).padStart(2, '0')).join('\n');
      $('mcp-client-label').textContent = CLIENTS[name].label;
      $('mcp-client-path').textContent = CLIENTS[name].path;
    }
    try {
      data = await api('/v1/mcp/snippets');
      $('mcp-port').textContent = data.port || '—';
      $('mcp-host').textContent = '127.0.0.1:' + (data.port || '—');
      $('mcp-binary').textContent = data.binary || '—';
      $('mcp-token').textContent = data.token_masked || '—';
      $('mcp-status').textContent = '服务在线';
      $('mcp-verified').textContent = '';
      (data.verified || []).forEach((agent) => {
        $('mcp-verified').appendChild(el('span', 'verify-chip', (CLIENTS[agent] ? CLIENTS[agent].label : agent) + ' 已验证'));
      });
      $('mcp-verified').appendChild(el('span', 'verify-chip template', '通用模板'));
      Array.from($('mcp-tabs').children).forEach((b) => b.addEventListener('click', () => paintTab(b.dataset.tab)));
      paintTab('qoder');
      $('mcp-copy').addEventListener('click', async () => {
        const btn = $('mcp-copy');
        const text = data.snippets[tab] || '';
        try {
          await navigator.clipboard.writeText(text);
          btn.textContent = '已复制';
          btn.classList.add('copied');
          toast('info', '配置已复制，粘贴到客户端后重启 agent');
          setTimeout(() => { btn.textContent = '复制配置'; btn.classList.remove('copied'); }, 1800);
        } catch (e) {
          window.prompt('复制失败，请手动复制', text);
        }
      });
    } catch (e) {
      code.textContent = '加载失败：' + e.message;
      gutter.textContent = '';
      $('mcp-status').textContent = '连接失败';
      const live = document.querySelector('.dock-live');
      if (live) live.classList.add('error');
      toast('error', 'MCP 配置加载失败');
    }
  }

  function enhanceSelect(sel) {
    if (!sel || sel.dataset.enhanced === '1') return;
    sel.dataset.enhanced = '1';
    const shell = el('div', 'select-shell');
    sel.parentNode.insertBefore(shell, sel);
    shell.appendChild(sel);
    sel.classList.add('select-native');
    sel.tabIndex = -1;
    const trigger = el('button', 'select-trigger');
    trigger.type = 'button';
    trigger.setAttribute('aria-haspopup', 'listbox');
    trigger.setAttribute('aria-expanded', 'false');
    const field = shell.parentNode;
    const label = field && field.querySelector(':scope > span');
    if (label) trigger.setAttribute('aria-label', label.textContent);
    const menu = el('div', 'select-menu');
    menu.hidden = true;
    menu.setAttribute('role', 'listbox');
    shell.appendChild(trigger);
    shell.appendChild(menu);
    function sync() {
      const opt = sel.options[sel.selectedIndex];
      trigger.textContent = opt ? opt.textContent : '—';
    }
    function close() {
      menu.hidden = true;
      trigger.setAttribute('aria-expanded', 'false');
    }
    function open() {
      clear(menu);
      Array.from(sel.options).forEach(function (opt, i) {
        const item = el('button', 'select-option');
        item.type = 'button';
        item.setAttribute('role', 'option');
        item.textContent = opt.textContent;
        if (i === sel.selectedIndex) item.classList.add('selected');
        item.addEventListener('click', function () {
          sel.selectedIndex = i;
          sync();
          close();
          sel.dispatchEvent(new Event('change', { bubbles: true }));
          trigger.focus();
        });
        menu.appendChild(item);
      });
      menu.hidden = false;
      trigger.setAttribute('aria-expanded', 'true');
    }
    trigger.addEventListener('click', function (e) {
      e.preventDefault();
      if (menu.hidden) open();
      else close();
    });
    trigger.addEventListener('keydown', function (e) {
      if (e.key === 'ArrowDown' || e.key === 'Enter' || e.key === ' ') {
        e.preventDefault();
        if (menu.hidden) open();
      } else if (e.key === 'Escape') {
        close();
      }
    });
    menu.addEventListener('keydown', function (e) {
      const items = Array.from(menu.children);
      const at = items.indexOf(document.activeElement);
      if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
        e.preventDefault();
        const next = items[Math.max(0, Math.min(items.length - 1, at + (e.key === 'ArrowDown' ? 1 : -1)))];
        if (next) next.focus();
      } else if (e.key === 'Escape') {
        close();
        trigger.focus();
      }
    });
    sel.addEventListener('change', sync);
    document.addEventListener('click', function (e) {
      if (!shell.contains(e.target)) close();
    });
    sel._syncSelect = sync;
    sync();
  }

  // enhanceAllSelects 把暗色自定义下拉应用到 root 内的所有 select（含动态插入的步骤行）。
  function enhanceAllSelects(root) {
    if (!root || !root.querySelectorAll) return;
    if (root.matches && root.matches('select')) enhanceSelect(root);
    root.querySelectorAll('select').forEach(enhanceSelect);
  }

  function observeSelects() {
    enhanceAllSelects(document);
    const mo = new MutationObserver(function (muts) {
      muts.forEach(function (m) {
        m.addedNodes.forEach(function (n) { if (n.nodeType === 1) enhanceAllSelects(n); });
      });
    });
    mo.observe(document.body, { childList: true, subtree: true });
  }

  /* ---------- 任务页 ---------- */
  async function initTasks() {
    const list = $('task-cards');
    const params = new URLSearchParams(location.search);
    const q = $('tf-q'), project = $('tf-project'), status = $('tf-status');
    const owner = $('tf-owner'), source = $('tf-source'), range = $('tf-range'), sort = $('tf-sort');
    const kind = $('tf-kind');
    q.value = params.get('q') || '';
    status.value = params.get('status') || '';
    owner.value = params.get('owner_agent') || '';
    source.value = params.get('source_agent') || '';
    range.value = params.get('range') || '';
    sort.value = params.get('sort') || '';
    // 默认「全部」：MCP / 桌面端新建的是合并任务，只给对话任务视图会把它们藏起来。
    kind.value = params.get('kind') || '';
    $('tasks-archived').checked = params.get('archived') === '1';
    [kind, project, status, source, range, sort].forEach(enhanceSelect);
    api('/v1/projects').then((d) => {
      (d.projects || []).forEach((p) => {
        const option = el('option', '', p.display_name || p.key);
        option.value = p.key;
        project.appendChild(option);
      });
      project.value = params.get('project_key') || '';
      if (project._syncSelect) project._syncSelect();
    }).catch(() => {}).finally(() => { load(); });
    async function load() {
      clear(list);
      list.appendChild(el('p', 'empty', '载入中…'));
      try {
        const query = ['limit=100'];
        if (kind.value) query.push('kind=' + enc(kind.value));
        if (q.value.trim()) query.push('q=' + enc(q.value.trim()));
        if (project.value) query.push('project_key=' + enc(project.value));
        if (status.value) query.push('status=' + enc(status.value));
        if (owner.value.trim()) query.push('owner_agent=' + enc(owner.value.trim()));
        if (source.value) query.push('source_agent=' + enc(source.value));
        if (range.value) {
          const days = Number(range.value);
          const after = new Date(Date.now() - days * 86400000).toISOString();
          query.push('updated_after=' + enc(after));
        }
        if (sort.value) query.push('sort=' + enc(sort.value));
        if ($('tasks-archived').checked) query.push('include_archived=1');
        const d = await api('/v1/tasks?' + query.join('&'));
        const tasks = d.tasks || [];
        clear(list);
        tasks.forEach((t) => list.appendChild(taskCard(t, { when: true, link: true })));
        if (!tasks.length) list.appendChild(el('p', 'empty', kind.value === 'session' ? '还没有对话任务，跑一轮同步就会把各 agent 的会话整理进来。' : '还没有任务。'));
        const sortLabel = sort.options[sort.selectedIndex] ? sort.options[sort.selectedIndex].textContent : '最近对话';
        $('tasks-meta').textContent = '共 ' + tasks.length + ' 条 · 按' + sortLabel + '排序';
      } catch (e) {
        clear(list);
        list.appendChild(el('p', 'empty', '加载失败：' + e.message));
      }
    }
    function applyFilters() {
      const qs = new URLSearchParams();
      qs.set('kind', kind.value);
      if (q.value.trim()) qs.set('q', q.value.trim());
      if (project.value) qs.set('project_key', project.value);
      if (status.value) qs.set('status', status.value);
      if (owner.value.trim()) qs.set('owner_agent', owner.value.trim());
      if (source.value) qs.set('source_agent', source.value);
      if (range.value) qs.set('range', range.value);
      if (sort.value) qs.set('sort', sort.value);
      if ($('tasks-archived').checked) qs.set('archived', '1');
      history.replaceState(null, '', qs.toString() ? '/tasks?' + qs.toString() : '/tasks');
      load();
    }
    const filters = $('task-filters');
    filters.addEventListener('submit', (e) => { e.preventDefault(); applyFilters(); });
    filters.addEventListener('change', applyFilters);
    let filterTimer = null;
    [q, owner].forEach(function (inp) {
      inp.addEventListener('input', function () {
        clearTimeout(filterTimer);
        filterTimer = setTimeout(applyFilters, 420);
      });
    });
    $('tf-reset').addEventListener('click', () => { location.search = ''; });
  }

  function isRealTime(v) {
    return !!v && new Date(v).getFullYear() > 1;
  }

  function initTask() {
    const id = document.body.dataset.taskId || '';
    const isNew = !id;
    const form = $('task-form');
    const stepsBox = $('task-steps');
    const rows = [];
    const projectField = $('task-project-field');
    if (!isNew) projectField.hidden = true;
    const statusSel = $('task-status');
    [['doing', '进行中'], ['blocked', '受阻'], ['done', '已完成'], ['unknown', '未知']].forEach(function (pair) {
      const opt = el('option', '', pair[1]); opt.value = pair[0]; statusSel.appendChild(opt);
    });
    statusSel.value = 'doing';

    function paintProgress() {
      const box = $('step-progress');
      if (!box) return;
      const done = rows.filter(function (r) { return r.status.value === 'done' || r.status.value === 'skipped'; }).length;
      box.textContent = '进度 ' + done + '/' + rows.length;
    }
    function stableKey(k) { return !/^(cmd|err|next|file):/.test(k); }
    function addStep(step) {
      const row = el('div', 'step-row');
      const status = el('select');
      status.setAttribute('aria-label', '步骤状态');
      [['pending', '待办'], ['doing', '进行中'], ['done', '完成'], ['blocked', '受阻'], ['skipped', '跳过']].forEach(function (pair) {
        const opt = el('option', '', pair[1]); opt.value = pair[0]; status.appendChild(opt);
      });
      status.value = step.Status || step.status || 'pending';
      const subject = el('input');
      subject.type = 'text';
      subject.value = step.Subject || step.subject || '';
      subject.placeholder = '步骤内容';
      const remove = el('button', 'step-remove', '×');
      remove.type = 'button';
      remove.title = '删除步骤';
      const entry = { row: row, status: status, subject: subject, sourceKey: step.SourceKey || step.source_key || '' };
      remove.addEventListener('click', function () {
        row.remove();
        const i = rows.indexOf(entry);
        if (i >= 0) rows.splice(i, 1);
        paintProgress();
      });
      status.addEventListener('change', async function () {
        if (!isNew && entry.sourceKey && stableKey(entry.sourceKey)) {
          try {
            await api('/v1/tasks/step/status', { method: 'POST', body: JSON.stringify({ task_id: Number(id), source_key: entry.sourceKey, status: status.value }) });
          } catch (e) { toast('error', '状态保存失败：' + e.message); }
        }
        paintProgress();
      });
      rows.push(entry);
      row.appendChild(status); row.appendChild(subject); row.appendChild(remove);
      stepsBox.appendChild(row);
      paintProgress();
    }
    function readSteps() {
      return rows.filter(function (r) { return r.subject.value.trim() !== ''; }).map(function (r) {
        return { subject: r.subject.value.trim(), status: r.status.value };
      });
    }
    function renderNextPreview() {
      const box = $('task-next-preview');
      if (box) renderMd(box, $('task-next').value || '');
    }
    $('task-next').addEventListener('input', renderNextPreview);

    $('task-add-step').addEventListener('click', function () { addStep({ Status: 'pending' }); });

    let convNext = 0, convSize = 0, convMode = 'db';
    async function loadConversation(reset) {
      const box = $('snippet-list');
      if (!box) return;
      if (reset) { clear(box); convNext = 0; convMode = 'db'; }
      try {
        const param = convMode === 'db' ? '&before_seq=' + convNext : '&before=' + convNext;
        const d = await api('/v1/task/conversation?task_id=' + enc(id) + param + '&limit=20');
        if (d.warning) { $('conv-warn').hidden = false; $('conv-warn').textContent = d.warning; }
        else { $('conv-warn').hidden = true; }
        convSize = d.file_size || convSize;
        convMode = d.source === 'db' ? 'db' : (d.source === 'file' ? 'file' : 'store');
        const frag = document.createDocumentFragment();
        // 最新在最上面，「加载更早」把更早的消息接在下面
        (d.messages || []).slice().reverse().forEach(function (m) {
          const wrap = el('div', 'snippet');
          wrap.appendChild(el('div', 'meta', (m.Role || m.role || '') + ' · ' + fmtTime(m.Ts || m.ts)));
          const body = el('div', 'md');
          renderMd(body, m.Text || m.text || ('[' + (m.Tool || m.tool || '') + ']'));
          if (m.Truncated || m.truncated) body.appendChild(el('span', 'meta', ' …源文件里更长'));
          wrap.appendChild(body);
          frag.appendChild(wrap);
        });
        box.appendChild(frag);
        if (d.source === 'db') {
          convNext = d.next || 0;
          $('conv-more').hidden = !convNext;
        } else if (d.source === 'file') {
          convNext = d.next || 0;
          $('conv-more').hidden = !convNext;
        } else {
          convNext = convSize;
          $('conv-more').hidden = !(d.source === 'store' && convSize > 0);
        }
        if (!(d.messages || []).length && reset) box.appendChild(el('p', 'empty', '还没有对话片段。'));
      } catch (e) { box.appendChild(el('p', 'empty', '对话加载失败：' + e.message)); }
    }
    async function loadFiles() {
      const box = $('file-list');
      if (!box) return;
      clear(box);
      try {
        const d = await api('/v1/task/files?task_id=' + enc(id));
        (d.files || []).forEach(function (f) {
          const row = el('div', 'src-row');
          row.appendChild(el('span', 'path', f.path));
          row.appendChild(el('span', 'when', fmtTime(f.last_ts) + ' · ' + f.count + ' 次 · ' + (f.agent || '')));
          box.appendChild(row);
        });
        if (!(d.files || []).length) box.appendChild(el('p', 'empty', '这场对话没有文件改动'));
        $('files-copy').onclick = function () {
          navigator.clipboard.writeText((d.files || []).map(function (f) { return f.path; }).join('\n'));
          toast('info', '已复制全部路径');
        };
      } catch (e) { box.appendChild(el('p', 'empty', '文件加载失败：' + e.message)); }
    }
    async function copyText(md) {
      try {
        await navigator.clipboard.writeText(md);
        toast('info', '已复制，粘给下一个 agent 即可');
      } catch (e) { window.prompt('复制失败，请手动复制', md); }
    }
    async function loadHandoff() {
      try {
        const brief = await api('/v1/tasks/export?id=' + enc(id) + '&mode=brief');
        renderMd($('handoff-brief'), brief.markdown || '');
        $('handoff-copy-brief').onclick = function () { copyText(brief.markdown || ''); };
        const full = await api('/v1/tasks/export?id=' + enc(id));
        renderMd($('handoff-full'), full.markdown || '');
        $('handoff-copy-full').onclick = function () { copyText(full.markdown || ''); };
      } catch (e) { renderMd($('handoff-brief'), '交接加载失败：' + e.message); }
    }
    function showTab(name) {
      [].slice.call(document.querySelectorAll('#task-tabs .tab')).forEach(function (t) {
        t.classList.toggle('active', t.dataset.tab === name);
      });
      ['conversation', 'steps', 'files', 'handoff'].forEach(function (p) {
        const node = $('pane-' + p);
        if (node) node.hidden = p !== name;
      });
      if (isNew) return;
      const u = new URL(location.href);
      u.searchParams.set('tab', name);
      history.replaceState(null, '', u);
      if (name === 'files') loadFiles();
      if (name === 'handoff') loadHandoff();
    }
    function initTabs() {
      const bar = $('task-tabs');
      if (!bar) return;
      if (isNew) { bar.hidden = true; showTab('steps'); return; }
      [].slice.call(bar.querySelectorAll('.tab')).forEach(function (t) {
        t.addEventListener('click', function () { showTab(t.dataset.tab); });
      });
      const want = new URLSearchParams(location.search).get('tab') || 'conversation';
      showTab(['conversation', 'steps', 'files', 'handoff'].indexOf(want) >= 0 ? want : 'conversation');
    }
    $('conv-more').addEventListener('click', function () { loadConversation(false); });
    $('task-save-steps').addEventListener('click', function () {
      if (isNew) return;
      if (!window.confirm('保存步骤后，这条任务将不再由扫描自动同步。继续？')) return;
      const btn = this;
      busy(btn, '保存', async function () {
        await api('/v1/tasks/update', { method: 'POST', body: JSON.stringify({ id: Number(id), steps: readSteps() }) });
        toast('info', '步骤已保存');
        location.reload();
      });
    });

    async function load() {
      if (isNew) { addStep({ Status: 'pending' }); renderNextPreview(); return; }
      const d = await api('/v1/tasks/item?id=' + enc(id));
      $('task-id').textContent = '#' + d.ID;
      $('task-title').textContent = clipText(plainPreview(d.Goal || '任务'), 28);
      $('task-crumb').textContent = '#' + d.ID;
      $('task-goal').value = d.Goal || '';
      statusSel.value = d.Status || 'doing';
      $('task-owner').value = d.OwnerAgent || '';
      $('task-next').value = d.NextSteps || '';
      renderNextPreview();
      (d.Steps || []).forEach(addStep);
      if (!rows.length) addStep({ Status: 'pending' });
      const kindName = (d.Kind || 'formal') === 'session' ? '对话任务' : '合并任务';
      const kindNote = kindName === '对话任务' ? ' · 步骤取自那次对话，保存后不再被扫描覆盖' : '';
      $('task-meta').textContent = kindName + ' · 来源 ' + (d.SourceAgent || '手动') + ' · 最近更新 ' + fmtTime(d.UpdatedAt) + (isRealTime(d.ArchivedAt) ? ' · 已归档' : '') + kindNote;
      paintSummary('task-ai-summary', 'task-ai-summary-text', d);
      $('task-archive').textContent = isRealTime(d.ArchivedAt) ? '恢复' : '归档';
      paintProgress();
      const sessionBox = $('session-list');
      clear(sessionBox);
      (d.Sessions || []).forEach(function (s) {
        const li = el('li', 'src-row');
        li.appendChild(el('span', 'agent', s.Agent || ''));
        li.appendChild(el('span', 'path', s.Title || ''));
        li.appendChild(el('span', 'when', fmtTime(s.UpdatedAt)));
        sessionBox.appendChild(li);
      });
      if (!(d.Sessions || []).length) sessionBox.appendChild(el('li', 'empty', '没有关联会话。'));
      loadConversation(true);
    }

    form.addEventListener('submit', function (e) {
      e.preventDefault();
      const btn = form.querySelector('button[type="submit"]');
      busy(btn, '保存', async function () {
        const payload = {
          goal: $('task-goal').value.trim(),
          status: statusSel.value,
          owner_agent: $('task-owner').value.trim(),
          next_steps: $('task-next').value
        };
        if (!payload.goal) { toast('error', '请先写目标'); return; }
        if (isNew) {
          payload.project_path = $('task-project').value.trim();
          payload.steps = readSteps();
          if (!payload.project_path) { toast('error', '请填写项目路径'); return; }
          const d = await api('/v1/tasks', { method: 'POST', body: JSON.stringify(payload) });
          location.href = '/t/' + d.id;
          return;
        }
        payload.id = Number(id);
        await api('/v1/tasks/update', { method: 'POST', body: JSON.stringify(payload) });
        toast('info', '已保存');
        location.reload();
      });
    });

    $('task-archive').addEventListener('click', function () {
      if (isNew) return;
      const btn = this;
      busy(btn, '处理', async function () {
        const d = await api('/v1/tasks/item?id=' + enc(id));
        const archived = isRealTime(d.ArchivedAt);
        await api('/v1/tasks/update', { method: 'POST', body: JSON.stringify({ id: Number(id), archived: !archived }) });
        toast('info', archived ? '已恢复' : '已归档');
        location.reload();
      });
    });

    $('task-copy').addEventListener('click', async function () {
      if (isNew) return;
      const d = await api('/v1/tasks/export?id=' + enc(id));
      const markdown = d.markdown || '';
      try {
        await navigator.clipboard.writeText(markdown);
        toast('info', '交接 Prompt 已复制');
      } catch (e) {
        window.prompt('复制失败，请手动复制', markdown);
      }
    });

    $('task-merge').addEventListener('click', function () {
      if (isNew) return;
      const target = window.prompt('输入要合并到的目标任务 ID');
      if (!target) return;
      const targetID = Number(target);
      if (!targetID || targetID === Number(id)) { toast('error', '目标 ID 无效'); return; }
      const btn = this;
      busy(btn, '合并', async function () {
        await api('/v1/tasks/merge', { method: 'POST', body: JSON.stringify({ source_id: Number(id), target_id: targetID }) });
        toast('info', '已合并');
        location.href = '/t/' + targetID;
      });
    });

    initTabs();
    load().catch(function (e) { toast('error', '任务加载失败：' + e.message); });
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
    const textarea = $('edit-text');
    const preview = $('edit-preview');
    let previewTimer = 0;
    function renderEditPreview() {
      if (preview) renderMd(preview, textarea.value || '');
    }
    textarea.addEventListener('input', function () {
      if (!preview) return;
      clearTimeout(previewTimer);
      previewTimer = setTimeout(renderEditPreview, 100);
    });
    renderEditPreview();
    $('back-link').href = next;
    $('edit-cancel').href = next;
    fillTypeSelect($('edit-type'), false, 'fact');
    api('/v1/memory/item?id=' + enc(id)).then((m) => {
      state.memory = m;
      document.title = '编辑记忆 #' + m.ID + ' · agent-memory';
      $('memory-id').textContent = '#' + m.ID;
      textarea.value = m.Text || '';
      renderEditPreview();
      fillTypeSelect($('edit-type'), false, m.Type || 'fact');
      $('edit-scope').textContent = m.Scope === 'user' ? '用户级' : '项目级';
      paintSummary('memory-ai-summary', 'memory-ai-summary-text', m);
      const imported = (m.Sources || []).some((s) => s.Path && s.Path !== 'mcp');
      const sources = (m.Sources || []).map((s) => s.Agent + ' · ' + (s.Path === 'mcp' ? '手动写入' : (s.Path || '—')));
      $('edit-meta').textContent = '来源：' + (sources.join('；') || '—') + '  ·  首次：' + fmtTime(m.FirstSeenAt) + '  ·  最后变更：' + fmtTime(m.LastSeenAt);
      $('edit-warning').hidden = !imported;
      $('edit-warning').textContent = imported ? '这条记忆来自 agent 文件；源文件再变化时，编辑可能被覆盖。' : '';
      $('edit-stale').textContent = m.Stale ? '恢复使用' : '不再使用';
      $('edit-stale').dataset.stale = m.Stale ? '1' : '0';
    }).catch((e) => {
      textarea.value = '';
      renderEditPreview();
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
  function initTheme() {
    const btn = $('theme-toggle');
    if (!btn) return;
    function paint() {
      const light = document.documentElement.dataset.theme === 'light';
      btn.textContent = light ? '☀' : '☾';
      btn.title = light ? '切换到深色' : '切换到浅色';
    }
    paint();
    btn.addEventListener('click', () => {
      const light = document.documentElement.dataset.theme === 'light';
      if (light) delete document.documentElement.dataset.theme;
      else document.documentElement.dataset.theme = 'light';
      try { localStorage.setItem('am-theme', light ? 'dark' : 'light'); } catch (e) {}
      paint();
    });
  }
  function init() {
    initTheme();
    bindSync();
    revealSections();
    if (!state.token) { showGate(); return; }
    loadHealth();
    const view = document.body.dataset.view;
    if (view === 'home') initHome();
    else if (view === 'project') initProject();
    else if (view === 'memories') initMemories();
    else if (view === 'sources') initSources();
    else if (view === 'tasks') initTasks();
    else if (view === 'task') initTask();
    else if (view === 'mcp') initMCP();
    else if (view === 'settings') initSettings();
    else if (view === 'edit') initEdit();
    observeSelects();
  }
  init();
})();
