const T = new URLSearchParams(location.search).get('t') || '';
const $ = id => document.getElementById(id);
const api = (path, body) => fetch(path, {
  method: body ? 'POST' : 'GET',
  headers: {'X-Token': T, 'Content-Type': 'application/json'},
  body: body ? JSON.stringify(body) : undefined
}).then(r => r.json());

const FIELDS = ['host','user','keyPath','sshPort','socksPort','httpPort','poolSize'];
const CHECKS = ['verbose','autoStart','localViaTunnel'];

/* Правила фильтра живут отдельно от полей формы: они правятся на своём
   экране и сохраняются своей кнопкой. */
let filterMode = 'all';
let filterApps = [];
// Пока правила правят, обновление с сервера их не трогает — иначе очередной
// опрос затирал бы несохранённый выбор, как это было с полями настроек.
let filterDirty = false;
let appPaths = {};   // имя -> путь, только для показа в списке
let seenApps = [];

function normApp(name){
  name = (name || '').trim().toLowerCase();
  if(!name) return '';
  const i = Math.max(name.lastIndexOf('\\'), name.lastIndexOf('/'));
  if(i >= 0) name = name.slice(i + 1);
  return name.endsWith('.exe') ? name : name + '.exe';
}

const MODE_NAMES = { all:'Все', only:'Только выбранные', except:'Все, кроме выбранных' };

function renderModes(){
  document.querySelectorAll('#modes .mode').forEach(el =>
    el.classList.toggle('on', el.dataset.mode === filterMode));
  $('appsBox').hidden = filterMode === 'all';
  $('filterSummary').textContent = MODE_NAMES[filterMode] +
    (filterMode === 'all' ? '' : ' — ' + filterApps.length + ' шт.');
}

document.querySelectorAll('#modes .mode').forEach(el => {
  el.onclick = () => { filterMode = el.dataset.mode; filterDirty = true; renderModes(); };
});

function renderApps(){
  const box = $('appList');
  box.innerHTML = '';
  $('appsCount').textContent = filterApps.length ? filterApps.length + ' шт.' : '';

  if(filterApps.length === 0){
    box.innerHTML = '<div class="empty">Пока пусто. Нажми «Добавить программу» ' +
      'и выбери её из запущенных или укажи файл на диске.</div>';
    return;
  }
  filterApps.forEach(name => {
    const row = document.createElement('div');
    row.className = 'appitem';
    const info = document.createElement('span');
    info.style.cssText = 'flex:1;min-width:0';
    const nm = document.createElement('div');
    nm.className = 'nm'; nm.textContent = name;
    info.appendChild(nm);
    if(appPaths[name]){
      const pt = document.createElement('div');
      pt.className = 'pt'; pt.textContent = appPaths[name];
      info.appendChild(pt);
    }
    const del = document.createElement('button');
    del.className = 'del'; del.textContent = '×'; del.title = 'Убрать';
    del.onclick = () => {
      filterApps = filterApps.filter(a => a !== name);
      filterDirty = true;
      renderApps(); renderModes();
    };
    row.appendChild(info); row.appendChild(del);
    box.appendChild(row);
  });
}

function addApp(name, path){
  const n = normApp(name);
  if(!n) return;
  if(path) appPaths[n] = path;
  if(!filterApps.includes(n)) filterApps = filterApps.concat(n);
  filterDirty = true;
  renderApps(); renderModes();
}

function toggleApp(name, path){
  const n = normApp(name);
  if(!n) return;
  if(filterApps.includes(n)) filterApps = filterApps.filter(a => a !== n);
  else { if(path) appPaths[n] = path; filterApps = filterApps.concat(n); }
  filterDirty = true;
  renderApps(); renderModes();
}

/* ---------- меню кнопки «Добавить программу» ---------- */
let menuEl = null;
function closeMenu(){ if(menuEl){ menuEl.remove(); menuEl = null; } }
document.addEventListener('click', ev => {
  if(menuEl && !menuEl.contains(ev.target) && ev.target !== $('addApp')) closeMenu();
});

$('addApp').onclick = ev => {
  ev.stopPropagation();
  if(menuEl){ closeMenu(); return; }
  const r = $('addApp').getBoundingClientRect();
  menuEl = document.createElement('div');
  menuEl.className = 'menu';
  menuEl.style.left = r.left + 'px';
  menuEl.style.top = (r.bottom + 4) + 'px';
  [['Из списка запущенных', openProcessPicker],
   ['Выбрать файл на диске', pickFromDisk],
   ['Ввести имя вручную', askManually]].forEach(([label, fn]) => {
    const b = document.createElement('button');
    b.textContent = label;
    b.onclick = () => { closeMenu(); fn(); };
    menuEl.appendChild(b);
  });
  document.body.appendChild(menuEl);
};

function askManually(){
  const v = prompt('Имя программы, например steam.exe');
  if(v) addApp(v);
}

async function pickFromDisk(){
  const r = await api('/api/pickfile');
  if(r.error){ alert(r.error); return; }
  if(r.path) addApp(r.path, r.path);
}

/* ---------- окно выбора запущенной программы ---------- */
let procs = [];

async function openProcessPicker(){
  $('procOverlay').hidden = false;
  $('procSearch').value = '';
  $('procList').innerHTML = '<div class="empty">Читаю список программ…</div>';
  const r = await api('/api/processes');
  procs = r.processes || [];
  renderProcs();
  $('procSearch').focus();
}
$('closeProc').onclick = () => { $('procOverlay').hidden = true; };
$('procOverlay').onclick = ev => { if(ev.target === $('procOverlay')) $('procOverlay').hidden = true; };
$('procSearch').oninput = renderProcs;

function renderProcs(){
  const q = $('procSearch').value.trim().toLowerCase();
  const box = $('procList');
  box.innerHTML = '';
  const list = procs.filter(p =>
    !q || p.name.toLowerCase().includes(q) || (p.path||'').toLowerCase().includes(q));
  if(list.length === 0){
    box.innerHTML = '<div class="empty">Ничего не нашлось.</div>';
    return;
  }
  list.forEach(p => {
    const b = document.createElement('button');
    b.className = 'proc' + (filterApps.includes(normApp(p.name)) ? ' picked' : '');
    const n = document.createElement('div'); n.className = 'n'; n.textContent = p.name;
    b.appendChild(n);
    if(p.path){
      const pa = document.createElement('div'); pa.className = 'p'; pa.textContent = p.path;
      b.appendChild(pa);
    }
    // Повторный щелчок снимает выбор — иначе лишнюю программу пришлось бы
    // убирать, закрыв окно, а это неочевидно.
    b.onclick = () => { toggleApp(p.name, p.path); renderProcs(); };
    box.appendChild(b);
  });
}

/* ---------- сохранение правил ---------- */
$('saveFilter').onclick = async () => {
  const cfg = Object.assign({}, lastConfig, {
    filterMode: filterMode,
    filterApps: filterApps,
  });
  const r = await api('/api/config', cfg);
  if(r.error){ $('filterNote').textContent = r.error; return; }
  lastConfig = r.config;
  filterDirty = false;
  $('filterNote').textContent = 'сохранено, работает сразу';
  setTimeout(() => $('filterNote').textContent = '', 3000);
};

let running = false, formDirty = false, speedRunning = false;
// Последние известные настройки: экран фильтра сохраняет их целиком, меняя
// только свои поля, иначе он затёр бы адрес сервера и порты.
let lastConfig = {};

/* ---------- переключение экранов ---------- */
const VIEWS = ['viewMain','viewSettings','viewFilter','viewLog'];
let current = 'viewMain';

function show(name){
  if(name !== 'viewSettings') hideKeyHelp();
  current = name;
  VIEWS.forEach(v => $(v).classList.toggle('on', v === name));
  closeMenu();
  if(name === 'viewLog') $('log').scrollTop = $('log').scrollHeight;
}

// Логотип всегда возвращает на главный экран — с любого места.
$('logo').parentElement.style.cursor = 'pointer';
$('logo').parentElement.title = 'На главный экран';
$('logo').parentElement.onclick = () => show('viewMain');

// Шестерёнка работает переключателем: из настроек она возвращает на главную,
// чтобы не искать кнопку «назад».
$('btnSettings').onclick = () =>
  show(current === 'viewSettings' ? 'viewMain' : 'viewSettings');
$('btnLog').onclick = () =>
  show(current === 'viewLog' ? 'viewMain' : 'viewLog');

$('backFromSettings').onclick = () => show('viewMain');
$('backFromLog').onclick = () => show('viewMain');
$('backFromFilter').onclick = () => show('viewSettings');
$('toFilter').onclick = () => show('viewFilter');

/* ---------- форматирование ---------- */
function fmtMbps(v){
  if(v >= 100) return v.toFixed(0) + ' Мбит/с';
  return v.toFixed(1) + ' Мбит/с';
}

/* ---------- состояние ---------- */
const STATES = {
  stopped:      ['Отключено',            'Подключить',  ''    ],
  connecting:   ['Подключаюсь…',         'Отмена',      'busy'],
  connected:    ['Защищено',             'Отключить',   'on'  ],
  reconnecting: ['Связь потеряна',       'Отключить',   'busy'],
  error:        ['Ошибка',               'Подключить',  'bad' ],
};
function setState(s, detail){
  const [text, cap, cls] = STATES[s] || [s, 'Подключить', ''];
  $('stateText').textContent = text;
  if(detail !== undefined) $('stateDetail').textContent = detail || '';
  $('powerCap').textContent = cap;
  $('power').className = cls;
  running = (s === 'connected' || s === 'reconnecting' || s === 'connecting');
  if(s !== 'connected') showPing(0);
  $('speedtest').disabled = !running || speedRunning;
}
function showError(msg, where){
  const box = $(where || 'error');
  box.textContent = msg || '';
  box.classList.toggle('on', !!msg);
}

/* ---------- журнал ---------- */
const logEl = $('log');
let lastKey = null, lastLine = null, lastCount = 1, lastAt = 0;

function addConn(e){
  const key = (e.process||'') + '|' + e.target + '|' + (e.failed?'f':'o');
  const now = Date.now();
  // Одна страница открывает десятки соединений к одному хосту — повторы
  // в пределах пяти секунд сворачиваем в счётчик, иначе журнал нечитаем.
  if(key === lastKey && now - lastAt < 5000 && lastLine){
    lastCount++; lastAt = now;
    lastLine.querySelector('.n').textContent = '×' + lastCount;
    return;
  }
  lastKey = key; lastCount = 1; lastAt = now;

  const d = document.createElement('div');
  d.className = 'l' + (e.failed ? ' fail' : '');
  d.innerHTML = '<span class="t"></span><span class="p"></span><span class="a"></span>'
              + '<span class="h"></span><span class="flag"></span><span class="n"></span>';
  d.querySelector('.t').textContent = new Date(e.time).toLocaleTimeString('ru-RU');
  d.querySelector('.p').textContent = e.process || 'приложение';
  d.querySelector('.a').textContent = e.failed ? '✗' : '→';
  d.querySelector('.h').textContent = e.target + (e.failed ? '  ' + (e.error||'') : '');
  const flag = d.querySelector('.flag');
  if(e.direct){
    flag.textContent = ' мимо';
    flag.title = 'по твоим правилам эта программа идёт напрямую, минуя туннель';
  } else if(e.dnsLeak){
    flag.textContent = ' DNS!';
    flag.title = 'DNS-запрос ушёл мимо туннеля';
  }
  push(d);
}
function addMsg(e){
  lastKey = null;
  const d = document.createElement('div');
  d.className = 'l msg ' + (e.level === 'error' ? 'err' : e.level === 'warn' ? 'warn' : '');
  d.textContent = new Date(e.time).toLocaleTimeString('ru-RU') + '  ' + e.text;
  push(d);
}
function push(el){
  const atBottom = logEl.scrollHeight - logEl.scrollTop - logEl.clientHeight < 40;
  logEl.appendChild(el);
  lastLine = el;
  while(logEl.childElementCount > 600) logEl.removeChild(logEl.firstChild);
  if(atBottom) logEl.scrollTop = logEl.scrollHeight;
}
$('clear').onclick = () => { logEl.innerHTML = ''; lastKey = null; };

/* ---------- поток событий ---------- */
function connectEvents(){
  const es = new EventSource('/events?t=' + encodeURIComponent(T));
  es.onmessage = ev => {
    const e = JSON.parse(ev.data);
    if(e.kind === 'conn')  return addConn(e);
    if(e.kind === 'speed') return onSpeed(e);
    if(e.kind === 'log')   return addMsg(e);
    if(e.kind === 'state'){
      setState(e.state, e.state === 'connected' ? e.detail : '');
      showError(e.state === 'error' ? e.detail : '');
      return;
    }
    if(e.kind === 'stats'){
      const s = e.stats;
      $('act').textContent = s.active;
      $('links').textContent = s.healthy + ' / ' + s.links;
      showPing(s.pingMs);
    }
  };
}

/* Задержка до сервера. Ноль означает, что замера ещё не было, — тогда
   ничего не пишем: прочерк выглядел бы как «связи нет». */
function showPing(ms){
  const el = $('ping');
  if(!ms){ el.textContent = ''; el.className = 'ping'; return; }
  el.textContent = ms + ' мс';
  el.className = 'ping ' + (ms < 100 ? 'good' : ms < 250 ? 'mid' : 'bad');
}

/* ---------- кнопка ---------- */
$('power').onclick = async () => {
  $('power').disabled = true;
  showError('');
  try{
    const r = running ? await api('/api/stop', {}) : await api('/api/start', {});
    if(r.error){ showError(r.error); show('viewMain'); }
  } finally {
    $('power').disabled = false;
    refresh();
  }
};

/* Отдельной плитки с внешним адресом больше нет — он и так написан под
   состоянием. Но проверить, что трафик действительно выходит через сервер,
   по-прежнему можно: щелчок по адресу спрашивает его у внешнего сервиса
   ЧЕРЕЗ туннель и показывает, совпал ли он с ожидаемым. */
$('stateDetail').onclick = async () => {
  if(!running) return;
  const shown = $('stateDetail').textContent;
  $('stateDetail').textContent = 'проверяю…';
  const r = await api('/api/checkip');
  if(r.error){
    $('stateDetail').textContent = shown;
    showError(r.error);
    return;
  }
  $('stateDetail').textContent = r.ip === shown ? shown + '  ✓' : r.ip;
  setTimeout(() => { if(running) $('stateDetail').textContent = r.ip; }, 4000);
};

/* ---------- тест скорости ---------- */
/* Замер идёт около двадцати секунд, поэтому показываем не только итог, но и
   ход: плитка нужного направления обновляется прямо во время теста. */
function onSpeed(e){
  if(e.done){
    speedRunning = false;
    $('speedtest').disabled = !running;
    $('speedtest').innerHTML = 'Тест скорости';
    return;
  }
  const tile = e.phase === 'up' ? 'tileUp' : 'tileDown';
  const val  = e.phase === 'up' ? 'up' : 'dn';
  $(tile).hidden = false;
  $(val).textContent = fmtMbps(e.mbps);
  $('speedtest').innerHTML = 'Измеряю…<span class="ph">' +
    (e.phase === 'up' ? 'отдача' : 'приём') + '</span>';
}

// Последний удачный результат держим отдельно: если следующий тест сорвётся,
// стирать уже измеренное незачем — пользователь останется вообще без цифр.
let lastSpeed = null;

$('speedtest').onclick = async () => {
  if(!running || speedRunning) return;
  speedRunning = true;
  clearTimeout(hideTimer);
  $('speedtest').disabled = true;
  $('speedtest').innerHTML = 'Измеряю…<span class="ph">приём</span>';
  $('tileDown').hidden = false; $('tileUp').hidden = false;
  $('dn').textContent = '…'; $('up').textContent = '…';
  showError('');

  const r = await api('/api/speedtest', {});
  speedRunning = false;
  $('speedtest').disabled = !running;
  $('speedtest').innerHTML = 'Тест скорости';

  if(r.error){
    showError(r.error);
    showSpeed(lastSpeed);   // вернуть прошлый результат, а не обнулять
    hideSpeedLater();
    return;
  }
  lastSpeed = { down: r.downMbps, up: r.upMbps };
  showSpeed(lastSpeed);
  hideSpeedLater();
  // Приём мог измериться, а отдача — нет. Это не ошибка целиком, поэтому
  // говорим об этом отдельно и результат оставляем.
  if(r.note) showError(r.note);
};

// Плитки со скоростью убираются сами через десять секунд: результат нужен
// сразу после теста, а дальше он только занимает место. Таймер сбрасывается
// при новом тесте, иначе он погасил бы свежий результат раньше времени.
let hideTimer = null;
function hideSpeedLater(){
  clearTimeout(hideTimer);
  hideTimer = setTimeout(() => {
    $('tileDown').hidden = true;
    $('tileUp').hidden = true;
  }, 10000);
}

// showSpeed рисует результат. null означает, что теста ещё не было — тогда
// плитки прячутся обратно, чтобы не показывать прочерки без причины.
function showSpeed(v){
  const has = v !== null;
  $('tileDown').hidden = !has;
  $('tileUp').hidden = !has;
  if(!has) return;
  $('dn').textContent = fmtMbps(v.down);
  $('up').textContent = v.up > 0 ? fmtMbps(v.up) : '—';
}

/* ---------- подсказка «как получить ключ» ---------- */
/* Команды собираются из полей формы: адрес сервера и пользователь берутся
   оттуда, чтобы не подставлять их руками. Раньше это была одна команда,
   которая сама клала ключ на сервер, — она перестала работать, как только
   пользователю туннеля запретили выполнять команды. Поэтому теперь шаги
   раздельные: два на своей машине, один на сервере под root. */
const KEYFILE = '$env:USERPROFILE\\.ssh\\id_ed25519';

function cmdCreateKey(){
  return 'if (!(Test-Path "' + KEYFILE + '")) { ssh-keygen -t ed25519 -f "' +
    KEYFILE + '" -N \'""\' }';
}
function cmdShowKey(){ return 'type "' + KEYFILE + '.pub"'; }

function cmdServer(){
  const user = ($('user').value || '').trim() || 'tunnel';
  if(user === 'root'){
    return 'mkdir -p ~/.ssh; chmod 700 ~/.ssh; ' +
      'echo \'ВСТАВЬ_СЮДА_КЛЮЧ_ИЗ_ШАГА_2\' >> ~/.ssh/authorized_keys; ' +
      'chmod 600 ~/.ssh/authorized_keys';
  }
  const u = user.replace(/'/g, '');
  return 'id -u ' + u + ' >/dev/null 2>&1 || useradd -m -s /usr/sbin/nologin ' + u + '; ' +
    'usermod -p \'*\' ' + u + '; install -d -m 700 -o ' + u + ' -g ' + u + ' /home/' + u + '/.ssh; ' +
    'echo \'restrict,port-forwarding ВСТАВЬ_СЮДА_КЛЮЧ_ИЗ_ШАГА_2\' >> /home/' + u + '/.ssh/authorized_keys; ' +
    'chown ' + u + ':' + u + ' /home/' + u + '/.ssh/authorized_keys; ' +
    'chmod 600 /home/' + u + '/.ssh/authorized_keys';
}

function renderSetupCmd(){
  $('cmd1').textContent = cmdCreateKey();
  $('cmd2').textContent = cmdShowKey();
  $('cmd3').textContent = cmdServer();
  const user = ($('user').value || '').trim() || 'tunnel';
  $('cmd3note').textContent = user === 'root'
    ? 'Замени ВСТАВЬ_СЮДА_КЛЮЧ_ИЗ_ШАГА_2 на строку из шага 2.'
    : 'Замени ВСТАВЬ_СЮДА_КЛЮЧ_ИЗ_ШАГА_2 на строку из шага 2. Команда заодно '
      + 'создаст пользователя «' + user + '», если его ещё нет: он умеет только '
      + 'пробрасывать соединения, ни команд, ни файлов.';
}

/* Одна кнопка копирования на все команды: какая именно — написано на самой
   кнопке в data-cmd. */
document.querySelectorAll('.cp[data-cmd]').forEach(b => {
  b.onclick = async () => {
    const ok = await copyToClipboard($(b.dataset.cmd).textContent);
    $('keyHelpNote').textContent = ok ? 'скопировано' : 'скопируй вручную';
    setTimeout(() => $('keyHelpNote').textContent = '', 2500);
  };
});

// Подсказки закрываются тем же вопросительным знаком и не переживают уход с
// экрана: вернувшись в настройки, человек видит их в обычном виде.
function toggleHelp(btnId, boxId, onShow){
  $(btnId).onclick = () => {
    const show = $(boxId).hidden;
    $(boxId).hidden = !show;
    $(btnId).classList.toggle('on', show);
    if(show && onShow) onShow();
  };
}
toggleHelp('keyHelpBtn', 'keyHelp', renderSetupCmd);
toggleHelp('localHelpBtn', 'localHelp');
toggleHelp('directHelpBtn', 'directHelp');

function hideKeyHelp(){
  ['keyHelp','localHelp','directHelp','scanResult'].forEach(id => $(id).hidden = true);
  ['keyHelpBtn','localHelpBtn','directHelpBtn'].forEach(id => $(id).classList.remove('on'));
  $('keyHelpNote').textContent = '';
  $('scanNote').textContent = '';
}

['host','user'].forEach(f => $(f).addEventListener('input', () => {
  if(!$('keyHelp').hidden) renderSetupCmd();
}));

/* ---------- проверка сетей ---------- */
/* Спрашивать у человека, какие у него сети, бессмысленно — он их обычно не
   знает. Зато их знает его компьютер. */
/* Результат убирается сам через 10 секунд — как плитки скорости: он нужен
   в момент проверки, а потом только занимает экран. Таймер отменяется, пока
   курсор над списком: иначе кнопка «Добавить» исчезала бы из-под руки. */
let scanTimer = null;
function hideScanLater(){
  clearTimeout(scanTimer);
  scanTimer = setTimeout(() => {
    $('scanResult').hidden = true;
    $('scanNote').textContent = '';
  }, 10000);
}

$('scanNet').onclick = async () => {
  clearTimeout(scanTimer);
  $('scanNote').textContent = 'смотрю…';
  const r = await api('/api/scannet');
  if(r.error){ $('scanNote').textContent = r.error; return; }
  const nets = r.nets || [];
  const box = $('scanResult');
  box.hidden = false;
  const bad = nets.filter(n => !n.ok);
  let html = bad.length
    ? '<p><b>Эти сети пойдут через сервер и станут недоступны:</b></p>'
    : '<p><b>Всё в порядке.</b> Ни одна твоя сеть не сломается.</p>';
  html += nets.map(n =>
    '<div class="netline ' + (n.ok ? 'ok' : 'bad') + '">' +
    '<span class="mark">' + (n.ok ? '✓' : '!') + '</span>' +
    '<span><span class="cidr">' + n.cidr + '</span> — ' + n.name +
    '<br><span class="hint" style="margin:0">' + n.why + '</span></span></div>').join('');
  if(bad.length){
    html += '<div style="margin-top:9px"><button class="btn" id="addBad" ' +
      'style="font-size:12.5px;padding:6px 12px">Добавить их в список</button></div>';
  }
  box.innerHTML = html;
  $('scanNote').textContent = bad.length ? 'найдено: ' + bad.length : '';
  if(bad.length){
    $('addBad').onclick = () => {
      const cur = ($('directHosts').value || '').trim();
      $('directHosts').value = cur ? cur + ', ' + r.problems : r.problems;
      formDirty = true;
      clearTimeout(scanTimer);
      $('scanNote').textContent = 'добавлено — не забудь «Сохранить»';
    };
  }
  box.onmouseenter = () => clearTimeout(scanTimer);
  box.onmouseleave = hideScanLater;
  hideScanLater();
};

async function copyToClipboard(text){
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch(e) {
    // Запасной путь на случай, если доступ к буферу закрыт.
    const ta = document.createElement('textarea');
    ta.value = text;
    document.body.appendChild(ta);
    ta.select();
    let ok = false;
    try { ok = document.execCommand('copy'); } catch(e2) {}
    ta.remove();
    return ok;
  }
}

// PowerShell открывается с уже скопированной командой первого шага —
// остальное человек копирует кнопками рядом с каждой командой.
$('openPs').onclick = async () => {
  const ok = await copyToClipboard(cmdCreateKey());
  const r = await api('/api/openterminal', {});
  if(r.error){ $('keyHelpNote').textContent = r.error; return; }
  $('keyHelpNote').textContent = ok ? 'команда шага 1 в буфере: Ctrl+V и Enter'
                                    : 'скопируй команду вручную';
};

/* ---------- настройки ---------- */
/* Список «всегда напрямую» человек пишет как получится: через запятую, через
   пробел, с новой строки. Разбираем так же, как это делает ядро. */
function splitEntries(v){
  return (v || '').split(/[\s,;]+/).map(x => x.trim()).filter(Boolean);
}

[...FIELDS, ...CHECKS, 'directHosts'].forEach(f => {
  const el = $(f);
  if(el) el.addEventListener('input', () => { formDirty = true; });
});

$('save').onclick = async () => {
  const cfg = {};
  FIELDS.forEach(f => {
    const el = $(f);
    cfg[f] = el.type === 'number' ? parseInt(el.value, 10) || 0 : el.value.trim();
  });
  CHECKS.forEach(f => cfg[f] = $(f).checked);
  cfg.directHosts = splitEntries($('directHosts').value);
  cfg.filterMode = filterMode;
  cfg.filterApps = filterApps;
  const r = await api('/api/config', cfg);
  if(r.error){ showError(r.error, 'setError'); return; }
  showError('', 'setError');
  fillConfig(r.config);
  $('savedNote').textContent = r.note ? 'сохранено — ' + r.note : 'сохранено';
  setTimeout(() => $('savedNote').textContent = '', 4000);
};

function fillConfig(cfg){
  lastConfig = cfg;
  FIELDS.forEach(f => { if($(f)) $(f).value = cfg[f] ?? ''; });
  CHECKS.forEach(f => { if($(f)) $(f).checked = !!cfg[f]; });
  $('directHosts').value = (cfg.directHosts || []).join(', ');
  if(!filterDirty){
    filterMode = cfg.filterMode || 'all';
    filterApps = (cfg.filterApps || []).map(normApp);
  }
  renderModes();
  renderApps();
  formDirty = false;
}


async function refresh(){
  const st = await api('/api/status');
  setState(st.state, st.state === 'connected' ? st.config.host : '');
  // Пока пользователь правит форму, обновление с сервера её не трогает —
  // иначе набранный, но ещё не сохранённый адрес затирался бы.
  seenApps = (st.seenApps || []);
  if(!formDirty) fillConfig(st.config);
}

connectEvents();
refresh();
setInterval(() => { if(!running) refresh(); }, 5000);
