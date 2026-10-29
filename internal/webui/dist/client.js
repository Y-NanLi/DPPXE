/* DP-PXEClient 界面逻辑：
   每 400ms 拉一次 /api/client/state?since=<seq>，增量追加日志。
   窗口是无边框的，最小化/关闭走 /api/client/cmd。 */

const $ = (id) => document.getElementById(id);
const RING = 226.19; // 2πr, r = 36

let lastSeq = 0;
let count = 0;
let atBottom = true;

function esc(s) {
  return String(s == null ? '' : s)
    .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

/* ---------------- 渲染 ---------------- */

function renderInfo(info) {
  const connected = !!info.connected;
  const no = info.no || '未知';
  $('lblNo').textContent = '机器号:' + no;
  $('lblNo').classList.toggle('on', connected && no !== '未知');

  $('lblCliIP').textContent = info.client_ip || '0.0.0.0';
  $('lblCliVer').textContent = info.client_ver || '1.0';
  $('lblSrvIP').textContent = info.server_ip || '0.0.0.0';
  $('lblSrvVer').textContent = info.server_ver || '未知';

  const pct = Math.max(0, Math.min(100, info.progress || 0));
  $('lblPct').textContent = pct + '%';
  $('ringBar').style.strokeDashoffset = String(RING * (1 - pct / 100));

  if (info.name) {
    $('title').textContent = 'DP-PXEClient ' + (info.client_ver || '1.0')
      + '  ·  ' + info.name + (info.mode ? '（' + info.mode + '）' : '');
    document.title = 'DP-PXEClient ' + (info.client_ver || '1.0');
  }
}

function appendLogs(logs) {
  const box = $('log');
  if (!logs || !logs.length) return;
  const frag = document.createDocumentFragment();
  for (const e of logs) {
    count++;
    const row = document.createElement('div');
    row.className = 'row';
    row.innerHTML = `<span class="n">${count}</span>`
      + `<span class="ts">[${esc(e.ts)}]</span>`
      + `<span class="tx">${esc(e.text)}</span>`;
    frag.appendChild(row);
  }
  box.appendChild(frag);
  while (box.childElementCount > 500) box.removeChild(box.firstChild);
  if (atBottom) box.scrollTop = box.scrollHeight;
}

$('log').addEventListener('scroll', () => {
  const box = $('log');
  atBottom = box.scrollTop + box.clientHeight >= box.scrollHeight - 8;
});

/* ---------------- 轮询 ---------------- */

async function poll() {
  try {
    const r = await fetch('/api/client/state?since=' + lastSeq);
    if (!r.ok) return;
    const st = await r.json();
    renderInfo(st.info || {});
    if (st.logs && st.logs.length) {
      appendLogs(st.logs);
      lastSeq = st.last_seq;
    } else if (st.last_seq > lastSeq) {
      lastSeq = st.last_seq;
    }
  } catch (e) {
    /* 后端还没起来时静默重试 */
  }
}

/* ---------------- 窗口按钮 ---------------- */

async function send(cmd) {
  try {
    await fetch('/api/client/cmd', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ cmd }),
    });
  } catch (e) { /* 忽略 */ }
}

$('btnMin').onclick = () => send('minimize');
$('btnClose').onclick = () => send('close');

/* 无边框窗口双击标题栏 = 最大化/还原（与原版一致的手感） */
document.querySelector('.tbar').addEventListener('dblclick', (ev) => {
  if (ev.target.closest('.tbtn')) return;
  send('maximize');
});

poll().then(() => setInterval(poll, 400));
