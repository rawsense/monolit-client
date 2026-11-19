// monolit client: интерфейс. Данные приходят из Go: mp.update(state) раз в секунду и после событий
(() => {
  const $ = (s, r = document) => r.querySelector(s);
  const $$ = (s, r = document) => [...r.querySelectorAll(s)];
  const go = window.mpState ? window : mock();
  let st = null, view = 'home', list = 'always', settings = null;

  // lucide 0.460 не знает root и ищет <i data-lucide> во всём документе: вызывать после вставки разметки.
  // Строка, ещё не вставленная в список, оставалась без значков (так пропадали иконки в «Соединениях»)
  const icons = () => window.lucide && lucide.createIcons({ attrs: { 'stroke-width': 1.5 } });
  const esc = (s) => String(s ?? '').replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));
  // Название с флагом страны: флаг — отдельным элементом со своим отступом, пробел из названия после него убирается
  const named = (s) => esc(s).replace(/((?:\p{Regional_Indicator}{2})|\u{1F3F4}[\u{E0061}-\u{E007A}]+\u{E007F})\s*/gu, '<span class="mc-flag">$1</span>');
  const setText = (el, t) => { if (el.textContent !== t) el.textContent = t; };

  // края прокручиваемых областей: класс mc-ft — есть что выше, mc-fb — есть что ниже (затемнение в app.css).
  // Пересчёт при прокрутке, смене размера области и её содержимого (строки добавились, список заменён)
  {
    const SEL = '.mc-list, .mc-srvlist, .mc-set';
    const edge = (el) => {
      const t = el.scrollTop > 1, b = el.scrollTop + el.clientHeight < el.scrollHeight - 1;
      if (el.classList.contains('mc-ft') !== t) el.classList.toggle('mc-ft', t);
      if (el.classList.contains('mc-fb') !== b) el.classList.toggle('mc-fb', b);
    };
    const ro = new ResizeObserver((es) => { for (const e of es) { const el = e.target.closest(SEL); if (el) edge(el); } });
    const watch = (el) => { ro.observe(el); for (const c of el.children) ro.observe(c); };
    const mo = new MutationObserver((ms) => { for (const m of ms) { for (const n of m.addedNodes) if (n.nodeType === 1) ro.observe(n); edge(m.target); } });
    document.querySelectorAll(SEL).forEach((el) => { watch(el); mo.observe(el, { childList: true }); edge(el); });
    addEventListener('scroll', (e) => { const el = e.target; if (el.nodeType === 1 && el.matches(SEL)) edge(el); }, { capture: true, passive: true });
  }

  function bytes(n) {
    if (!n) return '0 Б';
    const u = ['Б', 'КБ', 'МБ', 'ГБ', 'ТБ'];
    let i = 0; while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
    return (i >= 2 ? n.toFixed(1) : Math.round(n)).toString().replace('.', ',') + ' ' + u[i];
  }
  const mbit = (b) => ((b * 8) / 1e6).toFixed(1).replace('.', ',');
  const pad = (n) => String(n).padStart(2, '0');
  function since(t) {
    const s = Math.max(0, Math.floor(Date.now() / 1000 - t));
    return `${pad(Math.floor(s / 3600))}:${pad(Math.floor(s / 60) % 60)}:${pad(s % 60)}`;
  }
  function when(t) {
    const d = new Date(t * 1000), n = new Date();
    if (d.toDateString() === n.toDateString()) return `${pad(d.getHours())}:${pad(d.getMinutes())}`;
    const y = new Date(n); y.setDate(n.getDate() - 1);
    if (d.toDateString() === y.toDateString()) return 'вчера';
    return d.toLocaleDateString('ru-RU', { day: 'numeric', month: 'short' }).replace('.', '');
  }
  const date = (t) => new Date(t * 1000).toLocaleDateString('ru-RU', { day: 'numeric', month: 'long' });

  // ошибки вызовов Go: тост с коротким текстом
  const errText = (x) => String(x?.message || x || 'Неизвестная ошибка');
  const toastErr = (title, x) => window.toast?.({ type: 'error', title, text: errText(x) });
  // вызов Go, который может вернуть обещание или ничего; ошибку показываем тостом, onErr возвращает интерфейс
  function call(fn, title, onErr) {
    let p;
    try { p = Promise.resolve(fn()); } catch (x) { p = Promise.reject(x); }
    return p.catch((x) => { onErr?.(); toastErr(title, x); });
  }

  // ---------- смена разделов: гаснет 120 мс, проявляется 200 мс ----------
  // переход — отклик на нажатие, а не смена читаемого текста: при 200 + 360 мс пункты ощущались тугими.
  // Данные просим сразу, пока старый экран гаснет: новый появляется уже заполненным, без подстановки на виду
  function show(v) {
    if (v === view) return;
    const cur = $(`#v-${view}`), next = $(`#v-${v}`);
    view = v;
    const needSet = v === 'sites' || v === 'settings' || v.startsWith('s-');
    const pre = needSet ? Promise.resolve().then(() => go.mpSettings()).catch((x) => { toastErr('Настройки не загрузились', x); return null; }) : null;
    cur.animate([{ opacity: 1 }, { opacity: 0 }], { duration: 120, easing: 'ease-out' }).onfinish = async () => {
      cur.hidden = true;
      const data = pre ? await pre : null;
      if (view !== v) return; // пока гасло, раздел сменился (например, удалили последнюю подписку)
      if (data) settings = data;
      next.hidden = false;
      if (v === 'sites') renderList(false, !!data);
      else if (data) applySettings();
      if (v === 's-sub') renderSubPage(st);
      if (v === 'log') loadLog();
      if (v === 's-net') cxStart();
      if (v === 'settings') cxCount();
    };
  }
  // сайты и программы — один экран: у программ нет вкладок и свой заголовок
  function sitesFor(v) {
    const apps = v === 'apps';
    if (apps) list = 'apps';
    else if (list === 'apps') { list = 'always'; $$('#site-tabs .tab').forEach((x) => x.setAttribute('aria-selected', x.dataset.list === 'always')); }
    $('#site-tabs').hidden = apps;
    setText($('#sites-h'), apps ? 'Программы' : 'Сайты');
  }
  document.addEventListener('click', (e) => {
    if (e.target.closest('.mc-info')) { e.preventDefault(); return; } // «i» внутри пункта: подсказка, а не переключение
    const b = e.target.closest('[data-go]');
    if (!b) return;
    let v = b.dataset.go;
    if (v === 'apps' || v === 'sites') { sitesFor(v); v = 'sites'; }
    show(v);
  });

  // ---------- главная ----------
  $('#pwr').addEventListener('click', () => call(() => go.mpToggle(), 'Не получилось'));
  const pressMode = (m) => $$('.mc-mode').forEach((x) => x.setAttribute('aria-pressed', x.dataset.mode === m));
  $$('.mc-mode').forEach((b) => b.addEventListener('click', () => {
    pressMode(b.dataset.mode);
    call(() => go.mpMode(b.dataset.mode), 'Режим не сменился', () => pressMode(st?.mode));
  }));

  function update(s) {
    st = s;
    // без подписки — экран приветствия, из какого бы раздела ни удалили последнюю
    if (!s.hasSub && view !== 'welcome') { $$('.mc-view').forEach((x) => { x.hidden = x.id !== 'v-welcome'; }); view = 'welcome'; }
    if (s.hasSub && view === 'welcome') { $('#v-welcome').hidden = true; $('#v-home').hidden = false; view = 'home'; }
    const pwr = $('#pwr');
    pwr.setAttribute('aria-pressed', s.on);
    pwr.setAttribute('aria-busy', !!s.busy);
    pwr.setAttribute('aria-label', s.on ? 'Отключить' : 'Подключить');
    pwr.disabled = !s.hasSub || s.busy === 'Отключение'; // во время подключения кнопка отменяет его
    setText($('#st-title'), s.busy || (s.on ? 'Подключено' : 'Отключено'));
    const sub = $('#st-sub');
    sub.classList.toggle('mc-bad', !s.on && !!s.err);
    setText(sub, s.on ? since(s.since) : (s.err || ' '));

    renderSub(s);
    renderServers(s);
    const rf = $('#ss-refresh');
    rf.classList.toggle('is-loading', !!s.refreshing);
    if (view === 's-sub') renderSubPage(s);
    if (view === 'settings') renderRoot();
    $$('.mc-mode').forEach((b) => b.setAttribute('aria-pressed', b.dataset.mode === s.mode));
    $('.mc-mode[data-mode="ai"]').hidden = !s.showAI;
  }
  window.mp = { update };

  // ---------- подписка ----------
  $('#sub-form').addEventListener('submit', async (e) => {
    e.preventDefault();
    const err = $('#sub-err'), btn = e.submitter;
    if (btn.classList.contains('is-loading')) return;
    err.hidden = true; btn.classList.add('is-loading');
    try { await go.mpSub($('#sub-url').value); } catch (x) { err.textContent = String(x.message || x); err.hidden = false; }
    btn.classList.remove('is-loading');
  });

  // ---------- сайты ----------
  const listHints = {
    always: 'Эти сайты всегда открываются через VPN. Claude, ChatGPT и Gemini уже в списке по умолчанию',
    direct: 'Эти сайты всегда открываются напрямую, даже в режиме «Всё»',
    found: 'Сайты, которые не открылись напрямую и сами перешли на VPN. Удалите сайт, чтобы проверить его снова',
    apps: 'Весь трафик этих программ идёт выбранным путём, даже в умном режиме',
  };
  $$('#site-tabs .tab').forEach((t) => t.addEventListener('click', () => {
    $$('#site-tabs .tab').forEach((x) => x.setAttribute('aria-selected', x === t));
    list = t.dataset.list;
    renderList(true);
  }));
  async function renderList(fade, have) {
    if (!have) { try { settings = await go.mpSettings(); } catch (x) { toastErr('Список не загрузился', x); return; } }
    const apps = list === 'apps';
    // программы: оба списка вместе, у каждой подписан путь
    const items = (apps ? [...(settings.apps || []).map((r) => ({ ...r, l: 'apps' })), ...(settings.appsDirect || []).map((r) => ({ ...r, l: 'appsDirect' }))]
      : settings[list] || []).slice().sort((a, b) => (b.when || 0) - (a.when || 0));
    // программу выбирают из запущенных или файлом, а не вписывают имя: у игр оно вроде VALORANT-Win64-Shipping.exe
    $('#add-form').hidden = list === 'found' || apps;
    $('#app-pick').hidden = !apps;
    setText($('#list-hint'), listHints[list]);
    const el = $('#list');
    const row = (r) => apps
      ? `<div class="mc-row mc-app"><span class="app-ic" data-path="${esc(r.path || '')}"><i data-lucide="app-window"></i></span><span class="mc-row-tx"><b>${esc(r.app || r.domain)}</b><span>${esc([r.l === 'apps' ? 'Через VPN' : 'Напрямую', r.app ? r.domain : '', r.when ? when(r.when) : ''].filter(Boolean).join(', '))}</span></span><button class="icon-btn" data-del="${esc(r.domain)}" data-list="${r.l}" aria-label="Убрать ${esc(r.app || r.domain)}"><i data-lucide="x"></i></button></div>`
      : `<div class="mc-row"><span class="mc-row-tx"><b>${esc(r.domain)}</b><span>${esc([r.reason, r.when ? when(r.when) : ''].filter(Boolean).join(', ') || ' ')}</span></span><button class="icon-btn" data-del="${esc(r.domain)}" aria-label="Убрать"><i data-lucide="x"></i></button></div>`;
    const html = items.length ? items.map(row).join('')
      : `<p class="mc-empty">${list === 'found' ? 'Пока ничего не найдено' : apps ? 'Программ в списке нет' : 'Список пуст'}</p>`;
    el.innerHTML = html;
    icons(el);
    if (fade) el.animate([{ opacity: 0 }, { opacity: 1 }], { duration: 360, easing: 'ease-out' });
    if (apps) appIcons(el);
  }
  // значки программ списка: из файла по пути; пока не пришли — общий знак, пришёл — проявляется на его месте
  async function appIcons(el) {
    const spots = $$('.app-ic[data-path]', el).filter((s) => s.dataset.path);
    if (!spots.length || !go.mpIcons) return;
    let m;
    try { m = await go.mpIcons(spots.map((s) => s.dataset.path)); } catch { return; }
    for (const s of spots) if (m?.[s.dataset.path] && s.isConnected) s.innerHTML = `<img src="${m[s.dataset.path]}" alt="">`;
  }
  $('#list').addEventListener('click', async (e) => {
    const b = e.target.closest('[data-del]');
    if (!b) return;
    const row = b.closest('.mc-row');
    b.disabled = true;
    try { await go.mpRule(b.dataset.list || list, b.dataset.del, false); } catch (x) { b.disabled = false; toastErr('Не удалено', x); return; }
    const h = row.offsetHeight;
    row.animate([{ opacity: 1, height: h + 'px' }, { opacity: 0, height: '0px', paddingTop: 0, paddingBottom: 0 }], { duration: 200, easing: 'ease-out' }).onfinish = () => renderList();
  });
  $('#add-form').addEventListener('submit', async (e) => {
    e.preventDefault();
    const inp = $('#add-val');
    if (!inp.value.trim()) return;
    try { await go.mpRule(list, inp.value, true); inp.value = ''; renderList(); }
    catch (x) { toastErr('Не добавлено', x); }
  });

  // ---------- выбор программы ----------
  // окно: запущенные программы (сначала те, что ходят в сеть), поиск по названию и имени файла.
  // Нажал на строку — программа в списке, окно закрылось. Нет в списке — окно Windows «Открыть»
  let pkAll = [], pkTimer = 0;
  const pkRow = (p) => `<button class="alist-row pk-row" type="button" data-path="${esc(p.path)}"${p.listed ? ' disabled' : ''}><span class="app-ic">${p.icon ? `<img src="${p.icon}" alt="">` : '<i data-lucide="app-window"></i>'}</span><span class="alist-tx"><b>${esc(p.name)}</b><span>${esc(p.exe)}</span></span>${p.listed ? '<span class="badge">В списке</span>' : ''}</button>`;
  function pkRender(fade) {
    const q = $('#pk-q').value.trim().toLowerCase();
    const rows = q ? pkAll.filter((p) => p.name.toLowerCase().includes(q) || p.exe.toLowerCase().includes(q)) : pkAll;
    const el = $('#pk-list');
    el.innerHTML = rows.length ? `<div class="alist">${rows.map(pkRow).join('')}</div>`
      : `<p class="mc-empty">${q ? 'Ничего не нашлось' : 'Запущенных программ не нашлось. Выберите файл программы'}</p>`;
    icons(el);
    if (fade) el.animate([{ opacity: 0 }, { opacity: 1 }], { duration: 360, easing: 'ease-out' });
  }
  $('#app-pick').addEventListener('click', async () => {
    const d = $('#dlg-apps');
    $('#pk-q').value = ''; pkAll = [];
    $('#pk-list').innerHTML = '<p class="mc-empty">Ищу запущенные программы</p>';
    d.showModal();
    try { pkAll = (await go.mpProcs()) || []; } catch (x) { toastErr('Список программ не загрузился', x); }
    if (d.open) pkRender(true);
  });
  // поиск — после паузы в наборе: старый список гаснет, новый проявляется
  $('#pk-q').addEventListener('input', () => {
    clearTimeout(pkTimer);
    pkTimer = setTimeout(() => { $('#pk-list').animate([{ opacity: 1 }, { opacity: 0 }], { duration: 200, easing: 'ease-out' }).onfinish = () => pkRender(true); }, 250);
  });
  async function pkAdd(path) {
    try { await go.mpAppAdd(path); } catch (x) { toastErr('Не добавлено', x); return false; }
    $('#dlg-apps').close();
    settings = await go.mpSettings();
    renderList(true, true);
    const r = (settings.apps || []).find((a) => a.path === path || path.toLowerCase().endsWith(String(a.domain).toLowerCase()));
    window.toast?.({ type: 'success', title: `${r?.app || 'Программа'} теперь через VPN` });
    return true;
  }
  $('#pk-list').addEventListener('click', (e) => {
    const b = e.target.closest('.pk-row');
    if (b && !b.disabled) pkAdd(b.dataset.path);
  });
  $('#pk-file').addEventListener('click', async () => {
    let p;
    try { p = await go.mpPickExe(); } catch (x) { toastErr('Окно выбора не открылось', x); return; }
    if (p) pkAdd(p);
  });

  // ---------- подписка ----------
  const subMeta = (x) => [x.total ? `${bytes(x.used)} из ${bytes(x.total)}` : 'без лимита', x.expire ? 'до ' + date(x.expire) : ''].filter(Boolean).join(', ');
  function renderSub(s) {
    const subs = s.subs || [], cur = subs.find((x) => x.id === s.active) || subs[0];
    setText($('#sub-name'), cur ? cur.title : 'Нет подписки');
    // трафик — только у подписки с лимитом: шкала и сколько потрачено. Без лимита потраченное не показываем
    // (оно по-прежнему приходит в cur.used — понадобится в настройках)
    const tr = $('#traffic');
    tr.hidden = !cur;
    if (cur) {
      setText($('#tr-used'), cur.total ? `${bytes(cur.used)} из ${bytes(cur.total)}` : '');
      setText($('#tr-left'), cur.expire ? 'до ' + date(cur.expire) : '');
      const bar = $('#tr-bar');
      bar.style.visibility = cur.total ? '' : 'hidden'; // место под шкалу остаётся, чтобы кнопка не сдвигалась
      if (cur.total) { const p = Math.min(1, cur.used / cur.total); bar.firstElementChild.style.setProperty('--p', p.toFixed(3)); bar.setAttribute('aria-valuenow', Math.round(p * 100)); }
    }
    const m = $('#m-sub');
    const key = JSON.stringify([s.active, subs.map((x) => [x.id, x.title])]);
    if (m.dataset.key === key) return;
    m.dataset.key = key;
    m.innerHTML = subs.map((x) => `<div class="mc-subitem"><button class="menu-item" role="menuitemradio" aria-checked="${x.id === s.active}" data-sub="${esc(x.id)}"><span>${named(x.title)}</span></button><button class="icon-btn" type="button" data-del="${esc(x.id)}" aria-label="Удалить ${esc(x.title)}" data-tip="Удалить"><i data-lucide="trash-2"></i></button></div>`).join('');
    icons(m);
  }
  $('#m-sub').addEventListener('menu-pick', async (e) => {
    const id = e.detail.item.dataset.sub;
    if (!id || id === st.active) return;
    const name = (st.subs || []).find((v) => v.id === id)?.title || '';
    if (st.on && !(await ask('Переключить подписку?', `VPN переподключится на серверы «${name}», открытые соединения прервутся на несколько секунд: звонки, загрузки, игры`, 'Переключить'))) return;
    try { await go.mpSubUse(id); } catch (x) { toastErr('Не получилось', x); }
  });
  let delId = '';
  $('#m-sub').addEventListener('click', (e) => {
    const b = e.target.closest('[data-del]');
    if (!b) return;
    delId = b.dataset.del;
    const x = (st.subs || []).find((v) => v.id === delId);
    setText($('#dd-text'), `«${x?.title}» пропадёт из списка вместе с её серверами. Вернуть можно, снова вставив ссылку`);
    $('#dlg-del').showModal();
  });
  $('#dd-ok').addEventListener('click', () => { $('#dlg-del').close(); if (delId) call(() => go.mpSubDel(delId), 'Не удалено'); });
  // проверка серверов и обновление подписки: Go работает в фоне, кнопка крутится, пока идёт работа
  $('#srv-check').addEventListener('click', () => { if (!st?.checking) call(() => go.mpCheck(), 'Проверка не запустилась'); });
  $('#sub-refresh').addEventListener('click', () => { if (!st?.refreshing) call(() => go.mpRefresh(), 'Обновление не запустилось'); });
  // окно новой подписки: при каждом открытии и закрытии — пустое поле без старой ошибки
  const resetAddSub = () => { $('#add-sub-url').value = ''; $('#add-sub-err').hidden = true; setText($('#add-sub-err'), ''); };
  $('[data-open="dlg-add"]').addEventListener('click', resetAddSub);
  $('#dlg-add').addEventListener('close', resetAddSub);
  $('#add-sub-form').addEventListener('submit', async (e) => {
    e.preventDefault();
    const err = $('#add-sub-err'), btn = e.submitter, inp = $('#add-sub-url');
    if (btn.classList.contains('is-loading')) return;
    err.hidden = true; btn.classList.add('is-loading');
    try { await go.mpSub(inp.value); inp.value = ''; $('#dlg-add').close(); }
    catch (x) { err.textContent = String(x.message || x); err.hidden = false; }
    btn.classList.remove('is-loading');
  });

  // ---------- конфиги выбранной подписки ----------
  // строка: сервер, протокол серым, задержка справа. Список собирается заново только при смене набора, остальное — на месте
  function renderServers(s) {
    $('#srv-check').setAttribute('aria-busy', !!s.checking);
    const rf = $('#sub-refresh'), was = rf.getAttribute('aria-busy') === 'true';
    rf.setAttribute('aria-busy', !!s.refreshing);
    if (was && !s.refreshing) window.toast?.(s.refreshErr ? { type: 'error', title: 'Не получилось', text: s.refreshErr } : s.refreshNote ? { title: s.refreshNote } : { type: 'success', title: 'Подписка обновлена' });
    const el = $('#servers'), list = s.conns || [];
    const key = JSON.stringify(list.map((c) => c.name));
    if (el.dataset.key !== key) {
      el.dataset.key = key;
      el.innerHTML = list.map((c) => `<button class="mc-srvi" role="radio" data-conn="${esc(c.name)}" aria-checked="false"><span class="mc-srvi-tx"><b>${named(c.srv || c.name)}</b><span class="badge">${esc(c.proto)}</span></span><em class="mc-num"></em></button>`).join('');
    }
    [...el.children].forEach((b, i) => {
      const c = list[i];
      b.setAttribute('aria-checked', c.name === s.conn);
      const m = b.querySelector('em');
      const bad = s.on && c.known && !c.alive;
      setText(m, !s.on || !c.known ? '' : c.alive ? `${c.delay} мс` : 'нет ответа');
      m.classList.toggle('mc-bad', bad);
      const step = s.on && c.known && c.alive ? pingStep(c.delay) : 0;
      if (step) m.dataset.ping = step; else delete m.dataset.ping;
    });
  }
  // ступень цвета задержки: до 50, 80, 150, 300 мс и больше
  const pingSteps = [50, 80, 150, 300];
  function pingStep(ms) { const i = pingSteps.findIndex((v) => ms <= v); return i < 0 ? 5 : i + 1; }
  $('#servers').addEventListener('click', (e) => {
    const b = e.target.closest('[data-conn]');
    if (!b || b.getAttribute('aria-checked') === 'true') return;
    $$('#servers [data-conn]').forEach((x) => x.setAttribute('aria-checked', x === b));
    call(() => go.mpConn(b.dataset.conn), 'Сервер не сменился', () => $$('#servers [data-conn]').forEach((x) => x.setAttribute('aria-checked', x.dataset.conn === st?.conn)));
  });

  // ---------- настройки ----------
  // занятые кнопки — системное .is-loading (крутилка вместо надписи), а не disabled: у выключенной кнопки
  // курсор «нельзя», у занятой Windows рисовал «загрузку», и пользователю казалось, что программа подвисла
  const curSub = (s) => (s?.subs || []).find((x) => x.id === s.active) || s?.subs?.[0];
  const connName = { auto: 'Авто', tun: 'TUN', proxy: 'Прокси' };
  // подписи под разделами в списке настроек: что сейчас выбрано
  function renderRoot() {
    const cur = curSub(st);
    setText($('#sr-sub'), cur ? [cur.title, cur.expire ? 'до ' + date(cur.expire) : ''].filter(Boolean).join(', ') : 'Нет подписки');
    if (settings) setText($('#sr-conn'), [connName[settings.connect] || '', settings.autoStart ? 'запуск с Windows' : ''].filter(Boolean).join(', '));
    setText($('#sr-about'), st?.update ? `Доступна версия ${st.update}` : `Monolit ${st?.version || ''}`.trim());
  }
  function updatedText(t) {
    if (!t) return 'Ещё не обновлялись';
    const d = new Date(t * 1000);
    return 'Обновлены ' + (d.toDateString() === new Date().toDateString() ? `сегодня в ${pad(d.getHours())}:${pad(d.getMinutes())}` : when(t));
  }
  function renderSubPage(s) {
    const cur = curSub(s);
    if (!cur) return;
    setText($('#ss-title'), cur.title);
    setText($('#ss-expire'), cur.expire ? 'Действует до ' + date(cur.expire) : 'Без срока');
    setText($('#ss-traffic'), cur.total ? `${bytes(cur.used)} из ${bytes(cur.total)}` : `${bytes(cur.used)}, без лимита`);
    setText($('#ss-updated'), updatedText(cur.updated));
  }
  $('#ss-refresh').addEventListener('click', () => { if (!st?.refreshing) call(() => go.mpRefresh(), 'Обновление не запустилось'); });
  $('#ss-del').addEventListener('click', () => {
    const cur = curSub(st);
    if (!cur) return;
    delId = cur.id;
    setText($('#dd-text'), `«${cur.title}» пропадёт из списка вместе с её серверами. Вернуть можно, снова вставив ссылку`);
    $('#dlg-del').showModal();
  });
  const connectHint = { auto: 'Весь трафик компьютера через виртуальную сетевую карту. Если она не запустится, клиент включит системный прокси', tun: 'Весь трафик компьютера через виртуальную сетевую карту, включая Telegram, Discord и игры', proxy: 'Системный прокси и переменные для консольных программ. Программы, которые не читают прокси, пойдут напрямую' };
  async function loadSettings() {
    try { settings = await go.mpSettings(); } catch (x) { toastErr('Настройки не загрузились', x); return; }
    applySettings();
  }
  function applySettings() {
    $$('[data-set]').forEach((el) => {
      const v = settings[el.dataset.set];
      if (el.type === 'checkbox') el.checked = !!v; else el.value = v ?? '';
    });
    pressConnect(settings.connect, false);
    setText($('#s-version'), `Версия ${st?.version || ''}`);
    setText($('#s-update'), st?.update ? `Доступна версия ${st.update}` : 'Последняя версия');
    $('#s-upd-btn').hidden = !st?.update;
    setText($('#s-geo'), st?.geo ? 'обновлены ' + when(st.geo) : 'не скачаны');
    renderRoot();
  }
  // подтверждение перед тем, что рвёт соединения или меняет, какие программы идут через VPN.
  // Окно закрыли крестиком, фоном или Esc — это «Отмена»
  function ask(title, text, okLabel) {
    const d = $('#dlg-ok');
    setText($('#do-t'), title); setText($('#do-text'), text); setText($('#do-ok'), okLabel);
    d.returnValue = '';
    d.showModal();
    return new Promise((res) => d.addEventListener('close', () => res(d.returnValue === 'ok'), { once: true }));
  }
  $('#do-ok').addEventListener('click', () => $('#dlg-ok').close('ok'));

  const reconnect = 'VPN переподключится, открытые соединения прервутся на несколько секунд: звонки, загрузки, игры';
  const connectAsk = {
    auto: ['Переключить на «Авто»?', reconnect + '. Через VPN пойдёт весь трафик компьютера, включая Discord, Telegram и игры. Если виртуальная сетевая карта не запустится, включится системный прокси'],
    tun: ['Переключить на TUN?', reconnect + '. Через VPN пойдёт весь трафик компьютера, включая Discord, Telegram и игры'],
    proxy: ['Переключить на прокси?', reconnect + '. Через VPN пойдут только программы, которые читают системный прокси: браузеры и часть приложений. Discord, Telegram и игры пойдут напрямую'],
  };
  const optName = (el, v) => [...el.options].find((o) => o.value === v)?.textContent || v;
  // что сказать перед сменой настройки; null — подтверждение не нужно
  function setAsk(el, v) {
    if (!st?.on) return null;
    switch (el.dataset.set) {
      case 'port': return ['Сменить порт?', `${reconnect}. Программы, где вручную указан 127.0.0.1:${settings.port}, перестанут ходить через VPN, пока в них не указан новый порт`, 'Сменить'];
      case 'dns': return ['Сменить DNS?', `${reconnect}. Адреса заблокированных сайтов будут искаться через ${optName(el, v)}`, 'Сменить'];
      case 'fingerprint': return ['Сменить отпечаток TLS?', `${reconnect}. Если сервер не примет новый отпечаток, подключение пропадёт: тогда верните «Как в подписке»`, 'Сменить'];
    }
    return null;
  }
  let reverting = false;
  function revert(el) {
    reverting = true;
    if (el.type === 'checkbox') el.checked = !!settings[el.dataset.set]; else el.value = settings[el.dataset.set] ?? '';
    el.dispatchEvent(new Event('change', { bubbles: true })); // выпадающий список обновит подпись
    reverting = false;
  }
  $$('[data-set]').forEach((el) => el.addEventListener('change', async () => {
    if (reverting || !settings) return; // настройки ещё не загружены — сравнивать не с чем
    const key = el.dataset.set;
    let v = el.type === 'checkbox' ? el.checked : el.value;
    if (key === 'port' || key === 'updateHours') v = Number(v);
    // порт: целое 1024–65528, рядом с ним ядро занимает ещё несколько
    if (key === 'port' && (!Number.isInteger(v) || v < 1024 || v > 65528)) {
      window.toast?.({ type: 'error', title: 'Неверный порт', text: 'Укажите целое число от 1024 до 65528' });
      revert(el); return;
    }
    if (v === settings[key]) return;
    const q = setAsk(el, v);
    if (q && !(await ask(...q))) { revert(el); return; }
    const old = settings[key];
    settings[key] = v;
    try { await go.mpSave({ [key]: v }); }
    catch (x) { settings[key] = old; revert(el); toastErr('Не сохранено', x); }
  }));
  // ползунок переключателя двигает сам компонент (select), одной отметки aria-pressed мало
  function pressConnect(v, anim = true) {
    const box = $('#s-connect'), b = box.querySelector(`[data-v="${v}"]`);
    if (b && box.select) box.select(b, anim); else $$('#s-connect button').forEach((x) => x.setAttribute('aria-pressed', x.dataset.v === v));
    setText($('#s-connect-hint'), connectHint[v] || '');
  }
  $$('#s-connect button').forEach((b) => b.addEventListener('click', async () => {
    if (!settings) return; // настройки ещё не загружены
    const v = b.dataset.v, was = settings.connect;
    if (v === was) return;
    pressConnect(v);
    if (st?.on && !(await ask(...connectAsk[v], 'Переключить'))) { pressConnect(was); return; }
    settings.connect = v;
    try { await go.mpSave({ connect: v }); }
    catch (x) { settings.connect = was; pressConnect(was); toastErr('Не сохранено', x); }
  }));

  async function busyBtn(b, fn, okTitle) {
    if (b.classList.contains('is-loading')) return;
    b.classList.add('is-loading');
    try { await fn(); if (okTitle) window.toast && toast({ type: 'success', title: okTitle }); }
    catch (x) { toastErr('Не получилось', x); }
    b.classList.remove('is-loading');
  }
  $('#s-upd-btn').addEventListener('click', async (e) => {
    const b = e.currentTarget;
    if (!(await ask('Обновить Monolit?', `Программа скачает версию ${st?.update || ''} и перезапустится. ${st?.on ? 'На время перезапуска VPN отключится' : ''}`.trim(), 'Обновить'))) return;
    busyBtn(b, () => go.mpUpdate());
  });
  $('#s-geo-btn').addEventListener('click', (e) => busyBtn(e.currentTarget, async () => {
    const changed = await go.mpGeo();
    if (st) st.geo = Date.now() / 1000;
    loadSettings();
    window.toast?.(changed === false ? { title: 'Базы сайтов уже свежие' } : { type: 'success', title: 'Базы сайтов обновлены' });
  }));

  async function loadLog() {
    let lines;
    try { lines = await go.mpLog(); } catch (x) { $('#log').textContent = 'Журнал не загрузился'; toastErr('Журнал не загрузился', x); return; }
    $('#log').textContent = lines?.length ? lines.join('\n') : 'Записей пока нет';
  }

  // ---------- соединения ----------
  // Go держит открытые и закрытые за 30 минут соединения в памяти (conns.go). Пока экран открыт и окно видно —
  // опрос раз в 2 с; строки меняются на месте по ключу (новые проявляются, ушедшие сворачиваются, перестановка — FLIP)
  const NF = new Intl.NumberFormat('ru-RU');
  let cxAll = [], cxTime = 'now', cxBy = 'site', cxPath = '', cxKey = null, cxKeyBy = 'site', cxTimer = 0;
  const plural = (n, f) => f[n % 10 === 1 && n % 100 !== 11 ? 0 : n % 10 >= 2 && n % 10 <= 4 && (n % 100 < 10 || n % 100 >= 20) ? 1 : 2];
  const nConn = (n) => `${n} ${plural(n, ['соединение', 'соединения', 'соединений'])}`;
  const nSite = (n) => `${n} ${plural(n, ['сайт', 'сайта', 'сайтов'])}`;
  const cxPathName = { vpn: 'VPN', direct: 'Напрямую', block: 'Заблокировано' };
  const cxBadge = { vpn: 'badge badge-accent', direct: 'badge', block: 'badge badge-danger' };
  // почему соединение пошло этим путём: теги правил из xcore.go
  const cxRules = { dns: 'запрос DNS', ipv6: 'IPv6 сервер не поддерживает', probe: 'проверка умного режима', 'core-dns': 'DNS самого клиента',
    local: 'этот компьютер', 'local-name': 'этот компьютер', private: 'локальная сеть', server: 'адрес сервера VPN', ads: 'реклама и трекеры', apps: 'программа в списке «через VPN»', 'apps-direct': 'программа в списке «напрямую»', 'ads-allow': 'кабинет рекламы, не блокируется',
    'my-direct': 'твоё правило «напрямую»', 'my-vpn': 'твоё правило «через VPN»', ai: 'сайт ИИ', torrent: 'торрент', ru: 'российский сайт',
    'nameless-ru': 'российский адрес', found: 'найден умным режимом', blocked: 'в базе заблокированных', 'nameless-blocked': 'адрес в базе заблокированных',
    'svc-direct': 'проверен: открывается напрямую', 'svc-vpn': 'проверен: напрямую не открывается', 'svc-wait': 'проверяется, пока через VPN', 'temp-ip': 'продолжение зависшего HTTP/3, через VPN', 'check-vpn': 'новый сайт: через VPN, пока идёт проверка', 'race-vpn': 'напрямую не ответил, пошёл через VPN' };
  function cxReason(r) {
    if (cxRules[r.rule]) return cxRules[r.rule];
    if (r.rule === 'rest') return r.path === 'vpn' ? 'режим «Всё»' : 'нет в списках заблокированных';
    return 'по умолчанию';
  }
  // сайт — по основному имени (cdn.discordapp.com → discordapp.com); без имени — по адресу
  const sld = new Set(['com', 'co', 'org', 'net', 'gov', 'edu', 'ac', 'msk', 'spb']);
  function cxSite(r) {
    const d = r.domain;
    if (!d) return (r.dest || '').replace(/:\d+$/, '').replace(/^\[|\]$/g, '') || 'без адреса';
    const p = d.split('.');
    return p.slice(p.length > 2 && sld.has(p[p.length - 2]) ? -3 : -2).join('.');
  }
  const cxProc = (r) => r.proc ? r.proc.charAt(0).toUpperCase() + r.proc.slice(1) : 'Не определена';
  const cxKeyOf = (r) => cxBy === 'site' ? cxSite(r) : cxProc(r);
  const median = (a) => { if (!a.length) return -1; a.sort((x, y) => x - y); return a[a.length >> 1]; };
  const ms = (v) => v < 0 ? '' : v < 1000 ? `${v} мс` : `${(v / 1000).toFixed(1).replace('.', ',')} с`;
  const names = (set, max = 3) => { const a = [...set]; return a.length > max ? `${a.slice(0, max).join(', ')} и ещё ${a.length - max}` : a.join(', '); };

  function cxGroups() {
    const rows = cxTime === 'now' ? cxAll.filter((r) => !r.end) : cxAll;
    const groups = new Map(), counts = { '': 0, vpn: 0, direct: 0, block: 0 };
    for (const r of rows) {
      const k = cxKeyOf(r);
      let g = groups.get(k);
      if (!g) groups.set(k, g = { key: k, rows: [], paths: new Set() });
      g.rows.push(r); g.paths.add(r.path);
    }
    for (const g of groups.values()) { counts['']++; for (const p of g.paths) counts[p]++; }
    const list = [];
    for (const g of groups.values()) {
      const rs = cxPath ? g.rows.filter((r) => r.path === cxPath) : g.rows;
      if (!rs.length) continue;
      const x = { key: g.key, rows: rs, up: 0, down: 0, open: 0, paths: new Set(), reasons: new Set(), procs: new Set(), sites: new Set(), vias: new Set() };
      const firsts = [];
      for (const r of rs) {
        x.up += r.up; x.down += r.down; if (!r.end) x.open++;
        x.paths.add(r.path); x.reasons.add(cxReason(r)); x.procs.add(cxProc(r)); x.sites.add(cxSite(r));
        if (r.via) x.vias.add(r.via);
        if (r.first >= 0) firsts.push(r.first);
      }
      x.first = median(firsts);
      list.push(x);
    }
    list.sort((a, b) => (b.up + b.down) - (a.up + a.down) || a.key.localeCompare(b.key));
    return { list, counts };
  }
  const cxPathLabel = (g) => g.paths.size > 1 ? 'Разные пути' : cxPathName[[...g.paths][0]];
  const cxBadgeOf = (g) => g.paths.size > 1 ? 'badge' : cxBadge[[...g.paths][0]];
  // строка списка: значок, домен и сразу за ним метка пути; скорость сейчас одной строкой; пинг.
  // Скорость — по разнице переданного между опросами; стрелка подсвечена, пока по ней идут данные
  // скорость — по каждому соединению между опросами, а не по сумме группы: у сайта соединения закрываются
  // и пропадают из «сейчас», и разница сумм уходила в минус (обнулялась) — скорость была меньше реальной.
  // Закрытые остаются в истории за 30 минут, их последние байты тоже идут в скорость
  let cxSeen = new Map(), cxSeenT = 0, cxRate = new Map();
  function cxRates() {
    const now = Date.now(), dt = (now - cxSeenT) / 1000, seen = new Map();
    cxRate = new Map();
    for (const r of cxAll) {
      const p = cxSeen.get(r.id);
      seen.set(r.id, { up: r.up, down: r.down });
      if (!cxSeenT || dt <= 0) continue;
      // новое соединение открылось после прошлого опроса: всё переданное — за этот промежуток
      const u = r.up - (p ? p.up : 0), d = r.down - (p ? p.down : 0);
      if (u > 0 || d > 0) cxRate.set(r.id, { up: Math.max(0, u) / dt, down: Math.max(0, d) / dt });
    }
    cxSeen = seen; cxSeenT = now;
  }
  // скорость группы: все её соединения, и открытые, и закрытые с прошлого опроса
  function cxSpeed() {
    const by = new Map();
    for (const r of cxAll) {
      const v = cxRate.get(r.id);
      if (!v || (cxPath && r.path !== cxPath)) continue;
      const k = cxKeyOf(r), a = by.get(k) || { up: 0, down: 0 };
      a.up += v.up; a.down += v.down; by.set(k, a);
    }
    return by;
  }
  function cxRowEl(g) {
    const b = document.createElement('button');
    b.type = 'button'; b.className = 'alist-row cx-row'; b.dataset.k = g.key;
    b.innerHTML = `<span class="alist-ic"><i data-lucide="${cxBy === 'site' ? 'globe' : 'app-window'}"></i></span><span class="cx-name"><b></b><span class="badge"><i data-lucide="circle"></i><span></span></span></span><span class="cx-speed"><span><i data-lucide="arrow-down"></i><b></b></span><span><i data-lucide="arrow-up"></i><b></b></span></span><span class="cx-ping"></span>`;
    return b;
  }
  const rate = (n) => `${bytes(n)}/с`;
  const cxPathIcon = { VPN: 'shield-check', 'Напрямую': 'arrow-up-right', 'Заблокировано': 'ban', 'Разные пути': 'shuffle' };
  function cxFill(b, g, sp) {
    const name = b.children[1], speed = b.children[2];
    setText(name.children[0], g.key);
    // метка — значок пути; название — подсказкой (title)
    const badge = name.children[1], bc = cxBadgeOf(g), label = cxPathLabel(g), ic = cxPathIcon[label] || 'circle';
    if (badge.className !== bc) badge.className = bc;
    setText(badge.lastChild, label);
    if (badge.title !== label) badge.title = label;
    let needIcons = false;
    if (badge.dataset.ic !== ic) {
      const i = document.createElement('i'); i.dataset.lucide = ic;
      badge.firstChild.replaceWith(i); badge.dataset.ic = ic;
      needIcons = true; // значок нарисует icons() после вставки строк
    }
    setText(b.children[3], ms(g.first));
    const [dn, up] = speed.children;
    setText(dn.lastChild, rate(sp.down));
    setText(up.lastChild, rate(sp.up));
    dn.classList.toggle('on', sp.down > 0);
    up.classList.toggle('on', sp.up > 0);
    return needIcons;
  }
  function cxRender(fade) {
    const { list, counts } = cxGroups();
    $$('#cx-path .cx-st').forEach((t) => setText(t.querySelector('.stat-num'), NF.format(counts[t.dataset.v])));
    const box = $('#cx-list');
    let al = box.querySelector('.alist'), empty = box.querySelector('.mc-empty');
    if (!list.length) {
      if (al) al.remove();
      if (!empty) { empty = document.createElement('p'); empty.className = 'mc-empty'; box.append(empty); }
      setText(empty, !st?.on ? 'VPN выключен, через Monolit сейчас ничего не идёт' : cxPath ? 'Таким путём соединений нет' : cxTime === 'now' ? 'Сейчас открытых соединений нет' : 'За 30 минут соединений не было');
    } else {
      if (empty) empty.remove();
      if (!al) { al = document.createElement('div'); al.className = 'alist'; box.append(al); }
      // FLIP: запомнить, где строки были, переставить, плавно довести с прежнего места
      const before = new Map();
      for (const el of al.children) before.set(el.dataset.k, el.getBoundingClientRect().top);
      const keep = new Set(list.map((g) => g.key)), byKey = new Map([...al.children].map((el) => [el.dataset.k, el]));
      for (const [k, el] of byKey) if (!keep.has(k)) el.remove();
      let prev = null, added = false;
      const speed = cxSpeed();
      for (const g of list) {
        let el = byKey.get(g.key);
        const fresh = !el;
        if (fresh) { el = cxRowEl(g); added = true; }
        if (cxFill(el, g, speed.get(g.key) || { down: 0, up: 0 })) added = true;
        const want = prev ? prev.nextSibling : al.firstChild;
        if (el !== want) al.insertBefore(el, want);
        if (fresh && !fade) el.animate([{ opacity: 0 }, { opacity: 1 }], { duration: 200, easing: 'ease-out' });
        prev = el;
      }
      if (added) icons();
      if (!fade) for (const el of al.children) {
        const t0 = before.get(el.dataset.k);
        if (t0 === undefined) continue;
        const dy = t0 - el.getBoundingClientRect().top;
        if (Math.abs(dy) >= 1) el.animate([{ transform: `translateY(${dy}px)` }, { transform: 'none' }], { duration: 240, easing: 'cubic-bezier(.2,.8,.2,1)' });
      }
    }
    if (fade) box.animate([{ opacity: 0 }, { opacity: 1 }], { duration: 360, easing: 'ease-out' });
    if (cxKey !== null && $('#cx-sheet').open) cxDetail();
  }
  async function cxLoad() {
    try { cxAll = (await go.mpConns()) || []; } catch (x) { toastErr('Соединения не загрузились', x); return false; }
    cxRates();
    return true;
  }
  async function cxTick() {
    if (view !== 's-net') { clearInterval(cxTimer); cxTimer = 0; return; }
    if (document.hidden || cxFading) return;   // список гаснет перед сменой вида: не рисовать в него новый поверх старого
    if (await cxLoad()) cxRender(false);
  }
  async function cxStart() {
    if (await cxLoad()) cxRender(false);
    clearInterval(cxTimer);
    cxTimer = setInterval(cxTick, 2000);
  }
  // смена вида: список гаснет 200 мс и проявляется уже новым, с начала. Пока гаснет, обновление раз в 2 с
  // не рисует (иначе новый список появлялся поверх гаснущего и прыгала прокрутка); повторное нажатие не
  // запускает второе угасание, а просто дождётся текущего — фильтр к тому моменту уже новый
  let cxFading = false;
  function cxSwitch() {
    if (cxFading) return;
    cxFading = true;
    const box = $('#cx-list');
    box.animate([{ opacity: 1 }, { opacity: 0 }], { duration: 200, easing: 'ease-out', fill: 'forwards' }).onfinish = (e) => {
      box.querySelector('.alist')?.remove();
      box.scrollTop = 0;
      cxRender(true);
      e.target.cancel();
      cxFading = false;
    };
  }
  // меню в заголовке: время и группировка; подпись кнопки — выбранный пункт
  function cxMenu(m, fn) {
    $(m).addEventListener('menu-pick', (e) => {
      const it = e.detail.item;
      if (it.getAttribute('aria-checked') === 'true') return;
      $$('.menu-item', $(m)).forEach((x) => x.setAttribute('aria-checked', x === it));
      fn(it.dataset.v, it.dataset.short || e.detail.label);
      cxSwitch();
    });
  }
  cxMenu('#cx-m-time', (v, l) => { cxTime = v; setText($('#cx-time-l'), l); });
  cxMenu('#cx-m-by', (v, l) => {
    cxBy = v; setText($('#cx-by-l'), l);
    const i = document.createElement('i'); i.dataset.lucide = v === 'site' ? 'globe' : 'app-window'; i.id = 'cx-by-i';
    const old = $('#cx-by-i'), par = old.parentElement;
    old.replaceWith(i); icons(par);
  });
  // плитки путей: нажатая — фильтр, повторное нажатие снимает его
  $('#cx-path').addEventListener('click', (e) => {
    const b = e.target.closest('.cx-st');
    if (!b) return;
    cxPath = cxPath === b.dataset.v ? '' : b.dataset.v;
    $$('#cx-path .cx-st').forEach((x) => x.setAttribute('aria-pressed', x.dataset.v === cxPath));
    cxSwitch();
  });
  $('#cx-list').addEventListener('click', (e) => {
    const r = e.target.closest('.cx-row');
    if (!r) return;
    cxKey = r.dataset.k; cxKeyBy = cxBy;
    cxDetail();
    $('#cx-sheet').showModal();
  });

  // подробности: группа ищется заново при каждом опросе, шторка обновляется на месте
  function cxDetail() {
    const by = cxBy; cxBy = cxKeyBy;
    const { list } = cxGroups();
    cxBy = by;
    const g = list.find((x) => x.key === cxKey);
    setText($('#cx-st'), cxKey);
    const tot = $('#cx-tot'), gl = $('#cx-groups');
    if (!g) {
      setText($('#cx-ss'), 'Соединений больше нет');
      tot.innerHTML = gl.innerHTML = ''; tot._html = gl._html = '';
    } else {
      setText($('#cx-ss'), cxTime === 'now' ? `Открыто ${nConn(g.open)}` : `${nConn(g.rows.length)} за 30 минут, открыто ${g.open}`);
      const th = `<span>Получено <b>${esc(bytes(g.down))}</b></span><span>Отправлено <b>${esc(bytes(g.up))}</b></span>`;
      if (tot._html !== th) { tot.innerHTML = th; tot._html = th; }
      // строки: у программы — её сайты, у сайта — программы; внутри группы пути, сначала больший объём
      const items = new Map();
      for (const r of g.rows) {
        const name = cxKeyBy === 'site' ? cxProc(r) : (r.domain || (r.dest || '').replace(/:\d+$/, '').replace(/^\[|\]$/g, '') || 'без адреса');
        const k = r.path + '|' + name;
        let it = items.get(k);
        // у программы сайт с именем можно повести своим путём: правило на основное имя, как в шторке сайта
        if (!it) items.set(k, it = { name, path: r.path, down: 0, up: 0, reasons: new Map(), site: cxKeyBy === 'proc' && r.domain ? cxSite(r) : '' });
        it.down += r.down || 0; it.up += r.up || 0;
        const why = cxReason(r);
        it.reasons.set(why, (it.reasons.get(why) || 0) + 1);
      }
      const html = ['vpn', 'direct', 'block'].map((p) => {
        const list = [...items.values()].filter((it) => it.path === p).sort((a, b) => b.down + b.up - a.down - a.up);
        if (!list.length) return '';
        const rows = list.map((it) => {
          const why = [...it.reasons].sort((a, b) => b[1] - a[1])[0][0];
          const act = it.site ? ` cx-site" data-site="${esc(it.site)}" tabindex="0" role="button" aria-haspopup="menu` : '';
          return `<div class="cx-c pi ${p}${act}"><span><i data-lucide="${cxPathIc[p]}"></i></span><span><b>${esc(it.name)}</b><small>${esc(why.charAt(0).toUpperCase() + why.slice(1))}</small></span><span class="cx-num"><span><i data-lucide="arrow-down"></i><b>${esc(bytes(it.down))}</b></span><small><i data-lucide="arrow-up"></i><span>${esc(bytes(it.up))}</span></small></span></div>`;
        }).join('');
        return `<div class="cx-grp"><span class="t-label"><span>${cxGroupName[p]}</span><span>${list.length}</span></span><div class="cx-conns">${rows}</div></div>`;
      }).join('');
      if (gl._html !== html) {
        // строки перерисованы (растут цифры): строка с открытым меню остаётся подсвеченной, меню — при ней
        const open = cxSmRow && { site: cxSmRow.dataset.site, path: ['vpn', 'direct', 'block'].find((x) => cxSmRow.classList.contains(x)) };
        gl.innerHTML = html; gl._html = html; icons(gl);
        if (open) {
          const r = gl.querySelector(`.cx-site.${open.path}[data-site="${CSS.escape(open.site)}"]`);
          if (r) { cxSmRow = r; r.classList.add('on'); r.setAttribute('aria-expanded', 'true'); } else cxSmClose();
        }
      }
    }
    // путь: у сайта — свои списки «через VPN» / «напрямую», у программы — списки программ; адрес без имени не правится
    const exePath = cxKeyBy === 'proc' ? (g?.rows.find((r) => r.procDir)?.procDir || cxAppPath) : '';
    if (cxKeyBy === 'proc') cxAppPath = exePath;
    const site = cxKeyBy === 'site' && /[a-z]/i.test(cxKey);
    const seg = $('#cx-mode');
    seg.hidden = !site && !exePath;
    if (!seg.hidden && !seg.classList.contains('is-loading')) {
      const m = cxModeNow(site, exePath);
      const b = seg.querySelector(`[data-mode="${m}"]`);
      if (b.getAttribute('aria-pressed') !== 'true') seg.select?.(b, cxSegShown);
    }
    cxSegShown = true;
  }
  const cxPathIc = { vpn: 'shield-check', direct: 'arrow-up-right', block: 'ban' };
  const cxGroupName = { vpn: 'Через VPN', direct: 'Напрямую', block: 'Заблокировано' };
  let cxAppPath = '', cxSegShown = false;
  const cxExe = (p) => p.split(/[\\/]/).pop().toLowerCase();
  const cxIn = (l, v) => (settings?.[l] || []).some((r) => String(r.domain).toLowerCase() === v);
  const cxSiteMode = (d) => cxIn('always', d) ? 'vpn' : cxIn('direct', d) ? 'direct' : 'auto';
  // путь сайта во всех программах: свои списки «через VPN» / «напрямую», «Авто» — ни в одном
  async function cxSetSite(d, mode) {
    const prev = cxSiteMode(d);
    if (mode === prev) return;
    if (mode === 'auto') await go.mpRule(prev === 'vpn' ? 'always' : 'direct', d, false);
    else await go.mpRule(mode === 'vpn' ? 'always' : 'direct', d, true);
    settings = await go.mpSettings();
  }
  function cxModeNow(site, exePath) {
    if (site) return cxSiteMode(cxKey);
    const e = cxExe(exePath);
    return cxIn('apps', e) ? 'vpn' : cxIn('appsDirect', e) ? 'direct' : 'auto';
  }
  // выбор пути: плашка уже переехала; не вышло — возвращается назад
  $('#cx-mode').addEventListener('pick', async (e) => {
    const seg = e.currentTarget, mode = e.detail.dataset.mode;
    const site = cxKeyBy === 'site', key = cxKey, p = cxAppPath;
    const prev = cxModeNow(site, p);
    seg.classList.add('is-loading');
    try {
      if (!site) { await go.mpAppMode(p, mode); settings = await go.mpSettings(); }
      else await cxSetSite(key, mode);
    } catch (x) {
      toastErr('Путь не сменился', x);
      seg.select(seg.querySelector(`[data-mode="${prev}"]`));
    }
    seg.classList.remove('is-loading');
  });
  // меню пути у сайта в шторке программы: под строкой, отмечен текущий путь; закрывается выбором, нажатием мимо, Esc, прокруткой
  const cxSm = $('#cx-m-site');
  let cxSmRow = null;
  function cxSmClose(ret) {
    if (!cxSm.classList.contains('open')) return;
    cxSm.classList.remove('open');
    const r = cxSmRow; cxSmRow = null;
    if (r) { r.classList.remove('on'); r.setAttribute('aria-expanded', 'false'); if (ret && r.isConnected) r.focus(); }
  }
  function cxSmOpen(row, kb) {
    if (cxSmRow === row) { cxSmClose(true); return; }
    cxSmClose();
    cxSmRow = row; row.classList.add('on'); row.setAttribute('aria-expanded', 'true');
    const m = cxSiteMode(row.dataset.site);
    $$('.menu-item', cxSm).forEach((it) => it.setAttribute('aria-checked', it.dataset.mode === m));
    const r = row.getBoundingClientRect(), h = cxSm.offsetHeight, w = cxSm.offsetWidth, gap = 4;
    const below = r.bottom + gap + h <= innerHeight - 8;
    cxSm.style.left = Math.round(Math.min(r.left + 32, innerWidth - 8 - w)) + 'px';
    cxSm.style.top = Math.round(below ? r.bottom + gap : r.top - gap - h) + 'px';
    cxSm.style.setProperty('--origin', below ? 'top left' : 'bottom left');
    cxSm.classList.add('open');
    if (kb) cxSm.querySelector('[aria-checked=true]')?.focus();
  }
  $('#cx-groups').addEventListener('click', (e) => { const r = e.target.closest('.cx-site'); if (r) cxSmOpen(r, e.detail === 0); });
  $('#cx-groups').addEventListener('keydown', (e) => {
    const r = e.target.closest('.cx-site');
    if (r && (e.key === 'Enter' || e.key === ' ')) { e.preventDefault(); cxSmOpen(r, true); }
  });
  cxSm.addEventListener('keydown', (e) => {
    const it = $$('.menu-item', cxSm), i = it.indexOf(document.activeElement);
    if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); cxSmClose(true); }
    else if (e.key === 'ArrowDown' || e.key === 'ArrowUp') { e.preventDefault(); it[(i + (e.key === 'ArrowDown' ? 1 : -1) + it.length) % it.length].focus(); }
  });
  cxSm.addEventListener('click', async (e) => {
    const it = e.target.closest('.menu-item'), row = cxSmRow;
    if (!it || !row) return;
    const d = row.dataset.site;
    cxSmClose(true);
    try { await cxSetSite(d, it.dataset.mode); cxDetail(); } catch (x) { toastErr('Путь не сменился', x); }
  });
  addEventListener('pointerdown', (e) => { if (!cxSm.contains(e.target) && !e.target.closest?.('.cx-site')) cxSmClose(); });
  $('#cx-sheet .sheet-body').addEventListener('scroll', () => cxSmClose(), { passive: true });
  $('#cx-sheet').addEventListener('close', () => cxSmClose());
  // открыли другую строку — путь программы берётся заново, плашка встаёт на место без езды
  $('#cx-list').addEventListener('click', () => { cxAppPath = ''; cxSegShown = false; }, { capture: true });
  // подпись в списке настроек: сколько открыто сейчас
  async function cxCount() {
    let rows;
    try { rows = (await go.mpConns()) || []; } catch { return; }
    const n = rows.filter((r) => !r.end).length;
    setText($('#sr-net'), st?.on ? `Сейчас ${nConn(n)}` : 'Что открыто сейчас и через что');
  }

  // ---------- старт ----------
  icons(document);
  call(() => go.mpState().then(update), 'Состояние не загрузилось');
  setInterval(() => { if (st?.on && view === 'home') setText($('#st-sub'), since(st.since)); }, 1000);

  // предпросмотр в браузере без Go
  function mock() {
    const s = { on: true, busy: '', err: '', via: 'tun', since: Date.now() / 1000 - 724, mode: 'smart', showAI: false, hasSub: true, version: 'dev', today: 1288490188,
      active: 's1', conn: 'Нидерланды Hysteria2',
      subs: [{ id: 's1', title: 'north-star', used: 153612666, total: 0, expire: 0, updated: Date.now() / 1000 - 3600 }, { id: 's2', title: 'Рабочая', used: 40e9, total: 100e9, expire: Date.now() / 1000 + 30 * 86400 }],
      conns: [['Нидерланды', 'Vision', 38], ['Нидерланды', 'Hysteria2', 41], ['Нидерланды', 'XHTTP', 52], ['Германия', 'Vision', 47], ['Германия', 'XHTTP', 61], ['Финляндия', 'Vision', 55], ['Финляндия', 'Hysteria2', 59], ['Финляндия', 'XHTTP', 0]]
        .map(([srv, proto, delay]) => ({ name: `${srv} ${proto}`, srv, proto, known: true, alive: delay > 0, delay })) };
    const set = { mode: 'smart', connect: 'auto', port: 10808, autoStart: true, autoOn: true, dns: 'https://1.1.1.1/dns-query', fingerprint: '', blockAds: true, vpnFirst: true, updateHours: 12, always: [{ domain: 'habr.com', when: Date.now() / 1000 - 86400 }], direct: [], found: [{ domain: 'rutracker.org', reason: 'сброс соединения', when: Date.now() / 1000 - 600 }], apps: [{ domain: 'Telegram.exe', app: 'Telegram Desktop', path: 'C:/Programs/Telegram.exe', when: Date.now() / 1000 - 7200 }], appsDirect: [{ domain: 'steam.exe', app: 'Steam', path: 'C:/Programs/steam.exe', when: Date.now() / 1000 - 3600 }] };
    const ok = (v) => Promise.resolve(v);
    const upd = () => { update(s); return ok(); };
    // соединения для предпросмотра: разные программы, пути и причины; трафик растёт от опроса к опросу
    const t0 = Date.now();
    const cx = [['discord.com', 'discord', 'vpn', 'found'], ['cdn.discordapp.com', 'discord', 'vpn', 'blocked'], ['gateway.discord.gg', 'discord', 'vpn', 'blocked'],
      ['www.youtube.com', 'chrome', 'vpn', 'blocked'], ['rr3---sn-4g5e6nsz.googlevideo.com', 'chrome', 'vpn', 'blocked'], ['ya.ru', 'chrome', 'direct', 'ru'],
      ['mc.yandex.ru', 'chrome', 'block', 'ads'], ['github.com', 'code', 'direct', 'rest'], ['api.github.com', 'code', 'direct', 'rest'],
      ['chatgpt.com', 'chrome', 'vpn', 'ai'], ['claude.ai', 'claude', 'vpn', 'ai'], ['habr.com', 'firefox', 'vpn', 'my-vpn'], ['', 'telegram', 'vpn', 'nameless-blocked'],
      ['store.steampowered.com', 'steam', 'direct', 'rest'], ['doubleclick.net', 'chrome', 'block', 'ads'], ['mailflare.co', 'chrome', 'vpn', 'found']]
      .flatMap(([d, p, path, rule], i) => Array.from({ length: 1 + (i * 7) % 4 }, (_, j) => ({ id: i * 10 + j, start: t0 - (i * 97 + j * 31) * 1000, end: (i + j) % 5 === 0 ? t0 - j * 60000 : 0, net: 'tcp',
        domain: d, dest: d ? '' : '149.154.167.' + (50 + j) + ':443', in: 'tun-in', out: path === 'vpn' ? 'px-1' : path, path, via: path === 'vpn' ? 'Stockholm Hysteria2' : '', rule,
        up: 2000 * (i + 1) * (j + 1), down: 90000 * ((i * 13) % 17 + 1) * (j + 1), first: path === 'block' ? -1 : 40 + i * 9 + j * 5, proc: p, procDir: 'C:/Programs/' + p + '.exe' })));
    const mpConns = () => { const k = (Date.now() - t0) / 1000; return ok(cx.map((r) => r.end ? r : { ...r, down: Math.round(r.down * (1 + k / 20)), up: Math.round(r.up * (1 + k / 30)) })); };
    return { mpState: () => ok(s), mpToggle: () => { s.on = !s.on; update(s); }, mpMode: (m) => { s.mode = m; return ok(); },
      mpConn: (n) => { s.conn = n; return upd(); }, mpSubUse: (id) => { s.active = id; return upd(); },
      mpSubDel: (id) => { s.subs = s.subs.filter((x) => x.id !== id); s.active = s.subs[0]?.id || ''; s.hasSub = !!s.subs.length; return upd(); },
      mpConns, mpSub: () => ok(), mpSettings: () => ok(set),
      // программы для предпросмотра: значки — цветные квадраты с буквой
      mpProcs: () => new Promise((r) => setTimeout(() => r([['VALORANT', 'VALORANT-Win64-Shipping.exe', '#ff4655', true], ['Discord', 'Discord.exe', '#5865f2', true], ['Google Chrome', 'chrome.exe', '#4285f4', true],
        ['Riot Client', 'Riot Client.exe', '#eb0029', false], ['Telegram Desktop', 'Telegram.exe', '#29a9eb', false], ['Steam', 'steam.exe', '#1b2838', false], ['Очень длинное название программы, которое не помещается в строку', 'very-long-program-name-launcher-x64.exe', '#666', false]]
        .map(([name, exe, c, net]) => ({ name, exe, path: 'C:\\Programs\\' + exe, net, listed: (set.apps || []).some((a) => a.domain === exe),
          icon: 'data:image/svg+xml,' + encodeURIComponent(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24"><rect width="24" height="24" rx="5" fill="${c}"/><text x="12" y="16.5" font-family="Arial" font-size="12" font-weight="700" fill="#fff" text-anchor="middle">${name[0]}</text></svg>`) }))), 400)),
      mpPickExe: () => ok('C:\\Games\\Some Game\\game.exe'),
      mpAppMode: (p, m) => { const exe = p.split(/[\\/]/).pop(); for (const l of ['apps', 'appsDirect']) set[l] = (set[l] || []).filter((a) => a.domain.toLowerCase() !== exe.toLowerCase());
        if (m !== 'auto') set[m === 'vpn' ? 'apps' : 'appsDirect'].push({ domain: exe, app: exe.replace(/\.exe$/, ''), path: p, when: Date.now() / 1000 }); return new Promise((r) => setTimeout(r, 300)); },
      mpAppAdd: (p) => { const exe = p.split(/[\\/]/).pop(); set.apps = (set.apps || []).filter((a) => a.domain !== exe); set.apps.push({ domain: exe, app: exe === 'VALORANT-Win64-Shipping.exe' ? 'VALORANT' : exe.replace(/\.exe$/, ''), path: p, when: Date.now() / 1000 }); return ok(); },
      mpIcons: (ps) => ok(Object.fromEntries(ps.map((p) => [p, 'data:image/svg+xml;base64,' + btoa('<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24"><rect width="24" height="24" rx="5" fill="#ff4655"/></svg>')]))), mpSave: () => ok(), mpRule: (l, v, add) => { set[l] = (set[l] || []).filter((r) => r.domain !== v); if (add) set[l].push({ domain: v, when: Date.now() / 1000 }); return ok(); }, mpLog: () => ok(['00:00:00 пример строки журнала']),
      mpCheck: () => { s.checking = true; update(s); setTimeout(() => { s.checking = false; update(s); }, 1500); },
      mpRefresh: () => { s.refreshing = true; update(s); setTimeout(() => { s.refreshing = false; update(s); }, 1200); } };
  }
})();
