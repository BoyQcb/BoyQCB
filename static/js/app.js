/* M3U8 广告过滤后台 - 前端逻辑（无第三方框架，ECharts 已本地化） */
(function () {
  'use strict';

  /* ---------- 基础工具 ---------- */
  const $ = (sel, root) => (root || document).querySelector(sel);
  const $$ = (sel, root) => Array.from((root || document).querySelectorAll(sel));
  const esc = (s) => String(s == null ? '' : s).replace(/[&<>"']/g, c => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'
  }[c]));

  // adminPath 后台入口（index.html 由后端注入），未注入时退化为旧版根路径
  function adminPath() {
    if (window.ADMIN_PATH) return String(window.ADMIN_PATH).replace(/\/+$/, '');
    const seg = location.pathname.split('/').filter(Boolean);
    // 形如 /admin/xxx 时取首段作为入口，兜底 /admin
    return seg.length && seg[0] !== 'index.html' ? '/' + seg[0] : '/admin';
  }

  async function api(path, opts) {
    const o = opts || {};
    const url = String(path).startsWith('/api/') ? adminPath() + path : path;
    const res = await fetch(url, {
      method: o.method || 'GET',
      headers: o.body ? { 'Content-Type': 'application/json' } : {},
      body: o.body ? JSON.stringify(o.body) : undefined
    });
    let data = {};
    try { data = await res.json(); } catch (e) { data = {}; }
    if (data.code === 401) { showApp(false); }
    return data;
  }
  function toast(msg, type) {
    const t = $('#toast');
    t.textContent = msg;
    t.className = 'toast ' + (type || '');
    if (window._tt) clearTimeout(window._tt);
    window._tt = setTimeout(() => t.classList.add('hidden'), 2600);
  }
  function fmtBytes(b) {
    if (!b) return '0 B';
    const u = ['B', 'KB', 'MB', 'GB'];
    let i = 0; let v = b;
    while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
    return v.toFixed(1) + ' ' + u[i];
  }
  function fmtElapsed(ms) { return ms == null ? '-' : ms + ' ms'; }
  function cleanUnitName(u) { return u === 's' ? '秒' : (u === 'm' ? '分钟' : '小时'); }
function cleanTypeName(v) {
  const m = { expired: '过期缓存', all: '全部缓存', site: '指定站点缓存', root: '根级散落缓存', preview: '预览/临时缓存', seghash: '片段采样哈希' };
  return m[v] || v;
}

  /* ---------- 应用状态 ---------- */
  let current = 'dashboard';
  let sitesList = [];
  let lineChart = null, pieChart = null;
  let testChartReady = false;
  let testResetHook = null;   // 离开 M3U8 测试页时的清理钩子（由测试页模块注册）

  /* ---------- 认证 ---------- */
  /* ---------- 主题：自动（按时间）/ 亮色 / 暗色（localStorage: dytt.theme，默认 auto） ----------
     首屏防闪在 index.html 的 head 内联脚本里已提前写好 data-theme，
     这里只负责交互切换、状态记忆与图表配色同步。
  -------------------------------------------------------------------------- */
  const THEME_KEY = 'dytt.theme';
  // 自动（跟随系统）模式按时间段判定：18:00 至次日 07:00 为暗色，其余时段为亮色
  const NIGHT_START = 18;
  const NIGHT_END = 7;
  function autoDarkByTime() {
    const h = new Date().getHours();
    return h >= NIGHT_START || h < NIGHT_END;
  }

  function themeIsDark() { return document.documentElement.getAttribute('data-theme') === 'dark'; }
  function cssVar(name, fallback) {
    try {
      const v = getComputedStyle(document.documentElement).getPropertyValue(name);
      return (v && v.trim()) || fallback;
    } catch (e) { return fallback; }
  }

  function applyTheme(mode) {
    if (mode !== 'light' && mode !== 'dark') mode = 'auto';
    try { localStorage.setItem(THEME_KEY, mode); } catch (e) {}
    const dark = mode === 'dark' || (mode === 'auto' && autoDarkByTime());
    document.documentElement.setAttribute('data-theme', dark ? 'dark' : 'light');
    const btn = $('#themeBtn');
    if (btn) btn.textContent = mode === 'auto' ? '◐' : (mode === 'dark' ? '☾' : '☀');
    $$('#themeMenu button[data-theme-set]').forEach(b => b.classList.toggle('on', b.dataset.themeSet === mode));
  }

  function initTheme() {
    let mode = 'auto';
    try { mode = localStorage.getItem(THEME_KEY) || 'auto'; } catch (e) {}
    applyTheme(mode);

    const btn = $('#themeBtn'), menu = $('#themeMenu');
    if (!btn || !menu || btn.__themeBound) return;
    btn.__themeBound = true;

    btn.addEventListener('click', (e) => {
      e.stopPropagation();
      menu.classList.toggle('hidden');
    });
    menu.addEventListener('click', (e) => {
      const b = e.target.closest('button[data-theme-set]');
      if (!b) return;
      applyTheme(b.dataset.themeSet);
      menu.classList.add('hidden');
      try { toast('主题：' + (b.textContent || '').trim()); } catch (_) {}
      // 图表配色取自 CSS 变量，切换主题后重绘当前页
      if (current === 'dashboard') { try { renderDashboard(); } catch (_) {} }
    });
    document.addEventListener('click', (e) => {
      if (!menu.classList.contains('hidden') && !(e.target.closest && e.target.closest('.theme-wrap'))) {
        menu.classList.add('hidden');
      }
    });
    // 自动模式：跨过 18:00 / 07:00 边界时自动切换（每分钟检查一次）
    if (!initTheme.__autoTimer) {
      initTheme.__autoTimer = setInterval(() => {
        let m = 'auto';
        try { m = localStorage.getItem(THEME_KEY) || 'auto'; } catch (e) {}
        if (m !== 'auto') return;
        if (autoDarkByTime() !== themeIsDark()) {
          applyTheme('auto');
          if (current === 'dashboard') { try { renderDashboard(); } catch (_) {} }
        }
      }, 60000);
    }
  }

  /* ---------- ECharts 配色：跟随主题，避免深色背景上的深色文字 ---------- */
  function chartTheme() {
    return {
      text: cssVar('--text-2', themeIsDark() ? '#a8b2c6' : '#5b6478'),
      line: cssVar('--border', themeIsDark() ? '#2a3446' : '#e7eaf3'),
      split: cssVar('--border-soft', themeIsDark() ? '#202a3a' : '#eff1f7'),
      tipBg: cssVar('--surface-2', themeIsDark() ? '#1b2331' : '#fff'),
      primary: cssVar('--primary', '#4f6ef7'),
      success: cssVar('--success', '#10b981'),
      card: cssVar('--surface', themeIsDark() ? '#151b27' : '#fff')
    };
  }

  async function bootstrap() {
    initTheme();
    const d = await api('/api/me');
    const ver = d.version || '1.2.3';
    $('#ver').textContent = ver;
    if ($('#verTop')) $('#verTop').textContent = ver;
    if ($('#loginVer')) $('#loginVer').textContent = ver;
    showApp(!!d.logged);
    if (d.logged) { initSidebar(); bindScrollMemory(); route(); startClock(); }
  }
  function showApp(ok) {
    $('#loginPage').classList.toggle('hidden', ok);
    $('#app').classList.toggle('hidden', !ok);
    if (!ok) $('#loginPwd').focus();
  }
  function startClock() {
    setInterval(() => {
      const n = new Date();
      const pad = x => String(x).padStart(2, '0');
      $('#clock').textContent = n.getFullYear() + '-' + pad(n.getMonth() + 1) + '-' + pad(n.getDate()) + ' ' + pad(n.getHours()) + ':' + pad(n.getMinutes()) + ':' + pad(n.getSeconds());
    }, 1000);
  }

  /* ---------- 侧边栏：折叠 / 展开 / 窄屏抽屉（状态本地记住） ----------
     状态模型（唯一状态源，避免死状态）：
       collapsed: 宽屏（>1024px）有效，折叠成 68px 图标条（鼠标移入浮出完整菜单）
       open     : 窄屏（<=1024px）有效，抽屉是否展开
     1.1.1 起移除「固定 / 自动隐藏」开关：顶栏折叠按钮已能覆盖常驻与折叠两种诉求，
     相关的 pinned 状态、dytt.sb.pinned 存储键与 16px 自动隐藏态一并删除，不留死代码。
     所有变更都走 setSidebarState() → applySidebarState() 全量重算 body class，
     不残留互斥 class，因此「折叠后展不开」「小屏抽屉打不开」都不会出现。
  -------------------------------------------------------------------------- */
  const SB_COLLAPSE_KEY = 'dytt.sb.collapsed';// '1' 折叠成图标条
  const SB_NARROW_MQ = window.matchMedia ? window.matchMedia('(max-width: 1024px)')
    : { matches: false, addEventListener: function () {}, addListener: function () {} };

  const sbState = { collapsed: false, open: false };
  let sbPeekTimer = null;
  let sbBound = false;

  function isNarrow() { return !!SB_NARROW_MQ.matches; }

  function loadSidebarState() {
    // 未存过的值给明确默认：默认展开
    sbState.collapsed = localStorage.getItem(SB_COLLAPSE_KEY) === '1';
    sbState.open = false; // 抽屉不记忆，刷新后一律关闭
  }

  function applySidebarState() {
    const b = document.body;
    const narrow = isNarrow();

    // 两种互斥状态：窄屏抽屉 / 宽屏折叠图标条
    b.classList.toggle('sb-collapsed', !narrow && sbState.collapsed);
    b.classList.toggle('sb-open', narrow && sbState.open);
    if (!narrow || !sbState.open) b.classList.remove('sb-peek');
    syncToggleBtn();
  }

  function setSidebarState(patch) {
    Object.keys(patch || {}).forEach(k => { sbState[k] = !!patch[k]; });
    localStorage.setItem(SB_COLLAPSE_KEY, sbState.collapsed ? '1' : '0');
    applySidebarState();
  }

  function initSidebar() {
    loadSidebarState();
    applySidebarState();
    if (sbBound) return; // 事件只绑一次，重复调用只刷新状态
    sbBound = true;

    // 顶栏汉堡：窄屏=开关抽屉；宽屏=折叠 / 展开图标条
    const toggleBtn = $('#sbToggle');
    if (toggleBtn) {
      toggleBtn.addEventListener('click', (e) => {
        e.stopPropagation();
        if (isNarrow()) { setSidebarState({ open: !sbState.open }); return; }
        setSidebarState({ collapsed: !sbState.collapsed });
      });
      toggleBtn.addEventListener('keydown', (e) => { if (e.key === 'Enter' || e.key === ' ') e.preventDefault(); });
    }

    // 遮罩 / Esc 关闭抽屉
    const mask = $('#sbMask');
    if (mask) mask.addEventListener('click', () => setSidebarState({ open: false }));
    document.addEventListener('keydown', (e) => {
      if (e.key === 'Escape' && isNarrow() && sbState.open) setSidebarState({ open: false });
    });

    // 点击菜单项：窄屏自动收起抽屉
    const nav = $('#nav');
    if (nav) {
      nav.addEventListener('click', (e) => {
        if (e.target.closest('.nav-item') && isNarrow()) setSidebarState({ open: false });
      });
    }

    // 折叠态：鼠标移入浮出完整菜单、移出收起
    const sb = $('.sidebar');
    if (sb) {
      sb.addEventListener('mouseenter', () => {
        if (isNarrow()) return;
        if (!document.body.classList.contains('sb-collapsed')) return;
        if (sbPeekTimer) { clearTimeout(sbPeekTimer); sbPeekTimer = null; }
        document.body.classList.add('sb-peek');
      });
      sb.addEventListener('mouseleave', () => {
        if (sbPeekTimer) clearTimeout(sbPeekTimer);
        sbPeekTimer = setTimeout(() => document.body.classList.remove('sb-peek'), 180);
      });
    }

    // 断点切换：离开窄屏一定清掉抽屉态，进入窄屏先关抽屉避免半开半掩
    const onMQ = () => setSidebarState({ open: false });
    if (SB_NARROW_MQ.addEventListener) SB_NARROW_MQ.addEventListener('change', onMQ);
    else if (SB_NARROW_MQ.addListener) SB_NARROW_MQ.addListener(onMQ);
    window.addEventListener('resize', () => {
      if (!isNarrow() && sbState.open) setSidebarState({ open: false });
    });
  }

  // 顶栏折叠按钮的提示文案随状态变化（固定按钮已移除，不再同步任何按钮文案）
  function syncToggleBtn() {
    const t = $('#sbToggle');
    if (t) t.title = isNarrow()
      ? '展开 / 收起菜单'
      : (sbState.collapsed ? '展开侧边栏' : '折叠侧边栏');
  }

  // 窄屏下切换页面后收起抽屉
  function closeSidebarOnMobile() {
    if (isNarrow() && sbState.open) setSidebarState({ open: false });
  }

  /* ---------- 滚动保位：整页 innerHTML 重绘时，.content 高度会瞬间塌陷，浏览器把滚动位置夹回顶部 ---------- */
  let lastContentTop = 0;  // 最近一次用户滚动位置（容器塌陷导致 scrollTop 被夹为 0 时用它回填）
  let lastRoutePage = '';  // 上一次路由渲染的页面（用于区分「切页」与「同页重绘」）
  function scrollBox() { return document.querySelector('.content'); }
  function bindScrollMemory() {
    const c = scrollBox();
    if (!c || c.__scrollMem) return;
    c.__scrollMem = true;
    c.addEventListener('scroll', () => { lastContentTop = c.scrollTop; }, { passive: true });
  }
  function scrollSnapshot() {
    const c = scrollBox();
    const cur = c ? (c.scrollTop || 0) : (window.scrollY || 0);
    // 重绘前容器若已塌陷（scrollTop 被夹成 0），用最近一次用户滚动位置回填
    return cur || lastContentTop || 0;
  }
  // 重绘后连续两帧复位，避开表格/图表排版造成的二次塌陷
  function scrollRestore(sy) {
    const apply = () => {
      const c = scrollBox();
      if (c) c.scrollTop = sy; else window.scrollTo(0, sy);
    };
    apply();
    requestAnimationFrame(() => { apply(); requestAnimationFrame(apply); });
  }

  /* ---------- 路由 ---------- */
  function route() {
    const h = location.hash || '#/dashboard';
    const page = h.replace('#/', '').split('?')[0];
    const pages = ['dashboard', 'sites', 'logs', 'discover', 'test', 'system'];
    current = pages.indexOf(page) >= 0 ? page : 'dashboard';
    $$('#nav .nav-item').forEach(n => n.classList.toggle('active', n.dataset.page === current));
    const titles = { dashboard: '控制面板', sites: '资源站规则', logs: '调用日志', discover: '域名发现', test: 'M3U8 解析测试', system: '系统设置' };
    $('#pageTitle').textContent = titles[current];
    pages.forEach(p => $('#page-' + p).classList.toggle('active', p === current));
    // 只有真正切换页面才回顶；停留在同一页的重绘（含点当前页导航）保持原滚动位置
    if (lastRoutePage && lastRoutePage !== current) {
      const _rc = scrollBox(); if (_rc) _rc.scrollTop = 0;
      lastContentTop = 0;
    }
    lastRoutePage = current;
    // 离开测试页即清除解析记录，切回后不保留历史
    if (current !== 'test' && typeof testResetHook === 'function') {
      try { testResetHook(); } catch (e) { /* noop */ }
    }
    if (window._logsTimer) { clearInterval(window._logsTimer); window._logsTimer = null; }
    if (current === 'dashboard') renderDashboard();
    if (current === 'sites') renderSites();
    if (current === 'logs') renderLogs();
    if (current === 'discover') renderDiscover();
    if (current === 'test') renderTest();
    if (current === 'system') renderSystem();
    closeSidebarOnMobile();
  }

  /* ---------- 控制面板 ---------- */
  async function renderDashboard() {
    const box = $('#page-dashboard');
    box.innerHTML = '<div class="empty"><div class="ico">⟳</div>加载中...</div>';
    const d = await api('/api/dashboard');
    if (!d.code || d.code !== 200) { box.innerHTML = '<div class="empty">数据加载失败</div>'; return; }
    const s = d.data;
    const sys = s.system;

    box.innerHTML = `
      <div class="stat-grid">
        <div class="stat-card sc1"><div class="label">今日解析次数</div><div class="value big">${s.today_total}</div><div class="sub">截至 ${s.server_time}</div></div>
        <div class="stat-card success"><div class="label">今日成功数</div><div class="value">${s.today_ok}</div><div class="sub">成功缓存可用</div></div>
        <div class="stat-card warn"><div class="label">成功率</div><div class="value">${s.success_rate.toFixed(1)}%</div><div class="sub">今日成功/总数</div></div>
        <div class="stat-card violet"><div class="label">活跃站点数</div><div class="value">${s.active_sites}</div><div class="sub">今日有成功记录的站点</div></div>
      </div>

      <div class="chart-row">
        <div class="card"><div class="card-head"><h3>近 7 天解析趋势</h3></div><div class="card-body"><div id="trendChart" class="chart"></div></div></div>
        <div class="card"><div class="card-head"><h3>各站点解析占比</h3></div><div class="card-body"><div id="pieChart" class="chart"></div></div></div>
      </div>

      <div class="card">
        <div class="card-head"><h3>系统概况与运维</h3></div>
        <div class="card-body">
          <div class="sys-grid">
            <div class="sys-item"><div class="k">版本 / Go</div><div class="v">v${esc(sys.version)} · ${esc(sys.go_version)}</div></div>
            <div class="sys-item"><div class="k">监听地址</div><div class="v">${esc(sys.listen)}</div></div>
            <div class="sys-item"><div class="k">启动时间 / PID</div><div class="v">${esc(sys.started_at)} #${sys.pid}</div></div>
            <div class="sys-item"><div class="k">站点（启用/总数）</div><div class="v">${sys.site_enabled} / ${sys.site_total}</div></div>
            <div class="sys-item"><div class="k">缓存文件 / 占用</div><div class="v">${sys.cache_files} 个 · ${fmtBytes(sys.cache_bytes)}</div></div>
            <div class="sys-item"><div class="k">缓存有效期 / 哈希校验</div><div class="v">${sys.cache_ttl_days.toFixed(1)} 天 · ${sys.use_hash_check ? '开启' : '关闭'}</div></div>
            <div class="sys-item"><div class="k">解析容差 / 超时</div><div class="v">±${sys.tolerance}s · ${sys.fetch_timeout}s</div></div>
            <div class="sys-item"><div class="k">解析记录数 / 采样</div><div class="v">${sys.record_count} 条 · ${sys.sample_size_kb}KB</div></div>
            <div class="sys-item"><div class="k">定时清理 / 类型</div><div class="v">${sys.cache_auto_clean ? '开启 · 每 ' + (sys.cache_clean_every_h || 24) + ' ' + cleanUnitName(sys.cache_clean_unit) + (sys.cache_clean_next ? ' · 下次 ' + esc(sys.cache_clean_next) : '') : '关闭'} · ${(sys.cache_clean_types && sys.cache_clean_types.length) ? esc(sys.cache_clean_types.map(cleanTypeName).join('、')) : '仅过期缓存'}</div></div>
            <div class="sys-item"><div class="k">代理池</div><div class="v">${(sys.proxy_pool && sys.proxy_pool.enable) ? '已启用 · 池中 ' + (sys.proxy_pool.total || 0) + ' 个' : '未启用'}${sys.proxy_site_count ? ' · ' + sys.proxy_site_count + ' 个站点单独配置了代理' : ''}</div></div>
            <div class="sys-item"><div class="k">后台入口</div><div class="v">${esc(sys.admin_path || '/admin')}</div></div>
            <div class="sys-item sys-item-full"><div class="k">解析接口</div><div class="v path-row">${(sys.clean_paths && sys.clean_paths.length) ? sys.clean_paths.map(p => '<span class="pill ok mono">' + esc(p) + '</span>').join('') : '<span class="pill ok mono">/api/clean</span>'}</div></div>
          </div>
        </div>
      </div>

    `;

    // 图表
    drawTrend(s.trend || []);
    drawPie(s.site_pie || []);
  }

  /* ---------- 系统设置（独立页面） ---------- */
  let sysData = { settings: {}, cache: {}, pool: {}, ip: [], paths: [], clean_types: [] };
  let ipEditId = '';

  async function renderSystem() {
    const box = $('#page-system');
    const _sy = scrollSnapshot();
    box.innerHTML = '<div class="empty"><div class="ico">⟳</div>加载中...</div>';
    const d = await api('/api/system/settings');
    if (!d || d.code !== 200) { box.innerHTML = '<div class="empty">设置加载失败</div>'; return; }
    sysData.settings = d.settings || {};
    sysData.cache = d.cache || {};
    sysData.pool = d.proxy_pool || {};
    sysData.ip = d.ip_rules || [];
    sysData.paths = d.clean_paths || [];
    sysData.clean_types = d.clean_types || [];
    sysData.clean_path_default = d.clean_path_default || '/api/clean';
    sysData.clean_path_active = d.clean_path_active || ['/api/clean'];
    sysData.clean_path_samples = d.clean_path_samples || [];
    ipEditId = '';
    drawSystem();
    bindSystemEvents();
    renderIPTable();
    renderPoolTable();
    renderCleanPathTable(sysData.paths, sysData.clean_path_samples);
    scrollRestore(_sy);
  }

  function drawSystem() {
    const st = sysData.settings, c = sysData.cache, pl = sysData.pool;
    const box = $('#page-system');
    const typeList = sysData.clean_types || [];
    const selTypes = (st.cache_clean_types && st.cache_clean_types.length) ? st.cache_clean_types : ['expired'];
    const typeChips = typeList.map(t => `
      <label class="chk-chip" title="${esc(t.Desc || '')}">
        <input type="checkbox" class="clean-type-chk" value="${esc(t.Value)}" ${selTypes.indexOf(t.Value) >= 0 ? 'checked' : ''}>
        <span>${esc(t.Label || t.Value)}</span>
      </label>`).join('');
    // 生效地址与自定义地址表格由 renderActivePaths / renderCleanPathTable 渲染

    box.innerHTML = `
      <div class="set-wrap">
        <div class="set-2col">
        <!-- 1 访问设置 -->
        <div class="card">
          <div class="card-head"><h3>访问设置</h3><span class="card-sub">当前入口：<b id="adminPathNow"></b></span></div>
          <div class="card-body">
            <div class="set-row">
              <input id="adminPath" class="inp" style="width:220px;" placeholder="/admin">
            </div>
            <div class="set-form">
              <label class="field field-full"><span>访问协议</span>
                <select id="cacheScheme" class="inp">
                  <option value="auto">自动跟随（跟随当前访问协议：HTTPS 访问出 HTTPS，HTTP 访问出 HTTP）</option>
                  <option value="https">强制 HTTPS</option>
                  <option value="http">强制 HTTP</option>
                </select>
              </label>
              <label class="field field-full"><span>跨域允许来源</span><input id="corsOrigin" class="inp" placeholder="* 或逗号分隔域名，如 https://a.com,https://b.com"></label>
            </div>
            <div class="set-note">后台入口不可设为 /api、/cache、/static，保存后立即生效并自动跳转；忘记地址时改 data/rules.json 的 settings.admin_path 恢复。访问协议为解析接口返回的播放地址前缀，自动跟随按访客实际协议（含 X-Forwarded-Proto）输出，CDN 回源为 http 导致误判时选「强制 HTTPS」。跨域来源用于播放器跨域拉取 /cache 文件，留空默认 *。</div>
            <div class="set-actions">
              <button id="adminPathBtn" class="btn btn-primary">保存并跳转</button>
            </div>
          </div>
        </div>

        <!-- 2 修改密码 -->
        <div class="card">
          <div class="card-head"><h3>修改密码</h3></div>
          <div class="card-body">
            <div class="set-form">
              <label class="field field-full"><span>原密码</span><input id="pw-old" type="password" class="inp" placeholder="当前登录密码"></label>
              <label class="field field-full"><span>新密码</span><input id="pw-new" type="password" class="inp" placeholder="新密码"></label>
              <label class="field field-full"><span>确认新密码</span><input id="pw-new2" type="password" class="inp" placeholder="再次输入新密码"></label>
            </div>
            <div class="set-actions">
              <button id="pwBtn" class="btn btn-primary">修改密码</button>
            </div>
          </div>
        </div>
        </div>

        <!-- 3 自定义接口（接口地址列表管理） -->
        <div class="card">
          <div class="card-head"><h3>自定义接口</h3><span class="card-sub">对外解析接口地址列表，支持多个同时启用；至少保留一条启用中的地址</span></div>
          <div class="card-body">
            <div class="set-row">
              <span class="set-label">新增接口</span>
              <input id="cpName" class="inp" style="width:150px;" placeholder="备注名（可选）">
              <input id="cpPath" class="inp inp-grow" placeholder="接口地址，如 /api/v1/m3u8">
              <input id="cpNote" class="inp" style="width:220px;" placeholder="备注说明（可选）">
              <button id="cpAddBtn" class="btn btn-primary btn-sm">添加</button>
            </div>
            <div class="table-wrap"><table class="data">
              <thead><tr><th>接口地址</th><th>备注</th><th style="width:90px;">状态</th><th style="width:120px;">操作</th></tr></thead>
              <tbody id="cpTbody"></tbody>
            </table></div>
            <div class="set-note">
              列表内所有启用地址等价生效：可多个接口同时启用，请求进来都走同一套解析逻辑（GET 参数 url=）。<br>
              完整调用示例：<code class="mono">接口地址?url=</code> + m3u8 地址（地址列只显示路径本身）。<br>
              默认接口 <code class="mono">/api/clean</code> 为系统内置：固定启用，不可修改、不可删除；新增接口可自行切换启用 / 停用。<br>
              至少保留一条启用地址：停用或删除最后一条启用地址会被拒绝；地址不能占用 /cache、/static，也不能与后台入口相同。
            </div>
          </div>
        </div>

        <!-- 4 缓存清理（清理类型 + 定时清理合并） -->
        <div class="card">
          <div class="card-head"><h3>缓存清理</h3><span class="card-sub" id="cacheStatLine"></span></div>
          <div class="card-body">
            <div class="set-form">
              <label class="field"><span>有效期(天)</span><input id="ttlDays" type="number" step="0.1" min="0.1" max="365" class="inp"></label>
              <label class="field"><span>N 天前（可选）</span><input id="cacheDays" type="number" min="1" class="inp" placeholder="留空=按有效期"></label>
              <label class="field"><span>指定站点（可选）</span><input id="cleanSites" class="inp" placeholder="多个用逗号分隔，仅「指定站点缓存」类型生效"></label>
            </div>
            <div class="set-row">
              <span class="set-label">清理类型</span>
              <div class="chk-wrap" id="cleanTypes">${typeChips}</div>
            </div>
            <div class="set-row">
              <span class="set-label">定时清理</span>
              <select id="autoClean" class="inp" style="width:110px;"><option value="1">开启</option><option value="0">关闭</option></select>
              <span class="set-label">频率</span>
              <input id="cleanEvery" type="number" min="1" max="720" class="inp" style="width:90px;">
              <select id="cleanUnit" class="inp" style="width:100px;"><option value="s">秒</option><option value="m">分</option><option value="h">小时</option></select>
              <span id="nextCleanLine" class="set-note"></span>
            </div>
            <div class="set-actions">
              <button id="cacheBtn" class="btn btn-ghost">立即执行清理</button>
              <button id="logsBtn" class="btn btn-ghost">查看今日日志</button>
              <button id="settingsBtn" class="btn btn-primary">保存清理设置</button>
              <span id="cleanState" class="set-note"></span>
            </div>
            <pre id="logView" class="out-view hidden"></pre>
          </div>
        </div>

        <!-- 6 代理池 -->
        <div class="card">
          <div class="card-head"><h3>代理池</h3><span class="card-sub" id="poolStat"></span></div>
          <div class="card-body">
            <div class="set-grid">
              <label class="field"><span>启用代理池</span>
                <select id="poolEnable" class="inp"><option value="1">开启</option><option value="0">关闭</option></select>
              </label>
              <label class="field"><span>拉取数量（1-50）</span><input id="poolCount" type="number" min="1" max="50" class="inp"></label>
              <label class="field"><span>协议</span>
                <select id="poolProtocol" class="inp"><option value="https">https</option><option value="http">http</option><option value="socks5">socks5</option></select>
              </label>
              <label class="field"><span>国家代码（如 CN，留空不限）</span><input id="poolCountry" class="inp" placeholder="CN"></label>
              <label class="field"><span>刷新间隔（分钟）</span><input id="poolRefresh" type="number" min="1" max="1440" class="inp"></label>
              <label class="field"><span>取用策略</span>
                <select id="poolStrategy" class="inp"><option value="round">轮询 round</option><option value="random">随机 random</option></select>
              </label>
              <label class="field"><span>失败剔除阈值（次）</span><input id="poolFailMax" type="number" min="1" max="10" class="inp"></label>
              <label class="field field-full"><span>拉取接口地址</span><input id="poolApi" class="inp" placeholder="https://proxy.scdn.io/api/get_proxy.php"></label>
              <label class="field field-full"><span>手动代理列表（每行一个，优先于接口拉取；# 开头为注释，支持 ip:port 或 scheme://user:pass@ip:port）</span>
                <textarea id="poolManual" rows="4" placeholder="# 自备国内代理，每行一个&#10;1.2.3.4:7890&#10;socks5://user:pass@5.6.7.8:1080"></textarea>
              </label>
            </div>
            <div class="set-actions">
              <button id="poolSaveBtn" class="btn btn-primary">保存代理池设置</button>
              <button id="poolRefreshBtn" class="btn btn-ghost">立即刷新</button>
              <button id="poolTestBtn" class="btn btn-ghost">测试连通性</button>
              <button id="poolListBtn" class="btn btn-ghost">查看当前池中代理</button>
              <span class="set-note">代理拉取后直接入池，不做入池探测；实际取用时连续失败达到阈值会自动剔除并补位。</span>
            </div>
            <div id="poolBox" class="hidden">
              <div class="table-wrap"><table class="data">
                <thead><tr><th>代理地址</th><th>状态</th><th>使用</th><th>连续失败</th><th>最近成功</th><th>最近错误</th><th style="width:90px;">操作</th></tr></thead>
                <tbody id="poolTbody"></tbody>
              </table></div>
            </div>
            <pre id="poolOut" class="out-view hidden"></pre>
          </div>
        </div>

        <!-- 7 IP 授权（纯白名单） -->
        <div class="card">
          <div class="card-head"><h3>IP 授权</h3><span class="card-sub">纯白名单：开启后仅名单内 IP 可访问解析接口（/api/clean 及自定义接口）与 /cache/ 缓存文件，后台页面与后台接口不受限制</span></div>
          <div class="card-body">
            <div class="set-form">
              <label class="field"><span>访问控制</span>
                <select id="ipEnable" class="inp"><option value="1">开启（仅白名单放行）</option><option value="0">关闭（不限制）</option></select>
              </label>
              <label class="field"><span>IP / CIDR</span><input id="ipNew" class="inp" placeholder="1.2.3.4 或 1.2.3.0/24"></label>
              <label class="field"><span>备注（可选）</span><input id="ipNewNote" class="inp" placeholder="如 家里宽带"></label>
            </div>
            <div class="set-actions">
              <button id="ipAddBtn" class="btn btn-primary">添加白名单</button>
              <button id="ipCancelBtn" class="btn btn-ghost hidden">取消编辑</button>
              <button id="ipSwitchBtn" class="btn btn-ghost">保存开关</button>
              <button id="ipCheckBtn" class="btn btn-ghost">检测是否放行</button>
              <input id="ipCheck" class="inp inp-sm" placeholder="要检测的 IP（留空=本机）">
              <span id="ipCheckOut" class="set-note"></span>
            </div>
            <div class="table-wrap"><table class="data">
              <thead><tr><th>IP / CIDR</th><th>备注</th><th style="width:80px;">启用</th><th style="width:140px;">操作</th></tr></thead>
              <tbody id="ipTbody"></tbody>
            </table></div>
          </div>
        </div>

      </div>
    `;

    // ---- 回填：缓存 / 定时清理 ----
    $('#ttlDays').value = (st.cache_ttl_days != null ? Number(st.cache_ttl_days) : 7).toFixed(1);
    $('#cacheDays').value = st.cache_clean_days || '';
    $('#cleanSites').value = (st.cache_clean_sites || []).join(',');
    $('#cacheScheme').value = st.cache_scheme || 'auto';
    $('#corsOrigin').value = st.cors_allow_origin || '*';
    $('#autoClean').value = st.cache_auto_clean ? '1' : '0';
    $('#cleanEvery').value = st.cache_clean_every_h || 24;
    $('#cleanUnit').value = st.cache_clean_unit || 'h';
    const cs = [];
    cs.push('缓存文件 ' + (c.files || 0) + ' 个 · ' + fmtBytes(c.bytes || 0));
    cs.push(c.last ? '上次清理：' + c.last + '（' + (c.last_count || 0) + ' 个）' : '尚未执行清理');
    if (st.cache_auto_clean && c.next) cs.push('下次：' + c.next);
    $('#cleanState').textContent = cs.join(' · ');
    $('#cacheStatLine').textContent = (c.files || 0) + ' 个文件 · ' + fmtBytes(c.bytes || 0);
    $('#nextCleanLine').textContent = st.cache_auto_clean
      ? '每 ' + (st.cache_clean_every_h || 24) + ' ' + cleanUnitName(st.cache_clean_unit) + ' 执行一次 · 下次：' + (c.next || '待计算')
      : '已关闭';

    // ---- 回填：后台地址 ----
    $('#adminPath').value = st.admin_path || '/admin';
    $('#adminPathNow').textContent = st.admin_path || '/admin';

    // ---- 回填：代理池 ----
    $('#poolEnable').value = st.proxy_pool_enable ? '1' : '0';
    $('#poolApi').value = st.proxy_pool_api || 'https://proxy.scdn.io/api/get_proxy.php';
    $('#poolProtocol').value = st.proxy_pool_protocol || 'https';
    $('#poolCount').value = st.proxy_pool_count || 3;
    $('#poolCountry').value = st.proxy_pool_country || '';
    $('#poolRefresh').value = st.proxy_pool_refresh_m || 30;
    $('#poolStrategy').value = st.proxy_pool_strategy || 'round';
    $('#poolFailMax').value = st.proxy_pool_fail_max || 3;

    // ---- 回填：IP 授权 ----
    $('#ipEnable').value = st.ip_access_enable ? '1' : '0';

    // ---- 生效接口地址 ----
    const paths = sysData.paths || [];
    const samples = sysData.clean_path_samples || [];
    const active = sysData.clean_path_active || paths.filter(p => p.enabled !== false).map(p => p.path);
    renderActivePaths(active, samples);
    renderCleanPathTable(paths);
    updatePoolStat();
  }

  // 顶部「当前生效地址」胶囊列表
  function renderActivePaths(active, samples) {
    const box = $('#activePathList');
    if (!box) return;
    const tips = {};
    (samples || []).forEach(s => { const i = s.indexOf('?'); if (i > 0) tips[s.slice(0, i)] = s; });
    box.innerHTML = (active || []).map(p => {
      const key = p.replace(/^https?:\/\/[^/]+/, '');
      return '<span class="pill ok mono" title="' + esc(tips[key] || (p + '?url=...')) + '">' + esc(p) + '</span>';
    }).join('') || '<span class="set-note">无</span>';
  }

  // 接口地址列表（单行一排：地址 + 备注 + 启用开关 + 操作）
  function renderCleanPathTable(paths) {
    const tb = $('#cpTbody');
    if (!tb) return;
    if (!paths || !paths.length) {
      tb.innerHTML = '<tr><td colspan="4"><div class="empty">暂无接口地址，请添加（至少保留一条启用中的地址）</div></td></tr>';
      return;
    }
    tb.innerHTML = paths.map(p => {
      const np = p.path || p.Path || '';
      const on = p.enabled !== false;
      const id = esc(p.id || p.ID || '');
      const isDef = (p.id || p.ID) === 'default';
      const nm = esc(p.name || p.Name || '-');
      const note = esc(p.note || p.Note || '');
      // 状态按钮：绿色「启用」/ 红色「禁用」，点击切换；默认接口同样可停用，后端保证至少保留一条启用
      const stateHtml = '<button class="btn btn-sm btn-state ' + (on ? 'on' : 'off') + '" data-act="cp-toggle" data-id="' + id + '" title="点击切换启用 / 停用">' + (on ? '启用' : '禁用') + '</button>';
      const actHtml = isDef
        ? '<span class="set-note">—</span>'
        : '<button class="btn btn-danger btn-sm" data-act="cp-del" data-id="' + id + '">删除</button>';
      return `
      <tr${isDef ? ' class="cp-def"' : ''}>
        <td class="mono">${esc(np)}${isDef ? ' <span class="badge on">默认</span>' : ''}</td>
        <td>${nm}${note ? '<div class="sub-note">' + note + '</div>' : ''}</td>
        <td>${stateHtml}</td>
        <td>${actHtml}</td>
      </tr>`;
    }).join('');
  }

  function updatePoolStat(pool) {
    if (pool) sysData.pool = pool;
    const pl = sysData.pool || {};
    const el = $('#poolStat');
    if (!el) return;
    const parts = [];
    parts.push(pl.enable ? '已启用' : '已关闭');
    parts.push('池中 ' + (pl.total || 0) + ' 个');
    if (pl.last_refresh) parts.push('上次刷新 ' + pl.last_refresh);
    if (pl.next_refresh) parts.push('下次 ' + pl.next_refresh);
    if (pl.last_err) parts.push('错误：' + pl.last_err);
    el.textContent = parts.join(' · ');
  }

  function renderPoolTable() {
    const tb = $('#poolTbody');
    if (!tb) return;
    const items = (sysData.pool && sysData.pool.items) || [];
    if (!items.length) {
      tb.innerHTML = '<tr><td colspan="7"><div class="empty">代理池为空，点击「立即刷新」拉取（手动代理优先，不足时从接口补齐）</div></td></tr>';
      return;
    }
    tb.innerHTML = items.map(it => `
      <tr>
        <td class="mono">${esc(it.raw || it.addr)}</td>
        <td>${it.fails > 0 ? '<span class="badge err">失败 ' + it.fails + '</span>' : '<span class="badge ok">可用</span>'}</td>
        <td>${it.used || 0}</td>
        <td>${it.fails || 0}</td>
        <td>${esc(it.last_ok || '-')}</td>
        <td class="mono" style="font-size:12px;">${esc(it.last_err || '-')}</td>
        <td>
          <button class="btn btn-ghost btn-sm" data-act="pool-test" data-p="${esc(it.raw || it.addr)}">测试</button>
          <button class="btn btn-danger btn-sm" data-act="pool-del" data-p="${esc(it.raw || it.addr)}">删除</button>
        </td>
      </tr>`).join('');
  }

  function renderIPTable() {
    const tb = $('#ipTbody');
    if (!tb) return;
    const list = sysData.ip || [];
    if (!list.length) { tb.innerHTML = '<tr><td colspan="4"><div class="empty">暂无白名单，开启访问控制前请先添加自己的 IP</div></td></tr>'; return; }
    tb.innerHTML = list.map(r => `
      <tr>
        <td class="mono">${esc(r.ip)}</td>
        <td>${esc(r.note || '-')}</td>
        <td><button class="btn btn-ghost btn-sm" data-act="ip-toggle" data-id="${esc(r.id)}">${r.enabled ? '<span class="badge on">启用</span>' : '<span class="badge off">停用</span>'}</button></td>
        <td>
          <button class="btn btn-ghost btn-sm" data-act="ip-edit" data-id="${esc(r.id)}">编辑</button>
          <button class="btn btn-danger btn-sm" data-act="ip-del" data-id="${esc(r.id)}">删除</button>
        </td>
      </tr>`).join('');
  }


  // 读取某容器内的多选清理类型
  function checkedTypes(sel) {
    return Array.prototype.slice.call(document.querySelectorAll(sel + ' input:checked')).map(x => x.value);
  }
  function splitSites(v) {
    return String(v || '').split(/[,，\s]+/).map(x => x.trim()).filter(Boolean);
  }
  function selTypes(sel) {
    const t = checkedTypes(sel);
    return t.length ? t : ['expired'];
  }

  function bindSystemEvents() {
    // ---- 1 后台地址 ----
    $('#adminPathBtn').addEventListener('click', async () => {
      const v = ($('#adminPath').value || '').trim();
      if (!v) { toast('后台地址不能为空', 'err'); return; }
      // 本卡内的「访问协议 / 跨域允许来源」与后台地址一并提交（历史 BUG：只提交 admin_path，
      // 用户改了协议再点「保存并跳转」会被静默丢弃，只有缓存设置卡的保存按钮才存这两个字段）
      const r = await api('/api/settings', {
        method: 'POST',
        body: {
          admin_path: v,
          cache_scheme: $('#cacheScheme').value,
          cors_allow_origin: ($('#corsOrigin').value || '').trim() || '*'
        }
      });
      toast(r.msg, r.code === 200 ? 'ok' : 'err');
      if (r.code !== 200) return;
      const np = '/' + v.replace(/^\/+|\/+$/g, '');
      setTimeout(() => { location.href = np + '/'; }, 600);
    });

    // ---- 2 自定义解析接口地址 ----
    $('#cpAddBtn').addEventListener('click', async () => {
      const path = ($('#cpPath').value || '').trim();
      if (!path) { toast('请填写接口地址，如 /api/v1/m3u8', 'err'); return; }
      const body = {
        name: ($('#cpName').value || '').trim(),
        path: path,
        note: (($('#cpNote') && $('#cpNote').value) || '').trim(),
        enabled: true
      };
      const r = await api('/api/clean-paths', { method: 'POST', body: body });
      toast(r.msg, r.code === 200 ? 'ok' : 'err');
      if (r.code === 200) { resetCleanPathForm(); renderSystem(); }
    });
    $('#cpTbody').addEventListener('click', async (e) => {
      const b = e.target.closest('button[data-act]');
      if (!b) return;
      const id = b.dataset.id;
      const it = (sysData.paths || []).filter(x => x.id === id)[0];
      if (!it) return;
      if (b.dataset.act === 'cp-del') {
        if (!confirm('删除后该接口地址将立即失效，确定删除？')) return;
        const r = await api('/api/clean-paths/' + id, { method: 'DELETE' });
        toast(r.msg, r.code === 200 ? 'ok' : 'err');
        if (r.code === 200) { resetCleanPathForm(); renderSystem(); }
        return;
      }
      if (b.dataset.act === 'cp-toggle') {
        const r = await api('/api/clean-paths/' + id, {
          method: 'PUT', body: { name: it.name, path: it.path, note: it.note || '', enabled: it.enabled === false }
        });
        toast(r.msg, r.code === 200 ? 'ok' : 'err');
        if (r.code === 200) renderSystem();
      }
    });

    // ---- 3 修改密码 ----
    $('#pwBtn').addEventListener('click', async () => {
      const o = $('#pw-old').value, n1 = $('#pw-new').value, n2 = $('#pw-new2').value;
      if (!o || !n1) { toast('请填写原密码与新密码', 'err'); return; }
      if (n1 !== n2) { toast('两次输入的新密码不一致', 'err'); return; }
      const r = await api('/api/password', { method: 'POST', body: { old: o, new: n1 } });
      toast(r.msg, r.code === 200 ? 'ok' : 'err');
      if (r.code === 200) { ['#pw-old', '#pw-new', '#pw-new2'].forEach(id => { $(id).value = ''; }); }
    });

    // ---- 4 缓存维护 ----
    $('#cacheBtn').addEventListener('click', async () => {
      const btn = $('#cacheBtn'), old = btn.textContent;
      const types = selTypes('#cleanTypes');
      const days = parseInt($('#cacheDays').value, 10) || 0;
      const sites = splitSites($('#cleanSites').value);
      btn.disabled = true; btn.textContent = '清理中...';
      const r = await api('/api/cache', { method: 'POST', body: { types: types, days: days, sites: sites } });
      btn.disabled = false; btn.textContent = old;
      toast(r.msg + (r.deleted != null ? '（删除 ' + r.deleted + ' 个文件）' : ''), r.code === 200 ? 'ok' : 'err');
      if (r.code === 200) renderSystem();
    });
    $('#logsBtn').addEventListener('click', async () => {
      const v = $('#logView');
      if (!v.classList.contains('hidden')) { v.classList.add('hidden'); return; }
      const r = await api('/api/logs?limit=200');
      v.classList.remove('hidden');
      v.textContent = (r.lines && r.lines.length) ? r.lines.join('\n') : '（暂无日志）';
    });
    $('#settingsBtn').addEventListener('click', async () => {
      const ttl = parseFloat($('#ttlDays').value);
      if (!(ttl > 0) || ttl > 365) { toast('缓存有效期需在 0.1 ~ 365 天之间', 'err'); return; }
      let every = parseInt($('#cleanEvery').value, 10);
      if (!(every >= 1)) every = 24;
      const days = parseInt($('#cacheDays').value, 10);
      const r = await api('/api/settings', {
        method: 'POST',
        body: {
          cache_ttl_days: ttl,
          cache_auto_clean: $('#autoClean').value === '1',
          cache_clean_every_h: every,
          cache_clean_unit: $('#cleanUnit').value,
          cache_clean_types: selTypes('#cleanTypes'),
          cache_clean_sites: splitSites($('#cleanSites').value),
          cache_clean_days: days > 0 ? days : 0,
          cache_scheme: $('#cacheScheme').value,
          cors_allow_origin: ($('#corsOrigin').value || '').trim() || '*'
        }
      });
      toast(r.msg, r.code === 200 ? 'ok' : 'err');
      if (r.code === 200) renderSystem();
    });

    // ---- 6 代理池 ----
    $('#poolSaveBtn').addEventListener('click', async () => {
      const r = await api('/api/settings', {
        method: 'POST',
        body: {
          proxy_pool_enable: $('#poolEnable').value === '1',
          proxy_pool_api: $('#poolApi').value.trim(),
          proxy_pool_protocol: $('#poolProtocol').value,
          proxy_pool_count: parseInt($('#poolCount').value, 10) || 3,
          proxy_pool_country: $('#poolCountry').value.trim(),
          proxy_pool_refresh_m: parseInt($('#poolRefresh').value, 10) || 30,
          proxy_pool_strategy: $('#poolStrategy').value,
          proxy_pool_fail_max: parseInt($('#poolFailMax').value, 10) || 3,
          proxy_pool_manual: $('#poolManual').value
        }
      });
      toast(r.msg, r.code === 200 ? 'ok' : 'err');
      if (r.code === 200) renderSystem();
    });
    $('#poolRefreshBtn').addEventListener('click', async () => {
      const btn = $('#poolRefreshBtn');
      btn.disabled = true; btn.textContent = '刷新中...';
      const r = await api('/api/proxy-pool/refresh', { method: 'POST' });
      btn.disabled = false; btn.textContent = '立即刷新';
      toast(r.msg || '已刷新', r.code === 200 ? 'ok' : 'err');
      if (r.code !== 200) { const o = $('#poolOut'); o.classList.remove('hidden'); o.textContent = r.msg || ''; }
      if (r.data) { updatePoolStat(r.data); renderPoolTable(); $('#poolBox').classList.remove('hidden'); }
    });
    $('#poolTestBtn').addEventListener('click', async () => {
      const btn = $('#poolTestBtn'), out = $('#poolOut');
      btn.disabled = true; btn.textContent = '测试中...';
      out.classList.remove('hidden'); out.textContent = '正在测试…';
      const r = await api('/api/proxy-pool/test', { method: 'POST', body: {} });
      btn.disabled = false; btn.textContent = '测试连通性';
      if (r.code !== 200) { out.textContent = '测试失败：' + (r.msg || '未知错误'); return; }
      const rs = r.results || [];
      out.textContent = rs.map(x => x.proxy + ' → ' + (x.ok ? '连通' : '不通') + ' · HTTP ' + (x.status || '-') + ' · ' + (x.elapsed_ms || 0) + 'ms' + (x.error ? ' · ' + x.error : '')).join('\n') || '（无可测代理）';
      if (r.data) { updatePoolStat(r.data); renderPoolTable(); }
    });
    $('#poolListBtn').addEventListener('click', async () => {
      const box = $('#poolBox');
      if (!box.classList.contains('hidden')) { box.classList.add('hidden'); return; }
      const r = await api('/api/proxy-pool');
      if (r.data) { updatePoolStat(r.data); renderPoolTable(); }
      box.classList.remove('hidden');
    });
    $('#poolTbody').addEventListener('click', async (e) => {
      const b = e.target.closest('button[data-act]');
      if (!b) return;
      const p = b.dataset.p || '';
      if (b.dataset.act === 'pool-del') {
        if (!confirm('确定从代理池移除该代理？')) return;
        const rd = await api('/api/proxy-pool/remove', { method: 'POST', body: { proxy: p } });
        toast(rd.msg, rd.code === 200 ? 'ok' : 'err');
        if (rd.code === 200) { updatePoolStat(rd.data); renderPoolTable(); }
        return;
      }
      const out = $('#poolOut');
      out.classList.remove('hidden'); out.textContent = '正在测试…';
      const r = await api('/api/proxy-pool/test', { method: 'POST', body: { proxy: b.dataset.p } });
      const rs = r.results || [];
      out.textContent = rs.map(x => x.proxy + ' → ' + (x.ok ? '连通' : '不通') + ' · HTTP ' + (x.status || '-') + ' · ' + (x.elapsed_ms || 0) + 'ms' + (x.error ? ' · ' + x.error : '')).join('\n') || (r.msg || '（无结果）');
      if (r.data) { updatePoolStat(r.data); renderPoolTable(); }
    });

    // ---- 7 IP 授权（纯白名单） ----
    $('#ipAddBtn').addEventListener('click', async () => {
      const ip = ($('#ipNew').value || '').trim();
      if (!ip) { toast('请填写 IP 或 CIDR', 'err'); return; }
      const body = { ip: ip, note: ($('#ipNewNote').value || '').trim(), enabled: true };
      const r = ipEditId
        ? await api('/api/ip-rules/' + ipEditId, { method: 'PUT', body: body })
        : await api('/api/ip-rules', { method: 'POST', body: body });
      toast(r.msg, r.code === 200 ? 'ok' : 'err');
      if (r.code === 200) { resetIPForm(); renderSystem(); }
    });
    $('#ipCancelBtn').addEventListener('click', resetIPForm);
    $('#ipSwitchBtn').addEventListener('click', async () => {
      const r = await api('/api/ip-access', { method: 'POST', body: { enable: $('#ipEnable').value === '1' } });
      toast(r.msg, r.code === 200 ? 'ok' : 'err');
      if (r.code === 200) renderSystem();
    });
    $('#ipCheckBtn').addEventListener('click', async () => {
      const r = await api('/api/ip-rules/check', { method: 'POST', body: { ip: ($('#ipCheck').value || '').trim() } });
      $('#ipCheckOut').textContent = (r.ip || '') + ' → ' + (r.allowed ? '放行' : '拒绝') + '（' + (r.reason || '-') + '）';
    });
    $('#ipTbody').addEventListener('click', async (e) => {
      const b = e.target.closest('button[data-act]');
      if (!b) return;
      const id = b.dataset.id;
      const it = (sysData.ip || []).filter(x => x.id === id)[0];
      if (b.dataset.act === 'ip-edit' && it) {
        ipEditId = id;
        $('#ipNew').value = it.ip || '';
        $('#ipNewNote').value = it.note || '';
        $('#ipAddBtn').textContent = '保存修改';
        $('#ipCancelBtn').classList.remove('hidden');
        $('#ipNew').focus();
        return;
      }
      if (b.dataset.act === 'ip-del') {
        if (!confirm('删除该白名单规则？')) return;
        const r = await api('/api/ip-rules/' + id, { method: 'DELETE' });
        toast(r.msg, r.code === 200 ? 'ok' : 'err');
        if (r.code === 200) { resetIPForm(); renderSystem(); }
        return;
      }
      if (b.dataset.act === 'ip-toggle' && it) {
        const r = await api('/api/ip-rules/' + id, {
          method: 'PUT', body: { ip: it.ip, note: it.note, enabled: !it.enabled }
        });
        toast(r.msg, r.code === 200 ? 'ok' : 'err');
        if (r.code === 200) renderSystem();
      }
    });
  }

  function resetIPForm() {
    ipEditId = '';
    $('#ipNew').value = ''; $('#ipNewNote').value = '';
    $('#ipAddBtn').textContent = '添加白名单';
    $('#ipCancelBtn').classList.add('hidden');
  }

  function resetCleanPathForm() {
    $('#cpName').value = ''; $('#cpPath').value = '';
    if ($('#cpNote')) $('#cpNote').value = '';
    $('#cpPath').disabled = false;
    $('#cpAddBtn').textContent = '添加';
  }

  function trimUrl(u) {
    u = String(u || '');
    return u.length > 60 ? u.slice(0, 60) + '…' : u;
  }

  function drawTrend(trend) {
    if (lineChart) lineChart.dispose();
    const t = chartTheme();
    lineChart = echarts.init($('#trendChart'));
    lineChart.setOption({
      tooltip: {
        trigger: 'axis',
        backgroundColor: t.tipBg, borderColor: t.line, textStyle: { color: t.text }
      },
      legend: {
        data:  ['请求数', '成功数'], top: 0, textStyle: { color: t.text }
      },
      grid: { left: 40, right: 16, top: 36, bottom: 24 },
      xAxis: {
        type: 'category',
        data:  trend.map(x => x.date.slice(5)),
        boundaryGap: false,
        axisLine: { lineStyle: { color: t.line } },
        axisTick: { show: false },
        axisLabel: { color: t.text }
      },
      yAxis: {
        type: 'value', minInterval: 1,
        axisLine: { show: false }, axisTick: { show: false },
        axisLabel: { color: t.text },
        splitLine: { lineStyle: { color: t.split } }
      },
      series: [
        {
          name: '请求数', type: 'line', smooth: true,
          data:  trend.map(x => x.total),
          itemStyle: { color: t.primary }, areaStyle: { opacity: .12 }
        },
        {
          name: '成功数', type: 'line', smooth: true,
          data:  trend.map(x => x.ok),
          itemStyle: { color: t.success }
        }
      ]
    });
    bindChartResize();
  }

  function drawPie(pie) {
    if (pieChart) pieChart.dispose();
    const t = chartTheme();
    pieChart = echarts.init($('#pieChart'));
    const rows = pie.length ? pie : [{ name: '暂无数据', value: 1 }];
    pieChart.setOption({
      tooltip: {
        trigger: 'item', formatter: '{b}: {c} ({d}%)',
        backgroundColor: t.tipBg, borderColor: t.line, textStyle: { color: t.text }
      },
      legend: { bottom: 0, textStyle: { color: t.text } },
      series: [{
        type: 'pie', radius: ['42%', '68%'], center: ['50%', '45%'],
        itemStyle: { borderRadius: 8, borderWidth: 2, borderColor: t.card },
        label: { formatter: '{b}\n{d}%', color: t.text },
        labelLine: { lineStyle: { color: t.line } },
        data:  rows
      }]
    });
    bindChartResize();
  }

  // resize 监听只绑一次，避免每次重绘都累积一个监听器
  let chartResizeBound = false;
  function bindChartResize() {
    if (chartResizeBound) return;
    chartResizeBound = true;
    window.addEventListener('resize', () => {
      if (lineChart) lineChart.resize();
      if (pieChart) pieChart.resize();
    });
  }

  /* ---------- 调用日志（独立页面） ---------- */
  let logsState = { status: 'all', site: '', kw: '', n: 200, auto: false };

  async function renderLogs() {
    const box = $('#page-logs');
    const _sy = scrollSnapshot();
    if (window._logsTimer) { clearInterval(window._logsTimer); window._logsTimer = null; }

    const qs = new URLSearchParams({
      n: String(logsState.n),
      status: logsState.status,
      site: logsState.site,
      kw: logsState.kw
    });
    const d = await api('/api/records?' + qs.toString());
    if (!d.code || d.code !== 200) { box.innerHTML = '<div class="empty">日志加载失败</div>'; return; }

    // 空态提示：按系统设置里当前生效的解析接口动态拼出（不再写死 /api/clean）
    let activePaths = [];
    try {
      const pd = await api('/api/clean-paths');
      activePaths = (pd && pd.data && pd.data.active) || [];
    } catch (e) { activePaths = []; }
    if (!activePaths.length) activePaths = sysData.paths ? sysData.paths.filter(p => p.enabled !== false).map(p => p.path) : [];
    if (!activePaths.length) activePaths = ['/api/clean'];
    const emptyTip = '暂无调用记录，调用 ' + activePaths.map(p => '<code class="mono">' + esc(p) + '</code>').join('、') + ' 接口后此处会展示';

    const list = d.records || [];
    let sites = [];
    try { sites = await loadSites(); } catch (e) { sites = []; }
    const siteOpts = sites.map(s => `<option value="${esc(s.name)}"${logsState.site === s.name ? ' selected' : ''}>${esc(s.name)}</option>`).join('');

    const okCount = list.filter(r => r.code === 200).length;

    box.innerHTML = `
      <div class="card">
        <div class="card-head">
          <h3>调用日志</h3>
          <span style="color:var(--muted);font-size:12px;">库内共 ${d.total} 条 · 命中 ${d.matched} 条 · 当前展示 ${list.length} 条（成功 ${okCount} / 失败 ${list.length - okCount}）</span>
        </div>
        <div class="card-body">
          <div class="log-filter">
            <label class="lf"><span>状态</span>
              <select id="lf-status">
                <option value="all"${logsState.status === 'all' ? ' selected' : ''}>全部</option>
                <option value="ok"${logsState.status === 'ok' ? ' selected' : ''}>仅成功</option>
                <option value="fail"${logsState.status === 'fail' ? ' selected' : ''}>仅失败</option>
              </select>
            </label>
            <label class="lf"><span>站点</span>
              <select id="lf-site"><option value="">全部站点</option>${siteOpts}</select>
            </label>
            <label class="lf lf-grow"><span>关键字（URL / 结果 / 站点）</span>
              <input type="text" id="lf-kw" value="${esc(logsState.kw)}" placeholder="回车或点查询">
            </label>
            <label class="lf"><span>展示条数</span>
              <select id="lf-n">
                ${[100, 200, 500, 1000].map(v => `<option value="${v}"${logsState.n === v ? ' selected' : ''}>${v}</option>`).join('')}
              </select>
            </label>
            <div class="lf-actions">
              <button id="lf-query" class="btn btn-primary btn-sm">查询</button>
              <button id="lf-reset" class="btn btn-ghost btn-sm">重置</button>
              <button id="lf-refresh" class="btn btn-ghost btn-sm">刷新</button>
              <button id="lf-clear" class="btn btn-danger btn-sm">清空记录</button>
              <label class="lf-auto"><input type="checkbox" id="lf-auto"${logsState.auto ? ' checked' : ''}> 10s 自动刷新</label>
            </div>
          </div>
        </div>
      </div>

      <div class="card">
        <div class="card-body table-wrap">
          ${list.length ? `<table class="data">
            <thead><tr><th>时间</th><th>站点</th><th>状态</th><th>耗时</th><th>来源地址</th><th>出网</th><th>结果</th></tr></thead>
            <tbody>${list.map(r => `
              <tr>
                <td>${esc(r.time)}</td>
                <td>${esc(r.site || '-')}</td>
                <td><span class="badge ${r.code === 200 ? 'ok' : 'err'}">${r.code === 200 ? '成功' : '失败'}</span></td>
                <td>${fmtElapsed(r.elapsed_ms)}</td>
                <td title="${esc(r.url)}">${esc(trimUrl(r.url))}</td>
                <td title="${esc(r.proxy || '')}">${r.from_cache ? '-' : (r.proxy ? '<span class="badge on">代理</span> ' + esc(proxyShort(r.proxy)) : '<span class="badge">直连</span>')}</td>
                <td title="${esc(r.cache_url || r.msg || '')}">${r.from_cache ? '<span class="badge on">缓存命中</span>' : esc(resultText(r))}</td>
              </tr>`).join('')}</tbody>
          </table>` : '<div class="empty">' + emptyTip + '</div>'}
        </div>
      </div>
    `;

    // 事件
    const applyFilter = () => {
      logsState.status = $('#lf-status').value;
      logsState.site = $('#lf-site').value;
      logsState.kw = $('#lf-kw').value.trim();
      logsState.n = parseInt($('#lf-n').value, 10) || 200;
      logsState.auto = $('#lf-auto').checked;
      renderLogs();
    };
    $('#lf-query').addEventListener('click', applyFilter);
    $('#lf-refresh').addEventListener('click', () => renderLogs());
    $('#lf-status').addEventListener('change', applyFilter);
    $('#lf-site').addEventListener('change', applyFilter);
    $('#lf-n').addEventListener('change', applyFilter);
    $('#lf-kw').addEventListener('keydown', (e) => { if (e.key === 'Enter') applyFilter(); });
    $('#lf-reset').addEventListener('click', () => {
      logsState = { status: 'all', site: '', kw: '', n: 200, auto: false };
      renderLogs();
    });
    $('#lf-auto').addEventListener('change', () => {
      logsState.auto = $('#lf-auto').checked;
      if (logsState.auto) {
        window._logsTimer = setInterval(() => { if (current === 'logs') renderLogs(); }, 10000);
      } else if (window._logsTimer) {
        clearInterval(window._logsTimer); window._logsTimer = null;
      }
    });
    $('#lf-clear').addEventListener('click', async () => {
      if (!window.confirm('确定清空全部调用记录？该操作不可恢复。')) return;
      const r = await api('/api/records', { method: 'DELETE' });
      toast(r.msg || '已清空', r.code === 200 ? 'ok' : 'err');
      if (r.code === 200) renderLogs();
    });
    if (logsState.auto) {
      window._logsTimer = setInterval(() => { if (current === 'logs') renderLogs(); }, 10000);
    }
    scrollRestore(_sy);
  }

  // 代理地址展示：隐去账号密码，只留 scheme://host:port
  function proxyShort(u) {
    if (!u) return '';
    let s = String(u);
    const at = s.lastIndexOf('@');
    if (at >= 0 && s.indexOf('://') > 0) s = s.slice(0, s.indexOf('://') + 3) + s.slice(at + 1);
    return s;
  }

  function resultText(r) {
    if (r.cache_url) return '已产出缓存';
    return (r.msg || '').slice(0, 40) || '-';
  }

  /* ---------- 站点管理 ---------- */
  async function loadSites() {
    const d = await api('/api/sites');
    return d.sites || [];
  }

  async function renderSites() {
    const box = $('#page-sites');
    const _sy = scrollSnapshot();
    box.innerHTML = '<div class="empty"><div class="ico">⟳</div>加载中...</div>';
    sitesList = await loadSites();
    if (!sitesList.length) {
      box.innerHTML = `<div class="card"><div class="card-body"><div class="empty"><div class="ico">◈</div>暂无站点，点击右上角「新增站点」创建</div></div></div>`;
      box.insertAdjacentHTML('afterbegin', `<div style="display:flex;justify-content:flex-end;gap:8px;margin-bottom:16px;"><button id="exportSitesBtn" class="btn btn-outline ico-down" title="导出全部站点规则为 JSON 文件"><svg class="bico" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="7 10 12 15 17 10"/><line x1="12" y1="3" x2="12" y2="15"/></svg>导出规则</button><button id="importSitesBtn" class="btn btn-soft ico-up" title="从 JSON 文件导入站点规则"><svg class="bico" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="17 8 12 3 7 8"/><line x1="12" y1="3" x2="12" y2="15"/></svg>导入规则</button><button id="addSiteBtn" class="btn btn-primary">＋ 新增站点</button></div>`);
      $('#exportSitesBtn').addEventListener('click', exportSites);
      $('#importSitesBtn').addEventListener('click', importSites);
      $('#addSiteBtn').addEventListener('click', () => openSiteModal());
      return;
    }
    box.innerHTML = `
      <div style="display:flex;justify-content:flex-end;gap:8px;margin-bottom:16px;"><button id="exportSitesBtn" class="btn btn-outline ico-down" title="导出全部站点规则为 JSON 文件"><svg class="bico" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="7 10 12 15 17 10"/><line x1="12" y1="3" x2="12" y2="15"/></svg>导出规则</button><button id="importSitesBtn" class="btn btn-soft ico-up" title="从 JSON 文件导入站点规则"><svg class="bico" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="17 8 12 3 7 8"/><line x1="12" y1="3" x2="12" y2="15"/></svg>导入规则</button><button id="addSiteBtn" class="btn btn-primary">＋ 新增站点</button></div>
      <div class="card">
        <div class="card-head"><h3>资源站规则列表</h3><span style="color:var(--muted);font-size:12px;">共 ${sitesList.length} 个站点规则</span></div>
        <div class="card-body table-wrap">
          <table class="data">
            <thead><tr><th>排序</th><th>站点名</th><th>检测/解析</th><th>URL 匹配</th><th>出网代理</th><th>特征码（广告时长指纹）</th><th>状态</th><th>备注</th><th>操作</th></tr></thead>
            <tbody>
              ${sitesList.slice().sort((a, b) => (a.sort || 0) - (b.sort || 0)).map(s => `
                <tr>
                  <td>${s.sort || 0}</td>
                  <td><strong>${esc(s.name)}</strong></td>
                  <td><span class="badge on">${esc(s.detect_mode || 'sequence')}</span> <span class="badge">${esc(s.parse_type || 'both')}</span></td>
                  <td title="${esc(s.url_prefix || '')}${s.url_regex ? ' / ' + esc(s.url_regex) : ''}">${esc(prefixFmt(s))}</td>
                  <td title="${esc(s.proxy_url || '')}">${s.proxy_url ? (s.proxy_url === '-' ? '<span class="badge">直连</span>' : '<span class="badge on">已配置</span>') : '<span style="color:var(--muted)">全局</span>'}</td>
                  <td><span class="fp-tags">${(() => {
      // 第 1 组存在 fingerprint，其余在 fingerprints，两者合起来才是全部特征码
      const groups = [];
      if (s.fingerprint && s.fingerprint.length) groups.push(s.fingerprint);
      (s.fingerprints || []).forEach(g => { if (g && g.length) groups.push(g); });
      if (!groups.length) return '-';
      const one = g => {
        const a = g || [];
        return a.length > 8 ? a.slice(0, 8).join(',') + ',…' : a.join(',');
      };
      let html = groups.slice(0, 2).map(g => '<span class="fp-tag">' + esc(one(g)) + '</span>').join('');
      if (groups.length > 2) html += '<span class="fp-tag fp-more">＋' + (groups.length - 2) + '组</span>';
      return html;
    })()}</span></td>
                  <td><span class="badge ${s.enabled ? 'ok' : 'off'}">${s.enabled ? '启用' : '禁用'}</span></td>
                  <td title="${esc(s.remark || '')}">${esc((s.remark || '').slice(0, 12)) || '-'}</td>
                  <td><div class="row-actions">
                    <button class="btn btn-ghost btn-sm" data-act="edit" data-id="${esc(s.id)}">编辑</button>
                    <button class="btn btn-ghost btn-sm" data-act="toggle" data-id="${esc(s.id)}">${s.enabled ? '停用' : '启用'}</button>
                    <button class="btn btn-danger btn-sm" data-act="del" data-id="${esc(s.id)}">删除</button>
                  </div></td>
                </tr>`).join('')}
            </tbody>
          </table>
        </div>
      </div>
    `;
    $('#addSiteBtn').addEventListener('click', () => openSiteModal());
    $('#exportSitesBtn').addEventListener('click', exportSites);
    $('#importSitesBtn').addEventListener('click', importSites);
    $$('button[data-act]', box).forEach(b => b.addEventListener('click', () => {
      const id = b.dataset.id, act = b.dataset.act;
      const site = sitesList.find(x => x.id === id);
      if (act === 'edit') openSiteModal(site);
      if (act === 'toggle') toggleSite(site);
      if (act === 'del') delSite(site);
    }));
    scrollRestore(_sy);
  }

  /* ---------- 站点规则 导出 / 导入 ---------- */
  async function exportSites() {
    const d = await api('/api/sites/export');
    if (!d || d.code !== 200) { toast((d && d.msg) || '导出失败', 'err'); return; }
    const payload = { format: d.format, version: d.version, exported_at: d.exported_at, count: d.count, sites: d.sites };
    const blob = new Blob([JSON.stringify(payload, null, 2)], { type: 'application/json;charset=utf-8' });
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = 'sites_rules_' + new Date().toISOString().slice(0, 10) + '.json';
    document.body.appendChild(a);
    a.click();
    setTimeout(() => { URL.revokeObjectURL(a.href); a.remove(); }, 600);
    toast('已导出 ' + payload.count + ' 条站点规则');
  }

  function importSites() {
    const inp = document.createElement('input');
    inp.type = 'file';
    inp.accept = '.json,application/json';
    inp.onchange = async () => {
      const f = inp.files && inp.files[0];
      if (!f) return;
      try {
        const data = JSON.parse(await f.text());
        const sites = Array.isArray(data) ? data : (data.sites || []);
        if (!sites.length) { toast('文件中没有站点规则', 'err'); return; }
        let mode = 'append';
        if (!confirm('已读取 ' + sites.length + ' 条规则。\n\n点击「确定」开始追加导入（保留现有规则）。\n如需整体替换，请点击「取消」。')) {
          if (!confirm('是否整体替换全部现有规则？\n替换前会自动备份当前 rules.json。')) { toast('已取消导入'); return; }
          mode = 'replace';
        }
        const d = await api('/api/sites/import', { method: 'POST', body: { sites: sites, mode: mode } });
        if (d && d.code === 200) {
          toast(d.msg || '导入成功');
          await renderSites();
        } else {
          toast((d && d.msg) || '导入失败', 'err');
        }
      } catch (e) {
        toast('导入文件解析失败: ' + e.message, 'err');
      }
    };
    inp.click();
  }

  function prefixFmt(s) {
    const pres = [];
    (s.url_prefixes || []).forEach(v => { if (v && pres.indexOf(v) < 0) pres.push(v); });
    if (pres.length === 0 && s.url_prefix) pres.push(s.url_prefix);
    const rgs = [];
    (s.url_regexes || []).forEach(v => { if (v && rgs.indexOf(v) < 0) rgs.push(v); });
    if (rgs.length === 0 && s.url_regex) rgs.push(s.url_regex);
    let t = pres.length ? pres.map(p => '*.' + p + '*').join(' , ') : (rgs.length ? '(仅正则)' : '-');
    if (pres.length > 1) t = pres[0] ? '*.' + pres[0] + '* ×' + pres.length : t;
    if (rgs.length) t += ' + 正则' + (rgs.length > 1 ? ' ×' + rgs.length : '');
    return t;
  }

  /* ----- 站点弹窗 ----- */
  function openSiteModal(site) {
    $('#siteModalTitle').textContent = site ? '编辑站点：' + site.name : '新增站点';
    $('#f-name').value = site ? (site.name || '') : '';
    $('#f-parsetype').value = site ? (site.parse_type || 'both') : 'both';
    $('#f-detectmode').value = site ? (site.detect_mode || 'sequence') : 'sequence';
    // 匹配前缀 / 匹配正则：均可多个，合并新旧字段（url_prefixes 数组 + 旧单值 url_prefix）
    fillListRows('#urlPrefixRows', listVals(site && site.url_prefixes, site && site.url_prefix), '如 vip.dytt（主机名前缀）', 'ipt-pre');
    fillListRows('#urlRegexRows', listVals(site && site.url_regexes, site && site.url_regex), '如 lz-cdn\\d*（默认只匹配主机名；以 ^ 开头或含 :// 才匹配整条 URL）', 'ipt-rgx');
    $('#f-subdir').value = site ? (site.cache_subdir || '') : '';
    $('#f-sort').value = site ? (site.sort || 0) : 0;
    // 特征码：一行一组。fingerprint 存第 1 组、fingerprints 存第 2 组起（兼容旧数据）
    // 历史 BUG：这里曾「有 fingerprints 就只用 fingerprints」，导致第 1 组被丢弃，
    // 每次打开编辑再保存都会丢掉一组特征码。改为两者顺序合并，并按内容去重。
    const fpsArr = site ? (function () {
      const all = [];
      if (site.fingerprint && site.fingerprint.length) all.push(site.fingerprint);
      (site.fingerprints || []).forEach(function (g) { if (g && g.length) all.push(g); });
      const seen = {};
      return all.filter(function (g) {
        const k = (g || []).join(',');
        if (seen[k]) return false;
        seen[k] = 1;
        return true;
      });
    })() : [];
    $('#f-fingerprint').value = fpsArr.map(a => (a || []).join(',')).join('\n');
    $('#f-enabled').checked = site ? !!site.enabled : true;
    $('#f-remark').value = site ? (site.remark || '') : '';
    $('#f-indexmode').value = site ? (site.index_mode || 'auto') : 'auto';
    $('#f-streampick').value = site ? (site.stream_pick || 'first') : 'first';
    $('#f-fixedsub').value = site ? (site.fixed_sub || '') : '';
    $('#f-proxyurl').value = site ? (site.proxy_url || '') : '';
    // 探测区复位
    $('#f-probeurl').value = '';
    $('#f-probehash').checked = false;
    $('#probeResult').classList.add('hidden');
    $('#probeResult').innerHTML = '';
    $('#siteErr').textContent = '';
    // 正则行：只读 filter_regex（广告过滤正则），不再串台读取前缀/正则
    const rows = $('#regexRows'); rows.innerHTML = '';
    const list = (site && Array.isArray(site.filter_regex)) ? site.filter_regex.slice() : [];
    (list.length ? list : ['']).forEach(re => addRegexRow(re));
    window._editingSite = site || null;
    $('#siteModal').classList.remove('hidden');
  }
  /* 多值输入行
   * 三组列表各自使用独立的输入框 class，读取时按「容器 + class」精确定位，互不串台：
   *   URL 匹配前缀   -> ipt-pre
   *   URL 匹配正则   -> ipt-rgx
   *   广告过滤正则   -> ipt-flt
   * 历史 BUG：三组曾共用 .re-ipt，而保存 filter_regex 时用全局 $$('.re-ipt') 读取，
   * 把前缀和正则一并写进「广告过滤正则」，且每次保存都把上次结果再叠加一遍。
   */
  function listVals(arr, single) {
    const out = [];
    (arr || []).forEach(v => { if (v && String(v).trim()) out.push(String(v).trim()); });
    if (out.length === 0 && single && String(single).trim()) out.push(String(single).trim());
    return out.length ? out : [''];
  }
  function fillListRows(sel, vals, ph, iptCls) {
    const box = $(sel);
    if (!box) return;
    box.innerHTML = '';
    vals.forEach(v => addListRow(sel, v, ph, iptCls));
  }
  function addListRow(sel, val, ph, iptCls) {
    const box = $(sel);
    if (!box) return null;
    const cls = iptCls || 'ipt-pre';
    const row = document.createElement('div');
    row.className = 're-row';
    row.innerHTML = `<input type="text" class="${cls}" placeholder="${esc(ph || '')}" value="${esc(val || '')}"><button type="button" class="btn btn-danger btn-sm re-del" title="删除这一行">×</button>`;
    box.appendChild(row);
    $('.re-del', row).addEventListener('click', () => {
      row.remove();
      toast('已删除该行，保存后生效', 'ok');
    });
    return row;
  }
  function readListRows(sel, iptCls) {
    return $$(sel + ' .' + (iptCls || 'ipt-pre')).map(i => i.value.trim()).filter(Boolean);
  }

  function closeSiteModal() { $('#siteModal').classList.add('hidden'); }
  function addRegexRow(val) {
    return addListRow('#regexRows', val, '如 ad\\.ts（m3u8 片段行匹配；当前版本后端未使用该字段）', 'ipt-flt');
  }

  async function saveSite() {
    const name = $('#f-name').value.trim();
    if (!name) { $('#siteErr').textContent = '站点名不能为空'; return; }
    // 特征码：一行一组，第一组同时写入 fingerprint 以兼容旧逻辑
    const fpLines = $('#f-fingerprint').value.split('\n')
      .map(l => l.split(/[,，]/).map(x => parseFloat(x.trim())).filter(n => !isNaN(n)))
      .filter(a => a.length);
    const fp = fpLines.length ? fpLines[0] : [];
    const fps = fpLines.slice(1);
    // 广告过滤正则：只从 #regexRows 容器读取（历史 BUG：这里曾用全局 $$('.re-ipt')，
    // 把「URL 匹配前缀」「URL 匹配正则」一起写进 filter_regex 并每次保存重复叠加）
    const regexes = readListRows('#regexRows', 'ipt-flt');
    const prefixes = readListRows('#urlPrefixRows', 'ipt-pre');
    const urlRegexes = readListRows('#urlRegexRows', 'ipt-rgx');
    const data = {
      id: (window._editingSite && window._editingSite.id) || '',
      name,
      parse_type: $('#f-parsetype').value,
      detect_mode: $('#f-detectmode').value,
      url_prefixes: prefixes,
      url_regexes: urlRegexes,
      url_prefix: (prefixes[0] || ''),
      url_regex: (urlRegexes[0] || ''),
      cache_subdir: $('#f-subdir').value.trim(),
      sort: parseInt($('#f-sort').value || '0', 10) || 0,
      fingerprint: fp,
      fingerprints: fps,
      filter_regex: regexes,
      index_mode: $('#f-indexmode').value,
      stream_pick: $('#f-streampick').value,
      fixed_sub: $('#f-fixedsub').value.trim(),
      proxy_url: $('#f-proxyurl').value.trim(),
      enabled: $('#f-enabled').checked,
      remark: $('#f-remark').value.trim()
    };
    const isNew = !data.id;
    // 后端 update 路径需要 id
    const r = await api(isNew ? '/api/sites' : '/api/sites/' + data.id, { method: isNew ? 'POST' : 'PUT', body: data });
    toast(r.msg, r.code === 200 ? 'ok' : 'err');
    if (r.code === 200) { closeSiteModal(); renderSites(); }
  }

  /* ----- 新增站点向导：m3u8 自动探测 ----- */
  function kv(k, v) {
    return '<span class="probe-k"><b>' + esc(String(k)) + '</b><span>' + esc(v == null || v === '' ? '-' : String(v)) + '</span></span>';
  }
  function fmtDur(sec) {
    sec = Number(sec) || 0;
    const m = Math.floor(sec / 60);
    return m + ' 分 ' + (sec % 60).toFixed(1) + ' 秒';
  }

  async function runProbe() {
    const url = $('#f-probeurl').value.trim();
    if (!url) { toast('请先填入该站的 m3u8 示例地址', 'err'); return; }
    const btn = $('#probeBtn');
    const box = $('#probeResult');
    btn.disabled = true; btn.textContent = '探测中…';
    box.classList.remove('hidden');
    box.innerHTML = '<div class="probe-note">正在分析索引结构与广告区间，请稍候…（内容哈希探测较慢）</div>';
    try {
      const r = await api('/api/probe', {
        method: 'POST',
        body: {
          url,
          index_mode: $('#f-indexmode').value,
          stream_pick: $('#f-streampick').value,
          fixed_sub: $('#f-fixedsub').value.trim(),
          hash_check: $('#f-probehash').checked,
          proxy_url: $('#f-proxyurl') ? $('#f-proxyurl').value.trim() : ''
        }
      });
      if (r.code !== 200) {
        box.innerHTML = '<div class="probe-err">' + esc(r.msg || '探测失败') + '</div>';
        return;
      }
      box.innerHTML = renderProbe(r.data);
      bindProbeActions(r.data);
    } finally {
      btn.disabled = false; btn.textContent = '开始探测';
    }
  }

  function renderProbe(d) {
    const s = d.suggest || {};
    const streams = d.streams || [];
    const cands = d.candidates || [];
    let h = '<div class="probe-sec"><div class="probe-kv">'
      + kv('索引结构', d.is_master ? '主索引（含多码率子列表）' : '单级列表（该地址即片段列表）')
      + kv('实际片段列表', d.playlist_url)
      + kv('片段总数', d.total_segs + ' 段')
      + kv('总时长', fmtDur(d.total_dur))
      + kv('DISCONTINUITY', d.disc_count + ' 处')
      + kv('时长分布', (d.dur_histogram || []).slice(0, 4).map(x => x.dur + 's×' + x.count).join('，'))
      + '</div></div>';

    if (streams.length) {
      h += '<div class="probe-sec"><div class="probe-sec-t">码率流 ' + streams.length + ' 条，当前选取：' + esc(d.picked_uri || '-') + '</div>'
        + '<table class="probe-tb"><tr><th>#</th><th>带宽</th><th>分辨率</th><th>URI</th></tr>'
        + streams.map(x => '<tr><td>' + x.index + '</td><td>' + (x.bandwidth ? (x.bandwidth / 1000).toFixed(0) + ' kbps' : '-')
        + '</td><td>' + esc(x.resolution || '-') + '</td><td class="mono">' + esc(x.uri) + '</td></tr>').join('')
        + '</table></div>';
    }

    if (cands.length) {
      h += '<div class="probe-sec"><div class="probe-sec-t">可疑广告区间（点击某一行可单独采用该区间的特征码）</div>'
        + '<table class="probe-tb"><tr><th>片段区间</th><th>段数</th><th>时长</th><th>评分</th><th>命中依据</th></tr>'
        + cands.map((c, i) => '<tr class="probe-cand" data-idx="' + i + '"><td class="mono">#' + c.start + ' - #' + c.end
        + '</td><td>' + c.segs + '</td><td>' + c.dur + 's</td><td>' + c.score + '</td><td>'
        + esc((c.reasons || []).join('、'))
        + (c.hash_confirmed ? ' <b>[内容哈希确认]</b>' : '')
        + (c.seq_hit ? ' <b>[TS 序列不连贯]</b>' : '')
        + (c.repeat_count > 1 ? ' ×' + c.repeat_count : '') + '</td></tr>').join('')
        + '</table></div>';
    } else {
      h += '<div class="probe-sec"><div class="probe-note">未检出明显广告区间，可勾选「内容哈希探测」再试一次。</div></div>';
    }

    h += '<div class="probe-sec"><div class="probe-sec-t">建站建议</div><div class="probe-kv">'
      + kv('站点名', s.name) + kv('URL 前缀', s.url_prefix) + kv('缓存子目录', s.cache_subdir)
      + kv('解析类型', s.parse_type) + kv('检测方式', s.detect_mode) + kv('索引模式', s.index_mode) + kv('码率选择', s.stream_pick)
      + kv('特征码', s.fingerprint_sets && s.fingerprint_sets.length
          ? s.fingerprint_sets.length + ' 组：' + s.fingerprint_sets.map(a => a.join(',')).join(' | ')
          : ((s.fingerprint || []).join(',') || '-'))
      + '</div>'
      + (s.note ? '<div class="probe-note">' + esc(s.note) + '</div>' : '')
      + '<div class="probe-acts"><button type="button" class="btn btn-primary btn-sm" id="probeApplyBtn">一键填充到表单</button></div></div>';
    return h;
  }

  function bindProbeActions(d) {
    const box = $('#probeResult');
    $$('.probe-cand', box).forEach(tr => {
      tr.addEventListener('click', () => applySuggest(d, parseInt(tr.dataset.idx, 10) || 0, true));
    });
    const ab = $('#probeApplyBtn', box);
    if (ab) ab.addEventListener('click', () => applySuggest(d, 0));
  }

  function applySuggest(d, candIdx, fromCand) {
    const s = d.suggest || {};
    if (s.name) $('#f-name').value = s.name;
    if (s.url_prefixes && s.url_prefixes.length) fillListRows('#urlPrefixRows', s.url_prefixes, '如 vip.dytt（主机名前缀）', 'ipt-pre');
    else if (s.url_prefix) fillListRows('#urlPrefixRows', [s.url_prefix], '如 vip.dytt（主机名前缀）', 'ipt-pre');
    if (s.url_regexes && s.url_regexes.length) fillListRows('#urlRegexRows', s.url_regexes, '如 lz-cdn\\d*（默认只匹配主机名）', 'ipt-rgx');
    else if (s.url_regex) fillListRows('#urlRegexRows', [s.url_regex], '如 lz-cdn\\d*（默认只匹配主机名）', 'ipt-rgx');
    if (s.cache_subdir) $('#f-subdir').value = s.cache_subdir;
    if (s.parse_type) $('#f-parsetype').value = s.parse_type;
    if (s.detect_mode) $('#f-detectmode').value = s.detect_mode;
    if (s.index_mode) $('#f-indexmode').value = s.index_mode;
    if (s.stream_pick) $('#f-streampick').value = s.stream_pick;
    if (s.fixed_sub) $('#f-fixedsub').value = s.fixed_sub;
    if (s.proxy_url) $('#f-proxyurl').value = s.proxy_url;
    if (!fromCand && (s.detect_mode === 'sequence' || s.detect_mode === 'blockcut' || s.detect_mode === 'slicebatch')) {
      // 序列模式不依赖特征码，清空以免误按旧指纹删正片
      $('#f-fingerprint').value = '';
      toast('已按探测结果填充表单，并切换为「TS 序列连贯性」检测（无需特征码）', 'ok');
      return;
    }
    const c = (d.candidates || [])[candIdx || 0];
    const sets = s.fingerprint_sets || [];
    if (fromCand && c && c.pattern && c.pattern.length) {
      $('#f-fingerprint').value = c.pattern.join(',');
    } else if (!fromCand && sets.length) {
      $('#f-fingerprint').value = sets.map(a => a.join(',')).join('\n');
    } else if (s.fingerprint && s.fingerprint.length) {
      $('#f-fingerprint').value = s.fingerprint.join(',');
    }
    toast('已按探测结果填充表单，请核对后保存', 'ok');
  }

  async function toggleSite(site) {
    const s = Object.assign({}, site, { enabled: !site.enabled });
    const r = await api('/api/sites/' + s.id, { method: 'PUT', body: s });
    toast(r.msg, r.code === 200 ? 'ok' : 'err');
    if (r.code === 200) renderSites();
  }
  async function delSite(site) {
    if (!confirm('确认删除站点「' + site.name + '」？此操作不可恢复。')) return;
    const r = await api('/api/sites/' + site.id, { method: 'DELETE' });
    toast(r.msg, r.code === 200 ? 'ok' : 'err');
    if (r.code === 200) renderSites();
  }

  /* ---------- 域名发现（苹果 CMS 采集接口） ---------- */
  let dsHosts = [];
  let dsGroups = [];  // 本次扫描的域名族泛化建议（后端按骨架归并，编号位抽成 \d+）
  let dsMeta = null;  // 本次扫描的分页元信息（pagecount / pagesize / total / strategy）
  let dsSel = -1;        // 当前选中的域名下标
  let dsSites = [];      // 已有站点列表（用于「追加到已有规则」）
  let dsProbe = null;
  let dsProbeHost = '';
  let dsProbeM3u8 = '';
  let dsChecked = new Set(); // 勾选的域名下标（用于批量添加）
  let dsSources = [];   // 资源站列表（名称 + 采集接口 + 关联站点）
  let dsPresets = [];   // 预置采集源
let dsScanning = false; // 是否正在扫描（扫描期间禁用触发控件）
let dsScanTimerId = 0;  // 扫描计时器句柄
  let dsCurSrc = null;  // 当前操作的资源站

  // dsOpenModal 打开通用弹窗（域名发现的规则配置 / 批量添加共用）
  function dsOpenModal(title, bodyHTML, footHTML) {
    const m = $('#dsModal');
    if (!m) return;
    $('#dsModalTitle').textContent = title;
    $('#dsModalBody').innerHTML = bodyHTML || '';
    $('#dsModalFoot').innerHTML = footHTML || '';
    m.classList.remove('hidden');
    const b = $('#dsModalBody');
    if (b) b.scrollTop = 0;
  }

  function dsCloseModal() {
    const m = $('#dsModal');
    if (m) m.classList.add('hidden');
  }

  function dsModalOpen() {
    const m = $('#dsModal');
    return !!m && !m.classList.contains('hidden');
  }

  // 资源站列表（一栏：资源站名称 / 采集接口 / 添加规则）
  async function renderDiscover() {
    const box = $('#page-discover');
    box.innerHTML = '<div class="ds-page">'
      + '<div class="card ds-src-card">'
      + '<div class="card-h ds-col-h"><span>资源站</span>'
      + '<span class="ds-sub" id="dsSrcCount">共 0 个资源站</span>'
      + '<div class="ds-src-acts">'
      + '<label class="ds-pages" title="页数越大耗时越长，建议按资源站体量填写（上限 5000）">扫描页数<input type="number" id="dsPages" value="20" min="1" max="5000"></label>'
      + '<label class="ds-pages">取页策略<select id="dsStrategy">'
      + '<option value="stride">全库均匀跳页</option>'
      + '<option value="seq">连续翻页</option>'
      + '<option value="random">随机页</option>'
      + '</select></label>'
      + '<label class="ds-pages" title="临时指定出网代理，留空跟随全局设置">出网代理<input type="text" id="dsProxy" class="w-lg" placeholder="socks5://… 可空"></label>'
      + '<button type="button" class="btn btn-primary btn-sm" id="dsAddSource">＋ 添加资源站</button>'
      + '</div></div>'
      + '<div class="ds-src-thead"><span>资源站名称</span><span>资源站采集接口</span><span class="ds-src-op-h">添加规则</span></div>'
      + '<div class="ds-src-list" id="dsSrcList"><div class="ds-empty">加载中…</div></div>'
      + '</div>'
      + '<div class="ds-sum" id="dsSrcTip">点「添加规则」会扫描该资源站的采集接口，默认勾选全部未配置域名，确认后一次性写入规则。</div>'
      + '</div>';

    $('#dsAddSource').addEventListener('click', () => dsEditSource(null));
    dsBindModal();
    await dsLoadPresets();
    await Promise.all([dsLoadSources(), dsLoadSites()]);
    dsRenderSources();
  }

  // dsLoadPresets 拉取预置采集源（供新增资源站时快速填充）
  async function dsLoadPresets() {
    if (dsPresets.length) return;
    try {
      const r = await api('/api/discover');
      dsPresets = (r && r.sources) || [];
    } catch (e) { dsPresets = []; }
  }

  // dsLoadSources 拉取资源站列表
  async function dsLoadSources() {
    try {
      const r = await api('/api/sources');
      dsSources = (r && (r.data || r.sources)) || [];
    } catch (e) {
      dsSources = dsPresets.map((x, i) => ({ id: 'p' + i, name: x.name, api_url: x.url, kind: x.kind, preset: true, sort: i + 1 }));
    }
    if (!Array.isArray(dsSources)) dsSources = [];
  }

  // dsLoadSites 拉取已有站点（批量追加时作为候选）
  async function dsLoadSites() {
    try {
      const sr = await api('/api/sites');
      dsSites = (sr.data || sr.sites || sr.list || []);
    } catch (e) { dsSites = []; }
    if (!Array.isArray(dsSites)) dsSites = [];
  }

  // dsRenderSources 渲染资源站列表（一栏三列）
  function dsRenderSources() {
    const box = $('#dsSrcList');
    if (!box) return;
    const el = $('#dsSrcCount');
    if (el) el.textContent = '共 ' + dsSources.length + ' 个资源站';
    if (!dsSources.length) {
      box.innerHTML = '<div class="ds-empty">暂无资源站，点右上角「＋ 添加资源站」新建一个</div>';
      return;
    }
    box.innerHTML = dsSources.map(s => {
      const hasApi = !!String(s.api_url || '').trim();
      return '<div class="ds-src-row" data-id="' + esc(s.id) + '">'
        + '<div class="ds-src-name"><b>' + esc(s.name) + '</b>'
        + (s.preset ? '<span class="ds-tag">预置</span>' : '<span class="ds-tag ok">自定义</span>')
        + '<div class="ds-src-meta">' + esc(dsSiteName(s.site_id)) + '</div></div>'
        + '<div class="ds-src-api"><span class="mono" title="' + esc(s.api_url || '') + '">' + esc(s.api_url || '未配置采集接口') + '</span></div>'
        + '<div class="ds-src-op">'
        + '<button type="button" class="btn btn-primary btn-sm" data-act="rule"' + (hasApi ? '' : ' disabled title="请先填写采集接口"') + '>添加规则</button>'
        + '<button type="button" class="btn btn-ghost btn-sm" data-act="hist">历史</button>'
        + '<button type="button" class="btn btn-ghost btn-sm" data-act="edit">编辑</button>'
        + '<button type="button" class="btn btn-ghost btn-sm" data-act="del">删除</button>'
        + '</div></div>';
    }).join('');

    $$('.ds-src-row', box).forEach(row => {
      const src = dsSources.filter(x => x.id === row.dataset.id)[0];
      if (!src) return;
      const act = (n, fn) => { const b = $('[data-act="' + n + '"]', row); if (b) b.addEventListener('click', () => fn(src)); };
      act('rule', dsAddRule);
      act('hist', dsOpenHistory);
      act('edit', dsEditSource);
      act('del', dsDelSource);
    });
  }

  // dsOpenHistory 查看 / 清空某个采集接口的域名历史库
  async function dsOpenHistory(src) {
    if (!src) return;
    let r;
    try { r = await api('/api/discover/history'); } catch (e) { toast('历史库读取失败', 'err'); return; }
    const all = (r && r.data) || [];
    const st = all.filter(x => x.api_url === src.api_url)[0] || { hosts: 0, scans: 0, last_scan: '' };
    const body = '<div class="ds-hist">'
      + '<div class="probe-kv">' + kv('采集接口', src.api_url)
      + kv('历史域名数', st.hosts)
      + kv('累计扫描次数', st.scans)
      + kv('最后扫描', st.last_scan || '-') + '</div>'
      + '<div class="ds-note">扫描结果按采集接口累积：这次没抽到的域名不会丢，下次扫描继续累加出现次数。'
      + '多次不同策略的扫描叠加起来，才等于一次全量。</div></div>';
    const foot = '<button type="button" class="btn btn-ghost" id="dsHistClose">关闭</button>'
      + '<button type="button" class="btn btn-primary" id="dsHistClear"' + (st.hosts ? '' : ' disabled') + '>'
      + '清空该站历史（' + (st.hosts || 0) + ' 个）</button>';
    dsOpenModal('域名历史 · ' + src.name, body, foot);
    $('#dsHistClose').addEventListener('click', dsCloseModal);
    $('#dsHistClear').addEventListener('click', async () => {
      if (!window.confirm('确定清空「' + src.name + '」的 ' + (st.hosts || 0) + ' 个历史域名？已生成的站点规则不受影响。')) return;
      const rr = await api('/api/discover/history?api_url=' + encodeURIComponent(src.api_url), { method: 'DELETE' });
      toast(rr.msg, rr.code === 200 ? 'ok' : 'err');
      dsCloseModal();
    });
  }

  function dsSiteName(id) {
    if (!id) return '未关联站点（批量添加时各建独立站点）';
    const st = dsSites.filter(x => x.id === id)[0];
    return '关联站点：' + (st ? (st.name || id) : id);
  }

  // discoverProxy 域名发现页的临时代理输入（可空，用于境外服务器访问被拦的资源站）
  function discoverProxy() {
    return ($('#dsProxy') && $('#dsProxy').value) ? $('#dsProxy').value.trim() : '';
  }

  // dsScanBusy 扫描期间禁用页内触发控件，避免重复点击把后端同时压上多路扫描
  function dsScanBusy(on) {
    dsScanning = !!on;
    ['dsPages', 'dsStrategy', 'dsProxy', 'dsAddSource'].forEach(id => {
      const el = $('#' + id);
      if (el) el.disabled = !!on;
    });
    const box = $('#dsSrcList');
    if (box) $$('button', box).forEach(b => { b.disabled = !!on; });
  }

  // dsAddRule 扫描该资源站采集接口 → 默认勾选全部未配置域名 → 打开批量添加弹窗
  async function dsAddRule(src) {
    if (!src) return;
    if (dsScanning) { toast('正在扫描中，请等待本次扫描结束', 'err'); return; }
    if (!String(src.api_url || '').trim()) { toast('该资源站未配置采集接口，请先编辑', 'err'); return; }
    const pages = parseInt((($('#dsPages') || {}).value), 10) || src.pages || 20;
    const strategy = (($('#dsStrategy') || {}).value) || 'stride';
    const sname = strategy === 'seq' ? '连续翻页' : (strategy === 'random' ? '随机页' : '全库均匀跳页');
    const heavy = pages > 30 ? '<div class="ds-note">共 ' + pages + ' 页，页数越大耗时越长（大站可能需数分钟），扫描期间请勿重复点击。</div>' : '';
    dsOpenModal('扫描 ' + src.name,
      '<div class="ds-loading">正在抓取采集接口（' + sname + ' ' + pages + ' 页），请稍候…</div>'
      + '<div class="ds-sum" id="dsScanTimer">已用时 0 秒（共 ' + pages + ' 页，页数越大耗时越长）</div>' + heavy,
      '<button type="button" class="btn btn-ghost" id="dsScanCancel" disabled>扫描中…</button>');
    const c = $('#dsScanCancel'); if (c) c.addEventListener('click', dsCloseModal);
    const t0 = Date.now();
    dsScanBusy(true);
    dsScanTimerId = setInterval(() => {
      const el = $('#dsScanTimer');
      if (el) el.textContent = '已用时 ' + Math.round((Date.now() - t0) / 1000) + ' 秒（共 ' + pages + ' 页，页数越大耗时越长）';
    }, 1000);
    const stopScan = () => {
      if (dsScanTimerId) { clearInterval(dsScanTimerId); dsScanTimerId = 0; }
      dsScanBusy(false);
    };
    try {
      const r = await api('/api/discover', {
        method: 'POST',
        body: { api_url: src.api_url, pages: pages, strategy: strategy, history: true, proxy_url: discoverProxy() }
      });
      if (r.code !== 200) { toast(r.msg || '扫描失败', 'err'); dsCloseModal(); return; }
      dsHosts = (r.data && r.data.hosts) || [];
      dsGroups = (r.data && r.data.groups) || [];
      dsMeta = (r.data && r.data.meta) || null;
      dsChecked.clear();
      dsHosts.forEach((h, i) => { if (!h.configured) dsChecked.add(i); });
      if (!dsHosts.length) { toast('未发现播放域名', 'err'); dsCloseModal(); return; }
      const un = dsHosts.filter(h => !h.configured).length;
      if (!un) {
        toast('该资源站的 ' + dsHosts.length + ' 个域名均已配置，无需添加', 'ok');
      } else if (r.data && r.data.new_hosts) {
        toast('本次新增 ' + r.data.new_hosts + ' 个域名（历史累计 ' + (r.data.history_total || dsHosts.length) + ' 个）', 'ok');
      }
      dsOpenBatch(src);
    } catch (e) { dsCloseModal(); toast(String((e && e.message) || e), 'err'); } finally { stopScan(); }
  }

  // dsScanSummaryHTML 本次扫描概况：取页策略 + 实际覆盖比例
  function dsScanSummaryHTML() {
    const m = dsMeta;
    if (!m) return '';
    const sname = m.strategy === 'seq' ? '连续翻页' : (m.strategy === 'random' ? '随机页' : '全库均匀跳页');
    const cov = (m.pagecount && m.pages_scanned) ? (m.pages_scanned * 100 / m.pagecount) : 0;
    return '<div class="ds-scanmeta"><b>扫描概况</b> · ' + sname + '，实际抓 ' + (m.pages_scanned || 0) + ' 页'
      + (m.pagecount
          ? '（全库 ' + m.pagecount + ' 页 / ' + (m.total || '?') + ' 条影片，覆盖约 ' + cov.toFixed(2) + '%）'
          : '（该接口未返回总页数）')
      + '<div class="ds-note">播放域名按影片分布，每页条数由站方锁定，翻页穷举不现实；'
      + '未抽到的域名会累积进历史库，下次扫描继续补充。</div></div>';
  }

  // dsGroupsHTML 域名族泛化建议：同族编号位已抽成 \d+，勾选即整族覆盖
  function dsGroupsHTML() {
    const gs = (dsGroups || []).filter(g => g && g.variants > 1);
    if (!gs.length) return '';
    return '<div class="ds-groups"><div class="ds-groups-h">泛化建议'
      + '<span class="ds-note">同族域名的编号位已抽成 <code>\\d+</code>，勾选后写入正则，'
      + '整族覆盖（含本次没抽到的编号）</span></div>'
      + dsGroups.map((g, i) => (!g || g.variants <= 1) ? '' : ('<label class="ds-group-row">'
        + '<input type="checkbox" data-gp="' + i + '" checked>'
        + '<span class="mono ds-gp-pat">' + esc(g.pattern) + '</span>'
        + '<span class="ds-gp-lab">' + esc(g.label) + '</span>'
        + '<span class="ds-num">' + (g.variants || 0) + ' 个</span>'
        + '<span class="ds-gp-mem" title="' + esc((g.members || []).join(' / ')) + '">'
        + esc((g.members || []).slice(0, 4).join('、') + ((g.members || []).length > 4 ? ' 等' : ''))
        + '</span></label>')).join('')
      + '</div>';
  }

  // dsPickedRegexes 收集弹窗中勾选的泛化正则
  function dsPickedRegexes() {
    const out = [];
    $$('#dsModalBody input[data-gp]').forEach(cb => {
      if (!cb.checked) return;
      const g = (dsGroups || [])[parseInt(cb.dataset.gp, 10)];
      if (g && g.pattern) out.push(g.pattern);
    });
    return out;
  }

  // dsEditSource 新增 / 编辑资源站
  function dsEditSource(src) {
    const isNew = !src;
    const s = src || {};
    const body = '<div class="ds-form-src">'
      + '<label class="ds-f"><span>资源站名称 <em>*</em></span>'
      + '<input type="text" id="dsSName" value="' + esc(s.name || '') + '" placeholder="如 量子 lz"></label>'
      + '<label class="ds-f"><span>采集接口地址 <em>*</em></span>'
      + '<input type="text" id="dsSUrl" value="' + esc(s.api_url || '') + '" placeholder="https://xxx/api.php/provide/vod/at/xml/"></label>'
      + '<label class="ds-f"><span>快速填充（预置采集源）</span><select id="dsSPreset">'
      + '<option value="">（手动填写）</option>'
      + dsPresets.map(p => '<option value="' + esc(p.url || '') + '">' + esc(p.name + '（' + p.kind + '）') + '</option>').join('')
      + '</select></label>'
      + '<div class="ds-row2">'
      + '<label class="ds-f"><span>接口类型</span><select id="dsSKind">'
      + [['', '自动识别'], ['xml', 'xml'], ['json', 'json']].map(o =>
          '<option value="' + o[0] + '"' + ((s.kind || '') === o[0] ? ' selected' : '') + '>' + o[1] + '</option>').join('')
      + '</select></label>'
      + '<label class="ds-f"><span>默认扫描页数</span><input type="number" id="dsSPages" value="' + (s.pages || 3) + '" min="1" max="5000" title="页数越大耗时越长（上限 5000）"></label>'
      + '</div>'
      + '<label class="ds-f"><span>关联站点（可选）</span><select id="dsSSite">'
      + '<option value="">（不关联，批量添加时各建独立站点）</option>'
      + dsSites.map(x => '<option value="' + esc(x.id) + '"' + ((s.site_id || '') === x.id ? ' selected' : '') + '>'
          + esc(x.name || x.id) + '</option>').join('')
      + '</select></label>'
      + '<div class="ds-note">关联站点后，点「添加规则」时默认把新域名追加到该站点的匹配前缀，而不是各建独立站点。</div>'
      + '<div class="ds-err" id="dsSErr"></div>'
      + '</div>';
    const foot = '<button type="button" class="btn btn-ghost" id="dsSCancel">取消</button>'
      + '<button type="button" class="btn btn-primary" id="dsSSave">保存</button>';
    dsOpenModal(isNew ? '添加资源站' : '编辑资源站：' + (s.name || s.id), body, foot);

    $('#dsSPreset').addEventListener('change', () => {
      const v = $('#dsSPreset').value;
      if (v) $('#dsSUrl').value = v;
    });
    $('#dsSCancel').addEventListener('click', dsCloseModal);
    $('#dsSSave').addEventListener('click', () => dsSaveSource(src));
  }

  async function dsSaveSource(src) {
    const name = ($('#dsSName') || {}).value.trim();
    const api_url = ($('#dsSUrl') || {}).value.trim();
    if (!name) { $('#dsSErr').textContent = '请填写资源站名称'; return; }
    if (!api_url) { $('#dsSErr').textContent = '请填写采集接口地址'; return; }
    const data = {
      id: src ? src.id : '',
      name: name,
      api_url: api_url,
      kind: ($('#dsSKind') || {}).value || '',
      site_id: ($('#dsSSite') || {}).value || '',
      pages: parseInt(($('#dsSPages') || {}).value, 10) || 0,
      sort: src ? (src.sort || 0) : (dsSources.length + 1)
    };
    const r = await api('/api/sources', { method: src ? 'PUT' : 'POST', body: data });
    toast(r.msg, r.code === 200 ? 'ok' : 'err');
    if (r.code === 200) {
      dsCloseModal();
      await dsLoadSources();
      dsRenderSources();
    } else {
      $('#dsSErr').textContent = r.msg || '保存失败';
    }
  }

  async function dsDelSource(src) {
    if (!src) return;
    if (!window.confirm('确定删除资源站「' + src.name + '」？该操作只删除采集接口条目，不影响已生成的站点规则。')) return;
    const r = await api('/api/sources?id=' + encodeURIComponent(src.id), { method: 'DELETE' });
    toast(r.msg, r.code === 200 ? 'ok' : 'err');
    if (r.code === 200) {
      await dsLoadSources();
      dsRenderSources();
    }
  }

  // 弹窗通用交互：遮罩点击 / 关闭按钮 / Esc 关闭（幂等绑定）
  function dsBindModal() {
    const m = $('#dsModal');
    if (!m || m.dataset.bound === '1') return;
    m.dataset.bound = '1';
    const c = $('#dsModalClose');
    if (c) c.addEventListener('click', dsCloseModal);
    document.addEventListener('keydown', (ev) => {
      if (ev.key === 'Escape' && dsModalOpen()) dsCloseModal();
    });
  }

  function dsRows() {
    const kw = ($('#dsKw') && $('#dsKw').value || '').trim().toLowerCase();
    const only = $('#dsOnlyNew') && $('#dsOnlyNew').checked;
    return dsHosts.map((h, i) => Object.assign({ _i: i }, h))
      .filter(h => !only || !h.configured)
      .filter(h => !kw || String(h.host).toLowerCase().indexOf(kw) >= 0);
  }

  // dsRenderRows 渲染域名表（含多选勾选框）
  function dsRenderRows() {
    const tb = $('#dsBody');
    if (!tb) return;
    const rows = dsRows();
    const el = $('#dsCount');
    const un = dsHosts.filter(h => !h.configured).length;
    if (el) el.textContent = '共 ' + dsHosts.length + ' 个域名 · 未配置 ' + un;
    tb.innerHTML = rows.length ? rows.map(h => '<tr class="' + (h.configured ? 'cfg' : '')
      + (h._i === dsSel ? ' ds-sel' : '') + (dsChecked.has(h._i) ? ' ds-on' : '') + '" data-i="' + h._i + '">'
      + '<td class="ds-ck"><label class="ds-cb"><input type="checkbox" data-ck="' + h._i + '"'
      + (dsChecked.has(h._i) ? ' checked' : '') + '></label></td>'
      + '<td class="mono ds-host" title="' + esc(h.host) + '">' + esc(h.host) + '</td>'
      + '<td class="ds-num">' + h.count + '</td>'
      + '<td>' + (h.configured
          ? '<span class="ds-tag ok">已覆盖</span>'
          : '<span class="ds-tag new">未配置</span>') + '</td>'
      + '<td class="ds-op"><button type="button" class="btn btn-ghost btn-xs" data-act="rule">配置</button></td>'
      + '</tr>').join('')
      : '<tr><td colspan="5" class="ds-empty">暂无数据，先在「② 资源站采集地址」扫描一次</td></tr>';
    $$('tr[data-i]', tb).forEach(tr => tr.addEventListener('click', (ev) => {
      if (ev.target && ev.target.closest && ev.target.closest('.ds-cb')) return;
      dsSel = parseInt(tr.dataset.i, 10);
      dsRenderRows(); dsOpenRule(dsHosts[dsSel]);
    }));
    $$('input[data-ck]', tb).forEach(cb => cb.addEventListener('change', () => {
      const i = parseInt(cb.dataset.ck, 10);
      if (cb.checked) dsChecked.add(i); else dsChecked.delete(i);
      dsRenderRows(); dsSyncSel();
    }));
    dsSyncAllBox(rows);
  }

  // dsCheckedList 返回勾选的域名（按下标排序），含 host / 是否已配置
  function dsCheckedList() {
    return Array.from(dsChecked).sort((a, b) => a - b)
      .map(i => ({ i: i, host: (dsHosts[i] || {}).host, configured: !!(dsHosts[i] || {}).configured }))
      .filter(x => x.host);
  }

  function dsSyncAllBox(rows) {
    const box = $('#dsSelAll');
    if (!box) return;
    const list = (rows || dsRows()).filter(h => !h.configured);
    const on = list.filter(h => dsChecked.has(h._i)).length;
    box.checked = list.length > 0 && on === list.length;
    box.indeterminate = on > 0 && on < list.length;
  }

  // dsSyncSel 同步勾选统计条与批量按钮文案
  function dsSyncSel() {
    const list = dsCheckedList();
    const un = list.filter(x => !x.configured).length;
    const txt = list.length
      ? '已选 ' + list.length + ' 个（未配置 ' + un + ' 个' + (list.length - un ? '，已覆盖 ' + (list.length - un) + ' 个将跳过' : '') + '）'
      : '未选择域名';
    const t = $('#dsSelInfo'); if (t) t.textContent = txt;
    const b = $('#dsBarTxt'); if (b) b.textContent = txt;
    const btn = $('#dsBatchAdd');
    if (btn) {
      btn.disabled = un === 0;
      btn.textContent = un ? ('批量添加 ' + un + ' 个未配置域名') : '批量添加';
    }
    const bar = $('#dsBatchBar');
    if (bar) bar.classList.toggle('active', list.length > 0);
  }

  function dsBatchMode() {
    const el = document.querySelector('input[name="dsbmode"]:checked');
    return el ? el.value : 'new';
  }

  // dsOpenBatch 打开「添加规则」弹窗：默认勾选全部未配置域名，可现场调整
  function dsOpenBatch(src) {
    dsCurSrc = src || dsCurSrc;
    const pick = src || dsCurSrc;
    if (!dsHosts.length) { toast('请先扫描域名', 'err'); return; }
    const un = dsCheckedList().filter(x => !x.configured);
    // 关联了站点 → 默认追加到该站点；否则各建独立站点
    const defMode = (pick && pick.site_id && dsSites.filter(x => x.id === pick.site_id).length) ? 'append' : 'new';
    const mode = defMode;
    const body = '<div class="ds-batch">'
      + '<div class="ds-batch-h">添加规则 · ' + esc((pick && pick.name) || '未配置域名')
      + '<span class="ds-badge">' + dsHosts.length + ' 个域名</span>'
      + '<span class="ds-badge ' + (un.length ? 'warn' : 'ok') + '" id="dsBadgeUn">' + un.length + ' 待添加</span></div>'
      + '<div class="ds-pick">'
      + '<div class="ds-pick-h"><label class="mini-check"><input type="checkbox" id="dsPickAll"> 全选未配置</label>'
      + '<span class="ds-pick-info" id="dsPickInfo"></span></div>'
      + '<div class="ds-pick-list">' + dsHosts.map((h, i) => dsPickRowHTML(h, i)).join('') + '</div>'
      + '</div>'
      + '<div class="ds-mode">'
      + '<label class="mini-check"><input type="radio" name="dsbmode" value="new"' + (mode === 'append' ? '' : ' checked') + '> 新建一条规则（所有域名归入此规则）</label>'
      + '<label class="mini-check"><input type="radio" name="dsbmode" value="append"' + (mode === 'append' ? ' checked' : '') + '> 追加到已有规则</label>'
      + '</div>'
      + '<div id="dsBAppendWrap"' + (mode === 'append' ? '' : ' class="hidden"') + '>'
      + '<label class="ds-f"><span>目标站点</span><select id="dsBAppendSite">'
      + dsSites.map(x => '<option value="' + esc(x.id) + '"'
          + ((pick && pick.site_id) === x.id ? ' selected' : '') + '>' + esc(x.name || x.id) + '</option>').join('')
      + '</select></label>'
      + '<div class="ds-note">把所选域名追加为该站点的多个「URL 匹配前缀」，保留原有特征码与解析配置。</div>'
      + '</div>'
      + '<div id="dsBNewWrap"' + (mode === 'append' ? ' class="hidden"' : '') + '>'
      + '<div class="ds-row2">'
      + '<label class="ds-f"><span>检测方式</span><select id="dsBDetect">'
      + [['sequence', 'TS 序列连贯性'], ['fingerprint', '特征码匹配'], ['blockcut', '广告块截断'], ['slicebatch', '切片批次'], ['shortblock', '孤立短块']].map(o =>
          '<option value="' + o[0] + '"' + (o[0] === 'sequence' ? ' selected' : '') + '>' + o[1] + '</option>').join('')
      + '</select></label>'
      + '<label class="ds-f"><span>索引模式</span><select id="dsBIndex">'
      + [['auto', '自动'], ['single', '单级列表']].map(o =>
          '<option value="' + o[0] + '"' + (o[0] === 'auto' ? ' selected' : '') + '>' + o[1] + '</option>').join('')
      + '</select></label></div>'
      + '<div class="ds-row2">'
      + '<label class="ds-f"><span>码率选择</span><select id="dsBPick">'
      + [['first', '第一条'], ['highest', '最高码率'], ['lowest', '最低码率']].map(o =>
          '<option value="' + o[0] + '"' + (o[0] === 'first' ? ' selected' : '') + '>' + o[1] + '</option>').join('')
      + '</select></label>'
      + '<label class="ds-f"><span>缓存子目录</span><input type="text" id="dsBSub" value="自动（取资源站名）" disabled></label></div>'
      + '<div class="ds-note">一个资源站只对应一条规则：勾选的所有域名会写入同一条规则的「URL 匹配前缀」列表，共用一套特征码与解析配置。</div>'
      + '</div>'
      + dsScanSummaryHTML()
      + dsGroupsHTML()
      + '<label class="mini-check ds-bgen"><input type="checkbox" id="dsBGeneric" checked> 同时写入宽松泛化正则（如 <code>lzcdn\\d*</code>，兜底同系列编号域名）</label>'
      + '<div class="ds-note">规则创建后会与该资源站绑定，以后该站扫到的新域名默认追加进这条规则，不再新建。</div>'
      + '</div>';
    const foot = '<button type="button" class="btn btn-ghost btn-sm" id="dsBClear">取消</button>'
      + '<button type="button" class="btn btn-primary" id="dsBGo"' + (un.length ? '' : ' disabled') + '>'
      + (un.length ? '生成 1 条规则 · 覆盖 ' + un.length + ' 个域名' : '无可添加域名') + '</button>';
    dsOpenModal('添加规则 · ' + ((pick && pick.name) || '域名'), body, foot);

    $$('#dsModalBody input[name="dsbmode"]').forEach(r => r.addEventListener('change', () => {
      const ap = dsBatchMode() === 'append';
      $('#dsBAppendWrap').classList.toggle('hidden', !ap);
      $('#dsBNewWrap').classList.toggle('hidden', ap);
    }));
    $$('#dsModalBody input[data-pk]').forEach(cb => cb.addEventListener('change', () => {
      const i = parseInt(cb.dataset.pk, 10);
      if (cb.checked) dsChecked.add(i); else dsChecked.delete(i);
      dsPickSync();
    }));
    const all = $('#dsPickAll');
    if (all) all.addEventListener('change', () => {
      dsHosts.forEach((h, i) => {
        if (h.configured) return;
        if (all.checked) dsChecked.add(i); else dsChecked.delete(i);
      });
      $$('#dsModalBody input[data-pk]').forEach(cb => { cb.checked = dsChecked.has(parseInt(cb.dataset.pk, 10)); });
      dsPickSync();
    });
    dsPickSync();
    $('#dsBGo').addEventListener('click', dsBatchGo);
    $('#dsBCancel') && $('#dsBCancel').addEventListener('click', dsCloseModal);
    $('#dsBClear') && $('#dsBClear').addEventListener('click', dsCloseModal);
  }

  // dsPickRowHTML 弹窗内一行域名（未配置可勾选，已配置禁用）
  function dsPickRowHTML(h, i) {
    return '<label class="ds-pick-row' + (h.configured ? ' cfg' : '') + '">'
      + '<input type="checkbox" data-pk="' + i + '"' + (dsChecked.has(i) ? ' checked' : '')
      + (h.configured ? ' disabled' : '') + '>'
      + '<span class="mono ds-pk-host" title="' + esc(h.host) + '">' + esc(h.host) + '</span>'
      + '<span class="ds-num">' + (h.count || 0) + '</span>'
      + (h.new ? '<span class="ds-tag warn" title="本次扫描首次发现">新</span>' : '')
      + (h.history_only
          ? '<span class="ds-tag hist" title="历史累计 ' + (h.hits || 0) + ' 次，最后出现 ' + esc(h.last_seen || '-') + '">历史</span>'
          : ((h.hits || 0) > (h.count || 0) ? '<span class="ds-tag hist" title="历史累计 ' + h.hits + ' 次">累计 ' + h.hits + '</span>' : ''))
      + (h.configured
          ? '<span class="ds-tag ok" title="' + esc(h.site_name || h.site_id || '') + '">已覆盖</span>'
          : '<span class="ds-tag new">未配置</span>')
      + '</label>';
  }

  // dsPickSync 同步弹窗内勾选统计与底部按钮
  function dsPickSync() {
    const un = dsCheckedList().filter(x => !x.configured).length;
    const info = $('#dsPickInfo');
    if (info) info.textContent = '已选 ' + un + ' 个未配置域名';
    const bd = $('#dsBadgeUn');
    if (bd) { bd.textContent = un + ' 待添加'; bd.className = 'ds-badge ' + (un ? 'warn' : 'ok'); }
    const total = dsHosts.filter(h => !h.configured).length;
    const all = $('#dsPickAll');
    if (all) {
      all.checked = total > 0 && un === total;
      all.indeterminate = un > 0 && un < total;
    }
    const b = $('#dsBGo');
    if (b) {
      b.disabled = un === 0;
      b.textContent = un ? ('生成 1 条规则 · 覆盖 ' + un + ' 个域名') : '无可添加域名';
    }
  }

  // dsBatchGo 一键批量添加勾选的未配置域名
  async function dsBatchGo() {
    const list = dsCheckedList().filter(x => !x.configured);
    if (!list.length) { toast('所选域名均已配置', 'err'); return; }
    const mode = dsBatchMode();
    if (mode === 'append' && !dsSites.length) { toast('暂无可追加的站点', 'err'); return; }
    const btn = $('#dsBGo') || $('#dsBatchAdd');
    if (btn) { btn.disabled = true; btn.textContent = '添加中…'; }
    try {
      const body = {
        mode: mode,
        site_id: mode === 'append' ? (($('#dsBAppendSite') || {}).value || '') : '',
        source_id: (dsCurSrc || {}).id || '',
        source_name: (dsCurSrc || {}).name || '',
        hosts: list.map(x => x.host),
        generic_regex: ($('#dsBGeneric') && !$('#dsBGeneric').checked) ? false : true,
        extra_regexes: dsPickedRegexes(),
        detect_mode: ($('#dsBDetect') || {}).value || 'sequence',
        index_mode: ($('#dsBIndex') || {}).value || 'auto',
        stream_pick: ($('#dsBPick') || {}).value || 'first',
        parse_type: 'duration'
      };
      const r = await api('/api/discover/batch', { method: 'POST', body: body });
      toast(r.msg, r.code === 200 ? 'ok' : 'err');
      if (r.code === 200) {
        const stName = mode === 'append' ? ((dsSites.filter(x => x.id === body.site_id)[0] || {}).name || body.site_id) : (body.source_name || '新规则');
        (r.added || []).forEach(host => {
          const o = dsHosts.filter(h => h.host === host)[0];
          if (o) { o.configured = true; o.site_name = stName; }
        });
        dsChecked.clear();
        try { const sr = await api('/api/sites'); dsSites = (sr.data || sr.sites || sr.list || []); } catch (e) { }
        if (!Array.isArray(dsSites)) dsSites = [];
        if (r.skipped && r.skipped.length) {
          toast(r.msg + '（跳过 ' + r.skipped.length + ' 个已覆盖）', 'ok');
        }
        try { await dsLoadSources(); } catch (e) { } // 刷新资源站与规则的绑定关系
        dsRenderRows(); dsSyncSel(); dsCloseModal();
      }
    } finally {
      if (btn) { btn.disabled = false; }
      const b2 = $('#dsBGo'); if (b2) { b2.disabled = false; }
      dsSyncSel(); // 重新计算按钮文案与统计
    }
  }

  // 泛化域名主体：取二级名去尾部数字，如 lzcdn27 → lzcdn
  function dsGeneric(host) {
    const p = String(host).split('.');
    let m = p.length >= 2 ? p[p.length - 2] : host;
    m = String(m).toLowerCase().replace(/\d+$/, '');
    return m || host;
  }
  function dsGenericRe(host) {
    return dsGeneric(host).replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + '\\d*';
  }

  function dsRuleMode() {
    const el = document.querySelector('input[name="dsmode"]:checked');
    return el ? el.value : 'new';
  }

  // dsOpenRule 以弹窗方式打开单个域名的规则配置
  function dsOpenRule(h) {
    if (!h) { toast('请先在左侧点选一个域名', 'err'); return; }
    const same = dsProbe && dsProbeHost === h.host;
    const s = same ? (dsProbe.suggest || {}) : {};
    const sample = (same && dsProbeM3u8) ? dsProbeM3u8 : h.sample;
    const name = (same && s.name) ? s.name : (dsGeneric(h.host) + '(' + h.host + ')');
    const sub = (same && s.cache_subdir) ? s.cache_subdir : dsGeneric(h.host);
    const dm = (same && s.detect_mode) ? s.detect_mode : 'sequence';
    const im = (same && s.index_mode) ? s.index_mode : 'auto';
    const sp = (same && s.stream_pick) ? s.stream_pick : 'first';
    const fps = (same && s.fingerprint_sets && s.fingerprint_sets.length)
      ? s.fingerprint_sets.map(a => a.join(',')).join('\n') : '';

    const body = (h.configured ? '<div class="ds-rule-tip">该域名已被现有规则覆盖，可再次保存以更新</div>' : '')
      + '<div class="ds-rule-h mono">' + esc(h.host) + '</div>'
      + '<div class="probe-kv">' + kv('出现次数', h.count)
      + kv('地址类型', h.kind === 'm3u8' ? 'm3u8 直链' : '播放页')
      + kv('样本地址', sample)
      + kv('当前状态', h.configured ? ('已覆盖：' + (h.site_name || h.site_id)) : '未配置') + '</div>'
      + '<div id="dsProbeBox" class="ds-probe-box"></div>'
      + '<div class="ds-mode">'
      + '<label class="mini-check"><input type="radio" name="dsmode" value="new"' + (dsRuleMode() === 'append' ? '' : ' checked') + '> 新建站点</label>'
      + '<label class="mini-check"><input type="radio" name="dsmode" value="append"' + (dsRuleMode() === 'append' ? ' checked' : '') + '> 追加到已有规则</label>'
      + '</div>'
      + '<div id="dsAppendWrap"' + (dsRuleMode() === 'append' ? '' : ' class="hidden"') + '>'
      + '<label class="ds-f"><span>目标站点</span><select id="dsAppendSite">'
      + dsSites.map(x => '<option value="' + esc(x.id) + '">' + esc(x.name || x.id) + '</option>').join('')
      + '</select></label>'
      + '<div class="ds-note">把该域名追加为所选站点的一个「URL 匹配前缀」，保留原特征码与解析配置</div>'
      + '</div>'
      + '<div id="dsNewWrap"' + (dsRuleMode() === 'append' ? ' class="hidden"' : '') + '>'
      + '<label class="ds-f"><span>站点名</span><input type="text" id="dsRName" value="' + esc(name) + '"></label>'
      + '<label class="ds-f"><span>URL 匹配前缀（可多个，任一命中即匹配）</span>'
      + '<div id="dsPrefixRows" class="relist"></div>'
      + '<button type="button" class="btn btn-ghost btn-sm" id="dsAddPrefix">＋ 添加前缀</button></label>'
      + '<label class="ds-f"><span>URL 匹配正则（可选，可多个）</span>'
      + '<div id="dsRegexRows" class="relist"></div>'
      + '<button type="button" class="btn btn-ghost btn-sm" id="dsAddRegex">＋ 添加正则</button></label>'
      + '<div class="ds-row2">'
      + '<label class="ds-f"><span>检测方式</span><select id="dsRDetect">'
      + [['sequence', 'TS 序列连贯性'], ['fingerprint', '特征码匹配'], ['blockcut', '广告块截断'], ['slicebatch', '切片批次'], ['shortblock', '孤立短块']].map(o =>
          '<option value="' + o[0] + '"' + (dm === o[0] ? ' selected' : '') + '>' + o[1] + '</option>').join('')
      + '</select></label>'
      + '<label class="ds-f"><span>索引模式</span><select id="dsRIndex">'
      + [['auto', '自动'], ['single', '单级列表'], ['fixed', '固定子路径']].map(o =>
          '<option value="' + o[0] + '"' + (im === o[0] ? ' selected' : '') + '>' + o[1] + '</option>').join('')
      + '</select></label></div>'
      + '<div class="ds-row2">'
      + '<label class="ds-f"><span>码率选择</span><select id="dsRPick">'
      + [['first', '第一条'], ['highest', '最高码率'], ['lowest', '最低码率']].map(o =>
          '<option value="' + o[0] + '"' + (sp === o[0] ? ' selected' : '') + '>' + o[1] + '</option>').join('')
      + '</select></label>'
      + '<label class="ds-f"><span>缓存子目录</span><input type="text" id="dsRSub" value="' + esc(sub) + '"></label>'
      + '</div>'
      + '<label class="ds-f"><span>特征码（一行一组；序列模式可留空）</span>'
      + '<textarea id="dsRfp" rows="3" placeholder="如 2,3,4">' + esc(fps) + '</textarea></label>'
      + '</div>';
    const foot = '<button type="button" class="btn btn-ghost" id="dsRuleClose">取消</button>'
      + '<button type="button" class="btn btn-ghost" id="dsTest">去测试页验证</button>'
      + '<button type="button" class="btn btn-primary" id="dsProbeBtn">探测并填充</button>'
      + '<button type="button" class="btn btn-primary" id="dsSave">保存规则</button>';
    dsOpenModal('规则配置 · ' + h.host, body, foot);

    fillListRows('#dsPrefixRows', (same && s.url_prefixes && s.url_prefixes.length) ? s.url_prefixes : [h.host], '如 vip.dytt（主机名前缀）', 'ipt-pre');
    fillListRows('#dsRegexRows', (same && s.url_regexes && s.url_regexes.length) ? s.url_regexes : [dsGenericRe(h.host)], '如 lz-cdn\\d*（默认只匹配主机名）', 'ipt-rgx');
    $('#dsAddPrefix').addEventListener('click', () => addListRow('#dsPrefixRows', '', '如 vip.dytt（主机名前缀）', 'ipt-pre'));
    $('#dsAddRegex').addEventListener('click', () => addListRow('#dsRegexRows', '', '如 lz-cdn\\d*（默认只匹配主机名）', 'ipt-rgx'));
    $$('#dsModalBody input[name="dsmode"]').forEach(r => r.addEventListener('change', () => {
      const ap = dsRuleMode() === 'append';
      $('#dsAppendWrap').classList.toggle('hidden', !ap);
      $('#dsNewWrap').classList.toggle('hidden', ap);
    }));
    $('#dsRuleClose').addEventListener('click', dsCloseModal);
    $('#dsProbeBtn').addEventListener('click', () => dsProbeHostRun(h));
    $('#dsSave').addEventListener('click', dsSaveSite);
    $('#dsTest').addEventListener('click', () => {
      if (!dsProbeM3u8) { toast('请先对样本执行一次探测', 'err'); return; }
      localStorage.setItem('dytt_test_url', dsProbeM3u8);
      dsCloseModal();
      location.hash = '#/test';
      route();
    });
    // 已有探测结果时回填到弹窗内
    if (same && dsProbe) dsRenderProbeBox(h);
  }

  // dsRenderProbeBox 把探测结果渲染到弹窗内（并同步一份到页面结果卡片）
  function dsRenderProbeBox(h) {
    const box = $('#dsProbeBox');
    if (box) box.innerHTML = dsProbeHTML(h);
    const rb = $('#dsResult');
    if (rb) { rb.classList.remove('hidden'); rb.innerHTML = '<div class="card-h">探测结果 · ' + esc(h.host) + '</div>' + dsProbeHTML(h); }
  }

  function dsProbeHTML(h) {
    const s = dsProbe.suggest || {};
    return '<div class="ds-sp">'
      + '<div class="probe-kv">' + kv('探测地址', dsProbeM3u8)
      + kv('时长', (dsProbe.total_duration || 0).toFixed(1) + ' 秒')
      + kv('TS 分片', dsProbe.count)
      + kv('平均时长', (dsProbe.avg_duration || 0).toFixed(2) + ' 秒')
      + kv('文件大小', dsProbe.file_size ? Math.round(dsProbe.file_size / 1024) + ' KB' : '-')
      + kv('检测耗时', (dsProbe.elapsed_ms || 0) + ' ms')
      + kv('平均码率', (dsProbe.avg_bandwidth || 0) ? Math.round(dsProbe.avg_bandwidth / 1000) + ' kbps' : '-')
      + kv('分辨率', dsProbe.resolution || '-')
      + kv('序列步长', dsProbe.step || '-')
      + kv('一致前缀', dsProbe.prefix || '-')
      + kv('一致后缀', dsProbe.suffix || '-') + '</div>'
      + '<div class="ds-sum-ok">建议：' + esc(s.reason || '') + '</div>'
      + '<div class="ds-kv-row">'
      + kv('建议检测方式', s.detect_mode === 'fingerprint' ? '特征码匹配' : 'TS 序列连贯性')
      + kv('建议索引模式', s.index_mode === 'fixed' ? '固定子路径' : (s.index_mode === 'single' ? '单级列表' : '自动'))
      + kv('建议缓存目录', s.cache_subdir || '-')
      + kv('建议特征码', (s.fingerprint_sets || []).length ? s.fingerprint_sets.map(a => a.join(',')).join(' / ') : '无需（序列模式）')
      + '</div></div>';
  }

  async function dsResolve(h) {
    if (h.kind === 'm3u8') { dsShowM3u8(h, h.sample); return; }
    $('#dsSummary').textContent = '正在解析播放页：' + h.sample;
    const r = await api('/api/discover/resolve', { method: 'POST', body: { page_url: h.sample, proxy_url: discoverProxy() } });
    if (r.code !== 200) { toast(r.msg || '播放页解析失败', 'err'); return; }
    $('#dsSummary').textContent = '已提取 m3u8：' + r.data.m3u8;
    dsShowM3u8(h, r.data.m3u8);
  }

  function dsShowM3u8(h, u) {
    const box = $('#dsResult');
    box.classList.remove('hidden');
    box.innerHTML = '<div class="card-h">样本 m3u8 · ' + esc(h.host) + '</div>'
      + '<div class="probe-kv">' + kv('地址', u) + '</div>'
      + '<div class="ds-acts" style="margin-top:10px">'
      + '<button type="button" class="btn btn-primary btn-sm" id="dsDoProbe">探测特征码</button>'
      + '<a class="btn btn-ghost btn-sm" href="' + esc(u) + '" target="_blank" rel="noopener">新窗口打开</a>'
      + '</div>';
    $('#dsDoProbe').addEventListener('click', () => dsProbeUrl(h, u));
  }

  async function dsProbeHostRun(h) {
    let u = h.sample;
    if (h.kind !== 'm3u8') {
      $('#dsSummary').textContent = '正在解析播放页：' + h.sample;
      const r = await api('/api/discover/resolve', { method: 'POST', body: { page_url: h.sample, proxy_url: discoverProxy() } });
      if (r.code !== 200) { toast(r.msg || '播放页解析失败', 'err'); return; }
      u = r.data.m3u8;
    }
    dsProbeUrl(h, u);
  }

  let dsProbeTimer = null;

  function dsBusy(on) {
    $$('#dsBody button[data-act]').forEach(b => { b.disabled = on; });
    const bp = $('#dsDoProbe');
    if (bp) { bp.disabled = on; }
    const bs = $('#dsScan');
    if (bs) { bs.disabled = on; }
    const b2 = $('#dsProbeBtn');
    if (b2) { b2.disabled = on; b2.textContent = on ? '探测中…' : '探测并填充'; }
    ['#dsSave', '#dsBGo', '#dsTest'].forEach(sel => { const e = $(sel); if (e) e.disabled = on; });
  }

  // dsProbeTarget 探测进度/结果优先渲染在弹窗内，无弹窗时落到页面的结果卡片
  function dsProbeTarget() {
    const pb = $('#dsProbeBox');
    return (dsModalOpen() && pb) ? pb : $('#dsResult');
  }

  function dsTickStart() {
    const t0 = Date.now();
    const el = $('#dsProbeTimer');
    if (!el) return;
    dsProbeTimer = setInterval(() => {
      const s = ((Date.now() - t0) / 1000).toFixed(0);
      el.textContent = '已用时 ' + s + ' 秒（大站可能需 30-60 秒，请勿重复点击）';
    }, 500);
  }

  function dsTickStop() {
    if (dsProbeTimer) { clearInterval(dsProbeTimer); dsProbeTimer = null; }
  }

  async function dsProbeUrl(h, u) {
    dsBusy(true);
    const box = dsProbeTarget();
    const inModal = box && box.id === 'dsProbeBox';
    if (box) {
      box.classList.remove('hidden');
      box.innerHTML = '<div class="card-h">探测中…</div>'
        + '<div class="ds-sum mono">' + esc(u) + '</div>'
        + '<div class="ds-sum" id="dsProbeTimer">已用时 0 秒（大站可能需 30-60 秒，请勿重复点击）</div>';
    }
    dsTickStart();
    let r;
    try {
      r = await api('/api/probe', {
        method: 'POST',
        body: { url: u, index_mode: 'auto', stream_pick: 'first', fixed_sub: '', hash_check: true, proxy_url: discoverProxy() }
      });
    } catch (e) {
      dsTickStop(); dsBusy(false);
      if (box) box.innerHTML = '<div class="card-h">探测失败 · ' + esc(h.host) + '</div>'
        + '<div class="probe-err">' + esc(String(e && e.message || e)) + '</div>';
      return;
    }
    dsTickStop(); dsBusy(false);
    if (r.code !== 200) {
      if (box) box.innerHTML = '<div class="card-h">探测失败 · ' + esc(h.host) + '</div>'
        + '<div class="probe-err">' + esc(r.msg || '未知错误') + '</div>';
      return;
    }
    dsProbe = r.data; dsProbeHost = h.host; dsProbeM3u8 = u;
    dsSel = dsHosts.indexOf(h);
    dsRenderProbeBox(h);
    dsRenderRows();
    if (inModal) dsOpenRule(h);
    toast('探测完成，规则建议已填充', 'ok');
  }

  async function dsSaveSite() {
    const h = dsHosts[dsSel];
    if (!h) { toast('请先选择一个域名', 'err'); return; }
    const btn = $('#dsSave');
    if (btn) { btn.disabled = true; btn.textContent = '保存中…'; }
    try {
      if (dsRuleMode() === 'append') {
        const id = $('#dsAppendSite') && $('#dsAppendSite').value;
        const st = dsSites.filter(x => x.id === id)[0];
        if (!st) { toast('请选择要追加的目标站点', 'err'); return; }
        const pres = (st.url_prefixes || []).slice();
        if (st.url_prefix && pres.indexOf(st.url_prefix) < 0) pres.push(st.url_prefix);
        if (pres.indexOf(h.host) < 0) pres.push(h.host);
        const body = Object.assign({}, st, { url_prefixes: pres });
        const r = await api('/api/sites/' + id, { method: 'PUT', body: body });
        toast(r.msg, r.code === 200 ? 'ok' : 'err');
        if (r.code === 200) {
          h.configured = true; h.site_name = st.name; h.site_id = id;
          dsRenderRows(); dsSyncSel(); dsCloseModal();
        }
        return;
      }
      const s = (dsProbe && dsProbeHost === h.host) ? (dsProbe.suggest || {}) : {};
      const pres = readListRows('#dsPrefixRows', 'ipt-pre');
      if (pres.length === 0) pres.push(h.host);
      const fps = ($('#dsRfp').value || '').split('\n').map(x => x.trim()).filter(Boolean)
        .map(line => line.split(',').map(n => parseInt(n, 10)).filter(n => !isNaN(n)));
      const site = {
        id: '',
        name: ($('#dsRName').value || '').trim() || dsGeneric(h.host),
        parse_type: 'both',
        url_prefixes: pres,
        url_regexes: readListRows('#dsRegexRows', 'ipt-rgx'),
        url_prefix: pres[0] || '',
        cache_subdir: ($('#dsRSub').value || '').trim() || dsGeneric(h.host),
        fingerprint: fps.length ? fps[0] : [],
        fingerprints: fps,
        filter_regex: [],
        index_mode: $('#dsRIndex').value,
        detect_mode: $('#dsRDetect').value,
        stream_pick: $('#dsRPick').value,
        fixed_sub: (s.fixed_sub || ''),
        enabled: true,
        sort: 50,
        remark: '域名发现自动创建（样本 ' + (dsProbeM3u8 || h.sample) + '）'
      };
      const r = await api('/api/sites', { method: 'POST', body: site });
      toast(r.msg, r.code === 200 ? 'ok' : 'err');
      if (r.code === 200) {
        h.configured = true; h.site_name = site.name; h.site_id = r.id || '';
        try { const sr = await api('/api/sites'); dsSites = (sr.data || sr.sites || sr.list || []); } catch (e) { }
        if (!Array.isArray(dsSites)) dsSites = [];
        dsRenderRows(); dsSyncSel(); dsCloseModal();
      }
    } finally {
      if (btn) { btn.disabled = false; btn.textContent = '保存规则'; }
    }
  }

  /* ---------- M3U8 测试页 ---------- */
  // 播放预览状态
  let testData = null;      // 当前解析结果
  let hls = null;           // hls.js 实例
  let hlsPlaylistUrl = '';  // 当前播放列表地址（用于凭证过期探测）
  let hlsMode = 'clean';    // clean=过滤后整片 / origin=原始整片 / single=单片段
  let hlsSegIdx = 0;        // 单片段模式下的段号
  let hlsLoadedType = '';   // 已加载的整片类型 clean/origin，'' 表示未加载（含单片段模式）
  let hitCursor = -1;       // 当前定位的命中区间下标
  let segFilter = 'all';    // 片段列表筛选 all / ad / keep
  let segKw = '';
  let segShown = 200;       // 片段列表已渲染条数
  let originAbs = true;     // 原始文本是否展示补全后的绝对地址
  let segMarks = {};        // 人工标记：段号 -> {index, dur, url, kind, wasAd}
                            // kind: ad=用户判为广告(漏判) / keep=用户判为正片(误判) / suspect=疑似
  let markSaveTimer = null; // 标记自动保存防抖
  let learnCache = null;    // 最近一次学习建议结果（供写入时复用）
  let currentTestUrl = '';  // 当前解析的 m3u8 地址（标记按它归档）
  let learnBound = false;   // 学习弹窗事件是否已绑定

  // 测试页「临时代理」下拉：选项来自当前代理池节点，仅本次测试生效
  async function fillTestProxyOptions() {
    const sel = $('#testProxy');
    if (!sel) return;
    let items = [];
    try {
      const r = await api('/api/proxy-pool');
      if (r && r.code === 200 && r.data && Array.isArray(r.data.items)) items = r.data.items;
    } catch (e) { /* 取不到就只保留「不使用代理」 */ }
    if (!items.length) {
      sel.innerHTML = '<option value="">不使用代理（代理池暂无节点，可到系统设置页刷新）</option>';
      return;
    }
    const opts = items.map(p => {
      const proto = (p.raw || '').split('://')[0] || 'proxy';
      const parts = [p.addr || p.raw, proto];
      if (p.fails) parts.push('连续失败 ' + p.fails + ' 次');
      else if (p.used) parts.push('已用 ' + p.used + ' 次');
      if (p.last_ok) parts.push('最近成功 ' + p.last_ok);
      return `<option value="${esc(p.raw || '')}">${esc(parts.join(' · '))}</option>`;
    });
    sel.innerHTML = '<option value="">不使用代理（仅本次测试生效）</option>' + opts.join('');
  }

  async function renderTest() {
    const box = $('#page-test');
    if (!testChartReady) {
      sitesList = await loadSites();
      testChartReady = true;
    }
    // 不回填历史地址：切换页面后不保留任何解析记录
    const saved = '';
    const savedSite = '';
    const options = sitesList.map(s => `<option value="${esc(s.id)}"${s.id === savedSite ? ' selected' : ''}>${esc(s.name)}</option>`).join('');
    box.innerHTML = `
      <div class="card">
        <div class="card-head"><h3>M3U8 解析测试</h3></div>
        <div class="card-body">
          <form id="testForm" class="test-form">
            <input id="testUrl" type="text" class="url-input" placeholder="输入 m3u8 地址，如 http://vip.dytt.com/xxx/index.m3u8" value="${esc(saved)}">
            <select id="testSite">
              <option value="">（自动匹配站点）</option>
              ${options}
            </select>
            <select id="testProxy" class="url-input" style="max-width:300px;" title="临时代理：仅本次测试生效">
              <option value="">不使用代理（仅本次测试生效）</option>
              <option value="" disabled>加载代理池…</option>
            </select>
            <button type="submit" class="btn btn-primary">开始解析</button>
          </form>
          <div class="empty" id="testEmpty" style="padding:24px 0 8px;"><div class="ico">▶</div>输入地址并选择站点，解析后可直接定位命中区域、播放预览并查看片段</div>
          <div id="testResult"></div>
        </div>
      </div>
    `;
    // 临时代理：从代理池节点下拉选择（仅本次测试生效）
    fillTestProxyOptions();
    $('#testForm').addEventListener('submit', async (e) => {
      e.preventDefault();
      const url = $('#testUrl').value.trim();
      const site = $('#testSite').value;
      try { localStorage.removeItem('dytt_test_url'); localStorage.removeItem('dytt_test_site'); } catch (e) { /* noop */ }
      const btn = e.target.querySelector('button[type=submit]');
      btn.disabled = true; btn.textContent = '解析中...';
      $('#testEmpty').style.display = 'none';
      $('#testResult').innerHTML = '<div class="empty">解析中，请稍候…</div>';
      const r = await api('/api/test', { method: 'POST', body: { url: url, site_id: site, proxy_url: ($('#testProxy') ? $('#testProxy').value.trim() : '') } });
      btn.disabled = false; btn.textContent = '开始解析';
      if (r.code !== 200 || !r.data) {
        $('#testResult').innerHTML = '<div class="empty" style="color:var(--danger)">' + esc(r.msg || '解析失败') + '</div>';
        return;
      }
      hitCursor = -1; segFilter = 'all'; segKw = ''; segShown = 200; originAbs = true; hlsMode = 'clean';
      segMarks = {}; learnCache = null; currentTestUrl = url;
      renderTestResult(r.data);
    });
    // 规则学习弹窗：遮罩/关闭按钮/Esc 关闭（只绑一次）
    if (!learnBound) {
      const mask = $('#learnModal');
      if (mask) {
        const lc = $('#learnModalClose');
        if (lc) lc.addEventListener('click', learnCloseModal);
      }
      document.addEventListener('keydown', (e) => {
        if (e.key === 'Escape') { learnCloseModal(); closeMarkMenu(); }
      });
      learnBound = true;
    }
    // 切换页面后不恢复上次解析结果：进入测试页即为空白状态
  }

  /* ---------- 命中区间工具（hits 为 1-based 段号闭区间） ---------- */
  const inHits = (hits, p) => hits.some(h => p >= h[0] && p <= h[1]);
  const hitLabel = (h) => (h[0] === h[1] ? h[0] : h[0] + '-' + h[1]);

  // 原始文本渲染：可选把相对片段地址补全为绝对地址，命中广告区间标红并带锚点
  function renderOriginLines(d, absMode) {
    const segs = d.segments || [];
    const hits = d.hits || [];
    let k = 0, pos = 0;
    return (d.origin_text || '').split('\n').map(ln => {
      const t = ln.trim();
      if (t.startsWith('#EXTINF')) {
        pos++;
        const cls = inHits(hits, pos) ? 'removed' : '';
        return `<span class="${cls}" id="ext-${pos}">${esc(ln)}</span>`;
      }
      if (t !== '' && !t.startsWith('#')) {
        k++;
        const seg = segs[k - 1];
        const txt = (absMode && seg) ? seg.url : ln;
        const cls = inHits(hits, k) ? 'removed' : '';
        return `<span class="${cls}" id="seg-${k}">${esc(txt)}</span>`;
      }
      return esc(ln);
    }).join('\n');
  }

  function renderTestResult(d) {
    destroyHls(); // 旧 hls 绑定的是即将被替换掉的 video 元素
    resetThumbSystem(); // 清空上一份片段的缩略图缓存/队列/观察器（token 已更新）
    hlsLoadedType = ''; hlsSegIdx = 0;
    testData = d;
    const box = $('#testResult');
    const hits = d.hits || [];
    const hitBtns = hits.map((h, i) =>
      `<button class="hit-btn" data-i="${i}" title="段 ${hitLabel(h)}">${hitLabel(h)}<em>${h[1] - h[0] + 1}段</em></button>`).join('');
    const cleanTxt = '# 广告过滤完成 @ ' + d.cleaned_at + '\n' + (d.clean_text || '');

    box.innerHTML = `
      <div class="test-stats">
        <div class="ts total"><span>总片段</span><b>${d.total_segs}</b></div>
        <div class="ts ads"><span>过滤广告段</span><b>${d.ads_segs}</b></div>
        <div class="ts kept"><span>保留段</span><b>${d.kept_segs}</b></div>
        <div class="ts elapsed"><span>耗时</span><b>${fmtElapsed(d.elapsed_ms)}</b></div>
        ${d.site_name ? '<span class="badge on">' + esc(d.site_name) + '</span>' : ''}
        <span class="badge${d.proxy ? ' on' : ''}" title="${esc(d.proxy || '未启用代理，使用服务器自身 IP 出网')}">${d.proxy ? '代理出网 ' + esc(proxyShort(d.proxy)) : '直连出网'}</span>
        ${d.sub_url ? '<span style="color:var(--muted);font-size:12px;" title="' + esc(d.sub_url) + '">子索引: ' + esc(trimUrl(d.sub_url)) + '</span>' : ''}
      </div>

      ${d.direct ? `<div style="margin:8px 0;padding:8px 10px;border-radius:8px;background:var(--info-bg);border:1px solid var(--border);color:var(--text-2);font-size:13px;line-height:1.6;">
        <b>直链播放</b>：${d.direct_url
          ? '未检测到广告，解析接口将直接输出源站地址，不生成缓存副本 —— <code style="word-break:break-all;">' + esc(d.direct_url) + '</code>'
          : '检测到 ' + (d.ads_segs || 0) + ' 段广告，解析接口输出净化后的本地缓存地址'}
      </div>` : ''}

      <div class="hit-bar${hits.length ? '' : ' no-hit'}">
        ${hits.length ? `
          <span class="hit-label">命中区域</span>
          <span class="hit-list">${hitBtns}</span>
          <span class="hit-nav">
            <button class="btn btn-ghost btn-sm" id="hitPrev">‹ 上一处</button>
            <b id="hitPos">-</b>
            <button class="btn btn-ghost btn-sm" id="hitNext">下一处 ›</button>
          </span>
          <label class="mini-check" title="定位到命中区域时同时播放该段广告，便于确认是否误判">
            <input type="checkbox" id="hitPlay"> 定位并播放
          </label>
        ` : '<span style="color:var(--success);font-size:13px;">未识别到广告</span>'}
      </div>

      <div class="player-box">
        <div class="player-head">
          <div class="src-switch">
            <button class="src-btn${hlsMode === 'clean' ? ' on' : ''}" data-type="clean">播放过滤后</button>
            <button class="src-btn${hlsMode === 'origin' ? ' on' : ''}" data-type="origin">播放原始</button>
            <span class="play-mode" id="playMode">整片 · 过滤后</span>
          </div>
          <div class="player-act">
            <label class="mini-check"><input type="checkbox" id="followPlay" checked> 跟随播放定位片段</label>
            <span class="play-status" id="playStatus">未播放</span>
          </div>
        </div>
        <div class="player-main">
          <div class="player-left">
            <div class="player-video">
              <video id="previewVideo" controls playsinline preload="auto"></video>
            </div>
            <div class="now-seg">
              <div class="ns-t">当前播放片段</div>
              <div class="ns-grid">
                <div class="ns-row"><span class="ns-k">原始段号</span><b id="nsIdx">-</b><span id="nsTag" class="seg-tag">-</span></div>
                <div class="ns-row"><span class="ns-k">时长</span><b id="nsDur">-</b></div>
                <div class="ns-row"><span class="ns-k">起始</span><b id="nsStart">-</b></div>
                <div class="ns-row ns-url-row"><span class="ns-k">地址</span><span class="mono" id="nsUrl" title="">-</span></div>
              </div>
              <div class="ns-act">
                <button class="btn btn-ghost btn-sm" id="nsCopy">复制地址</button>
                <a class="btn btn-ghost btn-sm" id="nsOpen" href="#" target="_blank" rel="noopener">新窗口打开</a>
                <button class="btn btn-ghost btn-sm" id="nsStop">停止</button>
              </div>
            </div>
            <div class="player-tip">提示：源站若限制 Referer，播放走本地代理转发（后台 /api/test/ts），失败请查看浏览器控制台</div>
          </div>
          <div class="seg-panel side">
            <div class="seg-head">
              <strong>片段列表</strong>
              <span class="seg-filter">
                <button class="sf-btn${segFilter === 'all' ? ' on' : ''}" data-f="all">全部 ${d.total_segs}</button>
                <button class="sf-btn${segFilter === 'ad' ? ' on' : ''}" data-f="ad">广告 ${d.ads_segs}</button>
                <button class="sf-btn${segFilter === 'keep' ? ' on' : ''}" data-f="keep">正片 ${d.kept_segs}</button>
                <button class="sf-btn${segFilter === 'marked' ? ' on' : ''}" data-f="marked">已标记 <b id="mkCount">0</b></button>
              </span>
            </div>
            <div class="seg-marktools">
              <span class="mk-label">区间标记</span>
              <input type="text" id="mkFrom" class="mk-input" placeholder="起">
              <span class="mk-sep">-</span>
              <input type="text" id="mkTo" class="mk-input" placeholder="止">
              <select id="mkKind" class="mk-select">
                <option value="ad">广告（漏判）</option>
                <option value="keep">正片（误判）</option>
                <option value="suspect">疑似</option>
              </select>
              <button class="btn btn-ghost btn-sm" id="mkApplyRange">应用</button>
              <button class="btn btn-ghost btn-sm" id="mkClearRange">取消标记</button>
            </div>
            <div class="mark-bar" id="markBar"></div>
            <div class="seg-kw"><input type="text" id="segKw" placeholder="按段号/地址筛选"></div>
            <div class="seg-scroll">
              <table class="seg-tb">
                <thead><tr><th class="c-thumb">画面</th><th class="c-idx">段号</th><th class="c-dur">时长</th><th class="c-type">类型</th><th class="c-start">起始</th><th class="c-uri">地址（补全后）</th><th class="c-mark">标记</th><th class="c-act">操作</th></tr></thead>
                <tbody id="segBody"></tbody>
              </table>
            </div>
            <div class="seg-more" id="segMore"></div>
          </div>
        </div>
      </div>

      <div class="compare">
        <div class="cmp-box">
          <div class="cmp-head">
            <span>原始 M3U8<span class="tag src" style="margin-left:8px;">${d.total_segs} 段</span></span>
            <span class="cmp-tools">
              <button class="btn btn-ghost btn-sm${originAbs ? ' btn-on' : ''}" id="absBtn">${originAbs ? '绝对地址' : '相对地址'}</button>
              <button class="btn btn-ghost btn-sm" id="copyOriginBtn">复制</button>
            </span>
          </div>
          <div class="cmp-body"><pre id="originPre">${renderOriginLines(d, originAbs)}</pre></div>
        </div>
        <div class="cmp-box">
          <div class="cmp-head">
            <span>过滤后 M3U8<span class="tag clean" style="margin-left:8px;">${d.kept_segs} 段</span></span>
            <button class="btn btn-ghost btn-sm" id="copyBtn">复制内容</button>
          </div>
          <div class="cmp-body"><pre id="cleanPre">${esc(cleanTxt)}</pre></div>
        </div>
      </div>

    `;

    bindTestEvents(d);
    initThumbObserver();
    renderSegRows();
    renderMarkBar();
    loadMarks(d); // 载入该影片的历史标记（若标记过）
  }

  function bindTestEvents(d) {
    const box = $('#testResult') || document;
    // 区间标记：批量打标 / 取消
    const mar = box.querySelector('#mkApplyRange');
    if (mar) mar.addEventListener('click', () => {
      const idxs = parseRange();
      if (!idxs.length) { toast('请填写有效段号区间（如 105 或 105-120）', 'err'); return; }
      const kind = (box.querySelector('#mkKind') || {}).value || 'ad';
      toast('已标记 ' + setMarks(idxs, kind) + ' 段为「' + markLabel(kind) + '」', 'ok');
    });
    const mcr = box.querySelector('#mkClearRange');
    if (mcr) mcr.addEventListener('click', () => {
      const idxs = parseRange();
      if (!idxs.length) { toast('请填写有效段号区间', 'err'); return; }
      toast('已取消 ' + setMarks(idxs, '') + ' 段标记', 'ok');
    });

    const hits = d.hits || [];
    $$('.hit-btn').forEach(b => b.addEventListener('click', () => gotoHit(parseInt(b.dataset.i, 10))));
    const hp = $('#hitPrev'), hn = $('#hitNext');
    if (hp) hp.addEventListener('click', () => gotoHit(hitCursor > 0 ? hitCursor - 1 : hits.length - 1));
    if (hn) hn.addEventListener('click', () => gotoHit(hitCursor + 1 >= hits.length ? 0 : hitCursor + 1));

    $$('.src-btn').forEach(b => b.addEventListener('click', () => {
      hlsMode = b.dataset.type;
      $$('.src-btn').forEach(x => x.classList.toggle('on', x === b));
      playWhole(hlsMode);
    }));

    $('#followPlay'); // 状态在 FRAG_CHANGED 中读取
    $('#nsCopy').addEventListener('click', () => {
      const t = ($('#nsUrl').textContent || '').trim();
      if (!t || t === '-') { toast('请先播放，再复制当前片段地址', 'err'); return; }
      copyTextRaw(t);
    });
    $('#nsStop').addEventListener('click', stopPlay);

    $('#absBtn').addEventListener('click', () => {
      originAbs = !originAbs;
      $('#originPre').innerHTML = renderOriginLines(testData, originAbs);
      $('#absBtn').textContent = originAbs ? '绝对地址' : '相对地址';
      $('#absBtn').classList.toggle('btn-on', originAbs);
    });
    $('#copyOriginBtn').addEventListener('click', () => { copyTextRaw($('#originPre').textContent); });
    $('#copyBtn').addEventListener('click', () => { copyTextRaw($('#cleanPre').textContent); });

    $$('.sf-btn').forEach(b => b.addEventListener('click', () => {
      segFilter = b.dataset.f;
      $$('.sf-btn').forEach(x => x.classList.toggle('on', x === b));
      segShown = 200; renderSegRows();
    }));
    $('#segKw').addEventListener('input', (e) => { segKw = e.target.value.trim().toLowerCase(); segShown = 200; renderSegRows(); });
  }

  /* ---------- 片段列表 ---------- */
  function filteredSegs() {
    const segs = (testData.segments || []);
    return segs.filter(s => {
      if (segFilter === 'ad' && !s.ad) return false;
      if (segFilter === 'keep' && s.ad) return false;
      if (segFilter === 'marked' && !segMarks[s.index]) return false;
      if (segKw) {
        const hay = (String(s.index) + ' ' + s.url + ' ' + s.uri).toLowerCase();
        if (hay.indexOf(segKw) < 0) return false;
      }
      return true;
    });
  }

  function renderSegRows() {
    const body = $('#segBody');
    if (!body) return;
    const all = filteredSegs();
    const list = all.slice(0, segShown);
    body.innerHTML = list.map(s => {
      const mk = segMarks[s.index];
      const mkCls = mk ? ' mk-' + mk.kind : '';
      return `
      <tr id="segr-${s.index}" class="${s.ad ? 'is-ad' : ''}${mkCls}">
        <td class="c-thumb">${thumbCellHtml(s)}</td>
        <td class="c-idx">${s.index}</td>
        <td class="c-dur">${s.dur.toFixed(3)}</td>
        <td class="c-type">${s.ad ? '<span class="seg-tag ad">广告</span>' : '<span class="seg-tag ok">正片</span>'}</td>
        <td class="c-start">${fmtTime(s.start)}</td>
        <td class="c-uri mono" title="${esc(s.url)}">${esc(shortUrl(s.url))}</td>
        <td class="c-mark"><button class="mk-btn${mk ? ' on ' + mk.kind : ''}" data-mark="${s.index}" title="标记该片段（人工判定）">${mk ? markLabel(mk.kind) : '标记'}</button></td>
        <td class="c-act">
          <button class="btn btn-ghost btn-xs" data-play="${s.index}">播放</button>
          <button class="btn btn-ghost btn-xs" data-seek="${s.index}">定位</button>
        </td>
      </tr>`; }).join('');
    $$('#segBody button[data-play]').forEach(b => b.addEventListener('click', () => playSeg(parseInt(b.dataset.play, 10))));
    $$('#segBody button[data-seek]').forEach(b => b.addEventListener('click', () => seekSeg(parseInt(b.dataset.seek, 10))));
    $$('#segBody button[data-mark]').forEach(b => b.addEventListener('click', () => openMarkMenu(parseInt(b.dataset.mark, 10), b)));
    bindThumbCells();

    const more = $('#segMore');
    if (!more) return;
    more.innerHTML = all.length > segShown
      ? `<button class="btn btn-ghost btn-sm" id="segMoreBtn">加载更多（已显示 ${segShown} / ${all.length}）</button>`
      : `<span class="seg-count">共 ${all.length} 段</span>`;
    const mb = $('#segMoreBtn');
    if (mb) mb.addEventListener('click', () => { segShown += 300; renderSegRows(); });
  }

  /* ---------- 片段标记：人工判定 → 规则学习 ---------- */
  const MARK_KINDS = { ad: '广告', keep: '正片', suspect: '疑似' };
  const MARK_TIPS = { ad: '漏判（系统没删掉的广告）', keep: '误判（被错删的正片）', suspect: '疑似，待确认' };
  const markLabel = (k) => MARK_KINDS[k] || k;

  function segByIdx(i) { return (testData && testData.segments || []).find(x => x.index === i); }

  // kind 传空串表示取消标记
  function setMarks(indices, kind) {
    let n = 0;
    (indices || []).forEach(i => {
      const s = segByIdx(i);
      if (!s) return;
      if (kind === '') {
        if (segMarks[i]) { delete segMarks[i]; n++; }
        return;
      }
      // 字段名与后端 store.MarkSeg 保持一致
      segMarks[i] = { index: i, dur: s.dur, url: s.url, kind: kind, was_ad: !!s.ad };
      n++;
    });
    if (n) { renderSegRows(); renderMarkBar(); saveMarks(); }
    return n;
  }

  // 区间输入框解析：支持 "105" 或 "105-120"
  function parseRange() {
    const ef = $('#mkFrom'), et = $('#mkTo');
    if (!ef) return [];
    const a = parseInt(ef.value, 10);
    if (!a) return [];
    const b = parseInt((et && et.value) || '', 10);
    const lo = b ? Math.min(a, b) : a, hi = b ? Math.max(a, b) : a;
    if (hi - lo > 2000) return [];
    const out = [];
    for (let i = lo; i <= hi; i++) out.push(i);
    return out;
  }

  /* 单行标记菜单（浮层） */
  let markMenuEl = null;
  function onDocClickMark(e) { if (markMenuEl && !markMenuEl.contains(e.target)) closeMarkMenu(); }
  function closeMarkMenu() {
    if (!markMenuEl) return;
    markMenuEl.remove();
    markMenuEl = null;
    document.removeEventListener('click', onDocClickMark);
  }
  function openMarkMenu(idx, btn) {
    closeMarkMenu();
    const menu = document.createElement('div');
    menu.className = 'mark-menu';
    menu.innerHTML = ['ad', 'keep', 'suspect'].map(k =>
      `<button type="button" data-k="${k}"><b>${markLabel(k)}</b><em>${MARK_TIPS[k]}</em></button>`).join('') +
      `<button type="button" data-k="" class="mk-clear">取消标记</button>`;
    document.body.appendChild(menu);
    // 先隐藏实测菜单尺寸，避免闪烁
    menu.style.visibility = 'hidden';
    menu.style.position = 'fixed';
    menu.style.left = '-9999px';
    menu.style.top = '-9999px';
    const mw = menu.offsetWidth, mh = menu.offsetHeight;
    const r = btn.getBoundingClientRect();
    const GAP = 6;
    // 垂直：默认按钮下方；下方放不下翻转到上方；仍不够则贴视口底部
    let top = r.bottom + GAP;
    if (top + mh > window.innerHeight - GAP) {
      top = r.top - mh - GAP;
      if (top < GAP) top = Math.max(GAP, window.innerHeight - mh - GAP);
    }
    // 水平：默认左对齐按钮；超出右边界改为右对齐按钮；最后夹取在视口内
    let left = Math.min(r.left, window.innerWidth - mw - GAP);
    left = Math.max(GAP, left);
    menu.style.visibility = '';
    menu.style.top = top + 'px';
    menu.style.left = left + 'px';
    markMenuEl = menu;
    menu.querySelectorAll('button').forEach(b => b.addEventListener('click', (e) => {
      e.stopPropagation();
      setMarks([idx], b.dataset.k);
      closeMarkMenu();
    }));
    setTimeout(() => document.addEventListener('click', onDocClickMark), 0);
  }

  /* 标记汇总条 */
  function renderMarkBar() {
    const bar = $('#markBar');
    if (!bar) return;
    const list = Object.keys(segMarks).map(k => segMarks[k]).sort((a, b) => a.index - b.index);
    const c = $('#mkCount');
    if (c) c.textContent = list.length;
    if (!list.length) {
      bar.className = 'mark-bar is-empty';
      bar.innerHTML = '<span class="mk-tip">未标记：播放定位到广告段后点「标记」，或用「区间标记」批量打标，再点「规则学习建议」生成规则</span>';
      return;
    }
    const cnt = { ad: 0, keep: 0, suspect: 0 };
    list.forEach(m => { cnt[m.kind] = (cnt[m.kind] || 0) + 1; });
    // 合并连续同类型区间
    const spans = [];
    list.forEach(m => {
      const last = spans[spans.length - 1];
      if (last && last.kind === m.kind && m.index === last.to + 1) { last.to = m.index; return; }
      spans.push({ from: m.index, to: m.index, kind: m.kind });
    });
    const spanTxt = spans.map(s =>
      `<span class="mk-span ${s.kind}">${s.from}${s.to !== s.from ? '-' + s.to : ''}<em>${markLabel(s.kind)}</em></span>`).join('');
    bar.className = 'mark-bar';
    bar.innerHTML = `
      <div class="mk-sum"><span class="mk-title">已标记 ${list.length} 段</span>${spanTxt}</div>
      <div class="mk-cnt">漏判广告 <b>${cnt.ad || 0}</b> · 误判正片 <b>${cnt.keep || 0}</b> · 疑似 <b>${cnt.suspect || 0}</b></div>
      <div class="mk-act">
        <button class="btn btn-primary btn-sm" id="mkLearn">规则学习建议</button>
        <button class="btn btn-ghost btn-sm" id="mkExport">导出标记</button>
        <button class="btn btn-ghost btn-sm" id="mkClearAll">清空</button>
      </div>`;
    $('#mkLearn').addEventListener('click', openLearnModal);
    $('#mkExport').addEventListener('click', () => copyTextRaw(JSON.stringify(marksPayload(), null, 2)));
    $('#mkClearAll').addEventListener('click', async () => {
      if (!confirm('清空本次解析的全部标记？（已保存的标记也会一并删除）')) return;
      clearTimeout(markSaveTimer);
      segMarks = {};
      renderSegRows(); renderMarkBar();
      await api('/api/test/marks?site_id=' + encodeURIComponent((testData && testData.site_id) || '') +
        '&url=' + encodeURIComponent(currentTestUrl), { method: 'DELETE' });
    });
  }

  /* 标记的保存 / 载入（按站点 + 地址归档到 data/marks.json） */
  function marksPayload() {
    return {
      site_id: (testData && testData.site_id) || '',
      site_name: (testData && testData.site_name) || '',
      url: currentTestUrl,
      sub_url: (testData && testData.sub_url) || '',
      total_segs: (testData && testData.total_segs) || 0,
      hits: (testData && testData.hits) || [],
      segs: Object.keys(segMarks).map(k => segMarks[k]).sort((a, b) => a.index - b.index)
    };
  }

  function saveMarks() {
    if (!currentTestUrl) return;
    clearTimeout(markSaveTimer);
    markSaveTimer = setTimeout(async () => {
      const r = await api('/api/test/marks', { method: 'POST', body: marksPayload() });
      if (r.code !== 200) toast('标记保存失败：' + (r.msg || ''), 'err');
    }, 600);
  }

  async function loadMarks(d) {
    if (!currentTestUrl) return;
    const r = await api('/api/test/marks?site_id=' + encodeURIComponent((d && d.site_id) || '') +
      '&url=' + encodeURIComponent(currentTestUrl));
    if (r.code !== 200 || !r.data || !r.data.segs) return;
    segMarks = {};
    r.data.segs.forEach(m => { if (m.index) segMarks[m.index] = m; });
    renderSegRows();
    renderMarkBar();
  }

  /* ---------- 规则学习弹窗 ---------- */
  function learnModalOpen(title, bodyHTML, footHTML) {
    $('#learnModalTitle').textContent = title;
    $('#learnModalBody').innerHTML = bodyHTML || '';
    $('#learnModalFoot').innerHTML = footHTML || '';
    $('#learnModal').classList.remove('hidden');
  }
  function learnCloseModal() { const m = $('#learnModal'); if (m) m.classList.add('hidden'); }

  async function openLearnModal() {
    const list = Object.keys(segMarks).map(k => segMarks[k]).sort((a, b) => a.index - b.index);
    if (!list.length) { toast('请先标记片段', 'err'); return; }
    learnModalOpen('规则学习建议', '<div class="ds-loading">正在分析标记…</div>', '');
    const r = await api('/api/learn/suggest', {
      method: 'POST', body: { site_id: (testData && testData.site_id) || '', segs: list }
    });
    if (r.code !== 200) {
      learnModalOpen('规则学习建议', '<div class="empty" style="color:var(--danger)">' + esc(r.msg || '分析失败') + '</div>', '');
      return;
    }
    learnCache = r.data;
    learnCache.segs = list;
    renderLearn(r.data);
  }

  // 可选检测方式（与站点规则页 f-detectmode 保持一致）
  const LEARN_MODES = [
    { v: 'sequence', t: 'sequence（TS 序列连贯性，推荐）' },
    { v: 'blockcut', t: 'blockcut（广告块尾部截断）' },
    { v: 'slicebatch', t: 'slicebatch（切片批次 mtime）' },
    { v: 'shortblock', t: 'shortblock（孤立短块）' },
    { v: 'fingerprint', t: 'fingerprint（时长/路径特征为主判据）' },
    { v: 'both', t: 'both（指纹+哈希）' },
    { v: 'hash', t: 'hash（内容哈希）' },
  ];

  function renderLearn(d) {
    const st = d.stats || {};
    const spans = d.spans || [];
    const rows = spans.map(s => `
      <tr>
        <td class="sp-range">${s.from}${s.to !== s.from ? '-' + s.to : ''}</td>
        <td><span class="mk-span ${s.kind}">${markLabel(s.kind)}<em>${s.segs.length}段</em></span></td>
        <td>${s.total.toFixed(2)}s</td>
        <td class="mono fp-str">${esc(JSON.stringify(s.durs))}</td>
      </tr>`).join('');
    const fps = d.new_fps || [];
    const fpBlocks = fps.length ? fps.map((fp, i) => `
      <div class="fp-card">
        <div class="fp-h">候选指纹 ${i + 1} · ${fp.length} 段 · <span class="tag-new">新增</span></div>
        <div class="fp-body mono">${esc(JSON.stringify(fp))}</div>
      </div>`).join('') : '<div class="fp-empty">没有可新增的指纹：标记区间与规则已有指纹重复，或标记类型不是「广告」</div>';
    const dupNote = (d.dup_fps || []).length
      ? `<div class="fp-note">${d.dup_fps.length} 组与规则已有指纹重复（容差 ${d.tolerance}s），将跳过</div>` : '';
    const noSite = d.site_id ? '' :
      '<div class="fp-note warn">本次解析未匹配到站点规则，无法一键写入；可复制指纹后到「站点规则」页手动添加。</div>';

    // 指纹与正片同构预警：仅按时长滑动匹配会把正片成片误删（如正片大量 4.0s 段而广告也是 4.0s 段）
    const ld = d.low_distinct || [];
    const ldBlock = ld.length ? `<div class="learn-block" style="border-color:var(--danger);">
        <div class="learn-h">⚠ 指纹与正片同构 · 误删风险提示</div>
        ${ld.map((x, i) => `<div class="fp-card">
          <div class="fp-h">候选指纹 ${i + 1} · ${(x.fp || []).length} 段 · <span class="tag-new">同构</span></div>
          <div class="fp-body mono">${esc(JSON.stringify(x.fp))}</div>
          <div class="fp-note warn">${esc(x.reason || '')}</div>
        </div>`).join('')}
        <div class="learn-tip">检测端已内置「广告边界确认」：此类指纹只会在紧邻时长突变或 DISCONTINUITY 的插播边界处命中，正片成片不会再被误删。若仍误删，建议改用「哈希采样」学习内容指纹，识别更稳定。</div>
      </div>` : '';

    // 正片保护指纹：命中即禁止剔除
    const kpfps = d.keep_fps || [];
    const kpBlock = kpfps.length ? `<div class="learn-block" id="lnKeepBlock">
        <div class="learn-h">正片保护指纹 → keep_fingerprints</div>
        <div class="learn-tip">写入后，命中该时长序列的片段一律不再被剔除，用于杜绝同类正片被反复误删。默认全选：</div>
        ${kpfps.map((fp, i) => `<div class="fp-card">
          <div class="fp-h"><input type="checkbox" class="kp-ck" data-i="${i}" checked> 正片指纹 ${i + 1} · ${fp.length} 段 · 保护</div>
          <div class="fp-body mono">${esc(JSON.stringify(fp))}</div>
        </div>`).join('')}
        ${(d.keep_exist || []).length ? `<div class="fp-note">规则中已有保护指纹 ${d.keep_exist.length} 组：<span class="mono">${esc(JSON.stringify(d.keep_exist))}</span></div>` : ''}
      </div>` : '';

    // 误判修正：正片区间内出现的既有广告指纹，勾选后从规则中移除
    const rmfps = d.remove_fps || [];
    const rmBlock = rmfps.length ? `<div class="learn-block" id="lnRemoveBlock">
        <div class="learn-h">误判修正 · 从规则中移除</div>
        <div class="learn-tip">以下既有广告指纹出现在你标记的「正片」区间内，是正片被误删的疑似来源，默认全选，写入时即从站点规则删除：</div>
        ${rmfps.map((r, i) => `<div class="fp-card">
          <div class="fp-h"><input type="checkbox" class="rm-ck" data-i="${i}" checked> 误判指纹 ${i + 1} · ${(r.fp || []).length} 段 · <span class="tag-new">移除</span></div>
          <div class="fp-body mono">${esc(JSON.stringify(r.fp))}</div>
          <div class="fp-note">${esc(r.reason || '')}</div>
        </div>`).join('')}
      </div>` : '';

    // 与站点规则联动：检测方式提示 + 一键切换
    const linkBlock = `<div class="learn-block">
        <div class="learn-h">与站点规则联动</div>
        <div class="fp-note${d.structural ? ' warn' : ''}">${esc(d.mode_hint || '')}</div>
        <div class="learn-tip" style="display:flex;align-items:center;gap:8px;flex-wrap:wrap;">写入后检测方式：
          <select id="lnMode" style="padding:5px 8px;border-radius:6px;border:1px solid var(--line);background:var(--panel);color:var(--text);">
            <option value="">保持当前（${esc(d.detect_mode || '未设置')}）</option>
            ${LEARN_MODES.map(m => `<option value="${m.v}">${m.t}</option>`).join('')}
          </select>
        </div>
        <div class="learn-tip">结构型模式（sequence / blockcut / slicebatch / shortblock）下，时长指纹只作补充判据兜底并受 30% 安全闸门约束；若主检测长期漏判，可切换为 fingerprint 让指纹成为主判据。</div>
      </div>`;

    const writable = (fps.length + kpfps.length + rmfps.length) > 0;

    learnModalOpen('规则学习建议 · ' + (d.site_name || '未匹配站点'), `
      <div class="learn-block">
        <div class="learn-stats">
          <span>标记 <b>${st.marked || 0}</b> 段</span>
          <span class="warn">漏判广告 <b>${st.missed || 0}</b></span>
          <span class="info">误判正片 <b>${st.wrong || 0}</b></span>
          <span>疑似 <b>${st.suspect || 0}</b></span>
          <span class="muted">当前检测方式：${esc(d.detect_mode || '-')}</span>
        </div>
        <table class="learn-tb">
          <thead><tr><th>区间</th><th>人工判定</th><th>总时长</th><th>时长序列（指纹）</th></tr></thead>
          <tbody>${rows}</tbody>
        </table>
      </div>
      <div class="learn-block">
        <div class="learn-h">候选时长指纹 → 站点规则 fingerprints</div>
        ${fpBlocks}${dupNote}${ldBlock}${noSite}
      </div>
      ${kpBlock}${rmBlock}${linkBlock}
      <div class="learn-block">
        <div class="learn-h">内容哈希（可选，更强但需抓取源站）</div>
        <div class="learn-tip">按「广告」标记段抓取 TS 前 256KB 计算 SHA256 → 写入 ads_hashes，可精确剔除同一广告素材。</div>
        <div id="hashResult"></div>
      </div>
    `, `
      <button class="btn btn-ghost btn-sm" id="lnHash">生成内容哈希</button>
      <button class="btn btn-ghost btn-sm" id="lnCopy">复制指纹 JSON</button>
      <span style="flex:1"></span>
      <button class="btn btn-ghost btn-sm" id="lnClose">关闭</button>
      <button class="btn btn-primary btn-sm" id="lnApply"${(d.site_id && writable) ? '' : ' disabled'}>写入站点规则</button>
    `);

    $('#lnClose').addEventListener('click', learnCloseModal);
    $('#lnCopy').addEventListener('click', () => copyTextRaw(JSON.stringify(fps)));
    $('#lnHash').addEventListener('click', buildHashes);
    const ap = $('#lnApply');
    if (ap) ap.addEventListener('click', applyLearn);
  }

  async function buildHashes() {
    const list = ((learnCache && learnCache.segs) || []).filter(s => s.kind === 'ad');
    if (!list.length) { toast('请先标记「广告」类型的片段', 'err'); return; }
    $('#hashResult').innerHTML = '<div class="ds-loading">正在抓取 ' + list.length + ' 个片段的采样内容…</div>';
    const r = await api('/api/learn/hash', { method: 'POST', body: { segs: list } });
    const box = $('#hashResult');
    if (!box) return;
    if (r.code !== 200) { box.innerHTML = '<div class="fp-note warn">' + esc(r.msg || '采样失败') + '</div>'; return; }
    learnCache.hashGroup = r.data.group;
    box.innerHTML = `
      <div class="fp-card">
        <div class="fp-h">哈希组 · ${r.data.count} 段 · 采样 ${Math.round(r.data.sample_size / 1024)}KB${r.data.failed && r.data.failed.length ? ' · 失败段：' + r.data.failed.join(',') : ''}</div>
        <div class="fp-body mono">${esc(JSON.stringify(r.data.group))}</div>
      </div>`;
    const ap = $('#lnApply');
    if (ap) ap.disabled = false;
  }

  async function applyLearn() {
    const d = learnCache;
    if (!d) return;
    const fps = d.new_fps || [];
    const hashes = d.hashGroup ? [d.hashGroup] : [];
    const modeEl = $('#lnMode');
    const mode = modeEl ? modeEl.value : '';
    const kpArr = $$('#lnKeepBlock .kp-ck:checked').map(x => (d.keep_fps || [])[parseInt(x.dataset.i, 10)]).filter(Boolean);
    const rmArr = $$('#lnRemoveBlock .rm-ck:checked').map(x => ((d.remove_fps || [])[parseInt(x.dataset.i, 10)] || {}).fp).filter(x => x && x.length);
    if (!fps.length && !hashes.length && !kpArr.length && !rmArr.length && !mode) {
      toast('没有勾选任何要写入的内容', 'err'); return;
    }
    let tip = '将写入站点「' + (d.site_name || d.site_id) + '」：\n指纹 ' + fps.length + ' 组';
    if (kpArr.length) tip += '、保护指纹 ' + kpArr.length + ' 组';
    if (rmArr.length) tip += '、移除误判指纹 ' + rmArr.length + ' 组';
    tip += '、内容哈希 ' + hashes.length + ' 组';
    if (mode) tip += '\n检测方式切换为：' + mode;
    tip += '\n写入前自动备份 rules.json，确认继续？';
    if (!confirm(tip)) return;
    const r = await api('/api/learn/apply', {
      method: 'POST', body: {
        site_id: d.site_id, fingerprints: fps, hashes: hashes,
        detect_mode: mode, remove_fingerprints: rmArr, keep_fingerprints: kpArr,
        note: (currentTestUrl || '').slice(0, 60)
      }
    });
    if (r.code !== 200) { toast(r.msg || '写入失败', 'err'); return; }
    toast(r.msg || '已写入规则并重载', 'ok');
    learnCloseModal();
    // 联动：刷新站点规则缓存，使「站点规则」页立即看到最新指纹与检测方式
    try { sitesList = await loadSites(); } catch (e) { /* 忽略，不影响主流程 */ }
  }

  function shortUrl(u) {
    if (!u) return '';
    if (u.length <= 78) return u;
    const i = u.indexOf('?');
    const main = i > 0 ? u.slice(0, i) : u;
    if (main.length <= 78) return u;
    return main.slice(0, 34) + '…' + main.slice(-40);
  }
  function fmtTime(sec) {
    if (sec == null) return '-';
    const s = Math.floor(sec), m = Math.floor(s / 60), h = Math.floor(m / 60);
    const p = x => String(x).padStart(2, '0');
    return (h > 0 ? h + ':' : '') + p(m % 60) + ':' + p(s % 60);
  }

  /* ---------- 命中区域定位 ---------- */
  function gotoHit(i) {
    const hits = testData.hits || [];
    if (!hits.length) return;
    if (i < 0) i = hits.length - 1;
    if (i >= hits.length) i = 0;
    hitCursor = i;
    const h = hits[i];
    $$('.hit-btn').forEach(b => b.classList.toggle('on', parseInt(b.dataset.i, 10) === i));
    const pos = $('#hitPos'); if (pos) pos.textContent = (i + 1) + '/' + hits.length;
    const el = $('#seg-' + h[0]) || $('#ext-' + h[0]);
    if (el) {
      const prev = $$('.cmp-body .hit-flash');
      prev.forEach(x => x.classList.remove('hit-flash'));
      el.classList.add('hit-flash');
      el.scrollIntoView({ block: 'center' });
    }
    const cb = $('#hitPlay');
    if (cb && cb.checked) playSeg(h[0]);
  }

  /* ---------- 播放控制 ---------- */
  function setPlayStatus(t) { const el = $('#playStatus'); if (el) el.textContent = t; }

  function destroyHls() {
    if (hls) { try { hls.destroy(); } catch (e) { /* noop */ } hls = null; }
  }

  function stopPlay() {
    const v = $('#previewVideo');
    if (v) { v.pause(); v.removeAttribute('src'); v.load(); }
    destroyHls();
    hlsLoadedType = ''; hlsSegIdx = 0;
    setPlayStatus('已停止');
  }

  // 离开测试页：停止播放、销毁 hls、清空解析结果与标记缓存
  testResetHook = function () {
    if (!testData) return;
    stopPlay();
    resetThumbSystem();
    testData = null;
    segMarks = {};
    learnCache = null;
    currentTestUrl = '';
    hlsPlaylistUrl = '';
    hitCursor = -1; segFilter = 'all'; segKw = ''; segShown = 200; originAbs = true; hlsMode = 'clean';
    clearTimeout(markSaveTimer);
    try { localStorage.removeItem('dytt_test_url'); localStorage.removeItem('dytt_test_site'); } catch (e) { /* noop */ }
  };

  /* ---------- 片段缩略图（纯前端截帧，懒加载 + 串行队列） ----------
     复用单个隐藏 video + 截帧专用 hls 实例，逐个加载单段 m3u8（/api/test/segment），
     seek 到片段开头后 canvas 截帧生成 dataURL；成功结果内存缓存，失败可点击重试。
     广告行（s.ad）缩略图带红色描边 + 角标，便于肉眼分辨插入广告。
  ------------------------------------------------------------------- */
  const segThumbCache = {};    // 段号 -> dataURL(成功) | 'failed' | 'loading'
  const segThumbPending = {};  // 段号 -> true（排队中/处理中，防重复入队）
  const segThumbQueue = [];    // 待截帧段号 FIFO
  let segThumbBusy = false;    // 串行队列是否正在消费
  let thumbVideo = null;       // 复用的隐藏 video 元素（与主播放器 previewVideo 完全隔离）
  let thumbHls = null;         // 截帧专用 hls 实例（与主播放器 hls 完全隔离）
  let thumbObserver = null;    // 懒加载观察器（进入视口才入队）

  function thumbCellHtml(s) {
    const idx = s.index;
    const c = segThumbCache[idx];
    const adCls = s.ad ? ' is-ad' : '';
    if (c && c !== 'failed' && c !== 'loading') {
      return `<div class="seg-thumb${adCls}" title="段 ${idx} 缩略图"><img src="${c}" alt="段 ${idx}"></div>`;
    }
    if (c === 'failed') {
      return `<div class="seg-thumb${adCls}" data-idx="${idx}" title="点击重新加载缩略图"><span class="th-fail">加载失败，点击重试</span></div>`;
    }
    if (c === 'loading' || segThumbPending[idx]) {
      return `<div class="seg-thumb${adCls}" data-idx="${idx}"><span class="th-loading">加载中…</span></div>`;
    }
    return `<div class="seg-thumb${adCls}" data-idx="${idx}" title="点击加载缩略图"><span class="th-tip">▶ 缩略图</span></div>`;
  }

  // 列表（重新）渲染后绑定缩略图单元格：点击立即加载 + 进入视口自动加载
  function bindThumbCells() {
    $$('#segBody .seg-thumb').forEach(cell => {
      const idx = parseInt(cell.dataset.idx, 10);
      if (!idx) return;
      cell.addEventListener('click', (e) => {
        e.stopPropagation();
        enqueueThumb(idx);
      });
      if (thumbObserver) thumbObserver.observe(cell);
    });
  }

  // 懒加载观察器：以片段列表滚动容器为根，提前 150px 预加载
  function initThumbObserver() {
    if (thumbObserver) { try { thumbObserver.disconnect(); } catch (e) { /* noop */ } thumbObserver = null; }
    if (typeof IntersectionObserver === 'undefined') return;
    const root = $('.seg-scroll') || null;
    thumbObserver = new IntersectionObserver((entries) => {
      entries.forEach(en => {
        if (!en.isIntersecting) return;
        const cell = en.target;
        const idx = parseInt(cell.dataset.idx, 10);
        if (thumbObserver) thumbObserver.unobserve(cell);
        if (idx) enqueueThumb(idx);
      });
    }, { root: root, rootMargin: '150px 0px' });
  }

  // 入队（幂等）：成功缓存直接跳过，失败允许重试
  function enqueueThumb(idx) {
    if (!testData || !testData.token) return;
    const c = segThumbCache[idx];
    if (c && c !== 'failed') return;   // 已有成功缩略图
    if (segThumbPending[idx]) return;  // 已在队列/处理中
    segThumbPending[idx] = true;
    if (c !== 'failed') segThumbCache[idx] = 'loading';
    segThumbQueue.push(idx);
    // 行内占位立即切换为「加载中」（若该行已渲染）
    const cell = $('#segr-' + idx + ' .seg-thumb');
    if (cell) {
      if (thumbObserver) thumbObserver.unobserve(cell);
      cell.innerHTML = '<span class="th-loading">加载中…</span>';
    }
    pumpThumbQueue();
  }

  // 串行消费队列：每次只截一帧，完成后再处理下一个
  async function pumpThumbQueue() {
    if (segThumbBusy) return;
    segThumbBusy = true;
    while (segThumbQueue.length) {
      const idx = segThumbQueue.shift();
      await captureSegThumb(idx);
      delete segThumbPending[idx];
    }
    segThumbBusy = false;
  }

  function ensureThumbVideo() {
    if (!thumbVideo) {
      thumbVideo = document.createElement('video');
      thumbVideo.muted = true;
      thumbVideo.playsInline = true;
      thumbVideo.preload = 'auto';
      thumbVideo.crossOrigin = 'anonymous';
      thumbVideo.style.cssText = 'position:fixed;left:-99999px;top:0;width:320px;height:180px;opacity:0.01;pointer-events:none;';
      document.body.appendChild(thumbVideo);
    }
    return thumbVideo;
  }

  function destroyThumbHls() {
    if (thumbHls) { try { thumbHls.destroy(); } catch (e) { /* noop */ } thumbHls = null; }
    if (thumbVideo) { try { thumbVideo.removeAttribute('src'); thumbVideo.load(); } catch (e) { /* noop */ } }
  }

  // 单行缩略图刷新（截帧完成/失败后调用，行可能因列表重渲染不存在则忽略）
  function renderThumbCell(idx) {
    const cell = $('#segr-' + idx + ' .seg-thumb');
    if (!cell) return;
    const seg = segByIdx(idx);
    cell.className = 'seg-thumb' + ((seg && seg.ad) ? ' is-ad' : '');
    const c = segThumbCache[idx];
    if (c && c !== 'failed' && c !== 'loading') {
      cell.innerHTML = '<img src="' + c + '" alt="段 ' + idx + '">';
      cell.title = '段 ' + idx + ' 缩略图';
    } else if (c === 'failed') {
      cell.innerHTML = '<span class="th-fail">加载失败，点击重试</span>';
      cell.title = '点击重新加载缩略图';
    } else {
      cell.innerHTML = '<span class="th-loading">加载中…</span>';
    }
  }

  // 截帧单段：加载单段 m3u8 → seek 片段开头 → canvas 截帧 → 释放
  async function captureSegThumb(idx) {
    const seg = segByIdx(idx);
    if (!seg || !testData || !testData.token) {
      segThumbCache[idx] = 'failed';
      renderThumbCell(idx);
      return;
    }
    const v = ensureThumbVideo();
    destroyThumbHls();
    const H = window.Hls;
    if (!H || !H.isSupported()) {
      segThumbCache[idx] = 'failed';
      renderThumbCell(idx);
      return;
    }

    const url = adminPath() + '/api/test/segment?token=' + encodeURIComponent(testData.token) + '&i=' + idx;
    const ok = await new Promise((resolve) => {
      let done = false;
      const onSeeked = () => tryCapture();
      const onLoaded = () => setTimeout(tryCapture, 600); // seeked 未触发时兜底
      const fin = (good) => {
        if (done) return;
        done = true;
        clearTimeout(timer);
        v.removeEventListener('seeked', onSeeked);
        v.removeEventListener('loadeddata', onLoaded);
        resolve(good);
      };
      const timer = setTimeout(() => fin(false), 15000); // 整段截帧总超时
      let captured = false;

      const tryCapture = () => {
        if (captured) return;
        captured = true;
        requestAnimationFrame(() => requestAnimationFrame(() => {
          try {
            const cw = 160, ch = 90;
            const cv = document.createElement('canvas');
            cv.width = cw; cv.height = ch;
            const ctx = cv.getContext('2d');
            const vw = v.videoWidth || 640, vh = v.videoHeight || 360;
            const scale = Math.max(cw / vw, ch / vh);
            const dw = vw * scale, dh = vh * scale;
            ctx.drawImage(v, (cw - dw) / 2, (ch - dh) / 2, dw, dh);
            segThumbCache[idx] = cv.toDataURL('image/jpeg', 0.6);
            fin(true);
          } catch (e) { fin(false); }
        }));
      };

      thumbHls = new H({
        enableWorker: true,
        backBufferLength: 5,
        manifestLoadingMaxRetry: 1,
        levelLoadingMaxRetry: 1,
        fragLoadingMaxRetry: 1,
        fragLoadingRetryDelay: 300
      });
      thumbHls.on(H.Events.MANIFEST_PARSED, () => {
        // seek 到片段开头附近（片段过短时按时长的 1/4 截取，避免越界）
        const t = Math.min(0.05, Math.max(0, (seg.dur || 1) * 0.25));
        try { v.currentTime = t; } catch (e) { fin(false); return; }
        v.play().catch(() => { /* 静音自动播放被拦截不影响 seek 截帧 */ });
        v.addEventListener('seeked', onSeeked);
        v.addEventListener('loadeddata', onLoaded);
      });
      thumbHls.on(H.Events.ERROR, (e, data) => {
        if (data.fatal) { clearTimeout(timer); fin(false); }
        // 非致命错误（如片段加载重试）等待 hls 自行恢复
      });
      thumbHls.loadSource(url);
      thumbHls.attachMedia(v);
    });

    destroyThumbHls();
    if (!ok && !(segThumbCache[idx] && segThumbCache[idx] !== 'failed' && segThumbCache[idx] !== 'loading')) {
      segThumbCache[idx] = 'failed';
    }
    renderThumbCell(idx);
  }

  // 整体清理：新解析 / 离开测试页时释放全部缩略图资源
  function resetThumbSystem() {
    if (thumbObserver) { try { thumbObserver.disconnect(); } catch (e) { /* noop */ } thumbObserver = null; }
    segThumbQueue.length = 0;
    Object.keys(segThumbPending).forEach(k => delete segThumbPending[k]);
    Object.keys(segThumbCache).forEach(k => delete segThumbCache[k]);
    segThumbBusy = false;
    destroyThumbHls();
    if (thumbVideo) {
      try { thumbVideo.removeAttribute('src'); thumbVideo.load(); } catch (e) { /* noop */ }
      try { if (thumbVideo.parentNode) thumbVideo.parentNode.removeChild(thumbVideo); } catch (e) { /* noop */ }
      thumbVideo = null;
    }
  }

  // 检测测试凭证（preview token，30 分钟有效）是否已过期
  let expireCheckedAt = 0;
  function probePreviewExpired() {
    if (!hlsPlaylistUrl) return;
    const now = Date.now();
    if (now - expireCheckedAt < 3000) return;
    expireCheckedAt = now;
    fetch(hlsPlaylistUrl, { cache: 'no-store' }).then(r => r.text().then(txt => {
      const expired = r.status === 404 ||
        /预览已过期|请重新解析|preview expired/i.test(txt) ||
        /"code"\s*:\s*403/.test(txt);
      if (!expired) return;
      stopPlay();
      setPlayStatus('测试凭证已过期（30 分钟），请重新解析该链接');
      toast('测试凭证已过期，请重新点「测试解析」后再播放', 'err');
    })).catch(() => { /* 探测失败不干扰正常重试 */ });
  }

  document.addEventListener('visibilitychange', () => {
    const v = $('#previewVideo');
    if (document.hidden && v && !v.paused) {
      toast('页面已切到后台，浏览器会限制播放，切回前台后自动继续', 'err');
    }
  });

  function loadHls(url, onReady) {
    const v = $('#previewVideo');
    if (!v) return;
    hlsPlaylistUrl = url;
    destroyHls();
    setPlayStatus('加载中…');
    const H = window.Hls;
    if (H && H.isSupported()) {
      hls = new H({
        enableWorker: true,
        backBufferLength: 30,
        manifestLoadingMaxRetry: 2,
        levelLoadingMaxRetry: 3,
        fragLoadingMaxRetry: 4,
        fragLoadingRetryDelay: 500
      });
      hls.on(H.Events.MANIFEST_PARSED, () => {
        setPlayStatus('就绪');
        if (onReady) onReady();
        v.play().catch(() => { /* 自动播放被拦截时忽略 */ });
      });
      hls.on(H.Events.FRAG_CHANGED, (e, data) => {
        const m = /[?&]i=(\d+)/.exec((data.frag && data.frag.url) || '');
        if (m) updateNowSeg(parseInt(m[1], 10));
      });
      hls.on(H.Events.ERROR, (e, data) => {
        probePreviewExpired();
        if (!data.fatal) {
          if (data.details === H.ErrorDetails.BUFFER_STALLED_ERROR) setPlayStatus('缓冲中…');
          else if (data.details === H.ErrorDetails.FRAG_LOAD_ERROR ||
                   data.details === H.ErrorDetails.FRAG_LOAD_TIMEOUT) setPlayStatus('片段加载重试中…');
          return;
        }
        // 致命错误：优先尝试恢复，仍失败则提示
        if (data.type === H.ErrorTypes.NETWORK_ERROR) {
          setPlayStatus('网络错误，尝试恢复…');
          hls.startLoad();
          return;
        }
        if (data.type === H.ErrorTypes.MEDIA_ERROR) {
          setPlayStatus('媒体错误，尝试恢复…');
          hls.recoverMediaError();
          return;
        }
        setPlayStatus('播放失败：' + (data.details || data.type));
      });
      hls.loadSource(url);
      hls.attachMedia(v);
    } else if (v.canPlayType('application/vnd.apple.mpegurl')) {
      v.src = url;
      v.play().catch(() => { /* noop */ });
      setPlayStatus('原生 HLS 播放');
      if (onReady) onReady();
    } else {
      setPlayStatus('当前浏览器不支持 HLS');
    }
  }

  function playWhole(type) {
    if (!testData || !testData.token) return;
    hlsMode = type;
    const pm = $('#playMode');
    if (pm) pm.textContent = '整片 · ' + (type === 'clean' ? '过滤后' : '原始');
    hlsSegIdx = 0;
    loadHls(adminPath() + '/api/test/playlist?token=' + encodeURIComponent(testData.token) + '&type=' + type, () => {
      hlsLoadedType = type;
    });
  }

  function playSeg(idx) {
    if (!testData || !testData.token) return;
    hlsMode = 'single';
    hlsSegIdx = idx;
    $$('.src-btn').forEach(x => x.classList.remove('on'));
    const pm = $('#playMode');
    if (pm) pm.textContent = '单片段 · 第 ' + idx + ' 段';
    updateNowSeg(idx);
    hlsLoadedType = ''; // 当前加载的是单片段，整片标记失效，避免后续定位误判为已加载
    loadHls(adminPath() + '/api/test/segment?token=' + encodeURIComponent(testData.token) + '&i=' + idx);
  }

  function seekSeg(idx) {
    if (!testData || !testData.token) return;
    const segs = testData.segments || [];
    const seg = segs.find(s => s.index === idx);
    if (!seg) return;
    // 广告段在过滤后列表里不存在，只能跳到原始整片对应位置
    const type = seg.ad ? 'origin' : 'clean';
    let t = seg.start;
    if (!seg.ad) {
      let acc = 0;
      for (const s of segs) { if (s.ad) continue; if (s.index === idx) break; acc += s.dur; }
      t = acc;
    }
    const v = $('#previewVideo');
    const doSeek = () => {
      setTimeout(() => {
        try { v.currentTime = Math.max(0, t + 0.05); } catch (e) { /* noop */ }
        v.play().catch(() => { /* noop */ });
      }, 150);
    };
    if (hlsLoadedType !== type || hlsMode !== type) {
      // 尚未加载对应整片：先加载，清单解析完成再跳转
      hlsMode = type; hlsSegIdx = 0;
      $$('.src-btn').forEach(x => x.classList.toggle('on', x.dataset.type === type));
      const pm = $('#playMode');
      if (pm) pm.textContent = '整片 · ' + (type === 'clean' ? '过滤后' : '原始');
      loadHls(adminPath() + '/api/test/playlist?token=' + encodeURIComponent(testData.token) + '&type=' + type, () => {
        hlsLoadedType = type;
        doSeek();
      });
    } else {
      doSeek();
    }
    updateNowSeg(idx);
    flashSegRow(idx, true);
    toast(seg.ad ? '广告段已被过滤，已定位到原始列表' : '已跳转到第 ' + idx + ' 段', seg.ad ? 'err' : 'ok');
  }

  function updateNowSeg(idx) {
    const segs = (testData && testData.segments) || [];
    const s = segs.find(x => x.index === idx);
    if (!s) return;
    const set = (id, v) => { const el = $(id); if (el) el.textContent = v; };
    set('#nsIdx', '#' + s.index);
    set('#nsDur', s.dur.toFixed(3) + 's');
    set('#nsStart', fmtTime(s.start));
    const u = $('#nsUrl');
    if (u) { u.textContent = s.url; u.title = s.url; }
    const tag = $('#nsTag');
    if (tag) { tag.textContent = s.ad ? '广告' : '正片'; tag.className = 'seg-tag ' + (s.ad ? 'ad' : 'ok'); }
    const a = $('#nsOpen');
    if (a) a.href = adminPath() + '/api/test/ts?token=' + encodeURIComponent(testData.token) + '&i=' + s.index;
    const cb = $('#followPlay');
    if (cb && cb.checked) flashSegRow(idx, true);
  }

  function flashSegRow(idx, scroll) {
    let row = $('#segr-' + idx);
    if (!row) {
      // 只渲染了前 N 行时目标行可能尚未渲染，自动扩展渲染范围，保证跟随定位可用
      const pos = filteredSegs().findIndex(x => x.index === idx);
      if (pos >= segShown) {
        segShown = pos + 300;
        renderSegRows();
        row = $('#segr-' + idx);
      }
      if (!row) return;
    }
    $$('.seg-tb tr.playing').forEach(x => x.classList.remove('playing'));
    row.classList.add('playing');
    if (!scroll) return;
    // 只在片段列表容器内部滚动，避免带动整页滚动导致播放器按钮被滚出视口点不中
    const box = row.closest ? row.closest('.seg-scroll') : null;
    if (box) {
      const rr = row.getBoundingClientRect(), br = box.getBoundingClientRect();
      if (rr.top < br.top || rr.bottom > br.bottom) {
        box.scrollTop += (rr.top - br.top) - (br.height - rr.height) / 2;
      }
      return;
    }
    const r = row.getBoundingClientRect();
    if (r.top < 0 || r.bottom > window.innerHeight) row.scrollIntoView({ block: 'nearest' });
  }

  // 复制到剪贴板：优先 Clipboard API，失败回退 execCommand
  async function copyText(txt) {
    txt = txt == null ? '' : String(txt);
    if (!txt) { toast('没有可复制的内容', 'err'); return; }
    try {
      if (navigator.clipboard && window.isSecureContext) {
        await navigator.clipboard.writeText(txt);
        toast('已复制 ' + txt.length + ' 字符', 'ok');
        return;
      }
    } catch (e) { /* 继续回退 */ }
    try {
      const ta = document.createElement('textarea');
      ta.value = txt;
      ta.style.position = 'fixed';
      ta.style.opacity = '0';
      document.body.appendChild(ta);
      ta.select();
      const ok = document.execCommand('copy');
      document.body.removeChild(ta);
      toast(ok ? '已复制 ' + txt.length + ' 字符' : '复制失败，请手动选择复制', ok ? 'ok' : 'err');
    } catch (e) {
      toast('复制失败，请手动选择复制', 'err');
    }
  }

  function copyTextRaw(txt) { copyText(txt); }

  /* ---------- 事件绑定 ---------- */
  $('#loginForm').addEventListener('submit', async (e) => {
    e.preventDefault();
    const r = await api('/api/login', { method: 'POST', body: { password: $('#loginPwd').value } });
    if (r.code === 200) {
      $('#loginErr').textContent = '';
      $('#loginPwd').value = '';
      showApp(true); initSidebar(); bindScrollMemory(); route(); startClock();
      toast('欢迎回来', 'ok');
    } else {
      $('#loginErr').textContent = r.msg || '登录失败';
    }
  });
  $('#logoutBtn').addEventListener('click', async () => {
    await api('/api/logout', { method: 'POST' });
    showApp(false);
  });
  $('#siteSaveBtn').addEventListener('click', saveSite);
  $('#probeBtn').addEventListener('click', runProbe);
  $('#addRegexBtn').addEventListener('click', () => addRegexRow(''));
  $('#addPrefixBtn').addEventListener('click', () => addListRow('#urlPrefixRows', '', '如 vip.dytt（主机名前缀）'));
  $('#addUrlRegexBtn').addEventListener('click', () => addListRow('#urlRegexRows', '', '如 ^https?://vip\\.dytt.+', 'ipt-rgx'));
  $$('[data-close="1"]').forEach(b => b.addEventListener('click', closeSiteModal));
  // 站点弹窗支持 Esc 关闭（与 dsModal / learnModal 行为一致）
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && !$('#siteModal').classList.contains('hidden')) closeSiteModal();
  });
  window.addEventListener('hashchange', route);

  bootstrap();
})();