/* DP-PXE (Go) —— 前端逻辑
   与 Go 后端只通过两个接口通信：
     GET  /api/state?since=<seq>   轮询状态（客户端表 / 日志 / 配置 / 服务状态）
     POST /api/cmd  {cmd, args}    执行命令（下发指令、保存配置、弹系统对话框……）
   这样同一套前端既能跑在 Wails(WebView2) 里，也能跑在普通浏览器里。 */

const $ = (id) => document.getElementById(id);

let lastSeq = 0;
let cfg = null;
let clients = [];
let selectedCids = new Set();
let fileItems = [];
let diskItems = [];
let diskSig = "";
let partRowEls = [];
let starting = false;
let pollTimer = null;
let scriptTemplate = "";
let lastState = null;

/* ---------------------------------------------------------------- 通信 */

async function api(cmd, args) {
  const r = await fetch('/api/cmd', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ cmd, args: args || {} }),
  });
  if (!r.ok) throw new Error('HTTP ' + r.status);
  return r.json();
}

async function poll() {
  try {
    const r = await fetch('/api/state?since=' + lastSeq);
    if (!r.ok) return;
    const st = await r.json();
    applyState(st);
  } catch (e) {
    /* 后端还没起来时静默重试 */
  }
}

/* ---------------------------------------------------------------- 通用控件 */

function tgOn(el) { return el.classList.contains('on'); }
function tgSet(el, v) { el.classList.toggle('on', !!v); }

function bindToggle(el, cb) {
  el.addEventListener('click', () => {
    if (el.dataset.locked === '1') return;
    el.classList.toggle('on');
    if (cb) cb(tgOn(el));
  });
}

function setIfIdle(el, value) {
  if (document.activeElement !== el) el.value = value;
}

function fmtSize(bytes) {
  if (bytes >= 1073741824) return (bytes / 1073741824).toFixed(1) + ' GB';
  if (bytes >= 1048576) return (bytes / 1048576).toFixed(1) + ' MB';
  if (bytes >= 1024) return (bytes / 1024).toFixed(1) + ' KB';
  return bytes + ' B';
}

function esc(s) {
  return String(s == null ? '' : s)
    .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

/* ---------------------------------------------------------------- 对话框 */

function closeDialog() { $('mask').classList.add('hidden'); $('dlgFoot').innerHTML = ''; }

function dialog(opts) {
  return new Promise((resolve) => {
    $('dlgTitle').textContent = opts.title || '提示';
    $('dlgContent').innerHTML = opts.html || '';
    const foot = $('dlgFoot');
    foot.innerHTML = '';
    (opts.buttons || [{ label: '确定', value: true, primary: true }]).forEach((b) => {
      const btn = document.createElement('button');
      btn.textContent = b.label;
      if (!b.primary) btn.className = 'ghost';
      if (b.danger) btn.className = 'dangerbtn';
      btn.onclick = () => {
        const value = typeof b.value === 'function' ? b.value() : b.value;
        closeDialog();
        resolve(value);
      };
      foot.appendChild(btn);
    });
    $('mask').classList.remove('hidden');
  });
}

const info = (title, html) => dialog({ title, html });
const confirmBox = (title, html) => dialog({
  title, html,
  buttons: [{ label: '是', value: true, primary: true }, { label: '否', value: false }],
});

/* ---------------------------------------------------------------- 状态渲染 */

function applyState(st) {
  const first = cfg === null;
  lastState = st;
  cfg = st.cfg;
  $('appVer').textContent = st.version;

  if (first) {
    fillForm(cfg);
    $('serverIP').value = cfg.server_ip;
    $('ipList').innerHTML = st.ips.map((ip) => `<option value="${esc(ip)}">`).join('');
    loadScriptTemplate();
  } else {
    setIfIdle($('serverIP'), cfg.server_ip);
  }

  // 服务状态
  $('dotDhcp').classList.toggle('on', !!st.services.dhcp);
  $('dotTftp').classList.toggle('on', !!st.services.tftp);
  $('dotHttp').classList.toggle('on', !!st.services.http);
  $('dotFile').classList.toggle('on', !!st.services.file);
  $('lblStatus').textContent = st.running
    ? `服务运行中（${st.service_count} 项）` : '服务未启动';
  $('btnStart').style.display = st.running ? 'none' : '';
  $('btnStop').style.display = st.running ? '' : 'none';
  $('lblAdmin').style.display = st.admin ? 'none' : '';

  // 软件设置页：运行目录
  $('lblCfgDir').textContent = st.config_dir || '—';
  $('lblLogDir').textContent = st.log_dir || '—';

  // 客户端表
  clients = st.clients || [];
  renderClients();
  $('lblConn').textContent = `已连接客户端: ${st.online}（在线 ${st.online} / 离线 ${st.offline}`
    + (st.pxe ? ` / 引导中 ${st.pxe}` : '') + '）';
  $('lblSel').textContent = `选中客户端: ${selectedCids.size}`;

  // 引导方式提示
  $('bootHint').textContent = cfg.secure_boot
    ? `当前：原生引导（签名通过）\nEFI ${cfg.native_efi_file} / BIOS ${cfg.native_bios_file}`
    : `当前：iPXE 模式（需关闭安全引导）\nEFI ${cfg.boot_efi} / BIOS ${cfg.boot_bios}`;

  // 分区页磁盘
  syncDisks(st.disks || []);

  // 发送文件真实进度（已发分片 / 总片数）
  if (st.file_total > 0) {
    const pct = Math.min(100, Math.round((st.file_sent || 0) * 100 / st.file_total));
    $('sendBar').style.width = pct + '%';
  }

  // 日志
  if (st.logs && st.logs.length) {
    appendLogs(st.logs);
    lastSeq = st.last_seq;
  }
}

function fillForm(c) {
  $('edBootBios').value = c.boot_bios;
  $('edBootEfi').value = c.boot_efi;
  $('edMenu').value = c.menu_file;
  $('edRoot').value = c.root_dir;
  tgSet($('tgDhcp'), c.dhcp_enabled);
  tgSet($('tgTftp'), c.tftp_enabled);
  tgSet($('tgHttp'), c.http_enabled);
  tgSet($('tgProxy'), c.proxydhcp_enabled);
  tgSet($('tgFile'), c.filesvc_enabled);
  tgSet($('tgOnly'), c.only_selected);

  $('dhcpStart').value = c.dhcp_start;
  $('dhcpEnd').value = c.dhcp_end;
  $('dhcpMask').value = c.subnet_mask;
  $('dhcpGw').value = c.gateway;
  $('dhcpDns1').value = c.dns1;
  $('dhcpDns2').value = c.dns2;
  $('dhcpLease').value = c.lease_seconds;
  tgSet($('tgFast'), c.fast_response);
  tgSet($('tgSecure'), c.secure_boot);
  tgSet($('tgProxy2'), c.proxydhcp_enabled);

  $('recvPath').value = c.recv_path;
  $('edPrefix').value = c.name_prefix;
  $('chkVncHide').checked = !!c.vnc_hidden;
  document.querySelectorAll('input[name=nextboot]').forEach((r) => {
    r.checked = r.value === (c.next_boot || 'local');
  });
}

/* ---------------------------------------------------------------- 客户端表 */

function stateClass(s) {
  if (s === '在线') return 's-online';
  if (s === 'PXE') return 's-pxe';
  return 's-offline';
}

function renderClients() {
  const tbody = $('clientRows');
  const html = clients.map((r) => {
    const sel = r.cid !== '-' && selectedCids.has(r.cid) ? ' class="sel"' : '';
    const tip = [
      r.name ? `主机名：${r.name}` : '',
      r.last_seen ? `最后在线：${new Date(r.last_seen * 1000).toLocaleTimeString('zh-CN', { hour12: false })}` : '',
      `状态：${r.state}`,
    ].filter(Boolean).join('\n');
    return `<tr data-cid="${esc(r.cid)}" data-mac="${esc(r.mac)}"${sel} title="${esc(tip)}">`
      + `<td class="center">${esc(r.cid)}</td>`
      + `<td>${esc(r.ip)}</td>`
      + `<td>${esc(r.mac)}</td>`
      + `<td class="center ${stateClass(r.state)}">${esc(r.state)}</td>`
      + `<td class="center">${esc(r.speed)}</td>`
      + `<td class="center">${esc(r.root)}</td>`
      + `<td>${esc(r.boot)}</td></tr>`;
  }).join('');
  if (tbody.dataset.html !== html) {
    tbody.innerHTML = html;
    tbody.dataset.html = html;
  }
}

$('clientRows').addEventListener('click', (ev) => {
  const tr = ev.target.closest('tr');
  if (!tr) return;
  const cid = tr.dataset.cid;
  if (!cid || cid === '-') return;
  if (ev.ctrlKey) {
    selectedCids.has(cid) ? selectedCids.delete(cid) : selectedCids.add(cid);
  } else {
    selectedCids = new Set([cid]);
  }
  renderClients();
  const nums = [...selectedCids].map((x) => parseInt(x, 10)).filter((x) => !isNaN(x));
  api('select', { cids: nums });
});

$('clientRows').addEventListener('contextmenu', (ev) => {
  ev.preventDefault();
  const tr = ev.target.closest('tr');
  const row = tr ? clients.find((c) => c.cid === tr.dataset.cid) : null;
  showClientMenu(row, ev.clientX, ev.clientY);
});

function showClientMenu(row, x, y) {
  const m = $('ctxMenu');
  const items = [];
  if (row) {
    const online = row.state === '在线';
    items.push({ label: `连接 VNC ${row.ip}（内置）`, run: () => vncOpen(row.ip) });
    items.push({ label: `用外部 Viewer 连接 ${row.ip}`, run: () => cmd('vncExternal', { ip: row.ip }) });
    items.push({ label: `查看 ${row.ip} 信息`, run: () => showClientInfo(row) });
    items.push({ label: `修改 ${row.ip} 编号 和 IP`, run: () => editClient(row) });
    items.push({ label: `从列表中删除 ${row.ip}`, run: () => removeClient(row) });
    items.push({ sep: true });
    items.push({ label: `远程重启 ${row.ip}`, disabled: !online, run: () => cmd('clientPower', { cid: row.cid, action: 2 }) });
    items.push({ label: `远程关机 ${row.ip}`, disabled: !online, run: () => cmd('clientPower', { cid: row.cid, action: 1 }) });
    items.push({ label: `远程开机 ${row.mac}`, run: () => cmd('clientWol', { mac: row.mac }) });
    items.push({ sep: true });
    items.push({ label: `重启客户端进程 ${row.ip}`, disabled: !online, run: () => cmd('clientRestart', { cid: row.cid }) });
    items.push({ sep: true });
  }
  items.push({ label: '重启所有客户端', run: () => cmd('power', { action: 2 }) });
  items.push({ label: '关机所有客户端', run: () => cmd('power', { action: 1 }) });
  items.push({ label: '开机所有客户端', run: () => cmd('wolAll') });
  items.push({ sep: true });
  items.push({ label: '保存客户端列表', run: () => cmd('saveClientList') });

  m.innerHTML = items.map((it, i) => it.sep
    ? '<div class="sep"></div>'
    : `<div class="item${it.disabled ? ' disabled' : ''}" data-i="${i}">${esc(it.label)}</div>`).join('');
  m.querySelectorAll('.item').forEach((el) => {
    el.onclick = () => {
      const it = items[parseInt(el.dataset.i, 10)];
      hideMenus();
      if (!it.disabled) it.run();
    };
  });
  placeMenu(m, x, y);
}

async function showClientInfo(row) {
  const r = await api('clientInfo', { cid: row.cid });
  const d = r.data || {};
  await info(`${row.ip} 信息`,
    `编号：${esc(d.cid || row.cid)}<br>IP 地址：${esc(d.ip || row.ip)}<br>`
    + `MAC 地址：${esc(d.mac || row.mac)}<br>主机名：${esc(d.name || '-')}<br>`
    + `状态：${esc(d.state || row.state)}<br>引导文件：${esc(d.boot || row.boot)}<br>`
    + `最后在线：${esc(d.last_seen || '-')}`);
}

async function editClient(row) {
  const ok = await dialog({
    title: '修改编号 和 IP',
    html: `<div class="field"><label>机器号</label>
        <input type="number" id="edNewCid" min="1" max="9999" value="${esc(parseInt(row.cid, 10) || 1)}" style="width:110px"></div>
      <div class="field"><label>IP 地址</label>
        <input type="text" id="edNewIp" value="${esc(row.ip)}" style="width:180px"></div>`,
    buttons: [
      { label: '确定', primary: true, value: () => ({ cid: $('edNewCid').value, ip: $('edNewIp').value }) },
      { label: '取消', value: null },
    ],
  });
  if (!ok) return;
  await cmd('clientRename', { cid: row.cid, new_cid: parseInt(ok.cid, 10) || 1, ip: ok.ip.trim(), mac: row.mac });
}

async function removeClient(row) {
  const yes = await confirmBox('从列表中删除',
    `确定把 ${esc(row.ip)}（${esc(row.mac)}）从列表移除？<br>在线客户端会被断开，它会自动重连。`);
  if (!yes) return;
  await cmd('clientRemove', { cid: row.cid, mac: row.mac, ip: row.ip });
}

/* ---------------------------------------------------------------- 日志 */

function appendLogs(logs) {
  const box = $('logPanel');
  const frag = document.createDocumentFragment();
  logs.forEach((e) => {
    const div = document.createElement('div');
    div.className = 'logline k-' + (e.kind || 'info');
    div.innerHTML = `<span class="ts">${esc(e.ts)}</span> ${esc(e.text)}`;
    frag.appendChild(div);
  });
  box.appendChild(frag);
  while (box.childElementCount > 2000) box.removeChild(box.firstChild);
  box.scrollTop = box.scrollHeight;
}

/* ---------------------------------------------------------------- 左侧导航 */

/* 首页 / 日志 / 软件设置：切换 .page 的 active 状态。
   启动/停止服务按钮在导航栏底部，id 没变，逻辑照旧。 */
function setPage(name) {
  document.querySelectorAll('.nav-item').forEach((el) => {
    el.classList.toggle('active', el.dataset.page === name);
  });
  document.querySelectorAll('.page').forEach((el) => {
    el.classList.toggle('active', el.id === 'page-' + name);
  });
  if (name === 'log') {
    const lp = $('logPanel');
    lp.scrollTop = lp.scrollHeight; // 进日志页直接停在最新一行
  }
}

document.querySelectorAll('.nav-item').forEach((el) => {
  el.addEventListener('click', () => setPage(el.dataset.page));
});

/* ---------------------------------------------------------------- 菜单 */

function placeMenu(m, x, y) {
  m.classList.remove('hidden');
  const rect = m.getBoundingClientRect();
  const px = Math.min(x, window.innerWidth - rect.width - 6);
  const py = Math.min(y, window.innerHeight - rect.height - 6);
  m.style.left = Math.max(4, px) + 'px';
  m.style.top = Math.max(4, py) + 'px';
}

function hideMenus() {
  $('mainMenu').classList.add('hidden');
  $('ctxMenu').classList.add('hidden');
}

document.addEventListener('click', (ev) => {
  if (!ev.target.closest('.menu') && !ev.target.closest('#menuBtn')) hideMenus();
});

$('menuBtn').onclick = (ev) => {
  ev.stopPropagation();
  const m = $('mainMenu');
  const items = [
    { label: '启动被控端（本机 · 图形界面）', run: () => cmd('launchClient') },
    { label: '启动被控端（本机 · 控制台）', run: () => cmd('launchClientConsole') },
    { label: '启动客户端模拟器', run: () => cmd('launchSim') },
    { sep: true },
    { label: '网络启动自检', run: diagnose },
    { label: '放行防火墙端口（需管理员）', run: () => cmd('firewall') },
    { sep: true },
    { label: '开始抓包诊断', id: 'capItem', run: toggleCapture },
    { label: '导出运行日志', run: exportLog },
    { sep: true },
    { label: '打开配置目录', run: () => cmd('openCfgDir') },
    { label: '打开日志目录', run: () => cmd('openLogDir') },
    { sep: true },
    { label: '关于', run: about },
  ].filter((it) => it.label !== '' || it.sep);
  m.innerHTML = items.map((it, i) => it.sep
    ? '<div class="sep"></div>'
    : `<div class="item" data-i="${i}"${it.id ? ` id="${it.id}"` : ''}>${esc(it.label)}</div>`).join('');
  const cap = m.querySelector('#capItem');
  if (cap) cap.textContent = (lastState && lastState.capturing) ? '停止抓包并分析' : '开始抓包诊断';
  m.querySelectorAll('.item').forEach((el) => {
    el.onclick = () => {
      const it = items[parseInt(el.dataset.i, 10)];
      hideMenus();
      if (it.run) it.run();
    };
  });
  const r = $('menuBtn').getBoundingClientRect();
  placeMenu(m, r.right - 210, r.bottom + 4);
};

async function about() {
  await info('关于',
    `<b>DP-PXE (Go Rebuild)</b><br>版本 ${esc((cfg && cfg.version) || '2.1')}<br><br>`
    + `按 DP-PXE Server 1.6.9 的界面与功能重写的 Go 版本（原 Python/PySide6 版重构）。<br>`
    + `网络启动（DHCP/TFTP/HTTP）走标准协议；控制通道为按原协议风格重写的实现，`
    + `需配合本项目自带的被控端 / 模拟器演示。`);
}

async function diagnose() {
  const r = await api('diagnose');
  const d = r.data || {};
  const issues = (d.issues || []).map((i) => (i.level === 'error' ? '✗ ' : '! ') + i.msg);
  await info('网络启动自检',
    `根目录：${esc(d.root)}<br>服务端 IP：${esc(d.ip)}<br>地址池：${esc(d.pool)}<br><br>`
    + (issues.length ? issues.map(esc).join('<br>') : '未发现问题'));
}

async function exportLog() {
  const r = await api('exportLog');
  await info('导出运行日志', r.ok ? `已保存到：<br>${esc(r.msg.replace('已保存到 ', ''))}` : esc(r.msg));
}

async function toggleCapture() {
  const r = await api('toggleCapture');
  if (!r.ok && r.msg) await info('抓包诊断', esc(r.msg));
}

/* ---------------------------------------------------------------- 命令封装 */

async function cmd(name, args, opts) {
  try {
    const r = await api(name, args);
    if (opts && opts.quiet) return r;
    if (!r.ok) {
      if (r.data && r.data.need_confirm) return r;
      if (r.msg && r.msg !== '已取消') await info('提示', esc(r.msg));
      return r;
    }
    poll();
    return r;
  } catch (e) {
    await info('通信失败', esc(String(e)));
    return { ok: false, msg: String(e) };
  }
}

/* ---------------------------------------------------------------- 顶部卡片 */

$('serverIP').addEventListener('change', async () => {
  const ip = $('serverIP').value.trim();
  if ((ip.match(/\./g) || []).length !== 3) return;
  const r = await api('setIP', { ip });
  if (r.ok) {
    cfg = r.data;
    fillForm(cfg);
  }
});

const serviceToggles = {
  tgDhcp: 'dhcp_enabled', tgTftp: 'tftp_enabled', tgHttp: 'http_enabled',
  tgProxy: 'proxydhcp_enabled', tgFile: 'filesvc_enabled', tgOnly: 'only_selected',
};
Object.keys(serviceToggles).forEach((id) => {
  bindToggle($(id), (on) => {
    api('saveConfig', { [serviceToggles[id]]: on });
  });
});

$('btnPickRoot').onclick = async () => {
  const r = await api('pickRoot');
  if (r.ok) {
    $('edRoot').value = r.data;
    api('saveConfig', { root_dir: r.data });
  }
};

['edBootBios', 'edBootEfi', 'edMenu', 'edRoot'].forEach((id) => {
  $(id).addEventListener('change', () => {
    api('saveConfig', {
      boot_bios: $('edBootBios').value.trim(),
      boot_efi: $('edBootEfi').value.trim(),
      menu_file: $('edMenu').value.trim(),
      root_dir: $('edRoot').value.trim(),
    });
  });
});

/* ---------------------------------------------------------------- 页签切换 */

$('tabbar').addEventListener('click', (ev) => {
  const tab = ev.target.closest('.tab');
  if (!tab) return;
  document.querySelectorAll('.tab').forEach((t) => t.classList.toggle('active', t === tab));
  document.querySelectorAll('.panel').forEach((p) => {
    p.classList.toggle('active', p.id === 'panel-' + tab.dataset.tab);
  });
});

/* ---------------------------------------------------------------- 1. DHCP */

$('btnDhcpApply').onclick = async () => {
  await cmd('saveDhcp', {
    start: $('dhcpStart').value.trim(),
    end: $('dhcpEnd').value.trim(),
    mask: $('dhcpMask').value.trim(),
    gateway: $('dhcpGw').value.trim(),
    dns1: $('dhcpDns1').value.trim(),
    dns2: $('dhcpDns2').value.trim(),
    lease: parseInt($('dhcpLease').value, 10) || 86400,
    fast: tgOn($('tgFast')),
    secure: tgOn($('tgSecure')),
    proxy: tgOn($('tgProxy2')),
  });
  tgSet($('tgProxy'), tgOn($('tgProxy2')));
};

$('btnDhcpDerive').onclick = async () => {
  const r = await cmd('derivePool');
  if (r.ok && r.data) fillForm(r.data);
};

/* ---------------------------------------------------------------- 2. 远程 CMD */

$('btnRunCmd').onclick = async () => {
  const text = $('cmdText').value.trim();
  if (!text) { await info('提示', '命令为空'); return; }
  await cmd('shell', { text, wait: $('chkWait').checked, asUser: $('chkAsUser').checked });
};

/* ---------------------------------------------------------------- 3. 发送文件 */

function renderFileList() {
  $('fileList').innerHTML = fileItems.map((f, i) =>
    `<div class="fi" data-i="${i}">${esc(f.name)} <span class="sz">(${fmtSize(f.size)})</span></div>`).join('');
  $('fileList').querySelectorAll('.fi').forEach((el) => {
    el.onclick = () => {
      const i = parseInt(el.dataset.i, 10);
      $('fileList').querySelectorAll('.fi').forEach((x) => x.classList.toggle('sel', x === el));
      $('fileList').dataset.sel = i;
    };
  });
}

function addFiles(paths) {
  paths.forEach((p) => {
    if (fileItems.some((f) => f.path === p)) return;
    const name = p.split(/[\\/]/).pop();
    fileItems.push({ path: p, name, size: 0 });
  });
  renderFileList();
  refreshFileSizes();
}

function refreshFileSizes() {
  // 大小由后端返回（前端拿不到本地文件大小），这里只做展示占位
  api('fileSizes', { files: fileItems.map((f) => f.path) }).then((r) => {
    if (!r.ok || !r.data) return;
    r.data.forEach((sz, i) => { if (fileItems[i]) fileItems[i].size = sz; });
    renderFileList();
  }).catch(() => {});
}

$('btnAddFiles').onclick = async () => {
  const r = await api('pickFiles');
  if (r.ok && r.data) addFiles(r.data);
};

$('btnAddDir').onclick = async () => {
  const r = await api('pickFolder');
  if (r.ok && r.data) addFiles(r.data);
};

$('btnRemoveFile').onclick = () => {
  const i = parseInt($('fileList').dataset.sel, 10);
  if (isNaN(i)) return;
  fileItems.splice(i, 1);
  $('fileList').dataset.sel = '';
  renderFileList();
};

$('btnSendFiles').onclick = async () => {
  if (!fileItems.length) { await info('提示', '文件列表为空'); return; }
  const mode = document.querySelector('input[name=filemode]:checked').value;
  $('sendBar').style.width = '0%'; // 由状态轮询按真实分片进度推进，别提前跑满
  await cmd('sendFiles', {
    files: fileItems.map((f) => f.path),
    target: $('recvPath').value.trim() || (cfg && cfg.recv_path),
    mode: mode === 'p2p' ? 'P2P' : '单播',
  });
};

$('recvPath').addEventListener('change', () => {
  api('saveConfig', { recv_path: $('recvPath').value.trim() });
});

/* ---------------------------------------------------------------- 4. 高级分区 */

const FS_LIST = ['NTFS', 'FAT32', 'exFAT'];

function buildPartRows() {
  const box = $('partRows');
  box.innerHTML = '';
  partRowEls = [];
  for (let i = 0; i < 6; i++) {
    const row = document.createElement('div');
    row.className = 'partrow';
    row.innerHTML = `<span class="idx">${i + 1}:</span>
      <select>${FS_LIST.map((f) => `<option>${f}</option>`).join('')}</select>
      <input type="number" min="1" max="20000" value="20" class="size-in">
      <label class="check prim" style="display:none"><input type="checkbox" class="primary-in"${i === 0 ? ' checked' : ''}>主分区</label>`;
    box.appendChild(row);
    const els = {
      row,
      fs: row.querySelector('select'),
      size: row.querySelector('.size-in'),
      primary: row.querySelector('.primary-in'),
      prim: row.querySelector('.prim'),
    };
    els.fs.onchange = () => schedulePreview();
    els.size.onchange = () => rebalanceSizes(i);
    els.primary.onchange = () => { enforcePrimaryPrefix(i); schedulePreview(); };
    partRowEls.push(els);
  }
}

/* 改一个分区大小时，其余分区自动分摊剩余空间（按现有比例；最后一个吃零头） */
function rebalanceSizes(editedIdx) {
  const n = partCount();
  if (n < 1) return;
  let usable = parseInt($('spTotal').value, 10) || 60;
  if (partTable() === 'gpt') { // 扣掉 ESP(300MB) + MSR(16MB)
    if ($('ckNewEsp').checked) usable -= 0.3;
    if ($('ckMsr').checked) usable -= 0.016;
  }
  usable = Math.max(n, Math.floor(usable));
  let edited = parseInt(partRowEls[editedIdx].size.value, 10) || 1;
  edited = Math.max(1, Math.min(edited, usable - (n - 1)));
  partRowEls[editedIdx].size.value = edited;
  const others = [];
  for (let j = 0; j < n; j++) if (j !== editedIdx) others.push(j);
  if (others.length) {
    const remaining = usable - edited;
    const curSum = others.reduce((s, j) => s + (parseInt(partRowEls[j].size.value, 10) || 0), 0);
    let acc = 0;
    others.forEach((j, k) => {
      let v;
      if (k === others.length - 1) {
        v = remaining - acc; // 最后一个吃零头
      } else if (curSum > 0) {
        v = Math.round(remaining * (parseInt(partRowEls[j].size.value, 10) || 0) / curSum);
      } else {
        v = Math.round(remaining / others.length);
      }
      v = Math.max(1, v);
      partRowEls[j].size.value = v;
      acc += v;
    });
  }
  schedulePreview();
}

function partCount() {
  const v = document.querySelector('input[name=partCount]:checked').value;
  return v === 'custom' ? Math.max(1, Math.min(6, parseInt($('spCustom').value, 10) || 2)) : parseInt(v, 10);
}

function partTable() {
  return $('cbTable').value === 'MBR' ? 'mbr' : 'gpt';
}

/* MBR 相关控件的显隐/可用状态：每行的「主分区」勾选框只在 MBR 下出现；ESP/MSR 只在 GPT 下可用 */
function syncMbrUi() {
  const mbr = partTable() === 'mbr';
  $('ckMbr').disabled = !mbr;
  $('ckNewEsp').disabled = mbr;
  $('ckMsr').disabled = mbr;
  partRowEls.forEach((r) => { if (r.prim) r.prim.style.display = mbr ? '' : 'none'; });
  if (!mbr) $('ckMbr').checked = false;
  if (mbr) { $('ckNewEsp').checked = false; $('ckMsr').checked = false; }
}

/* 主分区必须是前缀；并遵守 MBR 约束（>4 个分区时主分区最多 3 个，留槽给扩展） */
function enforcePrimaryPrefix(changedIdx) {
  const n = partCount();
  if (partRowEls[changedIdx].primary.checked) {
    for (let j = 0; j <= changedIdx; j++) partRowEls[j].primary.checked = true;
  } else {
    for (let j = changedIdx; j < n; j++) partRowEls[j].primary.checked = false;
  }
  let pc = 0;
  for (let j = 0; j < n; j++) { if (partRowEls[j].primary.checked) pc++; else break; }
  const maxPC = n <= 4 ? n : 3;
  if (pc > maxPC) pc = maxPC;
  for (let j = 0; j < n; j++) partRowEls[j].primary.checked = j < pc;
}

function syncRowVisibility() {
  const n = partCount();
  partRowEls.forEach((r, i) => { r.row.style.display = i < n ? '' : 'none'; });
}

function currentSpec() {
  const n = partCount();
  return {
    disk: parseInt($('diskList').dataset.sel || '0', 10) || 0,
    table: partTable(),
    total_gb: parseInt($('spTotal').value, 10) || 60,
    count: n,
    sizes_gb: partRowEls.slice(0, n).map((r) => parseInt(r.size.value, 10) || 0),
    filesystems: partRowEls.slice(0, n).map((r) => r.fs.value),
    rebuild_mbr: $('ckMbr').checked,
    keep_esp: $('ckKeepEsp').checked,
    create_esp: $('ckNewEsp').checked,
    create_msr: $('ckMsr').checked,
    primaries: partRowEls.slice(0, n).map((r) => r.primary.checked),
    esp_mb: 300,
    msr_mb: 16,
    align: $('ckAlign').checked ? (parseInt($('spAlign').value, 10) || 2048) : 0,
  };
}

let previewTimer = null;
function schedulePreview() {
  if (previewTimer) clearTimeout(previewTimer);
  previewTimer = setTimeout(() => api('partitionScript', currentSpec()), 120);
}

async function applyDefaultSizes() {
  const n = partCount();
  const r = await api('partitionDefaults', {
    total_gb: parseInt($('spTotal').value, 10) || 60,
    count: n,
    table: partTable(),
    create_esp: $('ckNewEsp').checked,
    create_msr: $('ckMsr').checked,
    esp_mb: 300,
    msr_mb: 16,
  });
  if (!r.ok || !r.data) return;
  r.data.forEach((sz, i) => {
    if (partRowEls[i]) {
      partRowEls[i].size.value = sz;
    }
  });
  schedulePreview();
}

function onStructureChange() {
  syncRowVisibility();
  applyDefaultSizes();
}

document.querySelectorAll('input[name=partCount]').forEach((r) => {
  r.addEventListener('change', onStructureChange);
});
$('spCustom').addEventListener('change', () => {
  if (document.querySelector('input[name=partCount][value=custom]').checked) onStructureChange();
});
$('spTotal').addEventListener('change', onStructureChange);
$('cbTable').addEventListener('change', () => {
  syncMbrUi();
  onStructureChange();
});
syncMbrUi(); // 初始状态（默认 GPT）
$('ckKeepEsp').addEventListener('change', () => {
  if ($('ckKeepEsp').checked) $('ckNewEsp').checked = false;
  onStructureChange();
});
$('ckNewEsp').addEventListener('change', () => {
  if ($('ckNewEsp').checked) $('ckKeepEsp').checked = false;
  onStructureChange();
});
$('ckMsr').addEventListener('change', onStructureChange);
$('ckAlign').addEventListener('change', schedulePreview);
$('spAlign').addEventListener('change', schedulePreview);

$('btnDefaultSizes').onclick = applyDefaultSizes;

$('btnShowScript').onclick = async () => {
  const r = await api('partitionScript', currentSpec());
  await dialog({
    title: '生成的 DiskPart 脚本',
    html: `<pre style="width:520px;white-space:pre-wrap">${esc(r.data || '')}</pre>`,
    buttons: [{ label: '关闭', primary: true, value: true }],
  });
};

$('btnPartReset').onclick = () => {
  $('diskList').dataset.sel = '0';
  document.querySelector('input[name=partCount][value="3"]').checked = true;
  $('cbTable').value = 'GPT';
  $('ckKeepEsp').checked = false;
  $('ckNewEsp').checked = true;
  $('ckMsr').checked = true;
  $('ckAlign').checked = true;
  $('spAlign').value = 2048;
  $('ckMbr').checked = false;
  partRowEls.forEach((r) => { r.fs.value = 'NTFS'; });
  renderDiskList();
  syncMbrUi();
  onStructureChange();
};

$('btnPartRun').onclick = async () => {
  const sp = currentSpec();
  const yes = await confirmBox('确认分区',
    `将对硬盘 ${sp.disk} 执行 ${sp.keep_esp ? '保留 ESP 的分区' : 'Clean（整盘数据丢失）'}<br>`
    + `分区表 ${sp.table.toUpperCase()}，共 ${sp.count} 个数据分区。<br><br>继续？`);
  if (!yes) return;
  await cmd('partition', sp);
};

/* 磁盘列表 */
function syncDisks(disks) {
  const sig = JSON.stringify(disks.map((d) => [d.index, d.model, d.size_gb, d.style]));
  if (sig === diskSig) return;
  diskSig = sig;
  diskItems = disks;
  renderDiskList();
  applyDisk(0);
  syncInstallDisks(disks);
}

/* 把被控端上报的磁盘同步到「系统安装」页：硬盘下拉 + 硬盘数 + 主/副盘开关 */
function syncInstallDisks(disks) {
  const sel = $('spDisk');
  if (sel) {
    const prev = sel.value;
    sel.innerHTML = disks.length
      ? disks.map((d) => `<option value="${d.index}">HD${d.index}: ${esc(d.model || '磁盘')}（${d.size_gb || 0}GB）</option>`).join('')
      : '<option value="0">HD0</option>';
    if (prev && Array.from(sel.options).some((o) => o.value === prev)) sel.value = prev;
  }
  const cnt = $('lblDiskCount');
  if (cnt) cnt.textContent = disks.length ? `已识别 ${disks.length} 块硬盘` : '未上报硬盘';
  const roleBox = $('diskRoleBox');
  if (roleBox) roleBox.style.display = disks.length > 1 ? '' : 'none';
}

function renderDiskList() {
  const box = $('diskList');
  const cur = parseInt(box.dataset.sel || '0', 10) || 0;
  if (diskItems.length) {
    box.innerHTML = diskItems.map((d, i) =>
      `<div class="di${i === cur ? ' sel' : ''}" data-i="${i}">HD${d.index}: ${esc(d.model || '磁盘')}（${d.size_gb || 0}GB）${esc(d.style || '')}</div>`).join('');
  } else {
    box.innerHTML = Array.from({ length: 8 }, (_, i) =>
      `<div class="di${i === cur ? ' sel' : ''}" data-i="${i}">HD${i}: 目标磁盘 ${i}</div>`).join('');
  }
  box.querySelectorAll('.di').forEach((el) => {
    el.onclick = () => {
      box.dataset.sel = el.dataset.i;
      box.querySelectorAll('.di').forEach((x) => x.classList.toggle('sel', x === el));
      applyDisk(parseInt(el.dataset.i, 10));
    };
  });
}

function applyDisk(row) {
  if (diskItems.length && row < diskItems.length) {
    const d = diskItems[row];
    const gb = parseInt(d.size_gb, 10) || 0;
    if (gb > 0) $('spTotal').value = Math.min(Math.max(gb, 8), 20000);
  }
  onStructureChange();
}

/* ---------------------------------------------------------------- 5. 系统安装 */

function fillPartSelects() {
  const opts = 'CDEFGH'.split('').map((c) => `<option value="${c}:\\">${c}:\\</option>`).join('');
  $('cbInstall').innerHTML = opts;
  $('cbStreamPart').innerHTML = opts;
  syncBootSelect();
}

/* 引导分区跟随固件自动决定：UEFI → ESP（通常无盘符）、Legacy → 系统分区，都由 bcdboot 自动探测 */
function syncBootSelect() {
  const sel = $('cbBoot');
  if (!sel) return;
  sel.innerHTML = firmware() === 'legacy'
    ? '<option value="auto">自动检测（系统分区）</option>'
    : '<option value="auto">自动检测（ESP）</option>';
}

function firmware() {
  return document.querySelector('input[name=fw]:checked').value;
}

fillPartSelects();
document.querySelectorAll('input[name=fw]').forEach((r) => r.addEventListener('change', syncBootSelect));

$('btnAutopart').onclick = () => cmd('autopart', {
  disk: parseInt($('spDisk').value, 10) || 0,
  role: (document.querySelector('input[name=diskrole]:checked') || {}).value || 'primary',
  firmware: firmware(),
});
$('btnRebuildMbr').onclick = () => cmd('rebuildMbr', { disk: parseInt($('spDisk').value, 10) || 0 });
$('btnFixboot').onclick = () => cmd('fixboot', {
  win: $('cbInstall').value, boot: 'auto', firmware: firmware(),
});
$('btnUnattend').onclick = () => cmd('unattend', { target: $('cbInstall').value });
$('btnInstall').onclick = () => cmd('install', {
  image: $('edImage').value.trim(),
  index: parseInt($('spIndex').value, 10) || 1,
  target: $('cbInstall').value,
  compact: $('chkCompact').checked,
});
$('btnBackup').onclick = () => cmd('backup', {
  source: $('cbInstall').value,
  image: $('edBackup').value.trim(),
  incremental: $('chkIncr').checked,
});
$('btnInjectDrv').onclick = () => cmd('injectDrv', {
  target: $('cbInstall').value, dir: $('edDrv').value.trim(),
});

/* ---------------------------------------------------------------- 6. 流式安装 */

$('btnStreamConvert').onclick = () => info('流式镜像转换',
  '把普通 WIM/ESD 重打包为 pipable WIM（顺序可读，才能边下边解）。<br><br>'
  + '需要 wimlib-imagex.exe，等价命令：<br><br>'
  + '<pre>wimlib-imagex.exe export 源.wim 目标.wim all --pipable</pre><br>'
  + '转换在服务端一次性完成，之后客户端即可流式安装。');

$('btnStreamRun').onclick = () => {
  const partLetter = $('cbStreamPart').value;
  const part = Math.max(1, partLetter.toUpperCase().charCodeAt(0) - 64);
  return cmd('stream', {
    url: $('edUrl').value.trim(),
    part,
    disk: parseInt($('spStreamDisk').value, 10) || 0,
    autopart: tgOn($('tgStAutopart')),
    format: tgOn($('tgStFormat')),
    fixboot: tgOn($('tgStFixboot')),
    unattend: tgOn($('tgStUnattend')),
    pedrv: tgOn($('tgStPedrv')),
    client: tgOn($('tgStClient')),
    vnc: tgOn($('tgStVnc')),
    reboot: tgOn($('tgStReboot')),
  });
};

/* ---------------------------------------------------------------- 7. 自动化脚本 */

async function loadScriptTemplate() {
  const r = await api('scriptTemplate');
  if (r.ok && r.data) {
    scriptTemplate = r.data;
    if (!$('scriptText').value) $('scriptText').value = scriptTemplate;
  }
}

$('btnTpl').onclick = () => {
  $('scriptText').value = scriptTemplate;
  $('btnCheck').click();
};

$('btnCheck').onclick = async () => {
  const r = await api('scriptCheck', { body: $('scriptText').value });
  if (r.ok) $('scriptSummary').textContent = r.msg;
};

$('btnScriptRun').onclick = async () => {
  const r = await cmd('script', { body: $('scriptText').value, auto: $('chkAuto').checked });
  if (r.ok) $('btnCheck').click();
};

/* ---------------------------------------------------------------- 8. 其它设置 */

$('btnRestartClient').onclick = () => cmd('restartClient');
$('btnInjectClient').onclick = () => cmd('injectClient');
$('btnInjectVNC').onclick = () => cmd('injectVNC', { hide: $('chkVncHide').checked });
$('btnInjectPedrv').onclick = () => cmd('injectPEDrv');
$('chkVncHide').onchange = () => api('saveConfig', { vnc_hidden: $('chkVncHide').checked });
$('btnSetboot').onclick = () => cmd('setboot', {
  mode: document.querySelector('input[name=nextboot]:checked').value,
});
$('btnRename').onclick = () => cmd('rename', { prefix: $('edPrefix').value.trim() || 'Client-' });
$('btnPowerOff').onclick = () => cmd('power', { action: 1 });
$('btnReboot').onclick = () => cmd('power', { action: 2 });
$('btnWolAll').onclick = () => cmd('wolAll');
$('btnClearCache').onclick = () => cmd('clearCache');
$('btnSaveAll').onclick = () => cmd('saveConfig', {});

/* ---------------------------------------------------------------- 启停 */

$('btnStart').onclick = async () => {
  if (starting) return;
  starting = true;
  try {
    const r = await api('start', {});
    if (r.data && r.data.need_confirm) {
      const yes = await confirmBox('启动前自检发现问题',
        (r.data.errors || []).map((e) => '· ' + esc(e)).join('<br>')
        + '<br><br>服务仍可启动，但客户机大概率无法完成网启。要继续吗？');
      if (!yes) return;
      await api('start', { force: true });
    }
    poll();
  } finally {
    starting = false;
  }
};

$('btnStop').onclick = async () => {
  await api('stop');
  poll();
};

/* ---------------------------------------------------------------- 初始化 */

['tgFast', 'tgSecure', 'tgProxy2', 'tgStAutopart', 'tgStFormat', 'tgStReboot',
  'tgStClient', 'tgStFixboot', 'tgStUnattend', 'tgStPedrv', 'tgStVnc']
  .forEach((id) => bindToggle($(id), null));

// 流式安装的默认开关状态（与 Python 版一致）
tgSet($('tgStAutopart'), true);
tgSet($('tgStFormat'), true);
tgSet($('tgStFixboot'), true);
tgSet($('tgStUnattend'), true);
tgSet($('tgStClient'), true);
tgSet($('tgStPedrv'), true);
tgSet($('tgStReboot'), true);
tgSet($('tgStVnc'), false);

buildPartRows();
syncRowVisibility();

// 数字键 3/4/5/6 快速选分区数（对齐 Python 版提示）
document.addEventListener('keydown', (ev) => {
  if (document.activeElement && /INPUT|TEXTAREA|SELECT/.test(document.activeElement.tagName)) return;
  const panel = document.querySelector('.panel.active');
  if (!panel || panel.id !== 'panel-part') return;
  if (['3', '4', '5', '6'].includes(ev.key)) {
    const r = document.querySelector(`input[name=partCount][value="${ev.key}"]`);
    if (r) { r.checked = true; onStructureChange(); }
  }
});

poll().then(() => {
  pollTimer = setInterval(poll, 500);
});

/* ---------------------------------------------------------------- 内置 VNC 查看器 */
/* 帧格式（Go 端 /api/vnc/frame 输出）：
     u32 seq | u16 count | count * (u16 x,y,w,h + u32 len + RGBA 像素) */

const vncView = {
  on: false, seq: 0, timer: null, mask: 0, ip: '',
  sending: false, pending: null, w: 0, h: 0,
};

async function vncOpen(ip, password) {
  const r = await api('vncConnect', { ip, port: 5900, password: password || '' });
  if (!r.ok) {
    if (/密码/.test(r.msg)) {
      const pwd = await dialog({
        title: 'VNC 密码',
        html: `<div class="field"><label>密码</label>
            <input type="text" id="vncPwd" style="width:190px" autofocus></div>
          <div class="hint">TightVNC 服务端设了密码时需要填写；填过一次会记住。</div>`,
        buttons: [
          { label: '连接', primary: true, value: () => $('vncPwd').value },
          { label: '取消', value: null },
        ],
      });
      if (pwd !== null && pwd !== undefined) return vncOpen(ip, pwd);
      return;
    }
    await info('连接 VNC 失败', esc(r.msg));
    return;
  }

  vncView.on = true;
  vncView.seq = 0;
  vncView.ip = ip;
  vncView.w = 0;
  vncView.h = 0;
  const cv = $('vncCanvas');
  cv.width = 0;
  cv.height = 0;
  $('vncPanel').classList.remove('hidden');
  $('vncPanel').classList.remove('fullscreen');
  $('vncFull').textContent = '全屏';
  $('vncTitle').textContent = 'VNC · ' + ip;
  $('vncStat').textContent = '连接中…';
  if (r.data) vncApplyStatus(r.data);
  cv.focus();
  vncTick();
}

function vncApplyStatus(st) {
  if (!st) return;
  const cv = $('vncCanvas');
  if (st.width > 0 && (cv.width !== st.width || cv.height !== st.height)) {
    // 只有尺寸真的变了才重建画布（重建会清屏，但服务端随后会补整屏）
    if (vncView.w !== st.width || vncView.h !== st.height) {
      cv.width = st.width;
      cv.height = st.height;
      vncView.w = st.width;
      vncView.h = st.height;
    }
  }
  const size = st.width > 0 ? `${st.width}×${st.height}` : '';
  if (!st.connected) {
    $('vncStat').textContent = '已断开' + (st.error ? '：' + st.error : '');
    return;
  }
  $('vncStat').textContent = size + (st.name ? '  ' + st.name : '');
}

async function vncTick() {
  if (!vncView.on) return;
  try {
    const resp = await fetch('/api/vnc/frame?since=' + vncView.seq);
    if (resp.status === 200) {
      vncDraw(await resp.arrayBuffer());
    } else if (resp.status === 204) {
      // 没有新画面
    } else {
      vncView.on = false;
      $('vncStat').textContent = '画面流中断（HTTP ' + resp.status + '）';
      return;
    }
  } catch (e) {
    /* 忽略瞬时错误，继续拉 */
  }
  if (!vncView.on) return;
  vncView.timer = setTimeout(vncTick, 50);
}

function vncDraw(buf) {
  const dv = new DataView(buf);
  vncView.seq = dv.getUint32(0, true);
  const n = dv.getUint16(4, true);
  const cv = $('vncCanvas');
  const ctx = cv.getContext('2d');

  // 先把所有矩形头读出来：算出需要的画布尺寸，只在开画前 resize 一次
  //（canvas 一旦改 width/height 就会清屏，边画边 resize 会把已画的内容抹掉）
  const rects = [];
  let off = 6;
  let maxX = 0, maxY = 0;
  for (let i = 0; i < n; i++) {
    if (off + 12 > buf.byteLength) break;
    const x = dv.getUint16(off, true);
    const y = dv.getUint16(off + 2, true);
    const w = dv.getUint16(off + 4, true);
    const h = dv.getUint16(off + 6, true);
    const len = dv.getUint32(off + 8, true);
    off += 12;
    if (off + len > buf.byteLength) break;
    rects.push({ x, y, w, h, off, len });
    if (x + w > maxX) maxX = x + w;
    if (y + h > maxY) maxY = y + h;
    off += len;
  }
  if (cv.width < maxX || cv.height < maxY) {
    cv.width = Math.max(cv.width, maxX);
    cv.height = Math.max(cv.height, maxY);
    vncView.w = cv.width;
    vncView.h = cv.height;
  }

  for (const rc of rects) {
    try {
      const px = new Uint8ClampedArray(buf, rc.off, rc.len);
      ctx.putImageData(new ImageData(px, rc.w, rc.h), rc.x, rc.y);
    } catch (e) {
      /* 长度对不上就跳过这块 */
    }
  }
}

function vncCoords(ev) {
  const cv = $('vncCanvas');
  const r = cv.getBoundingClientRect();
  const x = Math.round((ev.clientX - r.left) * cv.width / Math.max(r.width, 1));
  const y = Math.round((ev.clientY - r.top) * cv.height / Math.max(r.height, 1));
  return [
    Math.max(0, Math.min(cv.width - 1, x)),
    Math.max(0, Math.min(cv.height - 1, y)),
  ];
}

function vncPointer(mask, x, y) {
  vncView.pending = { mask, x, y };
  if (vncView.sending) return;
  vncFlushPointer();
}

async function vncFlushPointer() {
  const p = vncView.pending;
  if (!p) return;
  vncView.pending = null;
  vncView.sending = true;
  try {
    await api('vncInput', { kind: 'pointer', mask: p.mask, x: p.x, y: p.y });
  } catch (e) { /* 忽略 */ }
  vncView.sending = false;
  if (vncView.pending) vncFlushPointer();
}

/* 鼠标 → RFB PointerEvent（左1 中2 右4 滚轮上8 下16） */
(function bindVncMouse() {
  const cv = $('vncCanvas');
  cv.addEventListener('mousemove', (ev) => {
    if (!vncView.on) return;
    const [x, y] = vncCoords(ev);
    vncPointer(vncView.mask, x, y);
  });
  cv.addEventListener('mousedown', (ev) => {
    if (!vncView.on) return;
    ev.preventDefault();
    cv.focus();
    const bit = ev.button === 0 ? 1 : ev.button === 1 ? 2 : ev.button === 2 ? 4 : 0;
    vncView.mask |= bit;
    const [x, y] = vncCoords(ev);
    vncPointer(vncView.mask, x, y);
  });
  cv.addEventListener('mouseup', (ev) => {
    if (!vncView.on) return;
    const bit = ev.button === 0 ? 1 : ev.button === 1 ? 2 : ev.button === 2 ? 4 : 0;
    vncView.mask &= ~bit;
    const [x, y] = vncCoords(ev);
    vncPointer(vncView.mask, x, y);
  });
  cv.addEventListener('contextmenu', (ev) => ev.preventDefault());
  cv.addEventListener('wheel', (ev) => {
    if (!vncView.on) return;
    ev.preventDefault();
    const bit = ev.deltaY < 0 ? 8 : 16;
    const [x, y] = vncCoords(ev);
    vncPointer(vncView.mask | bit, x, y);
    setTimeout(() => vncPointer(vncView.mask, x, y), 30);
  }, { passive: false });
})();

/* 键盘 → X11 keysym */
function keysymOf(ev) {
  const k = ev.key;
  const map = {
    Enter: 0xFF0D, Backspace: 0xFF08, Tab: 0xFF09, Escape: 0xFF1B,
    Delete: 0xFFFF, Insert: 0xFF63, Home: 0xFF50, End: 0xFF57,
    PageUp: 0xFF55, PageDown: 0xFF56,
    ArrowLeft: 0xFF51, ArrowUp: 0xFF52, ArrowRight: 0xFF53, ArrowDown: 0xFF54,
    Shift: 0xFFE1, Control: 0xFFE3, Alt: 0xFFE9, Meta: 0xFFEB,
    CapsLock: 0xFFE5, ' ': 0x0020,
  };
  if (map[k] !== undefined) return map[k];
  if (/^F\d{1,2}$/.test(k)) return 0xFFBE + (parseInt(k.slice(1), 10) - 1);
  if (k && k.length === 1) {
    const code = k.codePointAt(0);
    if (code < 128) {
      // 大写字母：X11 用「小写 keysym + Shift」，Shift 事件已经单独发过了
      if (code >= 65 && code <= 90) return code + 32;
      return code;
    }
    return 0x01000000 | code; // Unicode keysym
  }
  return 0;
}

(function bindVncKeys() {
  const cv = $('vncCanvas');
  cv.addEventListener('keydown', (ev) => {
    if (!vncView.on) return;
    const ks = keysymOf(ev);
    if (!ks) return;
    ev.preventDefault();
    api('vncInput', { kind: 'key', keysym: ks, down: true });
  });
  cv.addEventListener('keyup', (ev) => {
    if (!vncView.on) return;
    const ks = keysymOf(ev);
    if (!ks) return;
    ev.preventDefault();
    api('vncInput', { kind: 'key', keysym: ks, down: false });
  });
})();

async function vncCtrlAltDel() {
  const seq = [[0xFFE3, true], [0xFFE9, true], [0xFFFF, true],
               [0xFFFF, false], [0xFFE9, false], [0xFFE3, false]];
  for (const [ks, down] of seq) {
    await api('vncInput', { kind: 'key', keysym: ks, down });
  }
}

function vncStop(hide) {
  vncView.on = false;
  if (vncView.timer) clearTimeout(vncView.timer);
  vncView.timer = null;
  api('vncClose');
  if (hide) $('vncPanel').classList.add('hidden');
  else $('vncStat').textContent = '已断开';
}

$('vncDisc').onclick = () => vncStop(false);
$('vncHide').onclick = () => vncStop(true);
$('vncCad').onclick = () => vncCtrlAltDel();
$('vncFull').onclick = () => {
  const on = $('vncPanel').classList.toggle('fullscreen');
  $('vncFull').textContent = on ? '退出全屏' : '全屏';
};
$('vncFit').onclick = async () => {
  const r = await api('vncStatus');
  if (r.data) vncApplyStatus(r.data);
};
document.addEventListener('keydown', (ev) => {
  if (ev.key === 'Escape' && !$('vncPanel').classList.contains('hidden')) {
    $('vncPanel').classList.add('hidden');
  }
});
