/* Skipper 控制台 — 单页应用（无构建步骤，随服务内嵌分发）。
 * 数据来自控制平面的 JSON API（/api/v1/*），与 gRPC ClusterService 共享同一份状态。 */
(function () {
  'use strict';

  var API = '/api/v1';
  var P = {
    bg: '#F6F8FA', surface: '#fff', s2: '#F1F4F8', border: '#E3E8EF', borderL: '#EDF1F5',
    text: '#1A2233', t2: '#5B6676', t3: '#8A94A6', primary: '#2563EB', primaryBg: '#E8F0FE',
    accent: '#0E9F9C', success: '#16A34A', successBg: '#E7F6EC', warning: '#D97706', warningBg: '#FCF1E2',
    danger: '#DC2626', dangerBg: '#FCEBEC', neutral: '#64748B', neutralBg: '#EEF1F5'
  };

  // ---------- 全局状态 ----------
  var state = {
    page: 'dashboard', collapsed: false, refresh: 10, dashVariant: 'A',
    drawer: null, jobDetailId: null, toast: null, confirm: null,
    jobF: { state: '', owner: '', q: '' },
    nodeF: { state: '', partition: '', q: '' },
    devF: { kind: 'all', node: '', vendor: '', status: '', view: 'cards' },
    evF: { severity: '', type: '', q: '' },
    ntF: { status: '', channel: '' },
    sf: {
      name: '', owner: '', partition: 'default', priority: 0,
      command: '', workdir: '', gpus: 0, cpus: 1, mem: '', gpuType: '', walltime: '', env: []
    }
  };
  var data = { nodes: [], jobs: [], events: [], notifs: [], devices: [] };
  var now = Math.floor(Date.now() / 1000);
  var lastRefresh = now;
  var firstLoaded = false;
  var fetchError = null;
  var toastTimer = null;
  var composing = false;
  var logsCache = { id: null, text: '', loading: false, err: null };

  // ---------- 工具 ----------
  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }
  function clamp(p) { p = Number(p) || 0; return p < 0 ? 0 : p > 100 ? 100 : p; }
  function gib(b) { return (Number(b) || 0) / 1073741824; }
  function gibR(b) { var v = gib(b); return v >= 100 ? Math.round(v) : Math.round(v * 10) / 10; }
  function humanMem(b) {
    b = Number(b) || 0;
    if (b <= 0) return '—';
    var u = 1024, units = ['B', 'K', 'M', 'G', 'T', 'P'], i = 0, n = b;
    while (n >= u && i < units.length - 1) { n /= u; i++; }
    var r = n >= 100 ? Math.round(n) : Math.round(n * 10) / 10;
    return r + units[i];
  }
  function humanDur(sec) {
    sec = Number(sec) || 0;
    if (sec <= 0) return '不限';
    var h = Math.floor(sec / 3600), m = Math.floor((sec % 3600) / 60), s = sec % 60;
    if (h > 0) return h + 'h' + (m ? ' ' + m + 'm' : '');
    if (m > 0) return m + 'm';
    return s + 's';
  }

  // ---------- 格式化（源自设计稿逻辑） ----------
  function mc(p) { return p > 90 ? P.danger : p >= 70 ? P.warning : P.primary; }
  function tc(t) { return t >= 80 ? P.danger : t >= 70 ? P.warning : P.primary; }
  function badge(kind) {
    var M = {
      UP: [P.success, P.successBg], DRAIN: [P.warning, P.warningBg], DOWN: [P.danger, P.dangerBg],
      PENDING: [P.neutral, P.neutralBg], ASSIGNED: [P.primary, P.primaryBg], RUNNING: [P.primary, P.primaryBg, 1],
      COMPLETED: [P.success, P.successBg], FAILED: [P.danger, P.dangerBg], TIMEOUT: [P.danger, P.dangerBg],
      CANCELLING: [P.warning, P.warningBg], CANCELLED: [P.neutral, P.neutralBg],
      info: [P.neutral, P.neutralBg], warning: [P.warning, P.warningBg], critical: [P.danger, P.dangerBg],
      delivered: [P.success, P.successBg], sent: [P.success, P.successBg], pending: [P.neutral, P.neutralBg], failed: [P.danger, P.dangerBg]
    };
    var v = M[kind] || [P.neutral, P.neutralBg];
    return { label: kind, fg: v[0], bg: v[1], dot: v[0], anim: v[2] ? 'skp-pulse 1.6s ease-in-out infinite' : 'none' };
  }
  function devBadge(st) {
    if (st === 'busy') return { label: '占用中', fg: P.primary, bg: P.primaryBg, dot: P.primary };
    if (st === 'idle') return { label: '占用·空置', fg: P.warning, bg: P.warningBg, dot: P.warning };
    return { label: '空闲', fg: P.success, bg: P.successBg, dot: P.success };
  }
  function rel(t) {
    if (!t) return '—';
    var d = Math.max(0, now - t);
    if (d < 60) return d + ' 秒前';
    if (d < 3600) return Math.floor(d / 60) + ' 分钟前';
    if (d < 86400) return Math.floor(d / 3600) + ' 小时前';
    return Math.floor(d / 86400) + ' 天前';
  }
  function abs(t) {
    if (!t) return '—';
    var x = new Date(t * 1000), p = function (n) { return ('0' + n).slice(-2); };
    return x.getFullYear() + '-' + p(x.getMonth() + 1) + '-' + p(x.getDate()) + ' ' + p(x.getHours()) + ':' + p(x.getMinutes()) + ':' + p(x.getSeconds());
  }
  function dur(s) {
    s = Math.max(0, Math.floor(s));
    var h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), sec = s % 60;
    if (h > 0) return h + 'h ' + m + 'm';
    if (m > 0) return m + 'm ' + sec + 's';
    return sec + 's';
  }
  function reqText(j) {
    var t = j.req.cpus + 'c · ' + j.req.mem;
    if (j.req.gpus > 0) { var k = j.partition === 'npu' ? 'NPU' : (j.req.gpuType || 'GPU'); t += ' · ' + j.req.gpus + '×' + k; }
    return t;
  }
  function req2(j) {
    var parts = ['CPUs ' + j.req.cpus, '内存 ' + j.req.mem];
    if (j.req.gpus > 0) parts.push('加速卡 ' + j.req.gpus + '×' + (j.partition === 'npu' ? 'NPU' : (j.req.gpuType || 'GPU')));
    parts.push('walltime ' + j.req.walltime);
    return parts.join(' · ');
  }
  function jobDur(j) {
    if (j.state === 'PENDING' || j.state === 'ASSIGNED') return '等待 ' + dur(now - (j.submit || now));
    if (j.state === 'RUNNING' || j.state === 'CANCELLING') return '运行 ' + dur(now - (j.start || now));
    if (j.end && j.start) return '用时 ' + dur(j.end - j.start);
    return '—';
  }
  function ring(util, r, color) {
    var c = 2 * Math.PI * r;
    return { r: r, c: Math.round(c * 100) / 100, off: Math.round(c * (1 - clamp(util) / 100) * 100) / 100, color: color, util: util };
  }

  // ---------- 数据获取与映射 ----------
  function mapNodes(arr) {
    return arr.map(function (an) {
      var hasM = !!an.has_metrics, memTotal = gib(an.mem_total_bytes || (an.mem ? an.mem.total_bytes : 0));
      var cpuUtil = null, load = null, memUsed = null;
      if (hasM) {
        if (an.cpu) { cpuUtil = Math.round(an.cpu.utilization); load = [an.cpu.load1, an.cpu.load5, an.cpu.load15].map(function (x) { return Math.round((Number(x) || 0) * 10) / 10; }); }
        if (an.mem) memUsed = gib(an.mem.used_bytes);
      }
      var disks = (an.disks || []).map(function (d) {
        return { mount: d.mount, fs: d.fstype || '', usedGB: gibR(d.used_bytes), totalGB: gibR(d.total_bytes), pct: Math.round(d.used_percent || 0), inode: Math.round(d.inodes_used_percent || 0) };
      });
      return {
        id: an.id, name: an.name, state: an.state, partition: an.partition || '—', addr: an.addr || '—',
        agent: an.agent_version || '—', hb: an.last_heartbeat_unix || 0, cpus: an.cpus || (an.cpu ? an.cpu.cores : 0),
        memTotal: memTotal, memUsed: memUsed, cpuUtil: cpuUtil, load: load, disks: disks,
        labels: an.labels || {}, hasMetrics: hasM, _devices: an.devices || []
      };
    });
  }
  function mapJobs(arr) {
    return arr.map(function (aj) {
      var r = aj.request || {}, term3 = ['COMPLETED', 'FAILED', 'TIMEOUT'].indexOf(aj.state) >= 0;
      return {
        id: aj.id, name: aj.name || '(unnamed)', owner: aj.owner || '—', partition: aj.partition || '—', state: aj.state,
        priority: aj.priority || 0, command: aj.command || '', workdir: aj.workdir || '',
        env: Object.keys(aj.env || {}).map(function (k) { return { k: k, v: aj.env[k] }; }),
        req: { cpus: r.cpus || 0, mem: humanMem(r.mem_bytes), gpus: r.gpus || 0, gpuType: r.gpu_type || '', walltime: humanDur(r.walltime_sec) },
        node: aj.node_name || null, devices: (aj.devices || []).map(function (d) { return { kind: (d.kind || '').toUpperCase(), index: d.index }; }),
        submit: aj.submit_at_unix || null, start: aj.start_at_unix || null, end: aj.end_at_unix || null,
        exit: term3 ? aj.exit_code : null, reason: aj.reason || null
      };
    });
  }
  function buildDevices() {
    var occ = {};
    data.jobs.forEach(function (j) {
      if (j.node && (j.state === 'RUNNING' || j.state === 'CANCELLING')) j.devices.forEach(function (d) { occ[j.node + '#' + d.index] = j.name; });
    });
    var out = [];
    data.nodes.forEach(function (n) {
      (n._devices || []).forEach(function (d) {
        var kindUp = (d.kind || '').toUpperCase(), memT = gib(d.mem_total_bytes), memU = gib(d.mem_used_bytes), util = Math.round(d.utilization || 0);
        var job = occ[n.name + '#' + d.index] || null, status = 'free';
        if (job) status = util < 5 ? 'idle' : 'busy';
        out.push({
          id: n.name + '-' + kindUp + '-' + d.index, node: n.name, kind: kindUp, vendor: d.vendor || '', name: d.name || (kindUp + ' device'),
          index: d.index, uuid: d.uuid || '—', util: util, memU: memU, memT: memT, memPct: memT > 0 ? Math.round(memU / memT * 100) : 0,
          temp: Math.round(d.temperature_c || 0), power: Math.round(d.power_watts || 0), job: job, status: status, drain: n.state === 'DRAIN'
        });
      });
    });
    data.devices = out;
  }
  function fetchJSON(u) { return fetch(u).then(function (r) { if (!r.ok) throw new Error('HTTP ' + r.status + ' @ ' + u); return r.json(); }); }
  function refreshData() {
    return Promise.all([
      fetchJSON(API + '/nodes'), fetchJSON(API + '/jobs'),
      fetchJSON(API + '/events?limit=200'), fetchJSON(API + '/notifications?limit=200')
    ]).then(function (res) {
      data.nodes = mapNodes(res[0].nodes || []);
      data.jobs = mapJobs(res[1].jobs || []);
      buildDevices();
      data.events = (res[2].events || []).map(function (e) {
        return { id: e.id, type: e.type, severity: e.severity, source: e.source || '—', summary: e.summary || '', labels: e.labels || {}, time: e.time_unix };
      });
      data.notifs = (res[3].notifications || []).map(function (n) {
        return {
          id: n.id, status: n.status, event_type: n.event_type, rule: n.rule || '—', channel: n.channel || '—',
          recipients: (n.recipients || '').split(',').map(function (s) { return s.trim(); }).filter(Boolean),
          summary: n.summary || '', error: n.error || '', event_id: n.event_id, time: n.time_unix
        };
      });
      fetchError = null; lastRefresh = Math.floor(Date.now() / 1000);
    }).catch(function (err) { fetchError = String((err && err.message) || err); })
      .then(function () {
        firstLoaded = true;
        if (state.page === 'jobs' && state.jobDetailId) loadLogs(state.jobDetailId);
        render();
      });
  }
  function loadLogs(id) {
    logsCache.id = id; logsCache.loading = true; logsCache.err = null;
    fetch(API + '/jobs/' + encodeURIComponent(id) + '/logs').then(function (r) {
      if (!r.ok) throw new Error('HTTP ' + r.status);
      return r.text();
    }).then(function (t) { logsCache.text = t; })
      .catch(function (err) { logsCache.text = ''; logsCache.err = String((err && err.message) || err); })
      .then(function () { logsCache.loading = false; if (state.page === 'jobs' && state.jobDetailId === id) render(); });
  }

  function toast(msg, dot) {
    if (toastTimer) clearTimeout(toastTimer);
    state.toast = { show: true, msg: msg, dot: dot || P.success };
    toastTimer = setTimeout(function () { state.toast = null; render(); }, 2600);
    render();
  }

  // ---------- 通用渲染片段 ----------
  function badgePill(b, sz) {
    sz = sz || 11.5;
    return '<span style="display:inline-flex;align-items:center;gap:5px;padding:2px 9px;border-radius:999px;font-size:' + sz + 'px;font-weight:500;background:' + b.bg + ';color:' + b.fg + '">' +
      '<span style="width:6px;height:6px;border-radius:50%;background:' + b.dot + ';animation:' + (b.anim || 'none') + '"></span>' + esc(b.label) + '</span>';
  }
  function bar(pct, color, h) {
    h = h || 5;
    return '<div style="height:' + h + 'px;border-radius:3px;background:#EDF1F5;overflow:hidden"><div style="height:100%;width:' + clamp(pct) + '%;background:' + color + '"></div></div>';
  }
  function ringSvg(rg, size, txt, sub) {
    var cx = size / 2;
    var inner = '<circle cx="' + cx + '" cy="' + cx + '" r="' + rg.r + '" fill="none" stroke="#EDF1F5" stroke-width="' + Math.max(6, Math.round(size / 9)) + '"></circle>' +
      '<circle cx="' + cx + '" cy="' + cx + '" r="' + rg.r + '" fill="none" stroke="' + rg.color + '" stroke-width="' + Math.max(6, Math.round(size / 9)) + '" stroke-linecap="round" stroke-dasharray="' + rg.c + '" stroke-dashoffset="' + rg.off + '" transform="rotate(-90 ' + cx + ' ' + cx + ')"></circle>' +
      '<text x="' + cx + '" y="' + (cx + size * 0.06) + '" text-anchor="middle" font-size="' + Math.round(size * 0.22) + '" font-weight="700" fill="#1A2233" font-family="var(--mono)">' + esc(txt) + '</text>';
    if (sub) inner += '<text x="' + cx + '" y="' + (cx + size * 0.28) + '" text-anchor="middle" font-size="' + Math.round(size * 0.09) + '" fill="#8A94A6">' + esc(sub) + '</text>';
    return '<svg width="' + size + '" height="' + size + '" viewBox="0 0 ' + size + ' ' + size + '" style="flex:0 0 auto">' + inner + '</svg>';
  }
  var ICON = {
    dashboard: '<rect x="3" y="3" width="7" height="9" rx="1"></rect><rect x="14" y="3" width="7" height="5" rx="1"></rect><rect x="14" y="12" width="7" height="9" rx="1"></rect><rect x="3" y="16" width="7" height="5" rx="1"></rect>',
    nodes: '<rect x="3" y="4" width="18" height="6" rx="1.5"></rect><rect x="3" y="14" width="18" height="6" rx="1.5"></rect><path d="M7 7h.01M7 17h.01"></path>',
    devices: '<rect x="4" y="4" width="16" height="16" rx="2"></rect><rect x="9" y="9" width="6" height="6"></rect><path d="M9 2v2M15 2v2M9 20v2M15 20v2M2 9h2M2 15h2M20 9h2M20 15h2"></path>',
    jobs: '<path d="M8 6h13M8 12h13M8 18h13"></path><path d="M3 6h.01M3 12h.01M3 18h.01"></path>',
    submit: '<path d="M12 5v14M5 12h14"></path>',
    events: '<path d="M10.3 3.3a2 2 0 0 1 3.4 0l8 13.4A2 2 0 0 1 20 20H4a2 2 0 0 1-1.7-3.3z"></path><path d="M12 9v4M12 17h.01"></path>',
    notifications: '<path d="M6 8a6 6 0 0 1 12 0c0 7 3 9 3 9H3s3-2 3-9"></path><path d="M10.3 21a1.94 1.94 0 0 0 3.4 0"></path>',
    admin: '<path d="M12.2 2h-.4a2 2 0 0 0-2 2v.2a2 2 0 0 1-1 1.7l-.4.2a2 2 0 0 1-2 0l-.2-.1a2 2 0 0 0-2.7.7l-.2.4a2 2 0 0 0 .5 2.6l.2.1a2 2 0 0 1 0 3.4l-.2.1a2 2 0 0 0-.5 2.6l.2.4a2 2 0 0 0 2.7.7l.2-.1a2 2 0 0 1 2 0l.4.2a2 2 0 0 1 1 1.7v.2a2 2 0 0 0 2 2h.4a2 2 0 0 0 2-2v-.2a2 2 0 0 1 1-1.7l.4-.2a2 2 0 0 1 2 0l.2.1a2 2 0 0 0 2.7-.7l.2-.4a2 2 0 0 0-.5-2.6l-.2-.1a2 2 0 0 1 0-3.4l.2-.1a2 2 0 0 0 .5-2.6l-.2-.4a2 2 0 0 0-2.7-.7l-.2.1a2 2 0 0 1-2 0l-.4-.2a2 2 0 0 1-1-1.7V4a2 2 0 0 0-2-2z"></path><circle cx="12" cy="12" r="3"></circle>',
    refresh: '<path d="M3 12a9 9 0 1 0 3-6.7L3 8"></path><path d="M3 3v5h5"></path>',
    menu: '<path d="M3 6h18M3 12h18M3 18h18"></path>',
    search: '<circle cx="11" cy="11" r="7"></circle><path d="m21 21-4.3-4.3"></path>',
    close: '<path d="M18 6 6 18M6 6l12 12"></path>',
    plus: '<path d="M12 5v14M5 12h14"></path>',
    check: '<path d="m5 12 5 5L20 7"></path>',
    back: '<path d="m15 18-6-6 6-6"></path>'
  };
  function svg(name, size, stroke, sw) {
    return '<svg width="' + size + '" height="' + size + '" viewBox="0 0 24 24" fill="none" stroke="' + (stroke || 'currentColor') + '" stroke-width="' + (sw || 1.8) + '" stroke-linecap="round" stroke-linejoin="round">' + ICON[name] + '</svg>';
  }

  var TITLES = {
    dashboard: ['概览 Dashboard', '一屏掌握集群负载、作业与事件'],
    nodes: ['节点 Nodes', '集群节点健康与资源占用'],
    devices: ['设备 GPU/NPU', '跨节点查看所有加速卡，快速找空卡 / 抓「占着不用」'],
    jobs: ['作业队列 Jobs', '提交、盯作业、追日志、取消'],
    submit: ['提交作业 Submit', '对齐 SubmitJob 字段，提交到调度器'],
    events: ['事件 Events', '集群事件流（只读）'],
    notifications: ['通知 Notifications', '规则触发的通知投递记录'],
    admin: ['管理 / 设置', '分区 · 规则 · 通道 · 用户 · 设置']
  };
  var EMPTY = {
    dashboard: ['集群为空', '还没有节点注册到控制平面。在节点上启动 skipper-agent，注册后即可在此查看负载。', '刷新', { act: 'refresh' }],
    nodes: ['暂无节点', '控制平面尚未发现任何节点。在节点机器上运行 skipper-agent 注册后会自动出现。', '刷新', { act: 'refresh' }],
    devices: ['暂无加速卡', '已注册节点未上报 GPU/NPU 设备，或集群中没有加速卡节点。', '返回节点', { act: 'nav', page: 'nodes' }],
    jobs: ['队列为空', '当前没有作业。提交一个作业开始排队。', '提交作业', { act: 'nav', page: 'submit' }],
    events: ['暂无事件', '集群运行平稳，最近没有产生事件。', '刷新', { act: 'refresh' }],
    notifications: ['暂无通知记录', '还没有规则触发过通知。可在「管理 / 规则」配置告警规则。', '刷新', { act: 'refresh' }]
  };
  var ERR_ENDPOINT = { dashboard: 'ListNodes', nodes: 'ListNodes', devices: 'ListMetrics', jobs: 'ListJobs', events: 'ListEvents', notifications: 'ListNotifications' };

  // ---------- 渲染主流程 ----------
  function render() {
    var ae = document.activeElement, fid = null, ss = null, se = null, dir = null;
    if (ae && ae.id && (ae.tagName === 'INPUT' || ae.tagName === 'TEXTAREA')) {
      fid = ae.id;
      try { ss = ae.selectionStart; se = ae.selectionEnd; dir = ae.selectionDirection; } catch (e) { }
    } else if (ae && ae.id && ae.tagName === 'SELECT') { fid = ae.id; }
    document.getElementById('app').innerHTML = view();
    if (fid) {
      var ne = document.getElementById(fid);
      if (ne) {
        ne.focus({ preventScroll: true });
        if (ss != null && ne.setSelectionRange) { try { ne.setSelectionRange(ss, se, dir || 'none'); } catch (e) { } }
      }
    }
  }

  function derive() {
    var nodes = data.nodes, jobs = data.jobs, devices = data.devices;
    var up = nodes.filter(function (n) { return n.state === 'UP'; }).length;
    var down = nodes.filter(function (n) { return n.state === 'DOWN'; }).length;
    var drain = nodes.filter(function (n) { return n.state === 'DRAIN'; }).length;
    var running = jobs.filter(function (j) { return j.state === 'RUNNING'; }).length;
    var pending = jobs.filter(function (j) { return j.state === 'PENDING'; }).length;
    var crit = data.events.filter(function (e) { return e.severity === 'critical' && now - e.time < 3600; }).length;
    var total = devices.length;
    var busy = devices.filter(function (d) { return d.status === 'busy'; }).length;
    var idle = devices.filter(function (d) { return d.status === 'idle'; }).length;
    var free = devices.filter(function (d) { return d.status === 'free'; }).length;
    return { up: up, down: down, drain: drain, running: running, pending: pending, crit: crit, total: total, busy: busy, idle: idle, free: free, occN: busy + idle };
  }

  function view() {
    var d = derive();
    return '<div style="display:flex;height:100vh;overflow:hidden;background:#F6F8FA">' +
      sidebar(d) + mainColumn(d) + '</div>' +
      drawerView() + confirmView() + toastView();
  }


  // ---------- 侧边栏 ----------
  function navBtn(id, label, d) {
    var on = state.page === id || (id === 'jobs' && state.page === 'jobs');
    var bg = on ? P.primaryBg : 'transparent', fg = on ? P.primary : P.t2;
    var justify = state.collapsed ? 'center' : 'flex-start';
    var badgeHtml = '';
    if (id === 'events' && d.crit > 0 && !state.collapsed) {
      badgeHtml = '<span style="margin-left:auto;background:#DC2626;color:#fff;font-size:11px;font-weight:600;border-radius:999px;padding:1px 7px">' + d.crit + '</span>';
    }
    return '<button data-act="nav" data-page="' + id + '" class="skp-nav" title="' + esc(label) + '" style="width:100%;display:flex;align-items:center;gap:10px;padding:9px 10px;border-radius:8px;margin-bottom:2px;background:' + bg + ';color:' + fg + ';font-size:14px;font-weight:500;justify-content:' + justify + '">' +
      svg(id, 18, 'currentColor', 1.7) +
      (state.collapsed ? '' : '<span>' + esc(label) + '</span>') + badgeHtml + '</button>';
  }
  function navGroup(label) {
    if (state.collapsed) return '<div style="height:1px;background:#EDF1F5;margin:8px 6px"></div>';
    return '<div style="font-size:11px;font-weight:600;letter-spacing:.06em;color:#8A94A6;padding:14px 8px 6px">' + esc(label) + '</div>';
  }
  function sidebar(d) {
    var w = state.collapsed ? 64 : 224;
    var brand = '<div style="height:56px;display:flex;align-items:center;gap:10px;padding:0 16px;border-bottom:1px solid #EDF1F5">' +
      '<div style="width:28px;height:28px;border-radius:8px;background:#2563EB;display:flex;align-items:center;justify-content:center;flex:0 0 auto">' +
      '<svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="#fff" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M2 21c.6.5 1.2 1 2.5 1 2.5 0 2.5-2 5-2s2.5 2 5 2 2.5-2 5-2c1.3 0 1.9.5 2.5 1"></path><path d="M19.38 20A11.6 11.6 0 0 0 21 14l-9-4-9 4c0 2.9.94 5.34 2.81 7.76"></path><path d="M19 13V7a2 2 0 0 0-2-2H7a2 2 0 0 0-2 2v6"></path><path d="M12 10v4M12 2v3"></path></svg></div>' +
      (state.collapsed ? '' : '<span style="font-size:16px;font-weight:700;letter-spacing:-.01em;color:#1A2233">Skipper</span>') + '</div>';
    var nav = '<nav style="flex:1;overflow-y:auto;padding:12px 10px">';
    if (!state.collapsed) nav += '<div style="font-size:11px;font-weight:600;letter-spacing:.06em;color:#8A94A6;padding:8px 8px 6px">监控</div>';
    nav += navBtn('dashboard', '概览', d) + navBtn('nodes', '节点 Nodes', d) + navBtn('devices', '设备 GPU/NPU', d);
    nav += navGroup('作业') + navBtn('jobs', '作业队列 Jobs', d) + navBtn('submit', '提交作业 Submit', d);
    nav += navGroup('运维') + navBtn('events', '事件 Events', d) + navBtn('notifications', '通知 Notifications', d);
    nav += '</nav>';
    var foot = '<div style="padding:10px 10px 14px;border-top:1px solid #EDF1F5">' +
      (state.collapsed ? '' : '<div style="display:flex;align-items:center;gap:6px;font-size:11px;font-weight:600;letter-spacing:.06em;color:#8A94A6;padding:4px 8px 6px">管理 <span style="font-size:11px;color:#B0B8C6;font-weight:500">⏳</span></div>') +
      navBtn('admin', '管理 / 设置', d) + '</div>';
    return '<aside style="width:' + w + 'px;flex:0 0 auto;background:#fff;border-right:1px solid #E3E8EF;display:flex;flex-direction:column;transition:width .18s ease;z-index:20">' +
      brand + nav + foot + '</aside>';
  }

  // ---------- 主列：顶栏 + 内容 ----------
  function topbar(d) {
    var downColor = d.down > 0 ? P.danger : P.t2;
    var critColor = d.crit > 0 ? P.danger : P.t2, critBg = d.crit > 0 ? P.dangerBg : '#fff', critBorder = d.crit > 0 ? '#F3C9CC' : P.border, critWeight = d.crit > 0 ? 600 : 400;
    var lastTxt = (now - lastRefresh) === 0 ? '刚刚' : (now - lastRefresh) + 's 前';
    var refreshOpts = [['5', '5s'], ['10', '10s'], ['30', '30s'], ['0', '关']].map(function (o) {
      return '<option value="' + o[0] + '"' + (String(state.refresh) === o[0] ? ' selected' : '') + '>' + o[1] + '</option>';
    }).join('');
    return '<header style="height:56px;flex:0 0 auto;background:#fff;border-bottom:1px solid #E3E8EF;display:flex;align-items:center;gap:18px;padding:0 18px 0 12px;z-index:10">' +
      '<button data-act="collapse" class="skp-nav" style="width:34px;height:34px;border-radius:8px;display:flex;align-items:center;justify-content:center;color:#5B6676;flex:0 0 auto">' + svg('menu', 19) + '</button>' +
      '<div style="display:flex;align-items:center;gap:8px">' +
        '<button data-act="nav" data-page="nodes" class="skp-card-h" style="display:flex;align-items:center;gap:8px;padding:5px 11px;border:1px solid #E3E8EF;border-radius:8px;background:#fff">' +
          '<span style="display:inline-flex;align-items:center;gap:4px;font-size:13px;color:#1A2233"><span style="width:7px;height:7px;border-radius:50%;background:#16A34A"></span>' + d.up + ' UP</span>' +
          '<span style="width:1px;height:12px;background:#E3E8EF"></span>' +
          '<span style="display:inline-flex;align-items:center;gap:4px;font-size:13px;color:' + downColor + '"><span style="width:7px;height:7px;border-radius:50%;background:#DC2626"></span>' + d.down + ' DOWN</span></button>' +
        '<button data-act="nav" data-page="jobs" class="skp-card-h" style="display:flex;align-items:center;gap:8px;padding:5px 11px;border:1px solid #E3E8EF;border-radius:8px;background:#fff">' +
          '<span style="display:inline-flex;align-items:center;gap:4px;font-size:13px;color:#1A2233"><span style="width:7px;height:7px;border-radius:50%;background:#2563EB;animation:skp-pulse 1.6s ease-in-out infinite"></span>' + d.running + ' RUNNING</span>' +
          '<span style="width:1px;height:12px;background:#E3E8EF"></span>' +
          '<span style="font-size:13px;color:#5B6676">' + d.pending + ' PENDING</span></button>' +
        '<button data-act="nav" data-page="events" class="skp-card-h" style="display:flex;align-items:center;gap:6px;padding:5px 11px;border:1px solid ' + critBorder + ';border-radius:8px;background:' + critBg + '">' +
          svg('events', 14, critColor, 2) +
          '<span style="font-size:13px;color:' + critColor + ';font-weight:' + critWeight + '">近 1h ' + d.crit + ' critical</span></button>' +
      '</div>' +
      '<div style="flex:1"></div>' +
      '<div style="display:flex;align-items:center;gap:9px">' +
        '<span style="font-size:12px;color:#8A94A6;white-space:nowrap">上次更新 ' + esc(lastTxt) + '</span>' +
        '<div style="position:relative;display:flex;align-items:center">' +
          '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="#8A94A6" stroke-width="1.8" style="position:absolute;left:8px;pointer-events:none"><path d="M3 12a9 9 0 1 0 3-6.7L3 8"></path><path d="M3 3v5h5"></path></svg>' +
          '<select id="refresh-sel" data-act="setrefresh" style="appearance:none;border:1px solid #E3E8EF;border-radius:8px;background:#fff;font-size:13px;color:#1A2233;padding:6px 26px 6px 28px;cursor:pointer">' + refreshOpts + '</select>' +
          '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="#8A94A6" stroke-width="2" style="position:absolute;right:8px;pointer-events:none"><path d="M6 9l6 6 6-6"></path></svg></div>' +
        '<button data-act="refresh" class="skp-nav" title="立即刷新" style="width:34px;height:34px;border-radius:8px;display:flex;align-items:center;justify-content:center;color:#5B6676;border:1px solid #E3E8EF">' + svg('refresh', 16) + '</button>' +
      '</div>' +
      '<div style="width:1px;height:22px;background:#E3E8EF"></div>' +
      '<div title="登录态 ⏳ 规划中" style="display:flex;align-items:center;gap:8px">' +
        '<div style="width:30px;height:30px;border-radius:50%;background:#E8F0FE;color:#2563EB;display:flex;align-items:center;justify-content:center;font-size:13px;font-weight:600">S</div>' +
        (state.collapsed ? '' : '<span style="font-size:13px;color:#5B6676">skipper <span style="color:#B0B8C6">⏳</span></span>') +
      '</div></header>';
  }

  function contentState() {
    if (!firstLoaded) return 'loading';
    var dataEmpty = !data.nodes.length && !data.jobs.length && !data.events.length && !data.notifs.length;
    if (fetchError && dataEmpty) return 'error';
    if (state.page === 'jobs' && state.jobDetailId) return 'normal';
    if (state.page === 'submit' || state.page === 'admin') return 'normal';
    var baseEmpty = {
      dashboard: data.nodes.length === 0, nodes: data.nodes.length === 0, devices: data.devices.length === 0,
      jobs: data.jobs.length === 0, events: data.events.length === 0, notifications: data.notifs.length === 0
    }[state.page];
    return baseEmpty ? 'empty' : 'normal';
  }

  function mainColumn(d) {
    var t = TITLES[state.page] || ['', ''];
    var cs = contentState();
    var dataEmpty = !data.nodes.length && !data.jobs.length && !data.events.length && !data.notifs.length;
    var banner = (fetchError && !dataEmpty) ?
      '<div style="display:flex;align-items:center;gap:10px;background:#FCEBEC;border:1px solid #F3C9CC;border-radius:10px;padding:10px 14px;margin-bottom:14px;font-size:13px;color:#B42318">' +
      svg('events', 16, '#DC2626', 1.8) + '数据刷新失败：' + esc(fetchError) + '（展示上次成功的数据，将自动重试）</div>' : '';
    var body;
    if (cs === 'loading') body = stateLoading();
    else if (cs === 'error') body = stateError();
    else if (cs === 'empty') body = stateEmpty();
    else body = pageContent(d);
    return '<div style="flex:1;display:flex;flex-direction:column;min-width:0;height:100vh">' +
      topbar(d) +
      '<main style="flex:1;overflow-y:auto;padding:22px 26px 40px">' +
        '<div style="display:flex;align-items:flex-start;justify-content:space-between;gap:16px;margin-bottom:18px">' +
          '<div><h1 style="margin:0;font-size:20px;font-weight:700;letter-spacing:-.01em;color:#1A2233">' + esc(t[0]) + '</h1>' +
          '<p style="margin:4px 0 0;font-size:13px;color:#5B6676">' + esc(t[1]) + '</p></div></div>' +
        banner + body +
      '</main></div>';
  }

  function stateLoading() {
    var card = function (h, w) { return '<div class="skp-skel" style="height:' + h + 'px' + (w ? ';width:' + w : '') + '"></div>'; };
    return '<div style="display:flex;flex-direction:column;gap:14px">' +
      '<div style="display:grid;grid-template-columns:repeat(4,1fr);gap:14px">' + card(92) + card(92) + card(92) + card(92) + '</div>' +
      '<div style="background:#fff;border:1px solid #E3E8EF;border-radius:10px;padding:16px;display:flex;flex-direction:column;gap:12px">' +
        card(18, '180px') + card(14) + card(14, '92%') + card(14, '96%') + card(14, '88%') + '</div>' +
      '<div style="font-size:13px;color:#8A94A6;display:flex;align-items:center;gap:8px"><span style="width:7px;height:7px;border-radius:50%;background:#2563EB;animation:skp-blink 1s infinite"></span>正在加载数据…</div></div>';
  }
  function stateError() {
    var ep = ERR_ENDPOINT[state.page] || 'API';
    return '<div style="background:#fff;border:1px solid #F3C9CC;border-radius:10px;padding:40px;display:flex;flex-direction:column;align-items:center;text-align:center;max-width:520px;margin:30px auto">' +
      '<div style="width:52px;height:52px;border-radius:50%;background:#FCEBEC;display:flex;align-items:center;justify-content:center;margin-bottom:14px">' +
      '<svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="#DC2626" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M12 9v4M12 17h.01"></path><circle cx="12" cy="12" r="9"></circle></svg></div>' +
      '<h3 style="margin:0 0 6px;font-size:16px;font-weight:600;color:#1A2233">无法加载数据</h3>' +
      '<p style="margin:0 0 18px;font-size:13.5px;color:#5B6676;line-height:1.6">请求 <span class="mono" style="font-size:12.5px;color:#DC2626">' + esc(ep) + '</span> 失败：<br>' + esc(fetchError || 'connection error') + '</p>' +
      '<button data-act="refresh" style="display:inline-flex;align-items:center;gap:7px;padding:9px 18px;border-radius:8px;background:#2563EB;color:#fff;font-size:13.5px;font-weight:600">' + svg('refresh', 15, 'currentColor', 2) + '重试</button></div>';
  }
  function stateEmpty() {
    var e = EMPTY[state.page] || ['暂无数据', '', '刷新', { act: 'refresh' }];
    var a = e[3], attrs = a.act === 'nav' ? 'data-act="nav" data-page="' + a.page + '"' : 'data-act="refresh"';
    return '<div style="background:#fff;border:1px solid #E3E8EF;border-radius:10px;padding:48px;display:flex;flex-direction:column;align-items:center;text-align:center;max-width:520px;margin:30px auto">' +
      '<div style="width:54px;height:54px;border-radius:14px;background:#F1F4F8;display:flex;align-items:center;justify-content:center;margin-bottom:16px;color:#8A94A6">' +
      '<svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="3" width="18" height="18" rx="2"></rect><path d="M3 9h18M9 21V9"></path></svg></div>' +
      '<h3 style="margin:0 0 7px;font-size:16px;font-weight:600;color:#1A2233">' + esc(e[0]) + '</h3>' +
      '<p style="margin:0 0 18px;font-size:13.5px;color:#5B6676;line-height:1.6;max-width:360px">' + esc(e[1]) + '</p>' +
      '<button ' + attrs + ' style="display:inline-flex;align-items:center;gap:7px;padding:9px 18px;border-radius:8px;background:#2563EB;color:#fff;font-size:13.5px;font-weight:600">' + esc(e[2]) + '</button></div>';
  }

  function pageContent(d) {
    switch (state.page) {
      case 'dashboard': return dashboardView(d);
      case 'nodes': return nodesView();
      case 'devices': return devicesView();
      case 'jobs': return state.jobDetailId ? jobDetailView() : jobsView();
      case 'submit': return submitView();
      case 'events': return eventsView();
      case 'notifications': return notifsView();
      case 'admin': return adminView();
    }
    return '';
  }

  // ---------- 概览 Dashboard ----------
  function maxDisk(n) {
    if (!n.disks.length) return null;
    return n.disks.reduce(function (a, x) { return x.pct > a.pct ? x : a; });
  }
  function nodeCard(n) {
    var dn = n.state === 'DOWN', b = badge(n.state);
    var disk = maxDisk(n), diskPct = disk ? disk.pct : 0;
    var memPct = n.memUsed != null && n.memTotal > 0 ? Math.round(n.memUsed / n.memTotal * 100) : 0;
    var devs = data.devices.filter(function (x) { return x.node === n.name; });
    var docc = devs.filter(function (x) { return x.status !== 'free'; }).length;
    var border = dn ? '#F3C9CC' : (n.state === 'DRAIN' ? '#F0DCBE' : P.borderL), bg = dn ? '#FDF6F6' : '#fff';
    var rowMetric = function (label, pct, txt) {
      return '<div style="display:flex;align-items:center;gap:8px"><span style="width:30px;font-size:11px;color:#8A94A6">' + label + '</span>' +
        '<div style="flex:1;height:5px;border-radius:3px;background:#EDF1F5;overflow:hidden"><div style="height:100%;width:' + clamp(pct) + '%;background:' + mc(pct) + '"></div></div>' +
        '<span class="mono" style="width:34px;text-align:right;font-size:11px;color:#5B6676">' + esc(txt) + '</span></div>';
    };
    return '<button data-act="open" data-type="node" data-id="' + esc(n.id) + '" class="skp-card-h" style="text-align:left;border:1px solid ' + border + ';border-radius:10px;padding:13px 14px;background:' + bg + ';opacity:' + (dn ? 0.6 : 1) + '">' +
      '<div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:11px"><span style="font-size:13.5px;font-weight:600;color:#1A2233">' + esc(n.name) + '</span>' + badgePill(b) + '</div>' +
      '<div style="display:flex;flex-direction:column;gap:7px">' +
        rowMetric('CPU', dn ? 0 : n.cpuUtil, dn ? '—' : (n.cpuUtil + '%')) +
        rowMetric('内存', dn ? 0 : memPct, dn ? '—' : (memPct + '%')) +
        rowMetric('磁盘', dn ? 0 : diskPct, dn ? '—' : (diskPct + '%')) +
      '</div>' +
      '<div style="display:flex;align-items:center;justify-content:space-between;margin-top:11px;padding-top:10px;border-top:1px solid #EDF1F5">' +
        '<span style="font-size:11.5px;color:#5B6676">' + esc(devs.length ? (devs[0].kind + ' ' + devs.length + ' · 占 ' + docc) : '无加速卡') + '</span>' +
        '<span style="font-size:11px;color:' + (now - n.hb > 120 ? P.danger : P.t3) + '">♥ ' + esc(rel(n.hb)) + '</span></div></button>';
  }
  function devOverviewBar(d, tall) {
    var total = d.total || 1;
    var bw = Math.round(d.busy / total * 100), iw = Math.round(d.idle / total * 100), fw = Math.round(d.free / total * 100);
    var h = tall ? 20 : 14, rad = tall ? 8 : 7;
    return '<div style="display:flex;height:' + h + 'px;border-radius:' + rad + 'px;overflow:hidden;background:#F1F4F8">' +
      '<div style="width:' + bw + '%;background:#2563EB"></div><div style="width:' + iw + '%;background:#D97706"></div><div style="width:' + fw + '%;background:#E7F6EC"></div></div>';
  }
  function recentEvents(limit) {
    var rank = { critical: 0, warning: 1, info: 2 };
    return data.events.slice().sort(function (a, b) { return (rank[a.severity] - rank[b.severity]) || (b.time - a.time); }).slice(0, limit);
  }
  function dashboardView(d) {
    var vstyle = function (k) { var on = state.dashVariant === k; return 'padding:5px 14px;border-radius:6px;font-size:12.5px;font-weight:500;background:' + (on ? '#fff' : 'transparent') + ';color:' + (on ? P.text : P.t3); };
    var toggle = '<div style="display:flex;align-items:center;gap:10px;margin:-6px 0 16px"><span style="font-size:12px;color:#8A94A6">大盘布局</span>' +
      '<div style="display:flex;background:#F1F4F8;border-radius:8px;padding:2px;gap:2px">' +
      '<button data-act="variant" data-v="A" style="' + vstyle('A') + '">变体 A · 综合</button>' +
      '<button data-act="variant" data-v="B" style="' + vstyle('B') + '">变体 B · 分栏聚焦</button></div></div>';

    var cpuNodes = data.nodes.filter(function (n) { return n.cpuUtil != null; });
    var cpuAvg = cpuNodes.length ? Math.round(cpuNodes.reduce(function (a, n) { return a + n.cpuUtil; }, 0) / cpuNodes.length) : 0;
    var memNodes = data.nodes.filter(function (n) { return n.memUsed != null && n.memTotal > 0; });
    var memAvg = memNodes.length ? Math.round(memNodes.reduce(function (a, n) { return a + n.memUsed / n.memTotal * 100; }, 0) / memNodes.length) : 0;
    var completed = data.jobs.filter(function (j) { return j.state === 'COMPLETED'; }).length;
    var failed = data.jobs.filter(function (j) { return j.state === 'FAILED' || j.state === 'TIMEOUT'; }).length;
    var kpis = [
      { label: '节点', value: d.up, unit: 'UP', sub: d.down + ' DOWN · ' + d.drain + ' DRAIN', color: d.down > 0 ? P.danger : P.success },
      { label: '加速卡', value: d.occN, unit: '/ ' + d.total + ' 占用', sub: d.free + ' 空闲 · ' + d.idle + ' 空置', color: P.primary },
      { label: '运行作业', value: d.running, unit: 'RUNNING', sub: d.pending + ' PENDING', color: P.primary },
      { label: '完成作业', value: completed, unit: 'COMPLETED', sub: failed + ' FAILED', color: P.success },
      { label: 'CPU 平均', value: cpuAvg + '%', unit: 'util', sub: 'UP 节点均值', color: P.text },
      { label: '内存 平均', value: memAvg + '%', unit: 'util', sub: 'UP 节点均值', color: P.text }
    ];
    var nodeCards = data.nodes.map(nodeCard).join('');
    var evCard = function (compact) {
      var rows = recentEvents(compact ? 5 : 6).map(function (e) {
        var b = badge(e.severity), edge = e.severity === 'critical' ? P.danger : 'transparent', rb = e.severity === 'critical' ? '#FEF7F7' : '#fff';
        return '<button data-act="open" data-type="event" data-id="' + esc(e.id) + '" class="skp-row" style="width:100%;text-align:left;display:flex;gap:11px;padding:10px 16px;border-top:1px solid #EDF1F5;border-left:3px solid ' + edge + ';background:' + rb + '">' +
          badgePill(b, 11) +
          '<div style="min-width:0;flex:1"><div style="font-size:13px;color:#1A2233;line-height:1.45">' + esc(e.summary) + '</div>' +
          '<div style="display:flex;gap:8px;margin-top:3px"><span class="mono" style="font-size:11px;color:#8A94A6">' + esc(e.type) + '</span><span style="font-size:11px;color:#B0B8C6">·</span><span style="font-size:11px;color:#8A94A6">' + esc(rel(e.time)) + '</span></div></div></button>';
      }).join('');
      return '<div style="background:#fff;border:1px solid #E3E8EF;border-radius:10px;box-shadow:0 1px 2px rgba(16,24,40,.06)">' +
        '<div style="display:flex;align-items:center;justify-content:space-between;padding:14px 16px 10px"><h3 style="margin:0;font-size:14px;font-weight:600;color:#1A2233">近期事件</h3>' +
        '<button data-act="nav" data-page="events" style="font-size:12.5px;color:#2563EB;font-weight:500">查看全部 →</button></div>' + (rows || '<div style="padding:14px 16px;font-size:13px;color:#8A94A6;border-top:1px solid #EDF1F5">暂无事件</div>') + '</div>';
    };
    var queueCard = function () {
      var rows = data.jobs.filter(function (j) { return ['RUNNING', 'ASSIGNED', 'PENDING'].indexOf(j.state) >= 0; }).slice(0, 6).map(function (j) {
        var b = badge(j.state);
        return '<button data-act="job" data-id="' + esc(j.id) + '" class="skp-row" style="width:100%;text-align:left;display:flex;align-items:center;gap:10px;padding:10px 16px;border-top:1px solid #EDF1F5">' +
          badgePill(b, 10.5) +
          '<div style="min-width:0;flex:1"><div style="font-size:13px;color:#1A2233;font-weight:500;white-space:nowrap;overflow:hidden;text-overflow:ellipsis">' + esc(j.name) + '</div>' +
          '<div style="font-size:11px;color:#8A94A6">' + esc(j.owner) + ' · ' + esc(reqText(j)) + '</div></div>' +
          '<span class="mono" style="font-size:11.5px;color:#5B6676;white-space:nowrap">' + esc(jobDur(j).replace('运行 ', '').replace('等待 ', '')) + '</span></button>';
      }).join('');
      return '<div style="background:#fff;border:1px solid #E3E8EF;border-radius:10px;box-shadow:0 1px 2px rgba(16,24,40,.06)">' +
        '<div style="display:flex;align-items:center;justify-content:space-between;padding:14px 16px 10px"><h3 style="margin:0;font-size:14px;font-weight:600;color:#1A2233">队列概览</h3>' +
        '<button data-act="nav" data-page="jobs" style="font-size:12.5px;color:#2563EB;font-weight:500">作业队列 →</button></div>' + (rows || '<div style="padding:14px 16px;font-size:13px;color:#8A94A6;border-top:1px solid #EDF1F5">队列为空</div>') + '</div>';
    };
    var devCard = '<div style="background:#fff;border:1px solid #E3E8EF;border-radius:10px;padding:16px 18px;box-shadow:0 1px 2px rgba(16,24,40,.06)">' +
      '<div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:13px"><h3 style="margin:0;font-size:14px;font-weight:600;color:#1A2233">设备占用总览</h3><span style="font-size:12px;color:#8A94A6">' + d.total + ' 张加速卡</span></div>' +
      '<div style="margin-bottom:12px">' + devOverviewBar(d) + '</div>' +
      '<div style="display:flex;gap:20px;flex-wrap:wrap">' +
        '<div style="display:flex;align-items:center;gap:7px"><span style="width:9px;height:9px;border-radius:2px;background:#2563EB"></span><span style="font-size:13px;color:#1A2233;font-weight:500">占用 ' + d.busy + '</span></div>' +
        '<div style="display:flex;align-items:center;gap:7px"><span style="width:9px;height:9px;border-radius:2px;background:#D97706"></span><span style="font-size:13px;color:#1A2233;font-weight:500">已分配但空置 ' + d.idle + '</span><span style="font-size:11px;color:#D97706">隐患</span></div>' +
        '<div style="display:flex;align-items:center;gap:7px"><span style="width:9px;height:9px;border-radius:2px;background:#CFE8D6;border:1px solid #Bfe0c8"></span><span style="font-size:13px;color:#1A2233;font-weight:500">空闲 ' + d.free + '</span></div></div></div>';
    var nodeHealth = '<div style="background:#fff;border:1px solid #E3E8EF;border-radius:10px;padding:16px 18px;box-shadow:0 1px 2px rgba(16,24,40,.06)">' +
      '<h3 style="margin:0 0 13px;font-size:14px;font-weight:600;color:#1A2233">节点健康</h3>' +
      '<div style="display:grid;grid-template-columns:repeat(2,1fr);gap:12px">' + nodeCards + '</div></div>';

    if (state.dashVariant === 'A') {
      var kpiRow = '<div style="display:grid;grid-template-columns:repeat(6,1fr);gap:14px;margin-bottom:14px">' + kpis.map(function (k) {
        return '<div class="skp-card-h" style="background:#fff;border:1px solid #E3E8EF;border-radius:10px;padding:14px 15px;box-shadow:0 1px 2px rgba(16,24,40,.06)">' +
          '<div style="font-size:12px;color:#5B6676;font-weight:500;margin-bottom:9px">' + esc(k.label) + '</div>' +
          '<div style="display:flex;align-items:baseline;gap:5px"><span class="mono" style="font-size:25px;font-weight:700;letter-spacing:-.02em;color:' + k.color + '">' + esc(k.value) + '</span><span style="font-size:12px;color:#8A94A6">' + esc(k.unit) + '</span></div>' +
          '<div style="font-size:11.5px;color:#8A94A6;margin-top:5px">' + esc(k.sub) + '</div></div>';
      }).join('') + '</div>';
      return toggle + kpiRow +
        '<div style="display:grid;grid-template-columns:1.6fr 1fr;gap:14px;align-items:start">' +
        '<div style="display:flex;flex-direction:column;gap:14px">' + devCard + nodeHealth + '</div>' +
        '<div style="display:flex;flex-direction:column;gap:14px">' + evCard(false) + queueCard() + '</div></div>';
    }
    // 变体 B
    var bigDev = '<div style="background:#fff;border:1px solid #E3E8EF;border-radius:10px;padding:18px 20px;box-shadow:0 1px 2px rgba(16,24,40,.06)">' +
      '<div style="display:flex;align-items:flex-end;justify-content:space-between;margin-bottom:16px"><div>' +
        '<div style="font-size:13px;color:#5B6676;font-weight:500">加速卡占用</div>' +
        '<div style="display:flex;align-items:baseline;gap:8px;margin-top:4px"><span class="mono" style="font-size:38px;font-weight:700;letter-spacing:-.02em;color:#1A2233">' + d.busy + '</span><span style="font-size:15px;color:#8A94A6">/ ' + d.total + ' 占用 · ' + d.free + ' 空闲</span></div></div>' +
        '<div style="text-align:right"><div style="font-size:12px;color:#D97706;font-weight:600">' + d.idle + ' 张占着空置</div><div style="font-size:11px;color:#8A94A6">利用率 &lt;5%</div></div></div>' +
      devOverviewBar(d, true) + '</div>';
    var kpiRail = '<div style="background:#fff;border:1px solid #E3E8EF;border-radius:10px;padding:6px 0;box-shadow:0 1px 2px rgba(16,24,40,.06)">' + kpis.map(function (k) {
      return '<div style="display:flex;align-items:center;justify-content:space-between;padding:11px 16px;border-bottom:1px solid #F4F6F9">' +
        '<div><div style="font-size:12.5px;color:#5B6676">' + esc(k.label) + '</div><div style="font-size:11px;color:#8A94A6;margin-top:2px">' + esc(k.sub) + '</div></div>' +
        '<div style="text-align:right"><span class="mono" style="font-size:21px;font-weight:700;color:' + k.color + '">' + esc(k.value) + '</span> <span style="font-size:11px;color:#8A94A6">' + esc(k.unit) + '</span></div></div>';
    }).join('') + '</div>';
    return toggle + '<div style="display:grid;grid-template-columns:1fr 320px;gap:14px;align-items:start">' +
      '<div style="display:flex;flex-direction:column;gap:14px">' + bigDev + nodeHealth + '</div>' +
      '<div style="display:flex;flex-direction:column;gap:14px">' + kpiRail + evCard(true) + '</div></div>';
  }

  // ---------- 通用筛选组件 ----------
  function searchBox(id, group, val, ph, w) {
    return '<div style="position:relative;display:flex;align-items:center">' +
      '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="#8A94A6" stroke-width="2" style="position:absolute;left:10px;pointer-events:none"><circle cx="11" cy="11" r="7"></circle><path d="m21 21-4.3-4.3"></path></svg>' +
      '<input id="' + id + '" data-act="setf" data-group="' + group + '" data-key="q" value="' + esc(val) + '" placeholder="' + esc(ph) + '" style="border:1px solid #E3E8EF;border-radius:8px;padding:7px 12px 7px 32px;font-size:13px;width:' + (w || 230) + 'px;color:#1A2233"></div>';
  }
  function selectBox(group, key, val, opts) {
    var o = opts.map(function (op) {
      var v = Array.isArray(op) ? op[0] : op, l = Array.isArray(op) ? op[1] : op;
      return '<option value="' + esc(v) + '"' + (String(val) === String(v) ? ' selected' : '') + '>' + esc(l) + '</option>';
    }).join('');
    return '<select data-act="setf" data-group="' + group + '" data-key="' + key + '" style="appearance:auto;border:1px solid #E3E8EF;border-radius:8px;background:#fff;font-size:13px;color:#1A2233;padding:7px 11px;cursor:pointer">' + o + '</select>';
  }
  function noMatch(group, noun) {
    return '<div style="background:#fff;border:1px dashed #D5DEEC;border-radius:10px;padding:36px;text-align:center;color:#8A94A6">' +
      '<div style="font-size:14px;color:#5B6676;margin-bottom:4px">没有匹配的' + esc(noun) + '</div>' +
      '<div style="font-size:13px">试试调整筛选条件 · <button data-act="clearf" data-group="' + group + '" style="color:#2563EB;font-weight:500">清除筛选</button></div></div>';
  }
  function th(label, extra) { return '<th style="text-align:' + (extra && extra.align || 'left') + ';padding:10px 14px;font-size:13px;font-weight:500;color:#5B6676;background:#F1F4F8;border-bottom:1px solid #E3E8EF' + (extra && extra.w ? ';width:' + extra.w : '') + '">' + esc(label) + '</th>'; }
  var TD = 'padding:12px 14px;border-bottom:1px solid #EDF1F5;vertical-align:middle';

  // ---------- 节点 Nodes ----------
  function nodesView() {
    var f = state.nodeF;
    var parts = Array.from(new Set(data.nodes.map(function (n) { return n.partition; })));
    var rows = data.nodes.filter(function (n) {
      return (!f.state || n.state === f.state) && (!f.partition || n.partition === f.partition) &&
        (!f.q || (n.name + ' ' + n.id).toLowerCase().indexOf(f.q.toLowerCase()) >= 0);
    });
    var active = !!(f.state || f.partition || f.q);
    var toolbar = '<div style="display:flex;align-items:center;gap:10px;flex-wrap:wrap;margin-bottom:14px">' +
      searchBox('f-nodeF-q', 'nodeF', f.q, '搜索节点名 / ID') +
      selectBox('nodeF', 'state', f.state, [['', '全部状态'], 'UP', 'DRAIN', 'DOWN']) +
      selectBox('nodeF', 'partition', f.partition, [['', '全部分区']].concat(parts)) +
      (active ? '<button data-act="clearf" data-group="nodeF" style="font-size:12.5px;color:#2563EB;font-weight:500;padding:6px 4px">清除筛选</button>' : '') +
      '<div style="flex:1"></div><span style="font-size:12.5px;color:#8A94A6">' + rows.length + ' / ' + data.nodes.length + ' 节点</span></div>';
    if (!rows.length) return toolbar + noMatch('nodeF', '节点');
    var trs = rows.map(function (n) {
      var dn = n.state === 'DOWN', disk = maxDisk(n), diskPct = disk ? disk.pct : 0;
      var memPct = n.memUsed != null && n.memTotal > 0 ? Math.round(n.memUsed / n.memTotal * 100) : 0;
      var devs = data.devices.filter(function (x) { return x.node === n.name; });
      var gpu = devs.filter(function (x) { return x.kind === 'GPU'; }), npu = devs.filter(function (x) { return x.kind === 'NPU'; });
      var devT = gpu.length ? ('GPU ' + gpu.length + ' · 占 ' + gpu.filter(function (x) { return x.status !== 'free'; }).length)
        : (npu.length ? 'NPU ' + npu.length + ' · 占 ' + npu.filter(function (x) { return x.status !== 'free'; }).length : '—');
      return '<tr class="skp-row" data-act="open" data-type="node" data-id="' + esc(n.id) + '" style="cursor:pointer">' +
        '<td style="' + TD + '"><div style="font-size:14px;font-weight:600;color:#2563EB">' + esc(n.name) + '</div><div class="mono" style="font-size:11px;color:#8A94A6;margin-top:2px">' + esc(n.id) + '</div></td>' +
        '<td style="' + TD + '">' + badgePill(badge(n.state)) + '</td>' +
        '<td style="' + TD + '"><span style="font-size:12px;color:#5B6676;background:#EEF1F5;padding:2px 8px;border-radius:6px">' + esc(n.partition) + '</span></td>' +
        '<td style="' + TD + '"><div class="mono" style="font-size:12.5px;color:#1A2233;margin-bottom:4px">' + (dn ? '—' : esc(n.cpus + 'c · ' + n.cpuUtil + '%')) + '</div>' + bar4(dn ? 0 : n.cpuUtil) + '</td>' +
        '<td style="' + TD + '"><div class="mono" style="font-size:12.5px;color:#1A2233;margin-bottom:4px">' + (dn ? '—' : esc(gibR(n.memUsed * 1073741824) + '/' + gibR(n.memTotal * 1073741824) + 'G · ' + memPct + '%')) + '</div>' + bar4(dn ? 0 : memPct) + '</td>' +
        '<td style="' + TD + ';font-size:12.5px;color:#1A2233">' + esc(devT) + '</td>' +
        '<td style="' + TD + '"><div class="mono" style="font-size:12px;color:#5B6676;margin-bottom:4px">' + (dn ? '—' : esc(disk ? disk.mount + ' ' + diskPct + '%' : '—')) + '</div>' + bar4(dn ? 0 : diskPct) + '</td>' +
        '<td style="' + TD + ';font-size:12px;color:' + (now - n.hb > 120 ? P.danger : P.t3) + '">' + esc(rel(n.hb)) + '<div style="font-size:10.5px;color:#B0B8C6;margin-top:2px">' + esc(n.agent) + '</div></td>' +
        '<td style="' + TD + ';text-align:right"><span title="⏳ Drain / Resume 需后端接口支持" style="display:inline-block;font-size:12px;color:#B0B8C6;border:1px solid #EDF1F5;border-radius:7px;padding:4px 10px;cursor:not-allowed">Drain ⏳</span></td></tr>';
    }).join('');
    var head = '<thead><tr>' + th('名称') + th('状态') + th('分区') + th('CPU', { w: '170px' }) + th('内存', { w: '200px' }) + th('GPU/NPU') + th('磁盘', { w: '150px' }) + th('心跳') + th('操作', { align: 'right' }) + '</tr></thead>';
    return toolbar + '<div style="background:#fff;border:1px solid #E3E8EF;border-radius:10px;overflow:hidden;box-shadow:0 1px 2px rgba(16,24,40,.06)"><table style="width:100%;border-collapse:collapse">' + head + '<tbody>' + trs + '</tbody></table></div>';
  }
  function bar4(pct) { return '<div style="height:4px;border-radius:2px;background:#EDF1F5;overflow:hidden"><div style="height:100%;width:' + clamp(pct) + '%;background:' + mc(pct) + '"></div></div>'; }

  // ---------- 设备 GPU/NPU ----------
  function devicesView() {
    var f = state.devF;
    var ds = data.devices.filter(function (d) {
      return (f.kind === 'all' || d.kind === f.kind) && (!f.node || d.node === f.node) && (!f.vendor || d.vendor === f.vendor) && (!f.status || d.status === f.status);
    });
    var active = !!(f.node || f.vendor || f.status || f.kind !== 'all');
    var kindBtn = function (k, label) { var on = f.kind === k; return '<button data-act="setf" data-group="devF" data-key="kind" data-val="' + k + '" style="padding:6px 14px;border-radius:6px;font-size:13px;font-weight:500;background:' + (on ? P.primary : 'transparent') + ';color:' + (on ? '#fff' : P.t2) + '">' + esc(label) + '</button>'; };
    var viewBtn = function (v, icon) { var on = f.view === v; return '<button data-act="setf" data-group="devF" data-key="view" data-val="' + v + '" style="padding:6px 9px;border-radius:6px;background:' + (on ? '#fff' : 'transparent') + ';color:' + (on ? P.text : P.t3) + ';display:flex">' + icon + '</button>'; };
    var nodeOpts = Array.from(new Set(data.devices.map(function (d) { return d.node; })));
    var vendorOpts = Array.from(new Set(data.devices.map(function (d) { return d.vendor; }).filter(Boolean)));
    var iconCards = '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><rect x="3" y="3" width="7" height="7" rx="1"></rect><rect x="14" y="3" width="7" height="7" rx="1"></rect><rect x="3" y="14" width="7" height="7" rx="1"></rect><rect x="14" y="14" width="7" height="7" rx="1"></rect></svg>';
    var iconTable = '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><path d="M3 6h18M3 12h18M3 18h18"></path></svg>';
    var toolbar = '<div style="display:flex;align-items:center;gap:10px;flex-wrap:wrap;margin-bottom:14px">' +
      '<div style="display:flex;background:#F1F4F8;border-radius:8px;padding:2px;gap:2px">' + kindBtn('all', '全部') + kindBtn('GPU', 'GPU') + kindBtn('NPU', 'NPU') + '</div>' +
      selectBox('devF', 'node', f.node, [['', '全部节点']].concat(nodeOpts)) +
      selectBox('devF', 'vendor', f.vendor, [['', '全部 vendor']].concat(vendorOpts)) +
      '<button data-act="setf" data-group="devF" data-key="status" data-val="free" style="font-size:12.5px;color:#16A34A;font-weight:500;border:1px solid #C6E8D1;background:#F2FBF5;border-radius:7px;padding:6px 11px">空闲可用</button>' +
      '<button data-act="setf" data-group="devF" data-key="status" data-val="idle" style="font-size:12.5px;color:#D97706;font-weight:500;border:1px solid #F0DCBE;background:#FDF7EC;border-radius:7px;padding:6px 11px">占用但空置</button>' +
      (active ? '<button data-act="clearf" data-group="devF" style="font-size:12.5px;color:#2563EB;font-weight:500;padding:6px 4px">清除</button>' : '') +
      '<div style="flex:1"></div>' +
      '<div style="display:flex;background:#F1F4F8;border-radius:8px;padding:2px;gap:2px">' + viewBtn('cards', iconCards) + viewBtn('table', iconTable) + '</div></div>';

    var byModel = {};
    ds.forEach(function (d) { (byModel[d.name] = byModel[d.name] || []).push(d); });
    var summary = '<div style="display:grid;grid-template-columns:repeat(auto-fill,minmax(220px,1fr));gap:12px;margin-bottom:16px">' + Object.keys(byModel).map(function (name) {
      var arr = byModel[name], occ = arr.filter(function (d) { return d.status !== 'free'; }).length, free = arr.length - occ;
      var avg = Math.round(arr.reduce(function (a, d) { return a + d.util; }, 0) / arr.length);
      return '<div style="background:#fff;border:1px solid #E3E8EF;border-radius:10px;padding:13px 15px;box-shadow:0 1px 2px rgba(16,24,40,.06)">' +
        '<div style="font-size:13px;font-weight:600;color:#1A2233;white-space:nowrap;overflow:hidden;text-overflow:ellipsis">' + esc(name) + '</div>' +
        '<div style="display:flex;align-items:baseline;gap:6px;margin:7px 0 4px"><span class="mono" style="font-size:22px;font-weight:700;color:#1A2233">' + occ + '</span><span style="font-size:12px;color:#8A94A6">/ ' + arr.length + ' 占用 · ' + free + ' 空闲</span></div>' +
        '<div style="font-size:12px;color:#5B6676">平均利用率 <span class="mono" style="color:' + mc(avg) + ';font-weight:600">' + avg + '%</span></div></div>';
    }).join('') + '</div>';

    if (!ds.length) return toolbar + summary + '<div style="background:#fff;border:1px dashed #D5DEEC;border-radius:10px;padding:36px;text-align:center;color:#8A94A6"><div style="font-size:14px;color:#5B6676;margin-bottom:4px">没有匹配的设备</div><button data-act="clearf" data-group="devF" style="font-size:13px;color:#2563EB;font-weight:500">清除筛选</button></div>';

    var content;
    if (f.view === 'cards') {
      content = '<div style="display:grid;grid-template-columns:repeat(auto-fill,minmax(252px,1fr));gap:13px">' + ds.map(function (d) {
        var rg = ring(d.util, 22, d.status === 'idle' ? P.warning : P.primary);
        var occ = d.job ? (d.status === 'idle' ? esc(d.job) + ' · 占着空置' : esc(d.job)) : '空闲可用';
        var occColor = d.status === 'idle' ? P.warning : (d.job ? P.text : P.success);
        var wrapBorder = d.status === 'idle' ? '#F0DCBE' : (d.status === 'busy' ? P.borderL : '#CFE8D6');
        return '<button data-act="open" data-type="device" data-id="' + esc(d.id) + '" class="skp-card-h" style="text-align:left;background:#fff;border:1px solid ' + wrapBorder + ';border-radius:10px;padding:14px 15px;box-shadow:0 1px 2px rgba(16,24,40,.06)">' +
          '<div style="display:flex;align-items:flex-start;justify-content:space-between;gap:8px;margin-bottom:10px"><div style="min-width:0">' +
          '<div style="font-size:13px;font-weight:600;color:#1A2233">' + esc(d.kind + ' · ' + d.vendor) + ' <span class="mono" style="color:#8A94A6">#' + d.index + '</span></div>' +
          '<div style="font-size:11.5px;color:#5B6676;white-space:nowrap;overflow:hidden;text-overflow:ellipsis">' + esc(d.name) + '</div></div>' + badgePill(devBadge(d.status), 10.5) + '</div>' +
          '<div style="display:flex;align-items:center;gap:14px">' + ringSvg(rg, 58, d.util + '%') +
          '<div style="flex:1;min-width:0"><div style="font-size:11px;color:#8A94A6;margin-bottom:3px">显存 ' + esc(d.memU >= 0 ? (Math.round(d.memU * 10) / 10) + '/' + (Math.round(d.memT * 10) / 10) + 'G' : '—') + '</div>' +
          '<div style="margin-bottom:9px">' + bar(d.memPct, mc(d.memPct)) + '</div>' +
          '<div style="display:flex;gap:12px"><span class="mono" style="font-size:11.5px;color:' + tc(d.temp) + '">' + (d.temp ? d.temp + '°C' : '—') + '</span><span class="mono" style="font-size:11.5px;color:#5B6676">' + (d.power ? d.power + 'W' : '—') + '</span></div></div></div>' +
          '<div style="margin-top:11px;padding-top:9px;border-top:1px solid #EDF1F5;font-size:11.5px;color:' + occColor + ';white-space:nowrap;overflow:hidden;text-overflow:ellipsis">' + occ + '</div></button>';
      }).join('') + '</div>';
    } else {
      var head = '<thead><tr>' + th('节点') + th('类型') + th('型号 · #') + th('UUID') + th('利用率', { w: '150px' }) + th('显存') + th('温度/功耗') + th('占用者') + th('状态') + '</tr></thead>';
      content = '<div style="background:#fff;border:1px solid #E3E8EF;border-radius:10px;overflow:hidden;box-shadow:0 1px 2px rgba(16,24,40,.06)"><table style="width:100%;border-collapse:collapse">' + head + '<tbody>' + ds.map(function (d) {
        var utilC = d.status === 'idle' ? P.warning : P.primary;
        return '<tr class="skp-row" data-act="open" data-type="device" data-id="' + esc(d.id) + '" style="cursor:pointer">' +
          '<td style="' + TD + ';font-size:13px;color:#2563EB;font-weight:500">' + esc(d.node) + '</td>' +
          '<td style="' + TD + ';font-size:12.5px;color:#5B6676">' + esc(d.kind + '/' + d.vendor) + '</td>' +
          '<td style="' + TD + ';font-size:12.5px;color:#1A2233">' + esc(d.name) + ' <span class="mono" style="color:#8A94A6">#' + d.index + '</span></td>' +
          '<td style="' + TD + ';font-size:11.5px;color:#8A94A6" class="mono">' + esc(d.uuid) + '</td>' +
          '<td style="' + TD + '"><div class="mono" style="font-size:12.5px;color:#1A2233;margin-bottom:3px">' + d.util + '%</div>' + bar(d.util, utilC, 4) + '</td>' +
          '<td style="' + TD + ';font-size:12px;color:#1A2233" class="mono">' + esc((Math.round(d.memU * 10) / 10) + '/' + (Math.round(d.memT * 10) / 10) + 'G') + ' <span style="color:' + mc(d.memPct) + '">' + d.memPct + '%</span></td>' +
          '<td style="' + TD + ';font-size:12px" class="mono"><span style="color:' + tc(d.temp) + '">' + (d.temp ? d.temp + '°' : '—') + '</span> <span style="color:#8A94A6">· ' + (d.power ? d.power + 'W' : '—') + '</span></td>' +
          '<td style="' + TD + ';font-size:12.5px;color:#1A2233">' + esc(d.job || '—') + '</td>' +
          '<td style="' + TD + '">' + badgePill(devBadge(d.status), 11) + '</td></tr>';
      }).join('') + '</tbody></table></div>';
    }
    return toolbar + summary + content;
  }

  // ---------- 作业队列 Jobs ----------
  function jobsView() {
    var f = state.jobF;
    var owners = Array.from(new Set(data.jobs.map(function (j) { return j.owner; })));
    var rows = data.jobs.filter(function (j) {
      return (!f.state || j.state === f.state) && (!f.owner || j.owner === f.owner) &&
        (!f.q || (j.name + ' ' + j.id + ' ' + j.owner).toLowerCase().indexOf(f.q.toLowerCase()) >= 0);
    });
    var active = !!(f.state || f.owner || f.q);
    var states = ['PENDING', 'ASSIGNED', 'RUNNING', 'COMPLETED', 'FAILED', 'TIMEOUT', 'CANCELLING', 'CANCELLED'];
    var toolbar = '<div style="display:flex;align-items:center;gap:10px;flex-wrap:wrap;margin-bottom:14px">' +
      searchBox('f-jobF-q', 'jobF', f.q, '搜索作业名 / ID / owner', 250) +
      selectBox('jobF', 'state', f.state, [['', '全部状态']].concat(states)) +
      selectBox('jobF', 'owner', f.owner, [['', '全部属主']].concat(owners)) +
      (active ? '<button data-act="clearf" data-group="jobF" style="font-size:12.5px;color:#2563EB;font-weight:500;padding:6px 4px">清除</button>' : '') +
      '<div style="flex:1"></div><span style="font-size:12.5px;color:#8A94A6;margin-right:4px">' + rows.length + ' / ' + data.jobs.length + '</span>' +
      '<button data-act="nav" data-page="submit" style="display:flex;align-items:center;gap:6px;background:#2563EB;color:#fff;font-size:13px;font-weight:600;border-radius:8px;padding:8px 15px">' + svg('plus', 15, 'currentColor', 2.2) + '提交作业</button></div>';
    if (!rows.length) return toolbar + noMatch('jobF', '作业');
    var canCancel = function (s) { return ['PENDING', 'ASSIGNED', 'RUNNING'].indexOf(s) >= 0; };
    var term = function (s) { return ['COMPLETED', 'FAILED', 'TIMEOUT', 'CANCELLED'].indexOf(s) >= 0; };
    var trs = rows.map(function (j) {
      var exitTxt = term(j.state) ? (j.exit != null ? 'exit ' + j.exit : (j.reason || '—')) : '—';
      var exitC = (j.exit != null && j.exit !== 0) || j.state === 'FAILED' || j.state === 'TIMEOUT' ? P.danger : P.t2;
      var cancelBtn = canCancel(j.state) ? '<button data-act="askcancel" data-id="' + esc(j.id) + '" data-name="' + esc(j.name) + '" style="font-size:12px;color:#DC2626;font-weight:500;border:1px solid #F3C9CC;border-radius:7px;padding:4px 10px;background:#fff">取消</button><span style="display:inline-block;width:6px"></span>' : '';
      return '<tr class="skp-row" data-act="job" data-id="' + esc(j.id) + '" style="cursor:pointer">' +
        '<td style="' + TD + '"><div style="font-size:14px;font-weight:600;color:#2563EB">' + esc(j.name) + '</div><div class="mono" style="font-size:11px;color:#8A94A6;margin-top:2px">' + esc(j.id) + '</div></td>' +
        '<td style="' + TD + '">' + badgePill(badge(j.state)) + '</td>' +
        '<td style="' + TD + ';font-size:13px;color:#1A2233">' + esc(j.owner) + '</td>' +
        '<td style="' + TD + '"><span style="font-size:12px;color:#5B6676;background:#EEF1F5;padding:2px 8px;border-radius:6px">' + esc(j.partition) + '</span></td>' +
        '<td style="' + TD + ';text-align:center;font-size:13px;color:#1A2233" class="mono">' + j.priority + '</td>' +
        '<td style="' + TD + ';font-size:12px;color:#5B6676" class="mono">' + esc(reqText(j)) + '</td>' +
        '<td style="' + TD + ';font-size:12.5px;color:#1A2233">' + esc(j.node || '—') + '</td>' +
        '<td style="' + TD + ';font-size:12px;color:' + exitC + '" class="mono">' + esc(jobDur(j)) + '<div style="font-size:11px;color:#8A94A6;margin-top:2px">' + esc(exitTxt) + '</div></td>' +
        '<td style="' + TD + ';text-align:right;white-space:nowrap">' + cancelBtn + '<button data-act="job" data-id="' + esc(j.id) + '" style="font-size:12px;color:#2563EB;font-weight:500;padding:4px 4px">详情 →</button></td></tr>';
    }).join('');
    var head = '<thead><tr>' + th('作业') + th('状态') + th('属主') + th('分区') + th('优先级', { align: 'center' }) + th('请求资源') + th('节点') + th('用时 / 退出') + th('操作', { align: 'right' }) + '</tr></thead>';
    return toolbar + '<div style="background:#fff;border:1px solid #E3E8EF;border-radius:10px;overflow:hidden;box-shadow:0 1px 2px rgba(16,24,40,.06)"><table style="width:100%;border-collapse:collapse">' + head + '<tbody>' + trs + '</tbody></table></div>';
  }

  // ---------- 作业详情 ----------
  function jobDetailView() {
    var j = data.jobs.filter(function (x) { return x.id === state.jobDetailId; })[0];
    if (!j) return '<div style="background:#fff;border:1px solid #E3E8EF;border-radius:10px;padding:40px;text-align:center;color:#8A94A6">未找到该作业（可能已被清理）。<div style="margin-top:12px"><button data-act="jobback" style="color:#2563EB;font-weight:500">← 返回作业队列</button></div></div>';
    var active = ['PENDING', 'ASSIGNED', 'RUNNING'].indexOf(j.state) >= 0;
    var streaming = j.state === 'RUNNING' || j.state === 'CANCELLING';
    var tl = [
      { label: '提交 Submit', time: abs(j.submit), rel: rel(j.submit), done: true },
      { label: '开始 Start', time: j.start ? abs(j.start) : '—', rel: j.start ? ('排队 ' + dur(j.start - j.submit)) : '尚未开始', done: !!j.start },
      { label: '结束 End', time: j.end ? abs(j.end) : (streaming ? '运行中' : '—'), rel: j.end ? ('运行 ' + dur(j.end - j.start)) : (streaming ? dur(now - j.start) : '—'), done: !!j.end }
    ];
    var timeline = tl.map(function (t) {
      var dc = t.done ? P.primary : '#fff', db = t.done ? P.primary : '#D5DEEC';
      return '<div style="flex:1;position:relative"><div style="display:flex;align-items:center;gap:8px;margin-bottom:8px"><span style="width:11px;height:11px;border-radius:50%;background:' + dc + ';border:2px solid ' + db + ';flex:0 0 auto;z-index:1"></span><span style="height:2px;flex:1;background:#EDF1F5"></span></div>' +
        '<div style="font-size:12.5px;font-weight:600;color:#1A2233">' + esc(t.label) + '</div>' +
        '<div class="mono" style="font-size:11.5px;color:#5B6676;margin-top:3px">' + esc(t.time) + '</div>' +
        '<div style="font-size:11px;color:#8A94A6;margin-top:2px">' + esc(t.rel) + '</div></div>';
    }).join('');

    var logsBody;
    if (logsCache.id === j.id && logsCache.loading && !logsCache.text) logsBody = '<div style="color:#8A94A6">正在加载日志…</div>';
    else if (logsCache.id === j.id && logsCache.err) logsBody = '<div style="color:#F48771">日志读取失败：' + esc(logsCache.err) + '</div>';
    else if (logsCache.id === j.id && logsCache.text) {
      logsBody = logsCache.text.split('\n').map(function (line) { return '<div style="color:#D4D4D4;white-space:pre-wrap;word-break:break-word">' + (esc(line) || '&nbsp;') + '</div>'; }).join('');
    } else logsBody = '<div style="color:#8A94A6">' + (active && !j.start ? '作业尚未开始，暂无日志。进入调度后将实时输出。' : '暂无日志。') + '</div>';
    if (streaming) logsBody += '<div style="display:flex;gap:10px;margin-top:2px"><span style="width:8px;height:15px;background:#16A34A;display:inline-block;animation:skp-blink 1s infinite"></span></div>';

    var cancelBtn = active ? '<button data-act="askcancel" data-id="' + esc(j.id) + '" data-name="' + esc(j.name) + '" style="display:flex;align-items:center;gap:6px;font-size:13px;color:#DC2626;font-weight:600;border:1px solid #F3C9CC;border-radius:8px;padding:8px 14px;background:#fff;white-space:nowrap">' + svg('close', 14, 'currentColor', 2) + '取消作业</button>' : '';
    var envHtml = j.env.length ? '<div style="display:flex;flex-direction:column;gap:5px">' + j.env.map(function (e) {
      return '<div style="display:flex;gap:8px;font-size:12px" class="mono"><span style="color:#0E9F9C">' + esc(e.k) + '</span><span style="color:#8A94A6">=</span><span style="color:#1A2233">' + esc(e.v) + '</span></div>';
    }).join('') + '</div>' : '<div style="font-size:12px;color:#B0B8C6">（无）</div>';
    var reasonHtml = j.reason ? '<div style="margin-top:13px;padding-top:13px;border-top:1px solid #EDF1F5"><div style="font-size:11.5px;color:#8A94A6;margin-bottom:4px">退出 / 原因</div><div style="font-size:12.5px;color:' + ((j.state === 'FAILED' || j.state === 'TIMEOUT') ? P.danger : P.t2) + '">' + (j.exit != null ? '<span class="mono">exit ' + j.exit + ' · </span>' : '') + esc(j.reason) + '</div></div>' : '';

    var left = '<div style="display:flex;flex-direction:column;gap:14px">' +
      '<div style="background:#fff;border:1px solid #E3E8EF;border-radius:10px;padding:18px 20px;box-shadow:0 1px 2px rgba(16,24,40,.06)">' +
        '<div style="display:flex;align-items:flex-start;justify-content:space-between;gap:12px"><div>' +
        '<div style="display:flex;align-items:center;gap:10px"><h2 style="margin:0;font-size:19px;font-weight:700;color:#1A2233">' + esc(j.name) + '</h2>' + badgePill(badge(j.state), 12) + '</div>' +
        '<div style="display:flex;gap:14px;margin-top:9px;font-size:12.5px;color:#5B6676"><span class="mono" style="color:#8A94A6">' + esc(j.id) + '</span><span>属主 <b style="color:#1A2233;font-weight:500">' + esc(j.owner) + '</b></span><span>分区 <b style="color:#1A2233;font-weight:500">' + esc(j.partition) + '</b></span><span>优先级 <b style="color:#1A2233;font-weight:500">' + j.priority + '</b></span></div></div>' + cancelBtn + '</div></div>' +
      '<div style="background:#fff;border:1px solid #E3E8EF;border-radius:10px;padding:18px 20px;box-shadow:0 1px 2px rgba(16,24,40,.06)"><h3 style="margin:0 0 16px;font-size:14px;font-weight:600;color:#1A2233">生命周期</h3><div style="display:flex;gap:0">' + timeline + '</div></div>' +
      '<div style="background:#1E2433;border:1px solid #2A3142;border-radius:10px;overflow:hidden;box-shadow:0 1px 2px rgba(16,24,40,.06)">' +
        '<div style="display:flex;align-items:center;justify-content:space-between;padding:11px 15px;background:#252C3D;border-bottom:1px solid #2A3142">' +
        '<div style="display:flex;align-items:center;gap:10px"><span style="font-size:13px;font-weight:600;color:#E6EAF2">日志 · GetJobLogs</span><span class="mono" style="font-size:11.5px;color:' + (streaming ? '#16A34A' : '#8A94A6') + '">' + (streaming ? '● streaming' : '○ 已结束') + '</span></div></div>' +
        '<div class="mono" style="padding:12px 15px;max-height:340px;overflow-y:auto;font-size:12.5px;line-height:1.7">' + logsBody + '</div></div></div>';

    var right = '<div style="display:flex;flex-direction:column;gap:14px">' +
      '<div style="background:#fff;border:1px solid #E3E8EF;border-radius:10px;padding:16px 18px;box-shadow:0 1px 2px rgba(16,24,40,.06)">' +
        '<h3 style="margin:0 0 12px;font-size:14px;font-weight:600;color:#1A2233">资源</h3>' +
        '<div style="font-size:11.5px;color:#8A94A6;margin-bottom:5px">请求 request</div><div style="font-size:13px;color:#1A2233;line-height:1.6;margin-bottom:14px">' + esc(req2(j)) + '</div>' +
        '<div style="font-size:11.5px;color:#8A94A6;margin-bottom:5px">实际分配</div>' +
        '<div style="display:flex;align-items:center;gap:8px;margin-bottom:8px"><span style="font-size:12px;color:#5B6676;width:42px">节点</span><span style="font-size:13px;color:#2563EB;font-weight:500">' + esc(j.node || '未分配') + '</span></div>' +
        '<div style="display:flex;align-items:flex-start;gap:8px"><span style="font-size:12px;color:#5B6676;width:42px">设备</span><span class="mono" style="font-size:12.5px;color:#1A2233">' + esc(j.devices.length ? j.devices.map(function (d) { return d.kind + '#' + d.index; }).join('  ') : '—') + '</span></div></div>' +
      '<div style="background:#fff;border:1px solid #E3E8EF;border-radius:10px;padding:16px 18px;box-shadow:0 1px 2px rgba(16,24,40,.06)">' +
        '<h3 style="margin:0 0 12px;font-size:14px;font-weight:600;color:#1A2233">执行信息</h3>' +
        '<div style="font-size:11.5px;color:#8A94A6;margin-bottom:5px">command</div>' +
        '<div class="mono" style="background:#F1F4F8;border-radius:8px;padding:10px 12px;font-size:12px;color:#1A2233;white-space:pre-wrap;word-break:break-word;margin-bottom:13px">' + esc(j.command) + '</div>' +
        '<div style="display:flex;align-items:center;gap:8px;margin-bottom:13px"><span style="font-size:12px;color:#5B6676;width:54px">workdir</span><span class="mono" style="font-size:12px;color:#1A2233">' + esc(j.workdir || '—') + '</span></div>' +
        '<div style="font-size:11.5px;color:#8A94A6;margin-bottom:6px">env</div>' + envHtml + reasonHtml + '</div></div>';

    return '<button data-act="jobback" style="display:flex;align-items:center;gap:6px;font-size:13px;color:#5B6676;font-weight:500;margin-bottom:14px">' + svg('back', 16, 'currentColor', 2) + '返回作业队列</button>' +
      '<div style="display:grid;grid-template-columns:1fr 380px;gap:16px;align-items:start">' + left + right + '</div>';
  }

  // ---------- 提交作业 Submit ----------
  function submitView() {
    var sf = state.sf;
    var parts = Array.from(new Set(data.nodes.map(function (n) { return n.partition; })));
    if (!parts.length) parts = ['default', 'gpu', 'npu'];
    var input = function (key, val, ph, type, mono) {
      return '<input id="sf-' + key + '" data-act="setsf" data-key="' + key + '" value="' + esc(val) + '"' + (ph ? ' placeholder="' + esc(ph) + '"' : '') + (type ? ' type="' + type + '"' : '') +
        ' style="width:100%;border:1px solid #E3E8EF;border-radius:8px;padding:9px 12px;font-size:13.5px;color:#1A2233' + (mono ? ';font-family:var(--mono)' : '') + '">';
    };
    var small = function (key, val, ph) {
      return '<input id="sf-' + key + '" data-act="setsf" data-key="' + key + '" value="' + esc(val) + '"' + (ph ? ' placeholder="' + esc(ph) + '"' : '') + ' class="mono" style="width:100%;border:1px solid #E3E8EF;border-radius:8px;padding:8px 10px;font-size:13px;color:#1A2233">';
    };
    var partOpts = parts.map(function (p) { return '<option value="' + esc(p) + '"' + (sf.partition === p ? ' selected' : '') + '>' + esc(p) + '</option>'; }).join('');
    var envRows = sf.env.map(function (r, i) {
      return '<div style="display:flex;gap:8px;align-items:center">' +
        '<input id="env-' + i + '-k" data-act="setenv" data-idx="' + i + '" data-key="k" value="' + esc(r.k) + '" placeholder="KEY" class="mono" style="flex:1;border:1px solid #E3E8EF;border-radius:8px;padding:7px 11px;font-size:12.5px;color:#1A2233">' +
        '<span style="color:#8A94A6">=</span>' +
        '<input id="env-' + i + '-v" data-act="setenv" data-idx="' + i + '" data-key="v" value="' + esc(r.v) + '" placeholder="value" class="mono" style="flex:1.4;border:1px solid #E3E8EF;border-radius:8px;padding:7px 11px;font-size:12.5px;color:#1A2233">' +
        '<button data-act="delenv" data-idx="' + i + '" style="width:30px;height:30px;border-radius:7px;display:flex;align-items:center;justify-content:center;color:#8A94A6;border:1px solid #EDF1F5">' + svg('close', 14, 'currentColor', 2) + '</button></div>';
    }).join('');

    var gt = sf.gpus > 0 && sf.gpuType ? ' \\\n  --gpu-type ' + sf.gpuType : '';
    var envStr = sf.env.filter(function (r) { return r.k; }).map(function (r) { return ' \\\n  --env ' + r.k + '=' + r.v; }).join('');
    var preview = 'skctl submit \\\n  --name ' + (sf.name || '<name>') + ' \\\n  --partition ' + sf.partition + ' \\\n  --priority ' + sf.priority +
      ' \\\n  --cpus ' + sf.cpus + ' --mem ' + (sf.mem || '0') + ' --gpus ' + sf.gpus + gt +
      ' \\\n  --time ' + (sf.walltime || '0') + ' \\\n  --workdir ' + (sf.workdir || '') + envStr + ' \\\n  -- ' + sf.command.split('\n').join(' ');

    var form = '<div style="background:#fff;border:1px solid #E3E8EF;border-radius:10px;padding:22px 24px;box-shadow:0 1px 2px rgba(16,24,40,.06)">' +
      '<div style="display:grid;grid-template-columns:1fr 1fr;gap:16px 18px">' +
        '<div><label style="display:block;font-size:12.5px;font-weight:500;color:#5B6676;margin-bottom:6px">作业名 name <span style="color:#DC2626">*</span></label>' + input('name', sf.name, 'train-bert-large') + '</div>' +
        '<div><label style="display:block;font-size:12.5px;font-weight:500;color:#5B6676;margin-bottom:6px">属主 owner</label>' + input('owner', sf.owner, '默认 anonymous') + '</div>' +
        '<div><label style="display:block;font-size:12.5px;font-weight:500;color:#5B6676;margin-bottom:6px">分区 partition</label><select id="sf-partition" data-act="setsf" data-key="partition" style="width:100%;appearance:auto;border:1px solid #E3E8EF;border-radius:8px;background:#fff;font-size:13.5px;color:#1A2233;padding:9px 12px;cursor:pointer">' + partOpts + '</select></div>' +
        '<div><label style="display:block;font-size:12.5px;font-weight:500;color:#5B6676;margin-bottom:6px">优先级 priority</label>' + input('priority', sf.priority, '', 'number', true) + '</div>' +
      '</div>' +
      '<div style="margin-top:16px"><label style="display:block;font-size:12.5px;font-weight:500;color:#5B6676;margin-bottom:6px">命令 command <span style="color:#DC2626">*</span></label><textarea id="sf-command" data-act="setsf" data-key="command" rows="3" placeholder="python train.py --config big.yaml" class="mono" style="width:100%;border:1px solid #E3E8EF;border-radius:8px;padding:10px 12px;font-size:12.5px;color:#1A2233;resize:vertical;line-height:1.6">' + esc(sf.command) + '</textarea></div>' +
      '<div style="margin-top:16px"><label style="display:block;font-size:12.5px;font-weight:500;color:#5B6676;margin-bottom:6px">工作目录 workdir</label>' + input('workdir', sf.workdir, '/home/you/exp', '', true) + '</div>' +
      '<div style="margin-top:20px;padding-top:18px;border-top:1px solid #EDF1F5"><div style="font-size:13px;font-weight:600;color:#1A2233;margin-bottom:13px">资源请求 request</div>' +
        '<div style="display:grid;grid-template-columns:repeat(5,1fr);gap:12px">' +
          '<div><label style="display:block;font-size:11.5px;color:#8A94A6;margin-bottom:5px">CPUs</label>' + small('cpus', sf.cpus) + '</div>' +
          '<div><label style="display:block;font-size:11.5px;color:#8A94A6;margin-bottom:5px">内存</label>' + small('mem', sf.mem, '64G') + '</div>' +
          '<div><label style="display:block;font-size:11.5px;color:#8A94A6;margin-bottom:5px">加速卡 gpus</label>' + small('gpus', sf.gpus) + '</div>' +
          '<div><label style="display:block;font-size:11.5px;color:#8A94A6;margin-bottom:5px">gpu_type</label>' + small('gpuType', sf.gpuType, 'A100 (可选)') + '</div>' +
          '<div><label style="display:block;font-size:11.5px;color:#8A94A6;margin-bottom:5px">walltime</label>' + small('walltime', sf.walltime, '12h') + '</div>' +
        '</div></div>' +
      '<div style="margin-top:20px;padding-top:18px;border-top:1px solid #EDF1F5"><div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:11px"><div style="font-size:13px;font-weight:600;color:#1A2233">环境变量 env</div>' +
        '<button data-act="addenv" style="display:flex;align-items:center;gap:5px;font-size:12.5px;color:#2563EB;font-weight:500">' + svg('plus', 14, 'currentColor', 2.2) + '添加</button></div>' +
        '<div style="display:flex;flex-direction:column;gap:8px">' + (envRows || '<div style="font-size:12px;color:#B0B8C6">（无，可点「添加」）</div>') + '</div></div>' +
      '<div style="margin-top:22px;display:flex;gap:10px">' +
        '<button data-act="dosubmit" style="display:flex;align-items:center;gap:7px;background:#2563EB;color:#fff;font-size:14px;font-weight:600;border-radius:8px;padding:11px 22px">' + svg('check', 16, 'currentColor', 2) + '提交作业</button>' +
        '<button data-act="nav" data-page="jobs" style="font-size:14px;color:#5B6676;font-weight:500;border:1px solid #E3E8EF;border-radius:8px;padding:11px 20px">取消</button></div></div>';

    var preview_pane = '<div style="position:sticky;top:0">' +
      '<div style="background:#1E2433;border:1px solid #2A3142;border-radius:10px;overflow:hidden">' +
        '<div style="padding:11px 15px;background:#252C3D;border-bottom:1px solid #2A3142;display:flex;align-items:center;gap:8px"><span style="width:8px;height:8px;border-radius:50%;background:#0E9F9C"></span><span style="font-size:12.5px;font-weight:600;color:#E6EAF2">等价 CLI 命令预览</span></div>' +
        '<div class="mono" style="padding:15px;font-size:12px;line-height:1.7;color:#CBD3E1;white-space:pre-wrap;word-break:break-word">' + esc(preview) + '</div></div>' +
      '<div style="background:#F1F4F8;border:1px solid #E3E8EF;border-radius:10px;padding:13px 15px;margin-top:12px;font-size:12px;color:#5B6676;line-height:1.6">提交将调用 <span class="mono" style="color:#1A2233">SubmitJob</span>，成功后跳转作业详情并开始排队。walltime 留空表示不限。</div></div>';

    return '<div style="display:grid;grid-template-columns:1fr 400px;gap:16px;align-items:start">' + form + preview_pane + '</div>';
  }

  // ---------- 事件 Events ----------
  function eventsView() {
    var f = state.evF;
    var types = Array.from(new Set(data.events.map(function (e) { return e.type; })));
    var rows = data.events.filter(function (e) {
      return (!f.severity || e.severity === f.severity) && (!f.type || e.type === f.type) &&
        (!f.q || e.summary.toLowerCase().indexOf(f.q.toLowerCase()) >= 0);
    }).slice().sort(function (a, b) { return b.time - a.time; });
    var active = !!(f.severity || f.type || f.q);
    var toolbar = '<div style="display:flex;align-items:center;gap:10px;flex-wrap:wrap;margin-bottom:14px">' +
      searchBox('f-evF-q', 'evF', f.q, '搜索 summary') +
      selectBox('evF', 'severity', f.severity, [['', '全部严重度'], 'info', 'warning', 'critical']) +
      selectBox('evF', 'type', f.type, [['', '全部类型']].concat(types)) +
      (active ? '<button data-act="clearf" data-group="evF" style="font-size:12.5px;color:#2563EB;font-weight:500;padding:6px 4px">清除</button>' : '') +
      '<div style="flex:1"></div><span style="font-size:12.5px;color:#8A94A6">' + rows.length + ' / ' + data.events.length + ' 事件 · 只读</span></div>';
    if (!rows.length) return toolbar + noMatch('evF', '事件');
    var trs = rows.map(function (e) {
      var b = badge(e.severity), edge = e.severity === 'critical' ? P.danger : 'transparent', rb = e.severity === 'critical' ? '#FEF7F7' : '#fff';
      var labels = Object.keys(e.labels).map(function (k) { return '<span class="mono" style="font-size:11px;color:#5B6676;background:#EEF1F5;padding:2px 7px;border-radius:6px">' + esc(k) + '=' + esc(e.labels[k]) + '</span>'; }).join('');
      return '<tr class="skp-row" data-act="open" data-type="event" data-id="' + esc(e.id) + '" style="cursor:pointer;border-left:3px solid ' + edge + ';background:' + rb + '">' +
        '<td style="' + TD + ';font-size:12px;color:#5B6676" title="' + esc(abs(e.time)) + '">' + esc(rel(e.time)) + '</td>' +
        '<td style="' + TD + '">' + badgePill(b) + '</td>' +
        '<td style="' + TD + ';font-size:12.5px;color:#1A2233" class="mono">' + esc(e.type) + '</td>' +
        '<td style="' + TD + ';font-size:13px;color:#1A2233">' + esc(e.summary) + '<div style="font-size:11px;color:#8A94A6;margin-top:2px">' + esc(e.source) + '</div></td>' +
        '<td style="' + TD + '"><div style="display:flex;gap:5px;flex-wrap:wrap">' + labels + '</div></td></tr>';
    }).join('');
    var head = '<thead><tr>' + th('时间', { w: '120px' }) + th('严重度', { w: '100px' }) + th('类型', { w: '150px' }) + th('摘要') + th('标签') + '</tr></thead>';
    return toolbar + '<div style="background:#fff;border:1px solid #E3E8EF;border-radius:10px;overflow:hidden;box-shadow:0 1px 2px rgba(16,24,40,.06)"><table style="width:100%;border-collapse:collapse">' + head + '<tbody>' + trs + '</tbody></table></div>';
  }

  // ---------- 通知 Notifications ----------
  function notifsView() {
    var f = state.ntF;
    var statuses = Array.from(new Set(data.notifs.map(function (n) { return n.status; })));
    var channels = Array.from(new Set(data.notifs.map(function (n) { return n.channel; })));
    var rows = data.notifs.filter(function (n) {
      return (!f.status || n.status === f.status) && (!f.channel || n.channel === f.channel);
    }).slice().sort(function (a, b) { return b.time - a.time; });
    var active = !!(f.status || f.channel);
    var toolbar = '<div style="display:flex;align-items:center;gap:10px;flex-wrap:wrap;margin-bottom:14px">' +
      selectBox('ntF', 'status', f.status, [['', '全部状态']].concat(statuses)) +
      selectBox('ntF', 'channel', f.channel, [['', '全部通道']].concat(channels)) +
      (active ? '<button data-act="clearf" data-group="ntF" style="font-size:12.5px;color:#2563EB;font-weight:500;padding:6px 4px">清除</button>' : '') +
      '<div style="flex:1"></div><span style="font-size:12.5px;color:#8A94A6">' + rows.length + ' / ' + data.notifs.length + ' 通知</span></div>';
    if (!rows.length) return toolbar + noMatch('ntF', '通知');
    var trs = rows.map(function (n) {
      var b = badge(n.status), edge = n.status === 'failed' ? P.danger : 'transparent', rb = n.status === 'failed' ? '#FEF7F7' : '#fff';
      var errHtml = n.error ? '<div title="' + esc(n.error) + '" style="font-size:11.5px;color:#DC2626;margin-top:3px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;max-width:280px">⚠ ' + esc(n.error) + '</div>' : '';
      return '<tr class="skp-row" data-act="open" data-type="notif" data-id="' + esc(n.id) + '" style="cursor:pointer;border-left:3px solid ' + edge + ';background:' + rb + '">' +
        '<td style="' + TD + ';font-size:12px;color:#5B6676">' + esc(rel(n.time)) + '</td>' +
        '<td style="' + TD + '">' + badgePill(b) + '</td>' +
        '<td style="' + TD + ';font-size:12.5px;color:#1A2233" class="mono">' + esc(n.event_type) + '</td>' +
        '<td style="' + TD + ';font-size:12.5px;color:#5B6676">' + esc(n.rule) + '</td>' +
        '<td style="' + TD + ';font-size:12.5px;color:#1A2233">' + esc(n.channel) + '</td>' +
        '<td style="' + TD + ';font-size:12px;color:#5B6676" class="mono">' + esc(n.recipients.join(', ')) + '</td>' +
        '<td style="' + TD + ';font-size:12.5px;color:#1A2233">' + esc(n.summary) + errHtml + '</td></tr>';
    }).join('');
    var head = '<thead><tr>' + th('时间', { w: '110px' }) + th('状态', { w: '100px' }) + th('事件类型') + th('规则') + th('通道') + th('接收人') + th('摘要 / 错误') + '</tr></thead>';
    return toolbar + '<div style="background:#fff;border:1px solid #E3E8EF;border-radius:10px;overflow:hidden;box-shadow:0 1px 2px rgba(16,24,40,.06)"><table style="width:100%;border-collapse:collapse">' + head + '<tbody>' + trs + '</tbody></table></div>';
  }

  // ---------- 管理 / 设置（规划预览） ----------
  function adminView() {
    var parts = Array.from(new Set(data.nodes.map(function (n) { return n.partition; })));
    var partRows = (parts.length ? parts : ['default']).map(function (p) {
      var cnt = data.nodes.filter(function (n) { return n.partition === p; }).length;
      return { a: p, b: cnt + ' 节点' };
    });
    var cards = [
      { title: '分区 Partitions', desc: 'name / priority / max_walltime / allowed_users / limits 配置与增改。', api: '⏳ Partitions (需后端)', rows: partRows },
      { title: '规则 Rules', desc: 'match 标签条件 → notify users/channels，含 throttle cooldown/resolve。', api: '⏳ Rules (需后端)', rows: [{ a: 'disk-alert', b: 'disk>90% → email' }, { a: 'idle-gpu', b: 'util<5% 30m → slack' }, { a: 'job-fail', b: 'job.failed → owner' }] },
      { title: '通道 Channels', desc: 'name / kind / 配置摘要（凭据脱敏），支持「测试连通」TestChannel。', api: '⏳ Channels · TestChannel', rows: [{ a: 'log', b: '内置 · stdout' }] },
      { title: '用户 Users', desc: 'name / role / contacts / preferred_channels / quiet_hours。', api: '⏳ Users (需后端)', rows: [{ a: 'anonymous', b: 'default' }] },
      { title: '设置 / 登录 Settings', desc: '调度策略 / 指标保留 / Token 只读展示 + 登录页。', api: '⏳ Settings · Login/Token', rows: [{ a: '调度策略', b: 'priority+backfill' }, { a: '指标端点', b: 'Prometheus /metrics' }, { a: 'API', b: '/api/v1/*' }] }
    ];
    var notice = '<div style="display:flex;align-items:center;gap:10px;background:#FCF1E2;border:1px solid #F0DCBE;border-radius:10px;padding:13px 16px;margin-bottom:18px">' +
      '<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="#D97706" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"></circle><path d="M12 7v5l3 2"></path></svg>' +
      '<span style="font-size:13.5px;color:#92560A">⏳ 该分组需配套后端接口支持，当前为规划稿预览（IA / 导航已预留）。</span></div>';
    var grid = '<div style="display:grid;grid-template-columns:repeat(auto-fill,minmax(300px,1fr));gap:14px">' + cards.map(function (c) {
      var rows = c.rows.map(function (r) { return '<div style="display:flex;align-items:center;justify-content:space-between;background:#F7F9FC;border:1px solid #EDF1F5;border-radius:7px;padding:8px 11px"><span style="font-size:12.5px;color:#1A2233;font-weight:500">' + esc(r.a) + '</span><span class="mono" style="font-size:11.5px;color:#8A94A6">' + esc(r.b) + '</span></div>'; }).join('');
      return '<div style="background:#fff;border:1px solid #E3E8EF;border-radius:10px;padding:18px 20px;box-shadow:0 1px 2px rgba(16,24,40,.06);opacity:.95">' +
        '<div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:8px"><div style="font-size:15px;font-weight:600;color:#1A2233">' + esc(c.title) + '</div><span style="font-size:11px;color:#B0B8C6;border:1px solid #EDF1F5;border-radius:999px;padding:2px 9px">规划中 ⏳</span></div>' +
        '<div style="font-size:12.5px;color:#5B6676;line-height:1.6;margin-bottom:14px">' + esc(c.desc) + '</div>' +
        '<div style="display:flex;flex-direction:column;gap:7px">' + rows + '</div>' +
        '<div class="mono" style="margin-top:13px;font-size:11.5px;color:#B0B8C6">' + esc(c.api) + '</div></div>';
    }).join('') + '</div>';
    return notice + grid;
  }

  // ---------- 抽屉 Drawer ----------
  function drawerKV(label, valHtml) {
    return '<div style="display:flex;justify-content:space-between;align-items:center;padding:11px 14px;background:#fff"><span style="font-size:12.5px;color:#5B6676">' + esc(label) + '</span>' + valHtml + '</div>';
  }
  function drawerView() {
    var dr = state.drawer;
    if (!dr) return '';
    var w = 480, kicker = '', title = '', body = '';
    if (dr.type === 'node') {
      var n = data.nodes.filter(function (x) { return x.id === dr.id; })[0];
      if (!n) return '';
      w = 580; kicker = '节点 Node · ' + n.partition; title = n.name;
      var memPct = n.memUsed != null && n.memTotal > 0 ? Math.round(n.memUsed / n.memTotal * 100) : 0;
      var devs = data.devices.filter(function (x) { return x.node === n.name; });
      var njobs = data.jobs.filter(function (x) { return x.node === n.name && ['RUNNING', 'ASSIGNED', 'CANCELLING'].indexOf(x.state) >= 0; });
      var head = '<div style="display:flex;align-items:center;gap:10px;flex-wrap:wrap;margin-bottom:16px">' + badgePill(badge(n.state), 12) +
        '<span class="mono" style="font-size:12.5px;color:#5B6676">' + esc(n.addr) + '</span><span style="font-size:12px;color:#8A94A6">agent ' + esc(n.agent) + '</span>' +
        '<div style="flex:1"></div><span title="⏳ 需后端" style="font-size:12px;color:#B0B8C6;border:1px solid #EDF1F5;border-radius:7px;padding:4px 11px;cursor:not-allowed">' + (n.state === 'DRAIN' ? 'Resume' : 'Drain') + ' ⏳</span></div>' +
        '<div style="font-size:11.5px;color:#8A94A6;margin-bottom:14px">最近心跳 ' + esc(rel(n.hb)) + ' · ' + esc(abs(n.hb)) + '</div>';
      var rings = '<div style="display:grid;grid-template-columns:1fr 1fr;gap:12px;margin-bottom:16px">' +
        '<div style="border:1px solid #EDF1F5;border-radius:10px;padding:14px;display:flex;align-items:center;gap:13px">' + ringSvg(ring(n.cpuUtil || 0, 30, mc(n.cpuUtil || 0)), 74, (n.cpuUtil != null ? n.cpuUtil : '—') + '%') +
        '<div><div style="font-size:12px;color:#5B6676;font-weight:600;margin-bottom:4px">CPU</div><div style="font-size:11.5px;color:#8A94A6">' + n.cpus + ' cores</div><div style="font-size:11.5px;color:#8A94A6">load ' + esc(n.load ? n.load.join(' / ') : '—') + '</div></div></div>' +
        '<div style="border:1px solid #EDF1F5;border-radius:10px;padding:14px;display:flex;align-items:center;gap:13px">' + ringSvg(ring(memPct, 30, mc(memPct)), 74, memPct + '%') +
        '<div><div style="font-size:12px;color:#5B6676;font-weight:600;margin-bottom:4px">内存</div><div class="mono" style="font-size:11.5px;color:#8A94A6">' + esc((n.memUsed != null ? gibR(n.memUsed * 1073741824) : '—') + '/' + gibR(n.memTotal * 1073741824) + 'G') + '</div><div style="font-size:11.5px;color:#8A94A6">' + esc((n.memUsed != null ? gibR((n.memTotal - n.memUsed) * 1073741824) : '—') + 'G 可用') + '</div></div></div></div>';
      var disksHtml = n.disks.length ? '<div style="margin-bottom:16px"><div style="font-size:12.5px;font-weight:600;color:#1A2233;margin-bottom:9px">磁盘</div>' + n.disks.map(function (dk) {
        return '<div style="margin-bottom:9px"><div style="display:flex;justify-content:space-between;font-size:12px;margin-bottom:4px"><span class="mono" style="color:#1A2233">' + esc(dk.mount) + ' <span style="color:#8A94A6">' + esc(dk.fs) + '</span></span><span class="mono" style="color:#5B6676">' + esc(dk.usedGB + '/' + dk.totalGB + 'G · inode ' + dk.inode + '%') + '</span></div>' + bar(dk.pct, mc(dk.pct)) + '</div>';
      }).join('') + '</div>' : '';
      var devHtml = devs.length ? '<div style="margin-bottom:16px"><div style="font-size:12.5px;font-weight:600;color:#1A2233;margin-bottom:9px">设备 ' + devs.length + '</div><div style="display:grid;grid-template-columns:1fr 1fr;gap:8px">' + devs.map(function (x) {
        return '<button data-act="open" data-type="device" data-id="' + esc(x.id) + '" class="skp-card-h" style="text-align:left;border:1px solid #EDF1F5;border-radius:9px;padding:10px 11px;background:#fff"><div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:5px"><span class="mono" style="font-size:12px;font-weight:600;color:#1A2233">' + esc(x.kind + ' #' + x.index) + '</span><span style="font-size:10px;font-weight:500;padding:1px 7px;border-radius:999px;background:' + devBadge(x.status).bg + ';color:' + devBadge(x.status).fg + '">' + esc(devBadge(x.status).label) + '</span></div><div class="mono" style="font-size:11px;color:#5B6676">util ' + x.util + '% · ' + (x.temp ? x.temp + '°' : '—') + '</div><div style="font-size:11px;color:#8A94A6;margin-top:2px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis">' + esc(x.job || '空闲') + '</div></button>';
      }).join('') + '</div></div>' : '';
      var jobsHtml = njobs.length ? '<div style="margin-bottom:16px"><div style="font-size:12.5px;font-weight:600;color:#1A2233;margin-bottom:9px">本节点作业</div>' + njobs.map(function (x) {
        return '<button data-act="job" data-id="' + esc(x.id) + '" class="skp-row" style="width:100%;text-align:left;display:flex;align-items:center;gap:9px;padding:8px 10px;border:1px solid #EDF1F5;border-radius:8px;margin-bottom:6px"><span style="font-size:10.5px;font-weight:500;padding:2px 8px;border-radius:999px;background:' + badge(x.state).bg + ';color:' + badge(x.state).fg + '">' + esc(x.state) + '</span><span style="font-size:13px;color:#2563EB;font-weight:500">' + esc(x.name) + '</span><span style="font-size:11.5px;color:#8A94A6">' + esc(x.owner) + '</span></button>';
      }).join('') + '</div>' : '';
      var labels = '<div><div style="font-size:12.5px;font-weight:600;color:#1A2233;margin-bottom:9px">标签 labels</div><div style="display:flex;gap:6px;flex-wrap:wrap">' + Object.keys(n.labels).map(function (k) { return '<span class="mono" style="font-size:11.5px;color:#5B6676;background:#EEF1F5;padding:3px 9px;border-radius:6px">' + esc(k) + '=' + esc(n.labels[k]) + '</span>'; }).join('') + '</div></div>';
      body = head + rings + disksHtml + devHtml + jobsHtml + labels;
    } else if (dr.type === 'device') {
      var x = data.devices.filter(function (v) { return v.id === dr.id; })[0];
      if (!x) return '';
      w = 440; kicker = '设备 ' + x.kind + ' · ' + x.vendor; title = x.kind + ' #' + x.index;
      var topRing = '<div style="display:flex;flex-direction:column;align-items:center;margin-bottom:18px">' + ringSvg(ring(x.util, 46, x.status === 'idle' ? P.warning : P.primary), 120, x.util + '%', '利用率') +
        '<span style="margin-top:8px">' + badgePill(devBadge(x.status), 12) + '</span></div>';
      var kvs = '<div style="display:flex;flex-direction:column;gap:1px;background:#EDF1F5;border:1px solid #EDF1F5;border-radius:10px;overflow:hidden">' +
        drawerKV('型号', '<span style="font-size:12.5px;color:#1A2233;font-weight:500">' + esc(x.name) + '</span>') +
        drawerKV('节点', '<button data-act="jumpnode" data-name="' + esc(x.node) + '" style="font-size:12.5px;color:#2563EB;font-weight:500">' + esc(x.node) + ' →</button>') +
        drawerKV('UUID', '<span class="mono" style="font-size:11.5px;color:#8A94A6">' + esc(x.uuid) + '</span>') +
        drawerKV('显存', '<span class="mono" style="font-size:12.5px;color:#1A2233">' + esc((Math.round(x.memU * 10) / 10) + '/' + (Math.round(x.memT * 10) / 10) + 'G') + ' <span style="color:' + mc(x.memPct) + '">' + x.memPct + '%</span></span>') +
        drawerKV('温度 / 功耗', '<span class="mono" style="font-size:12.5px"><span style="color:' + tc(x.temp) + '">' + (x.temp ? x.temp + '°C' : '—') + '</span> <span style="color:#8A94A6">· ' + (x.power ? x.power + 'W' : '—') + '</span></span>') + '</div>';
      var occBox = '';
      if (x.job) {
        occBox = '<div style="margin-top:14px;padding:13px 15px;border-radius:10px;background:' + (x.status === 'idle' ? '#FDF7EC' : '#F7F9FC') + ';border:1px solid ' + (x.status === 'idle' ? '#F0DCBE' : '#EDF1F5') + '"><div style="font-size:11.5px;color:#8A94A6;margin-bottom:5px">占用者</div><button data-act="jumpjob" data-name="' + esc(x.job) + '" style="font-size:13.5px;color:#2563EB;font-weight:600">' + esc(x.job) + ' →</button>' + (x.status === 'idle' ? '<div style="font-size:11.5px;color:#D97706;margin-top:6px">⚠ 占用但利用率 &lt;5%，疑似占着空置（device.idle 隐患）</div>' : '') + '</div>';
      } else {
        occBox = '<div style="margin-top:14px;padding:13px 15px;border-radius:10px;background:#F2FBF5;border:1px solid #C6E8D1;font-size:12.5px;color:#16A34A">空闲可用，可被调度</div>';
      }
      body = topRing + kvs + occBox;
    } else if (dr.type === 'event') {
      var e = data.events.filter(function (v) { return v.id === dr.id; })[0];
      if (!e) return '';
      w = 460; kicker = '事件 Event'; title = e.type;
      var lk = Object.keys(e.labels).map(function (k) {
        var v = e.labels[k], jump = null;
        if (k === 'node') jump = 'jumpnode'; if (k === 'job') jump = 'jumpjob';
        var valHtml = jump ? '<button data-act="' + jump + '" data-name="' + esc(v) + '" class="mono" style="font-size:12.5px;color:#2563EB;font-weight:500">' + esc(v) + ' →</button>' : '<span class="mono" style="font-size:12.5px;color:#1A2233">' + esc(v) + '</span>';
        return '<div style="display:flex;align-items:center;justify-content:space-between;background:#F7F9FC;border:1px solid #EDF1F5;border-radius:8px;padding:9px 12px"><span class="mono" style="font-size:12.5px;color:#5B6676">' + esc(k) + '</span>' + valHtml + '</div>';
      }).join('');
      body = '<div style="display:flex;align-items:center;gap:10px;margin-bottom:14px">' + badgePill(badge(e.severity), 12) + '<span style="font-size:12.5px;color:#5B6676">来源 ' + esc(e.source) + '</span></div>' +
        '<div style="font-size:14px;color:#1A2233;line-height:1.6;margin-bottom:14px">' + esc(e.summary) + '</div>' +
        '<div style="font-size:12px;color:#8A94A6;margin-bottom:18px">' + esc(rel(e.time)) + ' · <span class="mono">' + esc(abs(e.time)) + '</span></div>' +
        '<div style="font-size:12.5px;font-weight:600;color:#1A2233;margin-bottom:9px">标签 labels</div><div style="display:flex;flex-direction:column;gap:7px">' + (lk || '<span style="font-size:12px;color:#B0B8C6">（无）</span>') + '</div>';
    } else if (dr.type === 'notif') {
      var nt = data.notifs.filter(function (v) { return v.id === dr.id; })[0];
      if (!nt) return '';
      w = 460; kicker = '通知 Notification'; title = nt.event_type;
      var errBox = nt.error ? '<div style="background:#FCEBEC;border:1px solid #F3C9CC;border-radius:10px;padding:12px 14px;margin-bottom:14px"><div style="font-size:11.5px;color:#DC2626;font-weight:600;margin-bottom:5px">投递错误 error</div><div class="mono" style="font-size:12px;color:#B42318;line-height:1.55;word-break:break-word">' + esc(nt.error) + '</div></div>' : '';
      var evBtn = nt.event_id ? '<button data-act="open" data-type="event" data-id="' + esc(nt.event_id) + '" style="font-size:13px;color:#2563EB;font-weight:500">查看关联事件 →</button>' : '';
      body = '<div style="display:flex;align-items:center;gap:10px;margin-bottom:16px">' + badgePill(badge(nt.status), 12) + '<span style="font-size:12px;color:#8A94A6">' + esc(rel(nt.time)) + '</span></div>' +
        '<div style="display:flex;flex-direction:column;gap:1px;background:#EDF1F5;border:1px solid #EDF1F5;border-radius:10px;overflow:hidden;margin-bottom:14px">' +
        drawerKV('规则 rule', '<span style="font-size:12.5px;color:#1A2233;font-weight:500">' + esc(nt.rule) + '</span>') +
        drawerKV('通道 channel', '<span style="font-size:12.5px;color:#1A2233">' + esc(nt.channel) + '</span>') +
        drawerKV('接收人', '<span class="mono" style="font-size:12px;color:#1A2233">' + esc(nt.recipients.join(', ')) + '</span>') +
        drawerKV('摘要', '<span style="font-size:12.5px;color:#1A2233;text-align:right;max-width:230px">' + esc(nt.summary) + '</span>') + '</div>' + errBox + evBtn;
    }
    return '<div data-act="closedrawer" style="position:fixed;inset:0;background:rgba(16,24,40,.28);z-index:40"></div>' +
      '<div style="position:fixed;top:0;right:0;bottom:0;width:' + w + 'px;max-width:94vw;background:#fff;z-index:41;box-shadow:-8px 0 30px rgba(16,24,40,.14);display:flex;flex-direction:column">' +
      '<div style="flex:0 0 auto;padding:16px 20px;border-bottom:1px solid #E3E8EF;display:flex;align-items:flex-start;justify-content:space-between;gap:12px"><div style="min-width:0">' +
      '<div style="font-size:11px;font-weight:600;letter-spacing:.05em;color:#8A94A6;text-transform:uppercase">' + esc(kicker) + '</div>' +
      '<div style="font-size:17px;font-weight:700;color:#1A2233;margin-top:3px;word-break:break-all">' + esc(title) + '</div></div>' +
      '<button data-act="closedrawer" class="skp-nav" style="width:32px;height:32px;border-radius:8px;display:flex;align-items:center;justify-content:center;color:#5B6676;flex:0 0 auto">' + svg('close', 18, 'currentColor', 2) + '</button></div>' +
      '<div style="flex:1;overflow-y:auto;padding:18px 20px">' + body + '</div></div>';
  }

  // ---------- 确认对话框 ----------
  function confirmView() {
    var c = state.confirm;
    if (!c) return '';
    return '<div data-act="confirmno" style="position:fixed;inset:0;background:rgba(16,24,40,.32);z-index:50;display:flex;align-items:center;justify-content:center">' +
      '<div data-act="stop" style="background:#fff;border-radius:14px;width:420px;max-width:92vw;box-shadow:0 18px 48px rgba(16,24,40,.24);overflow:hidden">' +
      '<div style="padding:22px 24px 18px"><div style="display:flex;align-items:center;gap:12px;margin-bottom:12px">' +
      '<div style="width:40px;height:40px;border-radius:50%;background:#FCEBEC;display:flex;align-items:center;justify-content:center;flex:0 0 auto"><svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="#DC2626" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><path d="M10.3 3.3a2 2 0 0 1 3.4 0l8 13.4A2 2 0 0 1 20 20H4a2 2 0 0 1-1.7-3.3z"></path><path d="M12 9v4M12 17h.01"></path></svg></div>' +
      '<h3 style="margin:0;font-size:16px;font-weight:700;color:#1A2233">取消作业？</h3></div>' +
      '<p style="margin:0;font-size:13.5px;color:#5B6676;line-height:1.6">将向调度器发送取消请求，作业 <span class="mono" style="color:#1A2233;font-weight:500">' + esc(c.name) + '</span> 会进入 <b style="color:#D97706">CANCELLING</b>，已占用的设备将被回收。此操作不可撤销。</p></div>' +
      '<div style="display:flex;gap:10px;justify-content:flex-end;padding:14px 24px;background:#F7F9FC;border-top:1px solid #EDF1F5">' +
      '<button data-act="confirmno" style="font-size:13.5px;color:#5B6676;font-weight:500;border:1px solid #E3E8EF;border-radius:8px;padding:9px 18px;background:#fff">返回</button>' +
      '<button data-act="confirmyes" style="font-size:13.5px;color:#fff;font-weight:600;border-radius:8px;padding:9px 18px;background:#DC2626">确认取消</button></div></div></div>';
  }

  // ---------- Toast ----------
  function toastView() {
    var t = state.toast;
    if (!t || !t.show) return '';
    return '<div style="position:fixed;bottom:22px;left:50%;transform:translateX(-50%);z-index:60;display:flex;align-items:center;gap:10px;background:#1A2233;color:#fff;padding:11px 18px;border-radius:10px;box-shadow:0 8px 24px rgba(16,24,40,.22);font-size:13.5px">' +
      '<span style="width:8px;height:8px;border-radius:50%;background:' + t.dot + '"></span>' + esc(t.msg) + '</div>';
  }

  // ---------- 交互动作 ----------
  function setPage(page) { state.page = page; state.jobDetailId = null; state.drawer = null; render(); }
  function openJob(id) { state.page = 'jobs'; state.jobDetailId = id; state.drawer = null; loadLogs(id); render(); }
  function jumpNode(name) { var n = data.nodes.filter(function (x) { return x.name === name; })[0]; if (n) { state.page = 'nodes'; state.drawer = { type: 'node', id: n.id }; render(); } }
  function jumpJob(name) { var j = data.jobs.filter(function (x) { return x.name === name; })[0]; if (j) openJob(j.id); }

  function doSubmit() {
    var sf = state.sf;
    if (!String(sf.name).trim()) { toast('请填写作业名 name', P.danger); return; }
    if (!String(sf.command).trim()) { toast('请填写命令 command', P.danger); return; }
    var env = {}; sf.env.forEach(function (r) { if (r.k) env[r.k] = r.v; });
    var body = {
      name: sf.name, owner: sf.owner, partition: sf.partition, priority: Number(sf.priority) || 0,
      command: sf.command, workdir: sf.workdir, cpus: Number(sf.cpus) || 0, mem: sf.mem,
      gpus: Number(sf.gpus) || 0, gpu_type: sf.gpuType, walltime: sf.walltime, env: env
    };
    fetch(API + '/jobs', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
      .then(function (r) { return r.json().then(function (d) { if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status)); return d; }); })
      .then(function (d) { toast('作业 ' + sf.name + ' 已提交 (' + d.job_id + ')', P.success); return refreshData().then(function () { openJob(d.job_id); }); })
      .catch(function (err) { toast('提交失败：' + ((err && err.message) || err), P.danger); });
  }
  function doCancel() {
    var c = state.confirm; state.confirm = null; render();
    if (!c) return;
    fetch(API + '/jobs/' + encodeURIComponent(c.id) + '/cancel', { method: 'POST' })
      .then(function (r) { return r.json().then(function (d) { if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status)); return d; }); })
      .then(function (d) { toast('已请求取消 → ' + d.state, P.warning); return refreshData(); })
      .catch(function (err) { toast('取消失败：' + ((err && err.message) || err), P.danger); });
  }
  function clearFilter(group) {
    var defs = {
      nodeF: { state: '', partition: '', q: '' }, jobF: { state: '', owner: '', q: '' },
      devF: { kind: 'all', node: '', vendor: '', status: '', view: state.devF.view },
      evF: { severity: '', type: '', q: '' }, ntF: { status: '', channel: '' }
    };
    if (defs[group]) { state[group] = defs[group]; render(); }
  }

  function dispatch(act, el, e) {
    var ds = el.dataset || {};
    switch (act) {
      case 'nav': setPage(ds.page); break;
      case 'collapse': state.collapsed = !state.collapsed; render(); break;
      case 'variant': state.dashVariant = ds.v; render(); break;
      case 'refresh': refreshData(); break;
      case 'setrefresh': state.refresh = parseInt(el.value, 10) || 0; render(); break;
      case 'open': state.drawer = { type: ds.type, id: ds.id }; render(); break;
      case 'closedrawer': state.drawer = null; render(); break;
      case 'job': openJob(ds.id); break;
      case 'jobback': state.jobDetailId = null; render(); break;
      case 'askcancel': state.confirm = { id: ds.id, name: ds.name }; render(); break;
      case 'confirmyes': doCancel(); break;
      case 'confirmno': state.confirm = null; render(); break;
      case 'jumpnode': jumpNode(ds.name); break;
      case 'jumpjob': jumpJob(ds.name); break;
      case 'stop': if (e && e.stopPropagation) e.stopPropagation(); break;
      case 'setf': {
        var val = ('val' in ds) ? ds.val : el.value;
        if (ds.key === 'status' && ds.group === 'devF' && state.devF.status === val) val = '';
        state[ds.group][ds.key] = val;
        if (!composing) render();
        break;
      }
      case 'clearf': clearFilter(ds.group); break;
      case 'setsf': state.sf[ds.key] = el.value; if (!composing) render(); break;
      case 'addenv': state.sf.env.push({ k: '', v: '' }); render(); break;
      case 'setenv': state.sf.env[parseInt(ds.idx, 10)][ds.key] = el.value; if (!composing) render(); break;
      case 'delenv': state.sf.env.splice(parseInt(ds.idx, 10), 1); render(); break;
      case 'dosubmit': doSubmit(); break;
    }
  }

  function onEvt(e) {
    var el = e.target.closest ? e.target.closest('[data-act]') : null;
    if (!el) return;
    var tag = el.tagName;
    if (e.type === 'click') { if (tag === 'INPUT' || tag === 'SELECT' || tag === 'TEXTAREA') return; }
    else if (e.type === 'change') { if (tag !== 'SELECT') return; }
    else if (e.type === 'input') { if (tag !== 'INPUT' && tag !== 'TEXTAREA') return; }
    dispatch(el.dataset.act, el, e);
  }

  // ---------- 启动 ----------
  function boot() {
    render();
    refreshData();
    setInterval(function () {
      now = Math.floor(Date.now() / 1000);
      if (state.refresh > 0 && now - lastRefresh >= state.refresh) { refreshData(); }
      else {
        var ae = document.activeElement, typing = ae && (ae.tagName === 'INPUT' || ae.tagName === 'TEXTAREA');
        if (!composing && !typing) render();
      }
    }, 1000);
    document.addEventListener('click', onEvt);
    document.addEventListener('change', onEvt);
    document.addEventListener('input', onEvt);
    document.addEventListener('compositionstart', function () { composing = true; });
    document.addEventListener('compositionend', function (e) {
      composing = false;
      var el = e.target.closest ? e.target.closest('[data-act]') : null;
      if (el) dispatch(el.dataset.act, el, { type: 'input' });
    });
  }
  boot();
})();
