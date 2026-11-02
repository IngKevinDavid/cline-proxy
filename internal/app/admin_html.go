package app

const adminHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Cline Proxy Admin</title>
<style>
:root{
  --bg:#0d0f12;--bg2:#131519;--bg3:#1a1d23;--panel:rgba(255,255,255,.028);
  --border:rgba(255,255,255,.08);--border-strong:rgba(255,255,255,.16);
  --text:#d6dae2;--text2:#8b919d;--text3:#5d626c;
  --accent:#8ab4f8;--accent2:#7ee2a8;--amber:#d9b04a;--danger:#e0705f;
  --accent-grad:linear-gradient(135deg,#8ab4f8,#a8c7fa);
  --glow:0 0 0 1px rgba(138,180,248,.2);
  --status-active-bg:rgba(126,226,168,.1);--status-cooldown-bg:rgba(217,176,74,.1);
  --status-expired-bg:rgba(224,112,95,.1);
  --btn-primary-bg:#2b4a77;--btn-primary-hover:#34578c;
  --btn-success-bg:#27513c;--btn-success-hover:#2f6249;
  --radius:10px;--radius-sm:7px;
}
[data-theme="light"]{
  --bg:#f6f7f8;--bg2:#ffffff;--bg3:#eef0f2;--panel:#ffffff;
  --border:rgba(15,18,22,.1);--border-strong:rgba(15,18,22,.22);
  --text:#1c1f24;--text2:#5c636e;--text3:#9aa0aa;
  --accent:#33629c;--accent2:#2b7a4b;--amber:#96700f;--danger:#b3442f;
  --accent-grad:linear-gradient(135deg,#33629c,#4a7ab5);
  --glow:0 0 0 1px rgba(51,98,156,.18);
  --status-active-bg:rgba(43,122,75,.1);--status-cooldown-bg:rgba(150,112,15,.1);
  --status-expired-bg:rgba(179,68,47,.08);
  --btn-primary-bg:#33629c;--btn-primary-hover:#2a5284;
  --btn-success-bg:#2b7a4b;--btn-success-hover:#246a40;
}
*{margin:0;padding:0;box-sizing:border-box}
html{-webkit-text-size-adjust:100%}
body{font-family:'Inter','Segoe UI','PingFang SC','Microsoft YaHei',system-ui,sans-serif;background:var(--bg);color:var(--text);font-size:14px;line-height:1.55;min-height:100vh}
.mono,code{font-family:'JetBrains Mono','Cascadia Code','Fira Code',Consolas,monospace;font-size:12px}

/* ===== Layout ===== */
.layout{display:flex;min-height:100vh}
.sidebar{width:236px;background:var(--panel);backdrop-filter:blur(14px);border-right:1px solid var(--border);padding:18px 10px;flex-shrink:0;display:flex;flex-direction:column;position:sticky;top:0;height:100vh;overflow-y:auto}
.sidebar h1{font-size:15px;font-weight:700;padding:2px 10px 16px;border-bottom:1px solid var(--border);margin-bottom:10px;display:flex;align-items:center;gap:8px;letter-spacing:.02em}
.sidebar h1 .logo{width:28px;height:28px;border-radius:7px;background:var(--btn-primary-bg);display:inline-flex;align-items:center;justify-content:center;color:#fff;box-shadow:var(--glow)}
.sidebar h1 .logo svg{width:15px;height:15px}
.sidebar h1 .brand-name{color:var(--text)}
.sidebar h1 .theme-toggle{margin-left:auto;padding:4px 8px}
.sidebar h1 span{color:var(--accent)}
.nav-item{display:flex;align-items:center;gap:10px;padding:8px 12px;border-radius:8px;cursor:pointer;color:var(--text2);transition:.15s;font-size:13.5px;margin-bottom:1px;position:relative}
.nav-item .nav-ico{width:18px;height:18px;display:inline-flex;align-items:center;justify-content:center;flex-shrink:0}
.nav-item .nav-ico svg{width:16px;height:16px;stroke:currentColor;fill:none;stroke-width:1.7;stroke-linecap:round;stroke-linejoin:round}
.nav-item:hover{color:var(--text);background:rgba(255,255,255,.05)}
.nav-item.active{color:var(--text);background:rgba(255,255,255,.07);font-weight:600}
.nav-item.active::before{content:'';position:absolute;left:-10px;top:22%;bottom:22%;width:2px;border-radius:2px;background:var(--accent)}
.nav-item.active .nav-ico svg{stroke:var(--accent)}
.nav-group{font-size:10.5px;font-weight:600;letter-spacing:.12em;text-transform:uppercase;color:var(--text3);padding:14px 12px 5px;user-select:none}
.sidebar-footer{margin-top:auto;padding:12px 8px 4px;font-size:11.5px;color:var(--text3);border-top:1px solid var(--border)}
.sidebar-footer a{color:var(--accent);text-decoration:none}
.main{flex:1;padding:26px 34px 60px;min-width:0;max-width:1500px;margin:0 auto;width:100%}
h2{font-size:21px;margin-bottom:18px;font-weight:700;letter-spacing:.01em}

/* ===== Cards ===== */
.cards{display:grid;grid-template-columns:repeat(auto-fit,minmax(170px,1fr));gap:14px;margin-bottom:26px}
.card{background:var(--panel);border:1px solid var(--border);border-radius:var(--radius);padding:18px;position:relative;overflow:hidden;transition:.2s}
.card:hover{border-color:var(--border-strong)}
.card .num{font-size:30px;font-weight:700;font-variant-numeric:tabular-nums;letter-spacing:-.02em;color:var(--text)}
.card .label{font-size:12px;color:var(--text2);margin-top:5px;display:flex;align-items:center;gap:6px}
.card .label::before{content:'';width:7px;height:7px;border-radius:50%;background:var(--lab-c,var(--accent))}
.card .num.green{color:var(--accent2);--lab-c:var(--accent2)}
.card .num.red{color:var(--danger);--lab-c:var(--danger)}
.card .num.yellow{color:var(--amber);--lab-c:var(--amber)}
.card .num.blue{color:var(--accent);--lab-c:var(--accent)}
.card .num.purple{color:#a78bfa;--lab-c:#a78bfa}
.cards .card{animation:rise .45s ease both}
.cards .card:nth-child(1){animation-delay:.02s}
.cards .card:nth-child(2){animation-delay:.08s}
.cards .card:nth-child(3){animation-delay:.14s}
.cards .card:nth-child(4){animation-delay:.2s}
.cards .card:nth-child(5){animation-delay:.26s}
@keyframes rise{from{opacity:0;transform:translateY(10px)}to{opacity:1;transform:none}}

/* ===== Sections ===== */
.section{background:var(--panel);border:1px solid var(--border);border-radius:var(--radius);margin-bottom:22px;overflow:hidden;animation:rise .4s ease both}
.section-title{padding:13px 18px;border-bottom:1px solid var(--border);font-weight:600;font-size:14px;display:flex;align-items:center;gap:8px}
.section-title .sec-ico{width:17px;height:17px;display:inline-flex;align-items:center;justify-content:center;flex-shrink:0}
.section-title .sec-ico svg{width:15px;height:15px;stroke:var(--text2);fill:none;stroke-width:1.7;stroke-linecap:round;stroke-linejoin:round}
.section-body{padding:18px}
.tabs{display:flex;border-bottom:1px solid var(--border);padding:0 8px;gap:4px;overflow-x:auto}
.tab{padding:11px 18px;cursor:pointer;color:var(--text2);border-bottom:2px solid transparent;font-size:13px;white-space:nowrap;transition:.15s}
.tab:hover{color:var(--text);background:rgba(255,255,255,.04)}
.tab.active{color:var(--text);border-bottom-color:var(--accent);font-weight:600}
.tab-content{display:none;padding:18px}
.tab-content.active{display:block;animation:rise .25s ease both}

/* ===== Tables ===== */
.table-wrap{overflow-x:auto}
table{width:100%;border-collapse:collapse}
th,td{text-align:left;padding:10px 14px;border-bottom:1px solid var(--border);font-size:13px;white-space:nowrap}
th{color:var(--text3);font-weight:600;font-size:11.5px;text-transform:uppercase;letter-spacing:.06em}
tbody tr{transition:.12s}
tbody tr:hover{background:rgba(255,255,255,.035)}
tbody tr:last-child td{border-bottom:none}

/* ===== Status badges ===== */
.status{display:inline-flex;align-items:center;gap:6px;padding:3px 10px;border-radius:999px;font-size:12px;font-weight:600}
.status.active{background:var(--status-active-bg);color:var(--accent2)}
.status.cooldown{background:var(--status-cooldown-bg);color:var(--amber)}
.status.expired{background:var(--status-expired-bg);color:var(--danger)}
.status-dot{width:7px;height:7px;border-radius:50%;display:inline-block}
.status-dot.active{background:var(--accent2)}
.status-dot.cooldown{background:var(--amber)}
.status-dot.expired{background:var(--danger)}

/* ===== Buttons ===== */
.btn{display:inline-flex;align-items:center;justify-content:center;gap:6px;padding:7px 15px;border:1px solid var(--border);border-radius:var(--radius-sm);background:rgba(255,255,255,.04);color:var(--text);cursor:pointer;font-size:13px;transition:.15s;text-decoration:none;font-family:inherit;white-space:nowrap}
.btn:hover{background:rgba(255,255,255,.09);border-color:var(--border-strong)}
.btn:active{transform:none}
.btn svg{width:14px;height:14px;stroke:currentColor;fill:none;stroke-width:1.8;stroke-linecap:round;stroke-linejoin:round;flex-shrink:0}
.btn-primary{background:var(--btn-primary-bg);border-color:transparent;color:#fff;font-weight:600}
.btn-primary:hover{background:var(--btn-primary-hover)}
.btn-success{background:var(--btn-success-bg);border-color:transparent;color:#fff;font-weight:600}
.btn-success:hover{background:var(--btn-success-hover)}
.btn-danger{border-color:rgba(224,112,95,.4);color:var(--danger);background:transparent}
.btn-danger:hover{background:rgba(224,112,95,.1);border-color:var(--danger)}
.btn-sm{padding:3px 10px;font-size:12px;border-radius:6px}
.btn-sm svg{width:13px;height:13px}

/* ===== Forms ===== */
input,textarea,select{width:100%;padding:9px 13px;background:rgba(0,0,0,.25);border:1px solid var(--border);border-radius:var(--radius-sm);color:var(--text);font-size:13px;font-family:inherit;transition:.15s}
[data-theme="light"] input,[data-theme="light"] textarea,[data-theme="light"] select{background:#fff}
input::placeholder,textarea::placeholder{color:var(--text3)}
input:focus,textarea:focus,select:focus{outline:none;border-color:var(--accent);box-shadow:0 0 0 3px rgba(138,180,248,.12)}
textarea{resize:vertical;min-height:84px;font-family:'JetBrains Mono','Cascadia Code',Consolas,monospace;font-size:12px}
select{cursor:pointer;appearance:none;background-image:linear-gradient(45deg,transparent 50%,var(--text2) 50%),linear-gradient(135deg,var(--text2) 50%,transparent 50%);background-position:calc(100% - 18px) 55%,calc(100% - 13px) 55%;background-size:5px 5px;background-repeat:no-repeat;padding-right:32px}
.form-row{display:flex;gap:14px;align-items:flex-end;margin-bottom:14px;flex-wrap:wrap}
.form-row .field{flex:1;min-width:180px}
.form-row .field label{display:block;font-size:12px;color:var(--text2);margin-bottom:6px;font-weight:500}
.form-actions{display:flex;gap:10px;margin-top:14px;flex-wrap:wrap}
.flex{display:flex;align-items:center;gap:8px}
.gap-4{gap:4px}
.text-right{text-align:right}
.mt-8{margin-top:8px}
.inline-flex{display:inline-flex;align-items:center;gap:6px}
.justify-between{display:flex;justify-content:space-between;align-items:center;gap:10px;flex-wrap:wrap}
.hint{font-size:12px;color:var(--text2);margin-top:8px;line-height:1.6}
.hint strong{color:var(--text)}

/* ===== Dashboard: quick start & endpoints ===== */
.steps{display:flex;flex-direction:column;gap:0}
.step{display:flex;gap:14px;position:relative;padding-bottom:18px}
.step:last-child{padding-bottom:0}
.step::before{content:'';position:absolute;left:13px;top:30px;bottom:2px;width:2px;background:var(--border);border-radius:2px}
.step:last-child::before{display:none}
.step-no{width:27px;height:27px;flex-shrink:0;border-radius:50%;border:1px solid var(--border-strong);color:var(--text2);font-weight:600;font-size:12.5px;display:flex;align-items:center;justify-content:center;background:rgba(255,255,255,.03)}
.step-title{font-weight:600;font-size:13.5px;margin-top:3px}
.step-desc{font-size:12.5px;color:var(--text2);margin-top:3px;line-height:1.6}
.step-desc code{background:rgba(255,255,255,.07);padding:1px 7px;border-radius:5px}
.endpoint-list{display:flex;flex-direction:column;gap:8px}
.endpoint{display:flex;align-items:center;gap:12px;padding:9px 13px;background:rgba(255,255,255,.025);border:1px solid var(--border);border-radius:8px;flex-wrap:wrap}
.endpoint code{background:rgba(138,180,248,.08);border:1px solid rgba(138,180,248,.2);color:var(--accent);padding:3px 10px;border-radius:6px;font-size:11.5px;white-space:nowrap}
.endpoint span{font-size:12px;color:var(--text2);flex:1;min-width:160px}
.danger-zone{border-color:rgba(224,112,95,.28)}
.danger-zone .section-title{color:var(--danger)}
.auto-pill{font-size:11px;color:var(--text2);background:rgba(255,255,255,.05);border:1px solid var(--border);border-radius:999px;padding:2px 10px;white-space:nowrap}
.auto-pill.paused{color:var(--text3);border-color:var(--border)}

/* ===== Toast ===== */
.toast{position:fixed;top:22px;right:22px;padding:12px 20px;border-radius:12px;color:#fff;z-index:9999;opacity:0;transform:translateY(-12px) scale(.97);transition:.3s cubic-bezier(.2,.9,.3,1.2);font-size:13px;max-width:420px;backdrop-filter:blur(12px);border:1px solid rgba(255,255,255,.14);box-shadow:0 12px 40px rgba(2,6,23,.5);white-space:pre-line}
.toast.show{opacity:1;transform:none}
.toast.success{background:#2b5a44}
.toast.error{background:#7a3a30}
.toast.info{background:#31465e}
.toast.warning{background:#6e5619}

/* ===== Misc ===== */
.loading{display:inline-block;width:14px;height:14px;border:2px solid var(--text3);border-top-color:var(--accent);border-radius:50%;animation:spin .7s linear infinite;vertical-align:-2px}
@keyframes spin{to{transform:rotate(360deg)}}
.empty{padding:30px;text-align:center;color:var(--text2)}
.empty-state{padding:44px 20px;text-align:center;color:var(--text2)}
.empty-state .icon{font-size:40px;margin-bottom:10px;display:block;opacity:.8}
.key-display{background:rgba(0,0,0,.3);padding:9px 13px;border-radius:var(--radius-sm);border:1px solid var(--border);font-family:'JetBrains Mono','Cascadia Code',Consolas,monospace;font-size:12px;word-break:break-all;cursor:pointer;transition:.15s}
.key-display:hover{border-color:var(--accent)}
.copy-icon{cursor:pointer;color:var(--text2);padding:2px 6px;border-radius:4px}
.copy-icon:hover{color:var(--text);background:var(--bg3)}
.model-tag{display:inline-block;padding:2px 9px;border-radius:6px;font-size:11px;background:rgba(255,255,255,.06);color:var(--text2);margin:2px;letter-spacing:.02em}
.model-tag.free{border:1px solid rgba(126,226,168,.35);color:var(--accent2);background:rgba(126,226,168,.07)}
.model-tag.pass{border:1px solid rgba(217,176,74,.35);color:var(--amber);background:rgba(217,176,74,.07)}
.theme-toggle{display:inline-flex;align-items:center;gap:5px;padding:4px 9px;border:1px solid var(--border);border-radius:7px;background:rgba(255,255,255,.04);color:var(--text2);cursor:pointer;font-size:12px;transition:.15s;font-family:inherit}
.theme-toggle:hover{color:var(--text);background:rgba(255,255,255,.09)}
[data-theme="dark"] .theme-toggle .light-label{display:none}
body:not([data-theme="dark"]) .theme-toggle .dark-label{display:none}
.probe-pill{font-size:11px;color:var(--text3)}
.oauth-card{border:1px solid var(--border);border-radius:10px;padding:16px;background:rgba(255,255,255,.03);margin-top:14px}
.stat-mini{font-family:'JetBrains Mono',Consolas,monospace;font-size:12px;color:var(--text2)}

/* ===== Responsive ===== */
@media (max-width:980px){
  .layout{flex-direction:column}
  .sidebar{width:100%;height:auto;position:sticky;top:0;flex-direction:row;align-items:center;padding:10px 12px;overflow-x:auto;gap:4px;z-index:50}
  .sidebar h1{display:flex;align-items:center;border-bottom:none;margin:0;padding:0 6px 0 0;gap:6px;font-size:13px;white-space:nowrap}
  .sidebar h1 .brand-name{display:none}
  .sidebar .nav-item{padding:7px 11px;white-space:nowrap}
  .sidebar .nav-item.active::before{display:none}
  .nav-group{display:none}
  .sidebar .theme-toggle{margin-left:4px}
  .sidebar-footer{display:none}
  .main{padding:20px 16px 48px}
}
@media (max-width:560px){
  .cards{grid-template-columns:repeat(2,1fr)}
  h2{font-size:18px}
  .section-title{padding:11px 14px}
  .section-body{padding:14px}
  .form-row .field{min-width:100%}
}
</style>
</head>
<body>
<div class="layout">
<div class="sidebar">
<h1><span class="logo"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M13 2 3 14h7l-1 8 10-12h-7l1-8z"/></svg></span><span class="brand-name">Cline Proxy</span><button class="theme-toggle" onclick="toggleTheme()" title="Toggle theme"><span class="icon" id="themeIcon"></span><span class="light-label">Light</span><span class="dark-label">Dark</span></button></h1>
<div class="nav-item active" data-tab="dashboard"><span class="nav-ico"><svg viewBox="0 0 24 24"><rect x="3" y="3" width="7" height="9" rx="1.5"/><rect x="14" y="3" width="7" height="5" rx="1.5"/><rect x="14" y="12" width="7" height="9" rx="1.5"/><rect x="3" y="16" width="7" height="5" rx="1.5"/></svg></span> Dashboard</div>
<div class="nav-group">Account pool</div>
<div class="nav-item" data-tab="accounts"><span class="nav-ico"><svg viewBox="0 0 24 24"><circle cx="12" cy="8" r="4"/><path d="M4 21c0-4 3.6-6.5 8-6.5s8 2.5 8 6.5"/></svg></span> Accounts</div>
<div class="nav-item" data-tab="import"><span class="nav-ico"><svg viewBox="0 0 24 24"><path d="M12 3v10m0 0 4-4m-4 4-4-4"/><path d="M4 17v2a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-2"/></svg></span> Add accounts</div>
<div class="nav-group">Services</div>
<div class="nav-item" data-tab="settings"><span class="nav-ico"><svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .34 1.87l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.7 1.7 0 0 0-1.87-.34 1.7 1.7 0 0 0-1 1.55V21a2 2 0 1 1-4 0v-.09a1.7 1.7 0 0 0-1-1.55 1.7 1.7 0 0 0-1.87.34l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.7 1.7 0 0 0 .34-1.87 1.7 1.7 0 0 0-1.55-1H3a2 2 0 1 1 0-4h.09a1.7 1.7 0 0 0 1.55-1 1.7 1.7 0 0 0-.34-1.87l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.7 1.7 0 0 0 1.87.34h0a1.7 1.7 0 0 0 1-1.55V3a2 2 0 1 1 4 0v.09a1.7 1.7 0 0 0 1 1.55h0a1.7 1.7 0 0 0 1.87-.34l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.7 1.7 0 0 0-.34 1.87v0a1.7 1.7 0 0 0 1.55 1H21a2 2 0 1 1 0 4h-.09a1.7 1.7 0 0 0-1.55 1z"/></svg></span> Gateway settings</div>
<div class="nav-item" data-tab="logs"><span class="nav-ico"><svg viewBox="0 0 24 24"><path d="M8 6h13M8 12h13M8 18h13"/><path d="M3 6h.01M3 12h.01M3 18h.01"/></svg></span> Request logs</div>
<div class="nav-item" data-tab="proxypool"><span class="nav-ico"><svg viewBox="0 0 24 24"><circle cx="12" cy="5" r="2.5"/><circle cx="5" cy="19" r="2.5"/><circle cx="19" cy="19" r="2.5"/><path d="M12 7.5v4m0 0-5.5 5m5.5-5 5.5 5"/></svg></span> Proxy & Egress (WARP/Tor)</div>
<div class="nav-item" data-tab="opencode"><span class="nav-ico"><svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3a14.5 14.5 0 0 1 0 18 14.5 14.5 0 0 1 0-18z"/></svg></span> opencode free models</div>
<div class="nav-item" data-tab="combos"><span class="nav-ico"><svg viewBox="0 0 24 24"><path d="M8 3v5a4 4 0 0 1-4 4 4 4 0 0 1 4 4v5M16 3v5a4 4 0 0 0 4 4 4 4 0 0 0-4 4v5"/></svg></span> Combos</div>
<div class="sidebar-footer">
  <div>Admin panel: <a href="/admin/">/admin/</a></div>
  <div>API address: <span id="footerApiAddr">http://127.0.0.1:3457</span></div>
</div>
</div>

<div class="main">

<div id="tab-dashboard" class="tab-panel">
<h2>Dashboard</h2>
<div class="cards">
  <div class="card"><div class="num blue" id="statTotal">-</div><div class="label">Total accounts</div></div>
  <div class="card"><div class="num green" id="statActive">-</div><div class="label">Active</div></div>
  <div class="card"><div class="num yellow" id="statCooldown">-</div><div class="label">Cooldown</div></div>
  <div class="card"><div class="num red" id="statExpired">-</div><div class="label">Expired</div></div>
  <div class="card"><div class="num purple" id="statStrategy" style="font-size:24px;text-transform:uppercase">FILL</div><div class="label">Scheduling strategy</div></div>
</div>
<div class="section">
  <div class="section-title"><span class="sec-ico"><svg viewBox="0 0 24 24"><path d="M13 2 3 14h7l-1 8 10-12h-7l1-8z"/></svg></span> Quick actions</div>
  <div class="section-body" style="display:flex;gap:10px;flex-wrap:wrap">
    <button class="btn btn-primary" onclick="switchTab('import')">Add account</button>
    <button class="btn" onclick="refreshAllTokens()">Refresh all tokens</button>
    <button class="btn" onclick="document.getElementById('fileInput').click()">Import from file</button>
    <input type="file" id="fileInput" accept=".json,.txt" style="display:none" onchange="handleFileImport(event)">
    <button class="btn" onclick="switchTab('settings');generateKey()">Generate API key</button>
  </div>
</div>
<div class="section">
  <div class="section-title"><span class="sec-ico"><svg viewBox="0 0 24 24"><path d="M4 19.5A2.5 2.5 0 0 1 6.5 17H20"/><path d="M6.5 2H20v20H6.5A2.5 2.5 0 0 1 4 19.5v-15A2.5 2.5 0 0 1 6.5 2z"/></svg></span> Quick start</div>
  <div class="section-body">
    <div class="steps">
      <div class="step"><div class="step-no">1</div><div class="step-body"><div class="step-title">Add Cline accounts</div><div class="step-desc">On the Add accounts page: browser OAuth, a refreshToken, or a static API key (<code>sk_...</code>) — accounts are the proxy's upstream quota.</div></div></div>
      <div class="step"><div class="step-no">2</div><div class="step-body"><div class="step-title">Generate an API key</div><div class="step-desc">Generate a key under Proxy settings → API keys to authenticate clients; with no keys configured, unauthenticated access is allowed.</div></div></div>
      <div class="step"><div class="step-no">3</div><div class="step-body"><div class="step-title">Configure your client</div><div class="step-desc">Set the Base URL to <code id="quickstartApi">http://127.0.0.1:3457/v1</code> and pick any model ID from the available models list.</div></div></div>
    </div>
  </div>
</div>
<div class="section">
  <div class="section-title"><span class="sec-ico"><svg viewBox="0 0 24 24"><path d="M9 12h6M12 9v6"/><circle cx="12" cy="12" r="9"/></svg></span> Endpoints</div>
  <div class="section-body">
    <div class="endpoint-list">
      <div class="endpoint"><code>POST /v1/chat/completions</code><span>OpenAI Chat format — works with most tools out of the box</span></div>
      <div class="endpoint"><code>POST /v1/messages</code><span>Anthropic Messages format — for Claude Code / Cline and similar clients</span></div>
      <div class="endpoint"><code>POST /v1/responses</code><span>OpenAI Responses format — for Cursor and similar clients</span></div>
      <div class="endpoint"><code>GET /v1/models</code><span>Model list</span></div>
    </div>
  </div>
</div>
</div>

<div id="tab-accounts" class="tab-panel" style="display:none">
  <div class="flex justify-between" style="margin-bottom:16px;align-items:center">
  <div style="display:flex;align-items:center;gap:12px">
    <h2 style="margin-bottom:0">Accounts</h2>
    <span class="badge" id="accountStrategyBadge" style="font-size:12px;font-weight:600;padding:3px 10px;border-radius:6px;border:1px solid var(--border);background:rgba(167,139,250,0.12);color:#a78bfa">Strategy: <span id="accountStrategyText">fill</span></span>
  </div>
  <div style="display:flex;gap:8px">
    <button class="btn btn-sm" onclick="exportAccounts()">Export accounts</button>
    <button class="btn btn-primary btn-sm" onclick="switchTab('import')">Add</button>
    <button class="btn btn-sm" onclick="loadAccounts()">Refresh</button>
  </div>
</div>
<div class="hint" style="margin:-4px 0 16px;padding:11px 14px;border:1px solid var(--border);border-radius:10px;background:rgba(148,163,184,.05)">
  <strong style="color:var(--text)">Tokens</strong> are counted locally by this proxy (input+output; exact when upstream returns usage, otherwise estimated from the request body) to gauge headroom before official rate limits. The Test button sends a real probe request. The Reset button <strong style="color:var(--text)">probes upstream rate-limit status</strong>: if still limited it stays in cooldown and shows the estimated recovery time; only a passing probe lifts the cooldown and resets today's stats.
</div>
<div class="section">
  <div class="section-body" style="padding:6px">
    <div class="table-wrap">
    <table>
      <thead>
        <tr><th>Email</th><th>Status</th><th title="Counted locally by this proxy — not the official free quota">Today/Total tokens</th><th>Last used</th><th>Created</th><th>Actions</th></tr>
      </thead>
      <tbody id="accountTableBody">
        <tr><td colspan="6" class="empty">Loading...</td></tr>
      </tbody>
    </table>
    </div>
  </div>
</div>
</div>

<div id="tab-import" class="tab-panel" style="display:none">
<h2>Add accounts</h2>
<div class="section">
  <div class="tabs" id="importTabs">
    <div class="tab active" data-tab="oauth">OAuth browser login</div>
    <div class="tab" data-tab="token">Manual entry</div>
    <div class="tab" data-tab="batch">Batch import</div>
  </div>

  <div id="import-oauth" class="tab-content active">
    <p class="hint">Cline accounts via browser OAuth — Google/GitHub/email sign-in supported; the refreshToken is captured automatically.</p>
    <div class="form-actions">
      <button class="btn btn-primary" onclick="startOAuth()" id="oauthBtn">Start OAuth login</button>
    </div>
    <div id="oauthProgress" style="display:none;margin-top:14px" class="oauth-card">
      <div style="display:flex;align-items:center;gap:14px">
        <div class="loading"></div>
        <div>
          <div style="font-weight:600" id="oauthStatus">Waiting for browser authorization...</div>
          <div class="hint">
            Open <a href="#" id="oauthUrl" target="_blank" style="color:var(--accent)"></a>
            and enter the code: <strong style="color:var(--accent);font-size:15px;letter-spacing:2px" id="oauthUserCode"></strong>
          </div>
        </div>
      </div>
    </div>
    <div id="oauthResult" style="display:none;margin-top:14px"></div>
  </div>

  <div id="import-token" class="tab-content">
    <p class="hint">Paste a Cline OAuth refreshToken or a static Cline API key (<code>sk_...</code>) — the type is detected automatically. API keys are used as-is (no validation call); refresh tokens are verified against the upstream first.</p>
    <div class="form-row">
      <div class="field">
        <label>Token or API key *</label>
        <input type="text" id="tokenInput" placeholder="workos-... refreshToken or sk_... API key" style="font-family:'JetBrains Mono',Consolas,monospace">
      </div>
      <div class="field">
        <label>Email / label (optional; auto-generated if empty)</label>
        <input type="text" id="tokenEmail" placeholder="user@example.com">
      </div>
    </div>
    <div class="form-actions">
      <button class="btn btn-primary" onclick="addByToken()">Add account</button>
    </div>
    <div id="tokenResult" style="margin-top:8px"></div>
  </div>

  <div id="import-batch" class="tab-content">
    <p class="hint">Import multiple accounts at once. Accepts a JSON array (entries with <code>refreshToken</code> or <code>apiToken</code>) or one credential per line — lines starting with <code>sk_</code> are treated as API keys, everything else as refresh tokens.</p>
    <div class="form-row">
      <div class="field">
        <label>JSON array or one credential per line</label>
        <textarea id="batchInput" placeholder='[{"refreshToken":"xxx","email":"u1@x.com"},{"apiToken":"sk_yyy","email":"u2@x.com"}]'></textarea>
      </div>
    </div>
    <div class="form-actions">
      <button class="btn btn-primary" onclick="batchImport()">Import all</button>
      <button class="btn" onclick="document.getElementById('fileInput2').click()">Choose file</button>
      <input type="file" id="fileInput2" accept=".json,.txt" style="display:none" onchange="handleFileImport(event)">
    </div>
    <div id="batchResult" style="margin-top:8px"></div>
  </div>
</div>
</div>

<div id="tab-proxypool" class="tab-panel" style="display:none">
<h2>Proxy & Egress Network (WARP & Tor)</h2>

<div class="section">
  <div class="section-title"><span class="sec-ico"><svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="10"/><path d="M12 2a14.5 14.5 0 0 0 0 20 14.5 14.5 0 0 0 0-20zM2 12h20"/></svg></span> Egress Network & Automatic/Manual IP Rotation</div>
  <div class="section-body">
    <div class="hint" style="margin-bottom:12px">Monitor real-time exit IPs for Direct connection, Embedded Tor daemon, and Cloudflare WARP. Manual and auto-failover (429 defense) rotations are tracked here.</div>
    <div class="cards" style="grid-template-columns:repeat(3,1fr);margin-bottom:16px">
      <!-- Direct IP Card -->
      <div class="card">
        <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:4px">
          <div class="label">Direct Connection</div>
          <span class="badge" style="font-size:11px;padding:2px 8px;border-radius:4px;background:rgba(59,130,246,0.15);color:#3b82f6">Direct</span>
        </div>
        <div class="num blue" id="egressDirectIp" style="font-size:18px;font-family:monospace">-</div>
        <div class="hint" style="margin-top:6px">Default machine egress</div>
      </div>
      <!-- Embedded Tor Card -->
      <div class="card">
        <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:4px">
          <div class="label">Embedded Tor (:9052)</div>
          <span id="egressTorBadge" class="badge" style="font-size:11px;padding:2px 8px;border-radius:4px;background:rgba(34,197,94,0.15);color:#22c55e">Checking...</span>
        </div>
        <div class="num purple" id="egressTorIp" style="font-size:18px;font-family:monospace">-</div>
        <div style="display:flex;gap:6px;margin-top:10px;align-items:center;flex-wrap:wrap">
          <button class="btn btn-sm btn-primary" id="btnRotateTor" onclick="rotateTorManual()">Rotate Tor IP</button>
          <span class="hint" id="egressTorRotates" style="font-size:11px">Rotations: 0</span>
        </div>
      </div>
      <!-- Cloudflare WARP Card -->
      <div class="card">
        <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:4px">
          <div class="label">Cloudflare WARP (:40000)</div>
          <span id="egressWarpBadge" class="badge" style="font-size:11px;padding:2px 8px;border-radius:4px;background:rgba(168,85,247,0.15);color:#a855f7">Checking...</span>
        </div>
        <div class="num green" id="egressWarpIp" style="font-size:18px;font-family:monospace">-</div>
        <div style="display:flex;gap:6px;margin-top:10px;align-items:center;flex-wrap:wrap">
          <button class="btn btn-sm btn-primary" id="btnRotateWarp" onclick="rotateWarpManual()">Rotate WARP IP</button>
          <span class="hint" id="egressWarpRotates" style="font-size:11px">Rotations: 0</span>
        </div>
      </div>
    </div>

    <!-- History Table -->
    <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:8px">
      <strong style="font-size:13px">IP Rotation Event Log (Auto & Manual)</strong>
      <button class="btn btn-sm" onclick="loadEgressStatus(true)">Refresh IPs</button>
    </div>
    <div class="table-wrap">
      <table>
        <thead>
          <tr><th style="width:140px">Time</th><th style="width:90px">Egress</th><th style="width:100px">Trigger</th><th>IP Change</th><th style="width:80px">Status</th><th>Details</th></tr>
        </thead>
        <tbody id="egressHistoryBody">
          <tr><td colspan="6" class="empty">No rotation events recorded yet</td></tr>
        </tbody>
      </table>
    </div>
  </div>
</div>
<div class="section">
  <div class="section-title"><span class="sec-ico"><svg viewBox="0 0 24 24"><circle cx="12" cy="5" r="2.5"/><circle cx="5" cy="19" r="2.5"/><circle cx="19" cy="19" r="2.5"/><path d="M12 7.5v4m0 0-5.5 5m5.5-5 5.5 5"/></svg></span> Shared egress proxies</div>
  <div class="section-body">
    <div class="hint" style="margin-bottom:10px">One proxy list serves every upstream that opts in below. One proxy per line: <code>http://user:pass@host:port</code> or <code>socks5://host:port</code>. Requests rotate across healthy proxies per request; a proxy that hits a rate limit is cooled down and skipped automatically.</div>
    <div class="form-row">
      <div class="field" style="flex:3"><label>Proxy list</label>
        <textarea id="ppProxies" rows="4" placeholder="one per line: http://user:pass@host:port or socks5://host:port"></textarea>
      </div>
      <div class="field"><label>Rotation strategy</label>
        <select id="ppStrategy"><option value="round_robin">Round-robin (round_robin)</option><option value="random">Random (random)</option><option value="fill">Fill (fill)</option></select>
      </div>
    </div>
    <div class="form-actions"><button class="btn btn-primary" onclick="saveProxyPool()">Save proxy pool</button></div>
    <div id="ppSaveResult" style="margin-top:8px"></div>
  </div>
</div>
<div class="section">
  <div class="section-title"><span class="sec-ico"><svg viewBox="0 0 24 24"><path d="M12 5v14M5 12h14"/></svg></span> Where this pool is used</div>
  <div class="section-body">
    <div class="form-row">
      <div class="field"><label>Cline upstream</label>
        <div style="display:flex;gap:6px;align-items:center">
          <select id="ppCline" style="flex:1"><option value="true">Use proxy pool</option><option value="false">Direct connection</option></select>
          <button class="btn btn-sm btn-primary" onclick="saveClineProxies()">Save</button>
        </div>
        <div class="hint">Off (direct) by default. The <code>CLINE_USE_PROXIES=true</code> env var always forces this on. Individual Combos can also opt in from the Combos page.</div>
      </div>
      <div class="field"><label>opencode zen upstream</label>
        <div class="hint" style="margin-top:6px" id="ppZenState">-</div>
        <div class="hint">Zen automatically routes through the pool above whenever the list is non-empty — no separate switch needed.</div>
      </div>
    </div>
  </div>
</div>
<div class="section">
  <div class="section-title"><span class="sec-ico"><svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="9"/><path d="M12 8v4m0 4h.01"/></svg></span> Cooldown status</div>
  <div class="section-body">
    <div class="hint" id="ppCooldownInfo" style="margin:0">-</div>
  </div>
</div>
</div>

<div id="tab-settings" class="tab-panel" style="display:none">
<h2>Gateway settings</h2>

<div class="section">
  <div class="section-title"><span class="sec-ico"><svg viewBox="0 0 24 24"><path d="M21 2l-2 2m-7.6 7.6a5.5 5.5 0 1 1-7.78 7.78 5.5 5.5 0 0 1 7.78-7.78zm0 0L15.5 7.5m0 0l3 3L22 7l-3-3m-3.5 3.5L19 4"/></svg></span> API keys</div>
  <div class="section-body">
    <p class="hint">Generated keys authenticate client access to the proxy API (sent as the x-api-key or Authorization header).</p>
    <div class="form-actions" style="margin-bottom:14px">
      <button class="btn btn-success" onclick="generateKey()">Generate new key</button>
    </div>
    <div id="keysList"></div>
    <div id="keyGenResult" style="margin-top:8px"></div>
  </div>
</div>

<div class="section">
  <div class="section-title"><span class="sec-ico"><svg viewBox="0 0 24 24"><rect x="2" y="7" width="20" height="14" rx="2"/><path d="M16 7V5a2 2 0 0 0-2-2h-4a2 2 0 0 0-2 2v2"/><path d="M12 12v3M8.5 13.5v0M15.5 13.5v0"/></svg></span> Available models <span id="modelsProbeInfo" class="probe-pill" style="font-weight:normal"></span></div>
  <div class="section-body">
    <div class="flex" style="margin-bottom:10px;gap:10px">
      <button class="btn btn-sm btn-primary" onclick="refreshModels()">Refresh models</button>
      <span class="hint" style="margin:0">Auto-syncs the official upstream free-model feed (60s); only quota-free models are shown</span>
    </div>
    <div id="modelsList">Loading...</div>
  </div>
</div>

<div class="section">
  <div class="section-title"><span class="sec-ico"><svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="3"/><path d="M12 2v4m0 12v4M2 12h4m12 0h4M4.9 4.9l2.8 2.8m8.6 8.6 2.8 2.8m0-14.2-2.8 2.8M7.7 16.3l-2.8 2.8"/></svg></span> General config</div>
  <div class="section-body">
    <div class="form-row">
      <div class="field"><label>Listen address</label><input type="text" id="settingAddr" disabled></div>
      <div class="field">
        <label>Default model</label>
        <div style="display:flex;gap:6px;align-items:center">
          <select id="settingDefModel" style="flex:1;font-family:'JetBrains Mono',Consolas,monospace"></select>
          <button class="btn btn-sm btn-primary" onclick="saveDefaultModel()">Save</button>
        </div>
      </div>
    </div>
    <div class="form-row">
      <div class="field">
        <label>Scheduling strategy</label>
        <select id="settingStrategy" onchange="updateConfig()">
          <option value="fill">Fill (fill — exhausts one account before rotating)</option>
          <option value="round_robin">Round-robin (round_robin — rotates accounts evenly)</option>
          <option value="random">Random (random)</option>
        </select>
      </div>
      <div class="field"><label>Engine version</label><input type="text" id="settingVersion" disabled></div>
    </div>
    <div class="form-row">
      <div class="field"><label>Accounts file</label><input type="text" id="settingPoolPath" disabled></div>
    </div>
  </div>
</div>

<div class="section">
  <div class="section-title"><span class="sec-ico"><svg viewBox="0 0 24 24"><path d="M4 7h16M4 12h16M4 17h10"/></svg></span> Request headers (mimicking the Cline CLI)</div>
  <div class="section-body">
    <div class="table-wrap">
    <table>
      <thead><tr><th style="width:220px">Header</th><th>Value</th><th style="width:40px"></th></tr></thead>
      <tbody id="headersTableBody">
        <tr><td colspan="3" class="empty">Loading...</td></tr>
      </tbody>
    </table>
    </div>
    <div class="form-actions">
      <button class="btn btn-sm" onclick="addHeaderRow()">Add header</button>
      <button class="btn btn-sm btn-primary" onclick="saveHeaders()">Save headers</button>
    </div>
    <div class="hint">These headers are attached to every request forwarded to the Cline API to mimic the official client.</div>
    <div id="headerSaveResult" style="margin-top:8px"></div>
  </div>
</div>

<div class="section danger-zone">
  <div class="section-title"><span class="sec-ico"><svg viewBox="0 0 24 24"><path d="M3 6h18M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2m3 0v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6"/></svg></span> Danger zone</div>
  <div class="section-body">
    <div style="display:flex;gap:10px;flex-wrap:wrap">
      <button class="btn btn-danger" onclick="deleteAllAccounts()">Delete all accounts</button>
      <button class="btn btn-danger" onclick="deleteAllKeys()">Delete all keys</button>
    </div>
  </div>
</div>
</div>

<div id="tab-logs" class="tab-panel" style="display:none">
<div class="flex justify-between" style="margin-bottom:16px">
  <h2>Request logs <span class="probe-pill" style="font-weight:normal">last 500 entries, persisted to data/requests.jsonl</span></h2>
  <div style="display:flex;gap:8px;align-items:center">
    <span class="auto-pill" id="logsAutoPill">Auto-refreshing</span>
    <button class="btn btn-sm" onclick="toggleLogsAuto()" id="logsAutoBtn">Pause</button>
    <button class="btn btn-sm" onclick="loadLogs()">Refresh</button>
  </div>
</div>
<div class="section">
  <div class="section-body" style="padding:6px">
    <div class="table-wrap">
    <table>
      <thead>
        <tr><th>Time</th><th>Source</th><th>Method</th><th>Path</th><th>Model</th><th>Route</th><th>Status</th><th>Duration</th></tr>
      </thead>
      <tbody id="logsTableBody">
        <tr><td colspan="8" class="empty">Loading...</td></tr>
      </tbody>
    </table>
    </div>
  </div>
</div>
</div>

<div id="tab-combos" class="tab-panel" style="display:none">
<h2>Combos (custom alias models)</h2>
<div class="section">
  <div class="section-title"><span class="sec-ico"><svg viewBox="0 0 24 24"><path d="M12 5v14M5 12h14"/></svg></span> Create combo</div>
  <div class="section-body">
    <div class="hint" style="margin-bottom:10px">Clients request the alias ID as their model, and the proxy rewrites it to the target model for the chosen platform. Alias IDs are user-defined (e.g. <code>cline-glm-5.3</code>) and must not collide with real model IDs; the target must belong to the same platform — cross-platform targets are not allowed.</div>
    <div class="form-row">
      <div class="field"><label>Alias ID</label><input type="text" id="comboId" placeholder="cline-glm-5.3"></div>
      <div class="field"><label>Platform</label>
        <select id="comboPlatform" onchange="fillComboModels()"><option value="cline">cline</option><option value="zen">opencode zen</option></select>
      </div>
      <div class="field" style="flex:2"><label>Target model (same platform only)</label><select id="comboTarget"></select></div>
      <div class="field" style="flex:0 0 auto;display:flex;align-items:flex-end"><label style="display:flex;gap:6px;align-items:center;white-space:nowrap;padding-bottom:8px"><input type="checkbox" id="comboUseProxies"> Use proxy pool</label></div>
    </div>
    <button class="btn btn-primary" onclick="createCombo()">Create combo</button>
  </div>
</div>
<div class="section">
  <div class="section-title"><span class="sec-ico"><svg viewBox="0 0 24 24"><path d="M8 6h13M8 12h13M8 18h13"/><path d="M3 6h.01M3 12h.01M3 18h.01"/></svg></span> Existing combos</div>
  <div class="section-body" id="combosList">Loading...</div>
</div>
</div>

<div id="tab-opencode" class="tab-panel" style="display:none">
<h2>opencode free models (unified gateway)</h2>

<!-- Egress Network Quick Status & Manual Rotation -->
<div class="cards" style="margin-bottom:18px">
  <div class="card">
    <div class="num blue" id="ocTorStatusBadge" style="font-size:20px">Tor Ready</div>
    <div class="label" id="ocTorIpLabel">IP: Loading...</div>
    <div style="margin-top:10px"><button class="btn btn-sm" id="btnOcRotateTor" onclick="rotateTorManual()">Rotate Tor Circuit</button></div>
  </div>
  <div class="card">
    <div class="num green" id="ocWarpStatusBadge" style="font-size:20px">WARP Connected</div>
    <div class="label" id="ocWarpIpLabel">IP: Loading...</div>
    <div style="margin-top:10px"><button class="btn btn-sm" id="btnOcRotateWarp" onclick="rotateWarpManual()">Rotate WARP Tunnel</button></div>
  </div>
  <div class="card">
    <div class="num purple" style="font-size:20px">Tor ↔ WARP</div>
    <div class="label">Alternating IP Rotation on 429</div>
    <div style="margin-top:10px;font-size:12px;color:var(--text2)">Strict OpenCode Mode</div>
  </div>
  <div class="card">
    <div class="num green" id="ocFidelityBadge" style="font-size:20px">CLI 2.0.22</div>
    <div class="label" id="ocFidelityStatus">SHA256 Verified (12 tools)</div>
    <div style="margin-top:10px;font-size:12px;color:var(--accent2)">Active Fidelity Profile</div>
  </div>
</div>

<!-- OpenCode Multi-Account Pool & Browser OAuth Login -->
<div class="section">
  <div class="section-title">
    <span class="sec-ico"><svg viewBox="0 0 24 24"><path d="M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M23 21v-2a4 4 0 0 0-3-3.87"/><path d="M16 3.13a4 4 0 0 1 0 7.75"/></svg></span>
    OpenCode Connected Accounts (OAuth Device Flow & Multi-Account Pool)
  </div>
  <div class="section-body">
    <div class="tabs" id="ocImportTabs" style="margin-bottom:16px">
      <div class="tab active" id="tabHeadOcOAuth" onclick="switchOcImportTab('oauth')">OAuth Device Login (Browser)</div>
      <div class="tab" id="tabHeadOcManual" onclick="switchOcImportTab('manual')">Manual Token / Refresh Token</div>
    </div>

    <!-- Sub-tab 1: OAuth Browser Login -->
    <div id="ocImport-oauth">
      <p class="hint" style="margin-top:0">
        Connect an OpenCode Console account securely via OAuth Device Code. The proxy contacts opencode.ai directly — no CLI or Node.js installation required.
      </p>
      <div class="form-row">
        <div class="field" style="flex:1">
          <label>Account Label / Name *</label>
          <input type="text" id="ocDeviceName" placeholder="e.g. Work-Account or dev1">
        </div>
        <div class="field" style="flex:1">
          <label>Egress Route</label>
          <select id="ocDeviceRoute">
            <option value="default">Default (Auto-Failover)</option>
            <option value="warp">Cloudflare WARP</option>
            <option value="tor">Embedded Tor (:9052)</option>
            <option value="direct">Direct</option>
            <option value="proxy">Egress Proxy Pool</option>
          </select>
        </div>
        <div class="field" style="flex:0 0 auto;align-self:flex-end">
          <button class="btn btn-primary" id="btnStartOcOAuth" onclick="startOcOAuth()">Start OpenCode Login</button>
        </div>
      </div>
      <div id="ocOAuthProgress" style="display:none;margin-top:14px" class="oauth-card">
        <div style="display:flex;align-items:center;gap:14px">
          <div class="loading"></div>
          <div>
            <div style="font-weight:600" id="ocOAuthStatus">Requesting authorization code from opencode.ai...</div>
            <div class="hint" id="ocOAuthHint" style="display:none">
              Open <a href="#" id="ocOAuthUrl" target="_blank" style="color:var(--accent);font-weight:600">OpenCode Verification Link</a>
              and confirm code: <strong style="color:var(--accent);font-size:16px;letter-spacing:2px" id="ocOAuthUserCode"></strong>
            </div>
          </div>
          <div style="margin-left:auto">
            <button class="btn btn-sm" onclick="cancelOcOAuth()">Cancel</button>
          </div>
        </div>
      </div>
      <div id="ocOAuthResult" style="display:none;margin-top:14px"></div>
    </div>

    <!-- Sub-tab 2: Manual Token -->
    <div id="ocImport-manual" style="display:none">
      <p class="hint" style="margin-top:0">
        Add an existing OpenCode Console token (<code>st_...</code>), Bearer token, or OAuth Refresh Token.
      </p>
      <div class="form-row">
        <div class="field">
          <label>Account Label / Name *</label>
          <input type="text" id="ocManualName" placeholder="e.g. Main-Account">
        </div>
        <div class="field">
          <label>Access Token (optional if refresh token provided)</label>
          <input type="text" id="ocManualToken" placeholder="st_... or Bearer ...">
        </div>
      </div>
      <div class="form-row">
        <div class="field">
          <label>Refresh Token (optional, enables automatic token renewal)</label>
          <input type="text" id="ocManualRefreshToken" placeholder="OAuth refresh_token">
        </div>
        <div class="field">
          <label>Organization ID (optional)</label>
          <input type="text" id="ocManualOrgID" placeholder="org_...">
        </div>
        <div class="field">
          <label>Egress Route</label>
          <select id="ocManualRoute">
            <option value="default">Default (Auto-Failover)</option>
            <option value="warp">Cloudflare WARP</option>
            <option value="tor">Embedded Tor (:9052)</option>
            <option value="direct">Direct</option>
            <option value="proxy">Egress Proxy Pool</option>
          </select>
        </div>
      </div>
      <div class="form-actions">
        <button class="btn btn-primary" onclick="submitOcManualAccount()">Add Account</button>
      </div>
    </div>

    <!-- Connected Accounts Table -->
    <div style="margin-top:20px">
      <div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:8px;flex-wrap:wrap;gap:10px">
        <div style="font-weight:600;font-size:13px;color:var(--text)">Connected Accounts in Pool</div>
        <div style="display:flex;align-items:center;gap:8px">
          <label style="font-size:12px;color:var(--text2);margin:0">Pool Strategy:</label>
          <select id="ocPoolStrategy" onchange="changeOcStrategy(this.value)" style="padding:3px 8px;font-size:12px;background:var(--bg2);color:var(--text);border:1px solid var(--border);border-radius:4px">
            <option value="fill">Fill / Sticky (Use account until exhausted)</option>
            <option value="round-robin">Round-Robin (Rotate each turn)</option>
          </select>
        </div>
      </div>
      <div class="table-wrap" style="margin-bottom:10px">
        <table>
          <thead>
            <tr>
              <th style="width:150px">Account Name</th>
              <th style="width:120px">Token</th>
              <th style="width:130px">Egress Route</th>
              <th style="width:90px">Status</th>
              <th style="width:100px">Org ID</th>
              <th style="width:120px">Expires</th>
              <th style="width:210px">Actions</th>
            </tr>
          </thead>
          <tbody id="ocAccountsBody">
            <tr><td colspan="7" class="empty">Loading OpenCode accounts...</td></tr>
          </tbody>
        </table>
      </div>
      <p class="hint" style="margin:0">
        In <b>Fill / Sticky</b> mode (recommended), requests stick to the active account until its quota is exhausted (429 / cooldown), then automatically transition to the next account. In <b>Round-Robin</b> mode, requests rotate across all active accounts on every turn.
      </p>
    </div>
  </div>
</div>

<!-- OpenCode Diagnostic Playground & Probe -->
<div class="section">
  <div class="section-title"><span class="sec-ico"><svg viewBox="0 0 24 24"><polygon points="5 3 19 12 5 21 5 3"/></svg></span> Diagnostic Playground & Probe (OpenCode v2 Inference)</div>
  <div class="section-body">
    <div class="form-row">
      <div class="field" style="flex:2">
        <label>Model</label>
        <select id="ocPlayModel"></select>
      </div>
      <div class="field">
        <label>Protocol</label>
        <select id="ocPlayProto">
          <option value="chat">Chat (/v1/chat/completions)</option>
          <option value="responses">Responses (/v1/responses)</option>
        </select>
      </div>
      <div class="field">
        <label>Egress Tunnel</label>
        <select id="ocPlayEgress">
          <option value="auto">Auto (Tor ↔ WARP Failover)</option>
          <option value="tor">Tor SOCKS5 (:9052)</option>
          <option value="warp">Cloudflare WARP (:40000)</option>
          <option value="direct">Direct</option>
        </select>
      </div>
    </div>
    <div class="form-row">
      <div class="field" style="flex:1">
        <label>Prompt</label>
        <input type="text" id="ocPlayPrompt" value="Say hello in one short sentence." placeholder="Enter test prompt...">
      </div>
      <div class="field" style="flex:0 0 auto;align-self:flex-end">
        <button class="btn btn-primary" id="btnRunPlayProbe" onclick="runPlaygroundProbe()">Send Test Probe</button>
      </div>
    </div>
    <div id="ocPlayResultBox" style="margin-top:14px;display:none">
      <div class="flex" style="gap:10px;margin-bottom:8px;align-items:center;flex-wrap:wrap">
        <span id="ocPlayStatusBadge" class="probe-pill" style="font-size:12px;font-weight:600"></span>
        <span id="ocPlayLatency" class="stat-mini" style="font-size:12px"></span>
        <span id="ocPlayEgressUsed" class="stat-mini" style="font-size:12px"></span>
      </div>
      <pre id="ocPlayOutput" class="mono" style="background:var(--bg2);border:1px solid var(--border);border-radius:var(--radius-sm);padding:14px;max-height:220px;overflow-y:auto;white-space:pre-wrap;word-break:break-all;font-size:12px;line-height:1.5"></pre>
    </div>
  </div>
</div>

<div class="section">
  <div class="section-title"><span class="sec-ico"><svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3a14.5 14.5 0 0 1 0 18 14.5 14.5 0 0 1 0-18z"/></svg></span> OpenCode Gateway Settings</div>
  <div class="section-body">
    <div class="form-row">
      <div class="field" style="flex:1">
        <label>Enable OpenCode Gateway</label>
        <select id="ocEnabled"><option value="true">On</option><option value="false">Off</option></select>
      </div>
      <div class="field" style="flex:2">
        <label>Upstream Base URL</label>
        <input type="text" id="ocBaseURL" placeholder="https://opencode.ai/zen/v1">
      </div>
      <div class="field" style="flex:1">
        <label>Max Concurrency</label>
        <input type="text" id="ocMaxConc" placeholder="8">
      </div>
      <div class="field" style="flex:1">
        <label>Rate-Limit Retries</label>
        <input type="text" id="ocRetries" placeholder="3">
      </div>
      <div class="field" style="flex:0 0 auto;align-self:flex-end">
        <button class="btn btn-primary" onclick="saveOcConfig()">Save Gateway Settings</button>
      </div>
    </div>
  </div>
</div>

<div class="section">
  <div class="section-title"><span class="sec-ico"><svg viewBox="0 0 24 24"><rect x="2" y="7" width="20" height="14" rx="2"/><path d="M16 7V5a2 2 0 0 0-2-2h-4a2 2 0 0 0-2 2v2"/></svg></span> Model Quotas & Availability
    <input type="text" id="ocModelSearch" oninput="filterOcModels()" placeholder="Search models..." style="max-width:200px;margin-left:auto;padding:3px 8px;font-size:12px;border-radius:4px;border:1px solid var(--border);background:var(--bg2);color:var(--text)">
    <button class="btn btn-sm" onclick="refreshOcModels()" style="margin-left:8px">Sync now</button>
  </div>
  <div class="section-body" style="padding:6px">
    <div id="ocModelsList" style="padding:12px">Loading...</div>
  </div>
</div>

<div class="section">
  <div class="section-title"><span class="sec-ico"><svg viewBox="0 0 24 24"><path d="M3 3v18h18"/><path d="M7 15l4-4 3 3 5-6"/></svg></span> opencode stats</div>
  <div class="section-body">
    <div class="table-wrap"><div id="ocStatsBox"></div></div>
    <div id="ocModelStatsBox" style="margin-top:14px"></div>
  </div>
</div>
</div>

</div>
</div>

<div id="toast" class="toast"></div>

<script>
const API = '/admin/api';

// ========== Theme toggle ==========
function getTheme() {
  return localStorage.getItem('theme') || 'dark';
}
function applyTheme(t) {
  if (t === 'dark') {
    document.documentElement.setAttribute('data-theme', 'dark');
    const ic = document.getElementById('themeIcon');
    if (ic) ic.innerHTML = '<svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"/></svg>';
  } else {
    document.documentElement.setAttribute('data-theme', 'light');
    const ic = document.getElementById('themeIcon');
    if (ic) ic.innerHTML = '<svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="4"/><path d="M12 2v2m0 16v2M4.9 4.9l1.4 1.4m11.4 11.4 1.4 1.4M2 12h2m16 0h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/></svg>';
  }
}
function toggleTheme() {
  const cur = getTheme();
  const next = cur === 'dark' ? 'light' : 'dark';
  localStorage.setItem('theme', next);
  applyTheme(next);
}
applyTheme(getTheme());

const _ = id => document.getElementById(id);
const esc = s => String(s ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const fmtNum = n => (n || 0).toLocaleString('en-US');
// fmtWhen 把服务器发来的 RFC3339 时刻渲染成浏览器的本地时间（同一时刻在不同
// 时区看到各自的钟点）。容器时区是 UTC，直接显示服务器格式化的读数会和本地
// 时间差一个时差；解析失败则原样返回，至少不丢信息。
// 零值时间（Go 的 time.Time{} = 0001-01-01T00:00:00Z）视为"没有这个时刻"：
// 它是 truthy，会被 new Date 解析成年份 1 并渲染成 "1/1/1, 12:00:00 AM"。
const fmtWhen = s => {
  if (!s || String(s).startsWith('0001-')) return '';
  const d = new Date(s);
  return isNaN(d.getTime()) ? s : d.toLocaleString('en-US');
};
const fmtTokens = n => {
  n = n || 0;
  if (n >= 1000000) return (n / 1000000).toFixed(2).replace(/\.?0+$/, '') + 'M';
  if (n >= 1000) return (n / 1000).toFixed(1).replace(/\.0$/, '') + 'K';
  return String(n);
};
if (window.location.host) {
  _('footerApiAddr').textContent = 'http://' + window.location.host;
  _('quickstartApi').textContent = 'http://' + window.location.host + '/v1';
}

function toast(msg, t, duration) {
  const el = _('toast');
  el.textContent = msg;
  el.style.whiteSpace = 'pre-line';
  el.className = 'toast ' + (t || 'info') + ' show';
  clearTimeout(el._timer);
  el._timer = setTimeout(() => el.classList.remove('show'), duration || 3500);
}

// ========== Navigation ==========
document.querySelectorAll('.nav-item').forEach(el => {
  el.addEventListener('click', () => {
    if (el.classList.contains('active')) return;
    document.querySelectorAll('.nav-item').forEach(e => e.classList.remove('active'));
    el.classList.add('active');
    document.querySelectorAll('.tab-panel').forEach(e => e.style.display = 'none');
    _('tab-' + el.dataset.tab).style.display = 'block';
    if (el.dataset.tab === 'dashboard') { loadStats(); loadAccounts(); }
    if (el.dataset.tab === 'accounts') loadAccounts();
    if (el.dataset.tab === 'settings') { loadKeys(); loadModels(); loadConfig(); }
    if (el.dataset.tab === 'logs') loadLogs();
    if (el.dataset.tab === 'proxypool') loadProxyPool();
    if (el.dataset.tab === 'opencode') { loadOcConfig(); loadOcModels(); loadOcStats(); loadOcAccounts(); }
    if (el.dataset.tab === 'combos') { loadCombos(); fillComboModels(); }
  });
});

function switchTab(name) {
  document.querySelectorAll('.nav-item').forEach(e => {
    e.classList.toggle('active', e.dataset.tab === name);
  });
  document.querySelectorAll('.tab-panel').forEach(e => e.style.display = 'none');
  _('tab-' + name).style.display = 'block';
  if (name === 'dashboard') { loadStats(); loadAccounts(); }
  if (name === 'accounts') loadAccounts();
  if (name === 'settings') { loadKeys(); loadModels(); }
  if (name === 'logs') loadLogs();
  if (name === 'proxypool') loadProxyPool();
  if (name === 'opencode') { loadOcConfig(); loadOcModels(); loadOcStats(); loadOcAccounts(); }
  if (name === 'combos') { loadCombos(); fillComboModels(); }
}

// Import sub-tabs
document.querySelectorAll('#importTabs .tab').forEach(el => {
  el.addEventListener('click', () => {
    document.querySelectorAll('#importTabs .tab').forEach(e => e.classList.remove('active'));
    el.classList.add('active');
    document.querySelectorAll('#import-oauth,#import-token,#import-batch').forEach(e => e.classList.remove('active'));
    _('import-' + el.dataset.tab).classList.add('active');
  });
});

// ========== API helper ==========
async function api(method, path, body) {
  const opts = { method, headers: {} };
  if (body) { opts.headers['Content-Type'] = 'application/json'; opts.body = JSON.stringify(body); }
  const res = await fetch(API + path, opts);
  if (res.status === 401 && path !== '/login') { location.reload(); return new Promise(() => {}); }
  const data = await res.json();
  if (!data.success && data.error) throw new Error(data.error);
  return data;
}

// ========== Dashboard ==========
async function loadStats() {
  try {
    const d = await api('GET', '/stats');
    const s = d.data;
    _('statTotal').textContent = s.total;
    _('statActive').textContent = s.active;
    _('statCooldown').textContent = s.cooldown;
    _('statExpired').textContent = s.expired;
    if (_('statStrategy')) _('statStrategy').textContent = (s.strategy || 'fill').toUpperCase();
    if (_('accountStrategyText')) _('accountStrategyText').textContent = s.strategy || 'fill';
    if (s.version) _('settingVersion').value = s.version;
  } catch (e) { /* ignore */ }
}

// ========== Accounts ==========
async function loadAccounts() {
  try {
    const d = await api('GET', '/accounts');
    const list = d.data.accounts;
    const tbody = _('accountTableBody');
    if (!list || list.length === 0) {
      tbody.innerHTML = '<tr><td colspan="6" class="empty">No accounts yet — add one on the <a href="#" onclick="switchTab(\'import\')" style="color:var(--accent);cursor:pointer">Add accounts</a> page</td></tr>';
      return;
    }
    const sn = { active: 'Active', cooldown: 'Cooldown', expired: 'Expired' };
    tbody.innerHTML = list.map(a => {
      const lu = a.lastUsed ? new Date(a.lastUsed).toLocaleString('en-US') : '-';
      const cr = a.createdAt ? new Date(a.createdAt).toLocaleString('en-US') : '-';
      // Cooldown label: show estimated recovery time
      let statusExtra = '';
      if (a.status === 'cooldown') {
        const until = a.cooldownUntil ? new Date(a.cooldownUntil).toLocaleString('en-US') : '';
        statusExtra = until ? '<div style="font-size:10px;color:var(--text3);margin-top:2px">Recovers ' + esc(until) + '</div>' : '';
      }
      const st = esc(a.status);
      return '<tr>' +
        '<td>' + esc(a.email) + '</td>' +
        '<td><span class="status ' + st + '"><span class="status-dot ' + st + '"></span>' + (sn[a.status] || st) + '</span>' + statusExtra + '</td>' +
          '<td title="Today ' + fmtNum(a.tokensToday) + ' / total ' + fmtNum(a.tokensTotal) + ' tokens (exact when upstream returns usage, otherwise estimated)">' + fmtTokens(a.tokensToday) + ' / ' + fmtTokens(a.tokensTotal) + '</td>' +
        '<td class="mono" style="font-size:11px">' + lu + '</td>' +
        '<td class="mono" style="font-size:11px">' + cr + '</td>' +
        '<td style="white-space:nowrap">' +
          // Action buttons dispatch via data-* attributes + delegated listeners:
          // inline onclick string concatenation gets HTML-decoded before JS parsing,
          // so even escaped quotes could be bypassed
          '<button class="btn btn-sm" data-act="test" data-acc="' + esc(a.accountId) + '" title="Test whether the account works (success clears cooldown/expired state)">Test</button> ' +
          '<button class="btn btn-sm" data-act="reset" data-acc="' + esc(a.accountId) + '" title="Probe the rate limit and lift it: probes upstream; if still limited, stays in cooldown and shows recovery time">Reset</button> ' +
          '<button class="btn btn-sm btn-danger" data-act="delete" data-acc="' + esc(a.accountId) + '" title="Delete">Delete</button>' +
        '</td></tr>';
    }).join('');
    tbody.onclick = e => {
      const b = e.target.closest('button[data-act]');
      if (!b) return;
      const id = b.dataset.acc;
      if (b.dataset.act === 'test') testAccount(id, b);
      else if (b.dataset.act === 'reset') resetAccount(id, b);
      else if (b.dataset.act === 'delete') deleteAccount(id);
    };
  } catch (e) { toast('Failed to load accounts: ' + e.message, 'error'); }
}

async function testAccount(id, btn) {
  const original = btn ? btn.innerHTML : '';
  if (btn) { btn.disabled = true; btn.innerHTML = '<span class="loading"></span>Testing'; }
  try {
    const d = await api('POST', '/accounts/test', { accountId: id });
    const r = d.data || {};
    const statusMap = { active: 'Available', cooldown: 'Cooldown', expired: 'Expired', error: 'Error' };
    const label = statusMap[r.status] || r.status;
    const prevMap = { active: 'Active', cooldown: 'Cooldown', expired: 'Expired', '': '' };
    // toast 用 textContent 渲染，参数一律不要再 esc()：转义会以字面量显示
    //（429 的错误体就变成可见的 &quot;error&quot;:…）
    let msg = 'Account ' + (r.email || '') + ' — ' + label;
    if (r.prevStatus && r.prevStatus !== r.status) msg += ' (was: ' + (prevMap[r.prevStatus] || r.prevStatus) + ')';
    if (r.cooldownUntil) msg += '\nEstimated recovery: ' + fmtWhen(r.cooldownUntil);
    if (r.remaining) msg += ' (remaining ' + r.remaining + ')';
    if (r.reason) msg += '\nReason: ' + r.reason;
    if (r.httpStatus) msg += '\nHTTP: ' + r.httpStatus;
    const type = r.status === 'active' ? 'success' : (r.status === 'cooldown' ? 'warning' : 'error');
    toast(msg, type, 6000);
    loadAccounts(); loadStats();
  } catch (e) {
    toast('Test failed: ' + e.message, 'error');
  } finally {
    if (btn) { btn.disabled = false; btn.innerHTML = original; }
  }
}

async function deleteAccount(id) {
  if (!confirm('Delete this account?')) return;
  try {
    await api('POST', '/accounts/delete', { accountId: id });
    toast('Account deleted', 'success');
    loadAccounts(); loadStats();
  } catch (e) { toast('Delete failed: ' + e.message, 'error'); }
}

async function resetAccount(id, btn) {
  const original = btn ? btn.innerHTML : '';
  if (btn) { btn.disabled = true; btn.innerHTML = '<span class="loading"></span>Checking'; }
  try {
    const d = await api('POST', '/accounts/reset', { accountId: id });
    const r = d.data || {};
    const type = d.success ? 'success' : (r.status === 'cooldown' ? 'warning' : 'error');
    let msg = d.message || 'Check complete';
    // 恢复时刻与剩余时长都由面板渲染：服务器发的是 RFC3339 时刻（浏览器本地
    // 时区显示），remaining 只在这里加一次，避免与服务器消息重复。
    // toast 走 textContent，不要再 esc()（会显示成字面量 &quot;）
    if (r.cooldownUntil && r.status === 'cooldown') msg += ' (estimated recovery ' + fmtWhen(r.cooldownUntil) + ')';
    if (r.remaining && r.status !== 'active') msg += ' (remaining ' + r.remaining + ')';
    toast(msg, type, 6000);
    loadAccounts(); loadStats();
  } catch (e) {
    toast('Check failed: ' + e.message, 'error');
  } finally {
    if (btn) { btn.disabled = false; btn.innerHTML = original; }
  }
}

async function deleteAllAccounts() {
  if (!confirm('Delete ALL accounts? This cannot be undone!')) return;
  try {
    await api('POST', '/accounts/delete-all', {});
    toast('All accounts deleted', 'success');
    loadAccounts(); loadStats();
  } catch (e) { toast('Delete failed: ' + e.message, 'error'); }
}

async function refreshAllTokens() {
  try {
    await api('POST', '/accounts/refresh-all', {});
    toast('All tokens refreshed', 'success');
    loadAccounts(); loadStats();
  } catch (e) { toast('Refresh failed: ' + e.message, 'error'); }
}

// ========== OAuth login ==========
async function startOAuth() {
  const btn = _('oauthBtn');
  btn.disabled = true;
  btn.innerHTML = '<span class="loading"></span> Starting...';
  _('oauthProgress').style.display = 'block';
  _('oauthResult').style.display = 'none';
  _('oauthStatus').textContent = 'Connecting to WorkOS...';
  try {
    const d = await api('POST', '/oauth/start');
    const s = d.data;
    _('oauthStatus').textContent = 'Open the link in your browser and enter the code';
    const u = _('oauthUrl');
    const vuri = String(s.verificationUri || '');
    u.textContent = vuri;
    // 仅对 https 链接设置 href：非 https 的 verificationUri 只展示不可点，
    // 防止 javascript: 等异常 scheme 成为注入面
    if (/^https:\/\//i.test(vuri)) { u.href = vuri; } else { u.removeAttribute('href'); }
    _('oauthUserCode').textContent = s.userCode;
    let pollFails = 0;
    const poll = setInterval(async () => {
      try {
        const r = await api('GET', '/oauth/status?sessionId=' + s.sessionId);
        pollFails = 0;
        if (r.data && r.data.done) {
          clearInterval(poll);
          btn.disabled = false;
          btn.innerHTML = 'Start OAuth login';
          if (r.data.success) {
            _('oauthProgress').style.display = 'none';
            _('oauthResult').innerHTML = '<div style="color:var(--accent2);font-weight:600;font-size:14px">Account added: ' + esc(r.data.email) + '</div>';
            _('oauthResult').style.display = 'block';
            loadAccounts(); loadStats();
            toast('Account added!', 'success');
          } else {
            _('oauthStatus').textContent = 'Failed: ' + (r.data.error || 'unknown error');
            toast('OAuth failed', 'error');
          }
        }
      } catch(e) {
        // 连续失败(会话丢失/服务重启)后停止轮询并恢复按钮，避免永久卡死；
        // 偶发网络抖动在 5 次(约10s)内自愈
        if (++pollFails >= 5) {
          clearInterval(poll);
          btn.disabled = false;
          btn.innerHTML = 'Start OAuth login';
          _('oauthStatus').textContent = 'Polling failed: ' + (e && e.message ? e.message : 'session not found');
          toast('OAuth polling stopped', 'error');
        }
      }
    }, 2000);
  } catch (e) {
    btn.disabled = false;
    btn.innerHTML = 'Start OAuth login';
    _('oauthStatus').textContent = 'Error: ' + e.message;
    toast('OAuth failed: ' + e.message, 'error');
  }
}

// ========== Token import ==========
// Credential type auto-detection: "sk_..." = static Cline API key, anything
// else = OAuth refreshToken (the backend validates refresh tokens upstream;
// API keys are pooled as-is).
const credPayload = t => t.startsWith('sk_') ? { apiToken: t } : { refreshToken: t };

async function addByToken() {
  const token = _('tokenInput').value.trim();
  if (!token) { toast('Enter a token or API key', 'error'); return; }
  const email = _('tokenEmail').value.trim();
  try {
    const d = await api('POST', '/accounts/add', { ...credPayload(token), email: email || undefined });
    toast('Account added: ' + (d.data.email || ''), 'success');
    _('tokenInput').value = '';
    _('tokenEmail').value = '';
    loadAccounts(); loadStats();
  } catch (e) { toast('Add failed: ' + e.message, 'error'); }
}

// ========== Batch import ==========
const parseBatchInput = raw => {
  try {
    let tokens = JSON.parse(raw);
    if (!Array.isArray(tokens)) tokens = [tokens];
    return tokens;
  } catch {
    return raw.split('\n').map(t => t.trim()).filter(Boolean).map(credPayload);
  }
};

async function batchImport() {
  const raw = _('batchInput').value.trim();
  if (!raw) { toast('Enter account data', 'error'); return; }
  const tokens = parseBatchInput(raw);
  try {
    const d = await api('POST', '/batch-import', { tokens });
    toast(d.message || 'Import complete', 'success');
    _('batchInput').value = '';
    loadAccounts(); loadStats();
  } catch (e) { toast('Import failed: ' + e.message, 'error'); }
}

async function handleFileImport(event) {
  const file = event.target.files[0];
  if (!file) return;
  const text = await file.text();
  const tokens = parseBatchInput(text);
  try {
    const d = await api('POST', '/batch-import', { tokens });
    toast(d.message || 'Imported ' + tokens.length + ' accounts', 'success');
    loadAccounts(); loadStats();
  } catch (e) { toast('Import failed: ' + e.message, 'error'); }
  event.target.value = '';
}

// ========== API keys ==========
async function loadKeys() {
  try {
    const d = await api('GET', '/keys');
    const keys = d.data.keys;
    const el = _('keysList');
    if (!keys || keys.length === 0) {
      el.innerHTML = '<div class="empty-state"><span class="icon"><svg viewBox="0 0 24 24" width="36" height="36" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M21 2l-2 2m-7.6 7.6a5.5 5.5 0 1 1-7.78 7.78 5.5 5.5 0 0 1 7.78-7.78zm0 0L15.5 7.5m0 0l3 3L22 7l-3-3m-3.5 3.5L19 4"/></svg></span>No API keys yet</div>';
      return;
    }
    el.innerHTML = keys.map(k =>
      '<div class="flex" style="margin-bottom:8px">' +
        '<span class="key-display" style="flex:1" data-copy="' + esc(k) + '" title="Click to copy">' + esc(k) + '</span>' +
        '<button class="btn btn-sm btn-danger" data-delkey="' + esc(k) + '">Delete</button>' +
      '</div>'
    ).join('');
    el.onclick = e => {
      const c = e.target.closest('[data-copy]');
      if (c) { copyText(c.dataset.copy); return; }
      const d = e.target.closest('button[data-delkey]');
      if (d) deleteKey(d.dataset.delkey);
    };
  } catch (e) { _('keysList').innerHTML = '<div class="empty">Failed to load</div>'; }
}

async function generateKey() {
  try {
    const d = await api('POST', '/keys/generate');
    const key = d.data.key;
    _('keyGenResult').innerHTML =
      '<div style="background:rgba(52,211,153,.08);border:1px solid rgba(52,211,153,.4);border-radius:10px;padding:12px">' +
        '<div style="color:var(--accent2);font-weight:600;margin-bottom:8px">New key generated (click to copy)</div>' +
        '<div class="key-display" data-copy="' + esc(key) + '">' + esc(key) + '</div>' +
      '</div>';
    const kr = _('keyGenResult').querySelector('[data-copy]');
    if (kr) kr.onclick = () => copyText(kr.dataset.copy);
    loadKeys();
    toast('Key generated', 'success');
    setTimeout(() => _('keyGenResult').innerHTML = '', 8000);
  } catch (e) { toast('Generation failed: ' + e.message, 'error'); }
}

async function deleteKey(key) {
  if (!confirm('Delete this key?')) return;
  try {
    await api('POST', '/keys/delete', { key });
    toast('Key deleted', 'success');
    loadKeys();
  } catch (e) { toast('Delete failed: ' + e.message, 'error'); }
}

async function deleteAllKeys() {
  if (!confirm('Delete ALL API keys?')) return;
  try {
    const d = await api('GET', '/keys');
    const keys = d.data.keys || [];
    for (const k of keys) await api('POST', '/keys/delete', { key: k });
    toast('All keys deleted', 'success');
    loadKeys();
  } catch (e) { toast('Delete failed: ' + e.message, 'error'); }
}

function copyText(t) {
  navigator.clipboard.writeText(t).then(() => toast('Copied to clipboard', 'success')).catch(() => {
    const ta = document.createElement('textarea');
    ta.value = t; document.body.appendChild(ta); ta.select(); document.execCommand('copy'); document.body.removeChild(ta);
    toast('Copied to clipboard', 'success');
  });
}

// ========== Request logs ==========
const ROUTE_LABEL = { zen: 'opencode', cline: 'cline pool', admin: 'admin', meta: 'meta', other: 'other' };
const STATUS_CLASS = s => s >= 500 ? 'color:var(--danger)' : (s >= 400 ? 'color:var(--amber)' : 'color:var(--accent2)');
let logsAuto = true;

function toggleLogsAuto() {
  logsAuto = !logsAuto;
  _('logsAutoBtn').textContent = logsAuto ? 'Pause' : 'Resume';
  const pill = _('logsAutoPill');
  pill.textContent = logsAuto ? 'Auto-refreshing' : 'Paused';
  pill.classList.toggle('paused', !logsAuto);
  if (logsAuto) loadLogs();
}

async function loadLogs() {
  const tbody = _('logsTableBody');
  try {
    const d = await api('GET', '/logs');
    const logs = d.data.logs || [];
    if (!logs.length) { tbody.innerHTML = '<tr><td colspan="8" class="empty">No requests logged</td></tr>'; return; }
    tbody.innerHTML = logs.map(l => {
      const t = l.time ? new Date(l.time).toLocaleString('en-US') : '-';
      const route = ROUTE_LABEL[l.route] || l.route || '-';
      const st = l.status || 0;
      return '<tr>' +
        '<td class="mono" style="font-size:11px">' + t + '</td>' +
        '<td class="mono" style="font-size:11px">' + esc(l.client || '-') + '</td>' +
        '<td>' + esc(l.method || '-') + '</td>' +
        '<td class="mono" style="font-size:11px">' + esc(l.path || '-') + '</td>' +
        '<td class="mono" style="font-size:12px">' + esc(l.model || '-') + '</td>' +
        '<td><span class="model-tag">' + esc(route) + '</span></td>' +
        '<td style="font-weight:600;color:' + STATUS_CLASS(st) + '">' + st + '</td>' +
        '<td class="mono" style="font-size:11px">' + (l.duration_ms != null ? l.duration_ms + ' ms' : '-') + '</td>' +
      '</tr>';
    }).join('');
  } catch (e) { tbody.innerHTML = '<tr><td colspan="8" class="empty">Failed to load</td></tr>'; }
}

// ========== Export accounts ==========
async function exportAccounts() {
  try {
    const res = await fetch(API + '/accounts/export');
    if (!res.ok) throw new Error('HTTP ' + res.status);
    const blob = await res.blob();
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url; a.download = 'cline-accounts-export.json';
    document.body.appendChild(a); a.click(); document.body.removeChild(a);
    URL.revokeObjectURL(url);
    toast('Accounts exported (JSON)', 'success');
  } catch (e) { toast('Export failed: ' + e.message, 'error'); }
}

// ========== Config updates ==========
async function updateConfig() {
  const strategy = _('settingStrategy').value;
  try {
    await api('POST', '/config/update', { strategy });
    if (_('statStrategy')) _('statStrategy').textContent = strategy.toUpperCase();
    if (_('accountStrategyText')) _('accountStrategyText').textContent = strategy;
    toast('Strategy updated to: ' + strategy, 'success');
  } catch (e) { toast('Update failed: ' + e.message, 'error'); }
}

function addHeaderRow() {
  const tbody = _('headersTableBody');
  const tr = document.createElement('tr');
  tr.innerHTML =
    '<td><input type="text" class="header-key" placeholder="Header-Name" style="font-size:12px;font-family:monospace"></td>' +
    '<td><input type="text" class="header-val" placeholder="value" style="font-size:12px;font-family:monospace"></td>' +
    '<td><button class="btn btn-sm btn-danger" onclick="this.closest(\'tr\').remove()">Delete</button></td>';
  tbody.appendChild(tr);
}

async function saveHeaders() {
  const tbody = _('headersTableBody');
  const rows = tbody.querySelectorAll('tr');
  const headers = {};
  let hasEmpty = false;
  rows.forEach(tr => {
    const keyInput = tr.querySelector('.header-key');
    const valInput = tr.querySelector('.header-val');
    if (keyInput && valInput) {
      const k = keyInput.value.trim();
      const v = valInput.value.trim();
      if (k) { headers[k] = v; }
      else if (v) { hasEmpty = true; }
    }
  });
  if (hasEmpty) { toast('Rows with a value but no key were ignored', 'info'); }
  try {
    const d = await api('POST', '/config/update', { headers });
    toast('Headers saved', 'success');
    _('headerSaveResult').innerHTML =
      '<div style="color:var(--accent2);font-size:12px">Saved ' + Object.keys(d.data.headers).length + ' headers</div>';
    setTimeout(() => _('headerSaveResult').innerHTML = '', 5000);
    loadConfig();
  } catch (e) { toast('Save failed: ' + e.message, 'error'); }
}

const MODEL_STYLE = {
  active:  { label: 'available', css: 'color:var(--accent2);border:1px solid rgba(52,211,153,.5);background:rgba(52,211,153,.08)' },
  empty:   { label: 'empty response', css: 'color:var(--amber);border:1px solid rgba(245,158,11,.5);background:rgba(245,158,11,.08)' },
  pass:    { label: 'subscription', css: 'color:var(--amber);border:1px solid rgba(245,158,11,.5);background:rgba(245,158,11,.08)' },
  removed: { label: 'removed', css: 'color:var(--text3);border:1px solid var(--border)' },
  error:   { label: 'error', css: 'color:var(--danger);border:1px solid rgba(248,113,113,.5);background:rgba(248,113,113,.08)' },
  unknown: { label: 'unprobed', css: 'color:var(--text3);border:1px dashed var(--border-strong)' }
};
const COST_LABEL = { free: 'free', pass: 'subscription', quota: 'uses quota' };

async function loadModels() {
  try {
    const d = await api('GET', '/models');
    const models = d.data.models || [];
    let info = '';
    if (d.data.lastSync) info += '· official feed: ' + new Date(d.data.lastSync).toLocaleTimeString('en-US');
    _('modelsProbeInfo').textContent = info;
    if (!models.length) { _('modelsList').innerHTML = '<div class="empty">No models</div>'; return; }
    _('modelsList').innerHTML = models.map(m => {
      const st = MODEL_STYLE[m.status] || MODEL_STYLE.unknown;
      const cost = COST_LABEL[m.cost] || m.cost || '';
      const synced = m.syncedAt ? new Date(m.syncedAt).toLocaleTimeString('en-US') : '-';
      return '<div style="display:flex;align-items:center;gap:10px;padding:8px 12px;margin:5px 0;background:rgba(148,163,184,.06);border:1px solid var(--border);border-radius:10px;transition:.15s">' +
        '<span style="font-family:\'JetBrains Mono\',monospace;font-size:13px;flex:1">' + esc(m.id) + '</span>' +
        (m.cost === 'free' ? '<span style="font-size:11px;color:var(--accent2)">no charge</span>' : '') +
        (cost ? '<span class="model-tag">' + esc(cost) + '</span>' : '') +
        '<span class="model-tag" style="' + st.css + '">' + st.label + '</span>' +
        '<span style="font-size:11px;color:var(--text3);min-width:60px;text-align:right">' + synced + '</span>' +
        '</div>';
    }).join('');
  } catch (e) { _('modelsList').textContent = 'Failed to load'; }
}

async function refreshModels() {
  try {
    _('modelsProbeInfo').textContent = '· syncing...';
    const d = await api('POST', '/models/refresh');
    toast(d.message || (d.data && d.data.message) || 'Sync started', 'info');
    setTimeout(loadModels, 3000);
  } catch (e) { toast('Refresh failed: ' + e.message, 'error'); _('modelsProbeInfo').textContent = ''; }
}

async function loadModelOptions() {
  try {
    const d = await api('GET', '/models');
    const models = d.data.models || [];
    const sel = _('settingDefModel');
    if (!sel) return;
    sel.innerHTML = models.map(m => {
      const st = MODEL_STYLE[m.status] || MODEL_STYLE.unknown;
      return '<option value="' + esc(m.id) + '">' + esc(m.id) + ' (' + st.label + ')</option>';
    }).join('');
    const c = await api('GET', '/config');
    if (c.data.defaultModel) sel.value = c.data.defaultModel;
    if (!sel.value && models.length) sel.value = models[0].id;
  } catch (e) { /* ignore */ }
}

async function saveDefaultModel() {
  const v = _('settingDefModel').value;
  if (!v) { toast('Select a model', 'error'); return; }
  try {
    const d = await api('POST', '/config/update', { defaultModel: v });
    toast('Default model saved: ' + d.data.defaultModel, 'success');
  } catch (e) { toast('Save failed: ' + e.message, 'error'); }
}

// ========== Config load ==========
async function loadConfig() {
  try {
    const d = await api('GET', '/config');
    const c = d.data;
    if (c.address) _('settingAddr').value = c.address;
    if (c.strategy) {
      _('settingStrategy').value = c.strategy;
      if (_('statStrategy')) _('statStrategy').textContent = c.strategy.toUpperCase();
      if (_('accountStrategyText')) _('accountStrategyText').textContent = c.strategy;
    }
    if (c.version) _('settingVersion').value = c.version;
    if (c.poolPath) _('settingPoolPath').value = c.poolPath;
    loadModelOptions();
    if (c.headers) {
      const tbody = _('headersTableBody');
      tbody.innerHTML = Object.entries(c.headers).map(([k, v]) =>
        '<tr>' +
          '<td><input type="text" class="header-key" value="' + esc(k) + '" style="font-size:12px;font-family:monospace;width:100%"></td>' +
          '<td><input type="text" class="header-val" value="' + esc(v) + '" style="font-size:12px;font-family:monospace;width:100%"></td>' +
          '<td><button class="btn btn-sm btn-danger" onclick="this.closest(\'tr\').remove()">Delete</button></td>' +
        '</tr>'
      ).join('');
    }
  } catch (e) { /* ignore */ }
}

// ========== opencode free models ==========
// Last-seen zen config; the proxy pool fields live on the Proxy pool page now,
// so saves must carry the cached values instead of removed form inputs.
let ocCfgCache = {};

async function loadOcConfig() {
  try {
    const d = await api('GET', '/opencode/config');
    const c = d.data;
    ocCfgCache = c;
    if (_('ocEnabled')) _('ocEnabled').value = String(c.enabled);
    if (_('ocBaseURL')) _('ocBaseURL').value = c.baseURL || '';
    if (_('ocMaxConc')) _('ocMaxConc').value = c.maxConcurrency || 8;
    if (_('ocRetries')) _('ocRetries').value = c.retries || 3;
    if (c.fidelity && _('ocFidelityBadge')) {
      _('ocFidelityBadge').textContent = 'CLI ' + (c.fidelity.version || '2.0.22');
      _('ocFidelityStatus').textContent = c.fidelity.verified ? ('SHA256 Verified (' + c.fidelity.tool_count + ' tools)') : 'Unverified';
    }
  } catch (e) { /* ignore */ }
}

// ========== OpenCode Multi-Account Pool & Device Flow ==========
function switchOcImportTab(tab) {
  _('ocImport-oauth').style.display = tab === 'oauth' ? 'block' : 'none';
  _('ocImport-manual').style.display = tab === 'manual' ? 'block' : 'none';
  _('tabHeadOcOAuth').classList.toggle('active', tab === 'oauth');
  _('tabHeadOcManual').classList.toggle('active', tab === 'manual');
}

async function loadOcAccounts() {
  try {
    const d = await api('GET', '/opencode/accounts');
    const data = d.data || {};
    if (data.fidelity && _('ocFidelityBadge')) {
      const f = data.fidelity;
      _('ocFidelityBadge').textContent = 'CLI ' + (f.version || '2.0.22');
      _('ocFidelityStatus').textContent = f.verified ? ('SHA256 Verified (' + f.tool_count + ' tools)') : 'Unverified';
    }
    if (data.routing_strategy && _('ocPoolStrategy')) {
      _('ocPoolStrategy').value = data.routing_strategy;
    }
    renderOcAccounts(data.accounts || [], data.active, data.routing_strategy);
  } catch (e) {
    _('ocAccountsBody').innerHTML = '<tr><td colspan="7" class="empty">Error loading OpenCode accounts: ' + esc(e.message) + '</td></tr>';
  }
}

function renderOcAccounts(accounts, activeName, strategy) {
  const tbody = _('ocAccountsBody');
  if (!accounts || accounts.length === 0) {
    tbody.innerHTML = '<tr><td colspan="7" class="empty">No OpenCode accounts connected yet. Connect via browser OAuth or add a token above.</td></tr>';
    return;
  }
  let html = '';
  accounts.forEach(a => {
    let statusClass = 'status-active';
    let statusLabel = 'Active';
    if (a.status === 'disabled') { statusClass = 'status-expired'; statusLabel = 'Disabled'; }
    else if (a.status === 'cooldown') { statusClass = 'status-cooldown'; statusLabel = 'Cooldown (429)'; }
    else if (a.status === 'expired') { statusClass = 'status-expired'; statusLabel = 'Expired'; }

    let expText = 'Permanent';
    if (a.expires_at > 0) {
      const diffHours = Math.round((a.expires_at - Date.now()) / (3600 * 1000));
      expText = diffHours > 0 ? ('In ' + diffHours + 'h') : 'Expired';
    }

    const isSticky = a.is_active || (activeName && a.name.toLowerCase() === activeName.toLowerCase());
    const activeBadge = isSticky ? ' <span class="status-pill status-active" style="font-size:10px;padding:2px 6px;margin-left:4px" title="Currently active account in Fill/Sticky pool">ACTIVE</span>' : '';

    const curRoute = a.route || 'default';
    const routeSelect = '<select class="route-select" onchange="changeOcAccountRoute(\'' + esc(a.name) + '\', this.value)" style="padding:2px 6px;font-size:12px;background:var(--bg2);color:var(--text);border:1px solid var(--border);border-radius:4px">' +
      '<option value="default"' + (curRoute === 'default' ? ' selected' : '') + '>Default (Auto)</option>' +
      '<option value="warp"' + (curRoute === 'warp' ? ' selected' : '') + '>WARP</option>' +
      '<option value="tor"' + (curRoute === 'tor' ? ' selected' : '') + '>Tor</option>' +
      '<option value="direct"' + (curRoute === 'direct' ? ' selected' : '') + '>Direct</option>' +
      '<option value="proxy"' + (curRoute === 'proxy' ? ' selected' : '') + '>Proxy</option>' +
      '</select>';

    html += '<tr>' +
      '<td><strong>' + esc(a.name) + '</strong>' + activeBadge + '</td>' +
      '<td><span class="mono">' + esc(a.token_mask) + '</span></td>' +
      '<td>' + routeSelect + '</td>' +
      '<td><span class="status-pill ' + statusClass + '">' + statusLabel + '</span></td>' +
      '<td>' + esc(a.org_id || '-') + '</td>' +
      '<td>' + expText + (a.has_refresh ? ' <span title="Auto-refreshing" style="color:var(--accent2);cursor:pointer">🔄</span>' : '') + '</td>' +
      '<td>' +
        '<button class="btn btn-sm" onclick="testOcAccount(\'' + esc(a.name) + '\')" title="Send probe request">Test</button> ' +
        (a.has_refresh ? '<button class="btn btn-sm" onclick="refreshOcAccount(\'' + esc(a.name) + '\')" title="Refresh OAuth token">Refresh</button> ' : '') +
        (!isSticky ? '<button class="btn btn-sm" onclick="setActiveOcAccount(\'' + esc(a.name) + '\')" title="Set as current active account">Set Active</button> ' : '') +
        '<button class="btn btn-sm" style="color:var(--danger)" onclick="deleteOcAccount(\'' + esc(a.name) + '\')">Delete</button>' +
      '</td>' +
      '</tr>';
  });
  tbody.innerHTML = html;
}

async function setActiveOcAccount(name) {
  try {
    const res = await api('POST', '/opencode/accounts/active', { name: name });
    if (!res.success) throw new Error(res.error || 'Failed to set active account');
    toast('Active account set to ' + name, 'success');
    loadOcAccounts();
  } catch (e) {
    toast('Failed: ' + e.message, 'error');
  }
}

async function changeOcStrategy(strat) {
  try {
    const res = await api('POST', '/opencode/accounts/strategy', { strategy: strat });
    if (!res.success) throw new Error(res.error || 'Failed to update strategy');
    toast('Routing strategy set to ' + strat, 'success');
    loadOcAccounts();
  } catch (e) {
    toast('Failed: ' + e.message, 'error');
  }
}

let ocOAuthPollInterval = null;

async function startOcOAuth() {
  const name = _('ocDeviceName').value.trim();
  const route = _('ocDeviceRoute').value;
  if (!name) { toast('Please enter an account label/name', 'error'); return; }

  const btn = _('btnStartOcOAuth');
  btn.disabled = true;
  btn.innerHTML = '<span class="loading"></span> Starting...';
  _('ocOAuthProgress').style.display = 'block';
  _('ocOAuthResult').style.display = 'none';
  _('ocOAuthStatus').textContent = 'Requesting Device Code from opencode.ai...';
  _('ocOAuthHint').style.display = 'none';

  try {
    const res = await api('POST', '/opencode/oauth/start', { name: name, route: route });
    if (!res.success) {
      throw new Error(res.error || 'Failed to start login');
    }

    let pollFails = 0;
    ocOAuthPollInterval = setInterval(async () => {
      try {
        const r = await api('GET', '/opencode/oauth/status');
        pollFails = 0;
        const s = r.data;
        if (s.status === 'pending') {
          _('ocOAuthStatus').textContent = 'Confirm code in your browser:';
          _('ocOAuthHint').style.display = 'block';
          const link = _('ocOAuthUrl');
          link.href = s.verification_uri || 'https://opencode.ai/auth/device';
          link.textContent = 'Open OpenCode Login (' + (s.verification_uri || 'opencode.ai') + ')';
          _('ocOAuthUserCode').textContent = s.user_code;
        } else if (s.status === 'validating') {
          _('ocOAuthStatus').textContent = 'Validating OpenCode token...';
        } else if (s.status === 'completed') {
          clearInterval(ocOAuthPollInterval);
          btn.disabled = false;
          btn.innerHTML = 'Start OpenCode Login';
          _('ocOAuthProgress').style.display = 'none';
          _('ocOAuthResult').innerHTML = '<div style="color:var(--accent2);font-weight:600;font-size:14px">✓ Account Connected: ' + esc(name) + '</div>';
          _('ocOAuthResult').style.display = 'block';
          _('ocDeviceName').value = '';
          toast('OpenCode account ' + name + ' connected successfully!', 'success');
          loadOcAccounts();
        } else if (s.status === 'failed' || s.status === 'expired' || s.status === 'canceled') {
          clearInterval(ocOAuthPollInterval);
          btn.disabled = false;
          btn.innerHTML = 'Start OpenCode Login';
          _('ocOAuthProgress').style.display = 'none';
          toast('OpenCode authorization ' + s.status + (s.error ? ': ' + s.error : ''), 'error');
        }
      } catch (e) {
        if (++pollFails >= 5) {
          clearInterval(ocOAuthPollInterval);
          btn.disabled = false;
          btn.innerHTML = 'Start OpenCode Login';
          toast('OpenCode login poll failed: ' + e.message, 'error');
        }
      }
    }, 2000);
  } catch (e) {
    btn.disabled = false;
    btn.innerHTML = 'Start OpenCode Login';
    _('ocOAuthProgress').style.display = 'none';
    toast('Error: ' + e.message, 'error');
  }
}

async function cancelOcOAuth() {
  if (ocOAuthPollInterval) {
    clearInterval(ocOAuthPollInterval);
    ocOAuthPollInterval = null;
  }
  await api('POST', '/opencode/oauth/cancel');
  _('btnStartOcOAuth').disabled = false;
  _('btnStartOcOAuth').innerHTML = 'Start OpenCode Login';
  _('ocOAuthProgress').style.display = 'none';
  toast('OpenCode login canceled', 'info');
}

async function submitOcManualAccount() {
  const name = _('ocManualName').value.trim();
  const token = _('ocManualToken').value.trim();
  const refreshToken = _('ocManualRefreshToken').value.trim();
  const orgID = _('ocManualOrgID').value.trim();
  const route = _('ocManualRoute').value;

  if (!name) { toast('Account name is required', 'error'); return; }
  if (!token && !refreshToken) { toast('Either token or refresh token is required', 'error'); return; }

  try {
    const res = await api('POST', '/opencode/accounts/add', {
      name: name,
      token: token,
      refresh_token: refreshToken,
      org_id: orgID,
      route: route
    });
    if (!res.success) throw new Error(res.error || 'Failed to add');
    toast('Account ' + name + ' added successfully', 'success');
    _('ocManualName').value = '';
    _('ocManualToken').value = '';
    _('ocManualRefreshToken').value = '';
    _('ocManualOrgID').value = '';
    loadOcAccounts();
  } catch (e) {
    toast('Add failed: ' + e.message, 'error');
  }
}

async function deleteOcAccount(name) {
  if (!confirm('Delete OpenCode account "' + name + '"?')) return;
  try {
    const res = await api('POST', '/opencode/accounts/delete', { name: name });
    if (!res.success) throw new Error(res.error || 'Failed to delete');
    toast('Account deleted', 'success');
    loadOcAccounts();
  } catch (e) {
    toast('Delete failed: ' + e.message, 'error');
  }
}

async function testOcAccount(name) {
  toast('Sending probe test for ' + name + '...', 'info');
  try {
    const res = await api('POST', '/opencode/accounts/test', { name: name });
    if (res.success) {
      toast('Test passed for ' + name + ' (' + (res.data.model || 'ok') + ')', 'success');
    } else {
      toast('Test failed for ' + name + ': ' + (res.data ? res.data.reason : res.error), 'error');
    }
    loadOcAccounts();
  } catch (e) {
    toast('Test error: ' + e.message, 'error');
  }
}

async function refreshOcAccount(name) {
  toast('Refreshing token for ' + name + '...', 'info');
  try {
    const res = await api('POST', '/opencode/accounts/refresh', { name: name });
    if (!res.success) throw new Error(res.error || 'Refresh failed');
    toast('Token refreshed for ' + name, 'success');
    loadOcAccounts();
  } catch (e) {
    toast('Refresh error: ' + e.message, 'error');
  }
}

async function changeOcAccountRoute(name, route) {
  try {
    const res = await api('POST', '/opencode/accounts/route', { name: name, route: route });
    if (!res.success) throw new Error(res.error || 'Route update failed');
    toast('Route for ' + name + ' set to ' + route, 'success');
  } catch (e) {
    toast('Route update failed: ' + e.message, 'error');
  }
}

async function saveOcConfig() {
  const body = {
    enabled: _('ocEnabled') ? _('ocEnabled').value === 'true' : true,
    baseURL: _('ocBaseURL') ? _('ocBaseURL').value.trim() : 'https://opencode.ai/zen/v1',
    maxConcurrency: _('ocMaxConc') ? parseInt(_('ocMaxConc').value) || 8 : 8,
    retries: _('ocRetries') ? parseInt(_('ocRetries').value) || 3 : 3,
    proxies: ocCfgCache.proxies || [],
    proxyStrategy: ocCfgCache.proxyStrategy || 'round_robin'
  };
  try {
    await api('POST', '/opencode/config/update', body);
    toast('OpenCode gateway settings saved', 'success');
    loadOcConfig();
  } catch (e) { toast('Save failed: ' + e.message, 'error'); }
}

function renderOcKeyStates() {}
async function testZenKey() {}

// ========== Proxy pool ==========
async function loadEgressStatus(force) {
  try {
    const res = await api('GET', '/egress/status');
    const d = res.data || {};
    if (_('egressDirectIp')) _('egressDirectIp').textContent = d.directIp || 'Unavailable';
    if (_('egressTorIp')) _('egressTorIp').textContent = (d.tor && d.tor.currentIp) ? d.tor.currentIp : ((d.tor && d.tor.active) ? 'Active (probing...)' : 'Offline');
    if (_('egressTorBadge')) {
      const active = d.tor && d.tor.active;
      _('egressTorBadge').textContent = active ? 'Online' : 'Offline';
      _('egressTorBadge').style.color = active ? '#22c55e' : '#ef4444';
      _('egressTorBadge').style.background = active ? 'rgba(34,197,94,0.15)' : 'rgba(239,68,68,0.15)';
    }
    if (_('egressTorRotates')) _('egressTorRotates').textContent = 'Rotations: ' + ((d.tor && d.tor.rotationCount) || 0);

    if (_('egressWarpIp')) _('egressWarpIp').textContent = (d.warp && d.warp.currentIp) ? d.warp.currentIp : ((d.warp && d.warp.active) ? 'Active (probing...)' : 'Inactive');
    if (_('egressWarpBadge')) {
      const active = d.warp && d.warp.active;
      _('egressWarpBadge').textContent = active ? 'Connected' : 'Disconnected';
      _('egressWarpBadge').style.color = active ? '#22c55e' : '#eab308';
      _('egressWarpBadge').style.background = active ? 'rgba(34,197,94,0.15)' : 'rgba(234,179,8,0.15)';
    }
    if (_('egressWarpRotates')) _('egressWarpRotates').textContent = 'Rotations: ' + ((d.warp && d.warp.rotationCount) || 0);

    const history = d.history || [];
    const tbody = _('egressHistoryBody');
    if (tbody) {
      if (history.length === 0) {
        tbody.innerHTML = '<tr><td colspan="6" class="empty">No rotation events recorded yet</td></tr>';
      } else {
        tbody.innerHTML = history.map(h => {
          const badgeClass = h.status === 'ok' ? 'green' : 'red';
          const ipChange = h.oldIp ? (esc(h.oldIp) + ' &rarr; ' + esc(h.newIp || '?')) : esc(h.newIp || '-');
          return '<tr>' +
            '<td style="font-size:12px">' + esc(fmtTime(h.timestamp)) + '</td>' +
            '<td><span class="badge" style="text-transform:uppercase;font-size:11px">' + esc(h.type) + '</span></td>' +
            '<td>' + esc(h.trigger) + '</td>' +
            '<td style="font-family:monospace;font-size:12px">' + ipChange + '</td>' +
            '<td><span class="badge ' + badgeClass + '">' + esc(h.status) + '</span></td>' +
            '<td style="font-size:12px">' + esc(h.message) + '</td>' +
            '</tr>';
        }).join('');
      }
    }
  } catch (e) {
    console.error('loadEgressStatus failed:', e);
  }
}

async function rotateTorManual() {
  const btn = _('btnRotateTor');
  if (btn) { btn.disabled = true; btn.textContent = 'Rotating...'; }
  try {
    const res = await api('POST', '/tor/rotate', {});
    toast(res.data && res.data.message ? res.data.message : 'Tor rotated successfully', 'success');
    await loadEgressStatus(true);
  } catch (e) {
    toast('Tor rotate error: ' + e.message, 'error');
  } finally {
    if (btn) { btn.disabled = false; btn.textContent = 'Rotate Tor IP'; }
  }
}

async function rotateWarpManual() {
  const btn = _('btnRotateWarp');
  if (btn) { btn.disabled = true; btn.textContent = 'Rotating...'; }
  try {
    const res = await api('POST', '/warp/rotate', {});
    toast(res.data && res.data.message ? res.data.message : 'WARP rotated successfully', 'success');
    await loadEgressStatus(true);
  } catch (e) {
    toast('WARP rotate error: ' + e.message, 'error');
  } finally {
    if (btn) { btn.disabled = false; btn.textContent = 'Rotate WARP IP'; }
  }
}

async function loadProxyPool() {
  loadEgressStatus();
  try {
    const [oc, cfg] = await Promise.all([api('GET', '/opencode/config'), api('GET', '/config')]);
    const c = oc.data;
    _('ppProxies').value = (c.proxies || []).join('\n');
    _('ppStrategy').value = c.proxyStrategy || 'round_robin';
    _('ppCline').value = String(!!cfg.data.clineUseProxies);
    _('ppZenState').innerHTML = c.enabled
      ? ((c.proxies && c.proxies.length)
          ? '<span style="color:var(--accent2)">routing through the pool</span>'
          : '<span style="color:var(--text2)">direct (pool list is empty)</span>')
      : '<span style="color:var(--text2)">upstream disabled</span>';
    const cd = (c.runtime || {}).proxyCooldowns || {};
    const keys = Object.keys(cd);
    _('ppCooldownInfo').textContent = keys.length
      ? keys.map(k => k + ' cooldown until ' + fmtWhen(cd[k])).join('; ')
      : 'No proxies cooling down';
  } catch (e) { /* ignore */ }
}

async function saveProxyPool() {
  const proxies = _('ppProxies').value.split('\n').map(s => s.trim()).filter(Boolean);
  const PROXY_RE = /^(https?|socks5h?):\/\/[^\s]+:\d+/;
  const bad = proxies.find(p => !PROXY_RE.test(p));
  if (bad) { toast('Invalid proxy format: ' + bad + ' (need http(s)://host:port or socks5://host:port)', 'error'); return; }
  try {
    await api('POST', '/opencode/config/update', { proxies: proxies, proxyStrategy: _('ppStrategy').value });
    _('ppSaveResult').innerHTML = '<div style="color:var(--accent2);font-size:12px">Saved ' + proxies.length + ' proxies</div>';
    setTimeout(() => _('ppSaveResult').innerHTML = '', 5000);
    toast('Proxy pool saved', 'success');
    loadProxyPool();
  } catch (e) { toast('Save failed: ' + e.message, 'error'); }
}

async function saveClineProxies() {
  try {
    await api('POST', '/config/update', { clineUseProxies: _('ppCline').value === 'true' });
    toast('Cline proxy setting saved', 'success');
    loadProxyPool();
  } catch (e) { toast('Save failed: ' + e.message, 'error'); }
}

let cachedOcModels = [];

function renderOcModelsTable(models) {
  _('ocModelsList').innerHTML = '<div class="table-wrap"><table><thead><tr><th style="text-align:left">Model ID</th><th>Context</th><th>Output</th><th>Tools</th><th>Reason</th><th>Attach</th><th>Endpoint</th><th>Source</th><th style="width:70px">Action</th></tr></thead><tbody>' +
    models.map(m => '<tr><td style="text-align:left;font-family:monospace;font-weight:600">' + esc(m.id) + '</td><td>' + esc(m.context) + '</td><td>' + esc(m.output) + '</td><td>' + (m.toolCall ? '✓' : '-') + '</td><td>' + (m.reasoning ? '✓' : '-') + '</td><td>' + (m.attach ? '✓' : '-') + '</td><td style="font-family:monospace;font-size:11px">' + esc(m.upstream === 'responses' ? 'responses' : 'chat') + '</td><td>' + esc(m.source) + '</td><td><button class="btn btn-sm btn-ghost" onclick="probeSpecificModel(\'' + esc(m.id) + '\', \'' + esc(m.upstream) + '\')">Probe</button></td></tr>').join('') +
    '</tbody></table></div><div class="hint">' + models.length + ' free models listed (auto-synced from public registry)</div>';
}

function filterOcModels() {
  const q = (_('ocModelSearch') ? _('ocModelSearch').value : '').trim().toLowerCase();
  if (!q) {
    renderOcModelsTable(cachedOcModels);
    return;
  }
  const filtered = cachedOcModels.filter(m => (m.id || '').toLowerCase().includes(q) || (m.upstream || '').toLowerCase().includes(q));
  renderOcModelsTable(filtered);
}

async function loadOcModels() {
  try {
    const d = await api('GET', '/opencode/models');
    cachedOcModels = d.data.models || [];
    filterOcModels();
    
    // Populate probe model selects
    const sel = _('ocProbeModel');
    if (sel) {
      const prev = sel.value;
      sel.innerHTML = '<option value="">auto — big-pickle first, then live models</option>' +
        cachedOcModels.map(m => '<option value="' + esc(m.id) + '">' + esc(m.id) + (m.source === 'live' ? '' : ' (seed)') + '</option>').join('');
      if (Array.from(sel.options).some(o => o.value === prev)) sel.value = prev;
    }
    const playSel = _('ocPlayModel');
    if (playSel) {
      const prev = playSel.value || 'deepseek-v4.1-flash-free';
      playSel.innerHTML = cachedOcModels.map(m => '<option value="' + esc(m.id) + '">' + esc(m.id) + (m.upstream === 'responses' ? ' [responses]' : ' [chat]') + '</option>').join('');
      if (Array.from(playSel.options).some(o => o.value === prev)) playSel.value = prev;
    }
    
    // Also refresh egress indicators
    if (typeof loadEgressStatus === 'function') {
      api('GET', '/egress/status').then(res => {
        const eg = res.data || {};
        if (_('ocTorIpLabel')) _('ocTorIpLabel').textContent = 'IP: ' + ((eg.tor && eg.tor.currentIp) ? eg.tor.currentIp : ((eg.tor && eg.tor.active) ? 'Active' : 'Offline'));
        if (_('ocWarpIpLabel')) _('ocWarpIpLabel').textContent = 'IP: ' + ((eg.warp && eg.warp.currentIp) ? eg.warp.currentIp : ((eg.warp && eg.warp.active) ? 'Connected' : 'Offline'));
      }).catch(() => {});
    }
  } catch (e) { _('ocModelsList').textContent = 'Failed to load'; }
}

async function runPlaygroundProbe() {
  const btn = _('btnRunPlayProbe');
  const model = _('ocPlayModel') ? _('ocPlayModel').value : 'deepseek-v4.1-flash-free';
  const proto = _('ocPlayProto') ? _('ocPlayProto').value : 'chat';
  const egress = _('ocPlayEgress') ? _('ocPlayEgress').value : 'auto';
  const prompt = (_('ocPlayPrompt') && _('ocPlayPrompt').value.trim()) ? _('ocPlayPrompt').value.trim() : 'Say hello in one short sentence.';
  
  if (btn) { btn.disabled = true; btn.textContent = 'Probing...'; }
  _('ocPlayResultBox').style.display = 'block';
  _('ocPlayStatusBadge').textContent = 'Testing...';
  _('ocPlayStatusBadge').className = 'probe-pill';
  _('ocPlayStatusBadge').style.background = 'rgba(138,180,248,0.15)';
  _('ocPlayStatusBadge').style.color = '#8ab4f8';
  _('ocPlayLatency').textContent = '';
  _('ocPlayEgressUsed').textContent = '';
  _('ocPlayOutput').textContent = 'Sending request to OpenCode inference gate...';

  try {
    const res = await api('POST', '/opencode/probe', {
      model: model,
      prompt: prompt,
      egress: egress,
      protocol: proto
    });
    const d = res.data || {};
    const ok = res.success && d.ok;
    _('ocPlayStatusBadge').textContent = d.http_status ? ('HTTP ' + d.http_status + (ok ? ' OK' : '')) : (ok ? 'Success' : 'Failed');
    _('ocPlayStatusBadge').style.background = ok ? 'rgba(34,197,94,0.15)' : 'rgba(239,68,68,0.15)';
    _('ocPlayStatusBadge').style.color = ok ? '#22c55e' : '#ef4444';
    _('ocPlayLatency').textContent = (d.latency_ms || 0) + 'ms';
    _('ocPlayEgressUsed').textContent = 'Via: ' + (d.egress || egress);
    if (d.response) {
      _('ocPlayOutput').textContent = d.response;
    } else if (d.error) {
      _('ocPlayOutput').textContent = 'Error: ' + d.error;
    } else {
      _('ocPlayOutput').textContent = JSON.stringify(d, null, 2);
    }
  } catch (e) {
    _('ocPlayStatusBadge').textContent = 'Error';
    _('ocPlayStatusBadge').style.background = 'rgba(239,68,68,0.15)';
    _('ocPlayStatusBadge').style.color = '#ef4444';
    _('ocPlayOutput').textContent = 'Probe request failed: ' + e.message;
  } finally {
    if (btn) { btn.disabled = false; btn.textContent = 'Send Test Probe'; }
  }
}

function probeSpecificModel(modelId, upstream) {
  if (_('ocPlayModel')) _('ocPlayModel').value = modelId;
  if (_('ocPlayProto')) _('ocPlayProto').value = (upstream === 'responses' ? 'responses' : 'chat');
  const box = _('ocPlayModel');
  if (box) box.scrollIntoView({ behavior: 'smooth', block: 'center' });
  runPlaygroundProbe();
}

// Obsolete session harvester stubs
let ocSessPoll = null;
async function loadOcSessions() { return null; }
function renderOcSessions() {}
function renderOcMintResults() {}
async function mintZenSessions() {}

// ========== Combos (alias models) ==========
const comboModels = { cline: [], zen: [] };

async function fillComboModels() {
  const platform = _('comboPlatform').value;
  const sel = _('comboTarget');
  try {
    if (!comboModels[platform].length) {
      if (platform === 'cline') {
        const d = await api('GET', '/models');
        comboModels.cline = (d.data.models || []).map(m => m.id).filter(Boolean);
      } else {
        const d = await api('GET', '/opencode/models');
        comboModels.zen = (d.data.models || []).map(m => m.id).filter(Boolean);
      }
    }
    if (!comboModels[platform].length) { sel.innerHTML = '<option value="">No models on this platform</option>'; return; }
    sel.innerHTML = comboModels[platform].map(id => '<option value="' + esc(id) + '">' + esc(id) + '</option>').join('');
  } catch (e) { sel.innerHTML = '<option value="">Failed to load model list</option>'; }
}

async function loadCombos() {
  try {
    const d = await api('GET', '/combos');
    const combos = d.data.combos || [];
    if (!combos.length) { _('combosList').innerHTML = '<div class="hint">No combos yet — create one with the form above.</div>'; return; }
    _('combosList').innerHTML = '<div class="table-wrap"><table><thead><tr><th style="text-align:left">Alias ID</th><th>Platform</th><th style="text-align:left">Target model</th><th>Proxy</th><th>Created</th><th>Actions</th></tr></thead><tbody>' +
      combos.map(c => '<tr>' +
        '<td style="text-align:left;font-family:monospace;font-weight:600">' + esc(c.id) + '</td>' +
        '<td><span class="model-tag">' + esc(c.platform === 'zen' ? 'opencode-zen' : 'cline') + '</span></td>' +
        '<td style="text-align:left;font-family:monospace">' + esc(c.target) + '</td>' +
        '<td>' + (c.useProxies ? '<span class="model-tag" style="color:var(--accent2)">socks5 pool</span>' : '-') + '</td>' +
        '<td style="font-size:11px">' + (c.createdAt ? new Date(c.createdAt).toLocaleString('en-US') : '-') + '</td>' +
        '<td><button class="btn btn-sm btn-danger" data-delcombo="' + esc(c.id) + '">Delete</button></td>' +
      '</tr>').join('') +
      '</tbody></table></div>';
    _('combosList').onclick = e => {
      const b = e.target.closest('button[data-delcombo]');
      if (b) deleteCombo(b.dataset.delcombo);
    };
  } catch (e) { _('combosList').textContent = 'Failed to load: ' + e.message; }
}

async function createCombo() {
  const id = _('comboId').value.trim();
  const platform = _('comboPlatform').value;
  const target = _('comboTarget').value;
  if (!id) { toast('Enter an alias ID', 'error'); return; }
  if (!target) { toast('Select a target model', 'error'); return; }
  try {
    await api('POST', '/combos/create', { id, platform, target, useProxies: _('comboUseProxies').checked });
    toast('Combo created: ' + id + ' → ' + target, 'success');
    _('comboId').value = '';
    _('comboUseProxies').checked = false;
    loadCombos();
  } catch (e) { toast('Create failed: ' + e.message, 'error'); }
}

async function deleteCombo(id) {
  if (!confirm('Delete combo ' + id + '? Clients using this alias will no longer be able to call it.')) return;
  try {
    await api('POST', '/combos/delete', { id });
    toast('Combo deleted', 'success');
    loadCombos();
  } catch (e) { toast('Delete failed: ' + e.message, 'error'); }
}

async function refreshOcModels() {
  try {
    const d = await api('POST', '/opencode/models/refresh');
    toast(d.message || 'Sync complete', 'success');
    loadOcModels();
  } catch (e) { toast('Sync failed: ' + e.message, 'error'); }
}

async function loadOcStats() {
  try {
    const d = await api('GET', '/opencode/stats');
    const t = d.data.today || {}, s = d.data.total || {};
    _('ocStatsBox').innerHTML = '<table><thead><tr><th style="text-align:left"></th><th>Requests</th><th>Input tokens</th><th>Output tokens</th><th>Compaction</th><th>Rate-limited</th></tr></thead><tbody>' +
      '<tr><td style="text-align:left">Today</td><td>' + (t.requests || 0) + '</td><td>' + (t.promptTokens || 0) + '</td><td>' + (t.completionTokens || 0) + '</td><td>' + (t.compaction || 0) + '</td><td>' + (t.rateLimited || 0) + '</td></tr>' +
      '<tr><td style="text-align:left">Total</td><td>' + (s.requests || 0) + '</td><td>' + (s.promptTokens || 0) + '</td><td>' + (s.completionTokens || 0) + '</td><td>' + (s.compaction || 0) + '</td><td>' + (s.rateLimited || 0) + '</td></tr>' +
      '</tbody></table>';
    const bm = t.byModel || {};
    const rows = Object.keys(bm).map(k => '<tr><td style="text-align:left;font-family:monospace">' + esc(k) + '</td><td>' + bm[k].requests + '</td><td>' + bm[k].promptTokens + '</td><td>' + bm[k].completionTokens + '</td></tr>').join('');
    _('ocModelStatsBox').innerHTML = '<div style="font-size:13px;font-weight:600;margin-bottom:6px">Per-model breakdown (today)</div>' +
      '<div class="table-wrap"><table><thead><tr><th style="text-align:left">Model</th><th>Requests</th><th>Input tokens</th><th>Output tokens</th></tr></thead><tbody>' +
      (rows || '<tr><td colspan="4" style="text-align:left;color:var(--text2)">No data</td></tr>') + '</tbody></table></div>';
  } catch (e) { /* ignore */ }
}

// ========== Init ==========
loadStats();
loadAccounts();
loadKeys();
loadModels();
loadConfig();
setInterval(() => { loadStats(); }, 10000);
setInterval(() => { loadOcStats(); }, 15000);
// Poll OpenCode accounts when OpenCode tab is visible
setInterval(() => {
  if (_('tab-opencode').style.display !== 'none') loadOcAccounts();
}, 15000);
setInterval(() => { if (logsAuto && _('tab-logs').style.display !== 'none') loadLogs(); }, 8000);
</script>
</body>
</html>`