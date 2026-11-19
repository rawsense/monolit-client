/* Витрина: демо-сценарии окон (не часть библиотеки). Проверка формы, приглашение, мастер подключения домена, «?» — список клавиш.
   Использует из библиотеки: toast(), btnLoading(), fieldError(), resetDialog(), el.setStep(). */
(() => {
  const once = (el, k) => { if (el.dataset[k]) return false; el.dataset[k] = '1'; return true };
  const $ = (id) => document.getElementById(id);
  const fieldError = window.fieldError, resetLater = window.resetDialog;

  const RULES = {
    slug: (v) => !v ? 'Введите название' : /^[a-z0-9]+(-[a-z0-9]+)*$/.test(v) ? '' : 'Только латиница в нижнем регистре, цифры и дефис',
    domain: (v) => !v ? 'Введите адрес' : /^(?!https?:)([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,}$/i.test(v) ? '' : 'Похоже, это не адрес. Пример: app.brand.studio',
  };
  // проверка при отправке; после первой ошибки поле перепроверяется на каждый ввод
  const validate = (scope) => {
    let first = null;
    scope.querySelectorAll('[data-rule]').forEach((inp) => {
      const msg = RULES[inp.dataset.rule](inp.value.trim());
      fieldError(inp, msg); if (msg && !first) first = inp;
      if (!inp.dataset.live) { inp.dataset.live = '1'; inp.addEventListener('input', () => { if (inp.hasAttribute('aria-invalid')) fieldError(inp, RULES[inp.dataset.rule](inp.value.trim())) }) }
    });
    first?.focus(); return !first;
  };

  /* ===== Форма в окне: проверка → загрузка на кнопке → закрыть → уведомление ===== */
  function formDlg(d) {
    if (!once(d, 'fd')) return;
    const form = d.querySelector('form'), ok = d.querySelector('[data-submit]');
    form.addEventListener('submit', (e) => {
      e.preventDefault(); if (!validate(form) || ok.classList.contains('is-loading')) return;
      const name = form.elements.name?.value.trim();
      btnLoading(ok, true);
      setTimeout(() => { btnLoading(ok, false); d.close(); resetLater(d); toast({ type: 'success', title: `Проект ${name} создан` }) }, 900);
    });
  }

  /* ===== Приглашение: почты через запятую → строки входят по высоте; × — строка схлопывается ===== */
  const plural = (n, a, b, c) => { const m = n % 10, h = n % 100; return m === 1 && h !== 11 ? a : m >= 2 && m <= 4 && (h < 12 || h > 14) ? b : c };
  function invite(d) {
    if (!once(d, 'inv')) return;
    const form = d.querySelector('form'), inp = d.querySelector('input[type=email]'), role = d.querySelector('.invite-row select'), list = d.querySelector('.members'), cnt = d.querySelector('[data-count]');
    const CH = '<svg class="lucide" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="m6 9 6 6 6-6"/></svg>';
    const ROLES = ['Администратор', 'Разработчик', 'Только чтение'];
    const count = () => { const n = list.querySelectorAll('li:not(.leaving)').length; cnt.textContent = `${n} ${plural(n, 'человек', 'человека', 'человек')}` };

    // меню роли: одно на окно, лежит в самом окне (верхний слой), открывается под кнопкой от её правого края
    const menu = document.createElement('div'); menu.className = 'menu'; menu.setAttribute('role', 'menu'); d.append(menu);
    let cur = null;
    const close = (focus) => { if (!cur) return; menu.classList.remove('open'); cur.setAttribute('aria-expanded', 'false'); if (focus) cur.focus({ preventScroll: true }); cur = null };
    const open = (btn) => {
      if (cur === btn) return close(true);
      close(); cur = btn;
      const li = btn.closest('li'), pending = li.hasAttribute('data-pending'), now = btn.querySelector('span').textContent;
      menu.innerHTML = ROLES.map((r) => `<button class="menu-item" type="button" role="menuitemradio" aria-checked="${r === now}" data-role="${r}"><span>${r}</span></button>`).join('')
        + `<div class="menu-sep"></div><button class="menu-item danger" type="button" role="menuitem" data-kick><span>${pending ? 'Отозвать приглашение' : 'Убрать из проекта'}</span></button>`;
      // от правого края кнопки; не влезает на экран (на телефоне роль слева) — от левого; в любом случае ≥ 8px от края экрана
      const dr = d.getBoundingClientRect(), br = btn.getBoundingClientRect(), w = menu.offsetWidth || 208, M = 8;
      const fromRight = br.right - w >= M;
      const x = Math.min(Math.max(fromRight ? br.right - w : br.left, M), innerWidth - M - w);
      menu.style.right = 'auto'; menu.style.left = Math.round(x - dr.left) + 'px'; menu.style.top = Math.round(br.bottom - dr.top + 4) + 'px';
      menu.style.setProperty('--origin', fromRight ? 'top right' : 'top left');
      btn.setAttribute('aria-expanded', 'true'); menu.classList.add('open');
      (menu.querySelector('[aria-checked=true]') || menu.firstElementChild).focus({ preventScroll: true });
    };
    const remove = (li) => {
      // фокус не теряется: на роль соседней строки, иначе в поле почты
      const next = [li.nextElementSibling, li.previousElementSibling].map((x) => x?.querySelector('.role-btn')).find(Boolean);
      (next || inp).focus({ preventScroll: true });
      li.classList.add('leaving'); li.classList.remove('on'); count();
      li.addEventListener('transitionend', (e) => { if (e.target === li && e.propertyName === 'grid-template-rows') li.remove() });
      setTimeout(() => li.remove(), 600);   // запасной путь, если перехода не было (меньше движения)
    };
    list.addEventListener('click', (e) => { const b = e.target.closest('.role-btn'); if (b) open(b) });
    menu.addEventListener('click', (e) => {
      const it = e.target.closest('.menu-item'); if (!it || !cur) return;
      const btn = cur, li = btn.closest('li');
      if (it.hasAttribute('data-kick')) { close(); remove(li); return }
      btn.querySelector('span').textContent = it.dataset.role; close(true);
    });
    menu.addEventListener('keydown', (e) => {
      const items = [...menu.querySelectorAll('.menu-item')], k = items.indexOf(document.activeElement);
      if (e.key === 'ArrowDown' || e.key === 'ArrowUp') { e.preventDefault(); items[(k + (e.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length].focus() }
      else if (e.key === 'Home' || e.key === 'End') { e.preventDefault(); items[e.key === 'Home' ? 0 : items.length - 1].focus() }
      else if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); close(true) }   // Esc закрывает меню, а не окно
      else if (e.key === 'Tab') close();
    });
    d.addEventListener('pointerdown', (e) => { if (cur && !menu.contains(e.target) && !cur.contains(e.target)) close() });
    d.addEventListener('close', () => close());

    inp.addEventListener('input', () => { if (inp.hasAttribute('aria-invalid')) fieldError(inp, '') });
    const add = () => {
      const all = inp.value.split(/[\s,;]+/).filter(Boolean), bad = all.filter((m) => !/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(m));
      if (!all.length) return fieldError(inp, 'Введите почту'), inp.focus();
      if (bad.length) return fieldError(inp, `Проверьте: ${bad.join(', ')}`), inp.focus();
      fieldError(inp, '');
      all.forEach((m, k) => {
        const li = document.createElement('li'); li.className = 'grow-in'; li.setAttribute('data-pending', '');
        li.innerHTML = `<div><div class="mem"><span class="avatar" data-hue="${(m.length % 6) + 1}">${m.slice(0, 2).toUpperCase()}</span><span class="mem-tx"><b></b><span>Ждёт ответа</span></span><button class="role-btn" type="button" aria-haspopup="menu" aria-expanded="false"><span></span>${CH}</button></div></div>`;
        li.querySelector('b').textContent = m; li.querySelector('.role-btn span').textContent = role.value;
        list.append(li); setTimeout(() => li.classList.add('on'), 20 + k * 50);
      });
      count(); inp.value = '';
      toast({ type: 'success', title: all.length > 1 ? `Отправлено ${all.length} ${plural(all.length, 'приглашение', 'приглашения', 'приглашений')}` : `Приглашение отправлено на ${all[0]}` });
    };
    form.addEventListener('submit', (e) => { e.preventDefault(); add() });
    count();
  }


  /* ===== По шагам: высота окна плавно подстраивается, шаг въезжает с той стороны, куда идём ===== */
  function wizard(d) {
    if (!once(d, 'wz')) return;
    const form = d.querySelector('form'), ol = d.querySelector('.steps'), panes = [...d.querySelectorAll('.wz-pane')];
    const back = d.querySelector('[data-back]'), next = d.querySelector('[data-next]'), chk = d.querySelector('.wz-check');
    const LABELS = ['Далее', 'Проверить', 'Готово'], reduce = matchMedia('(prefers-reduced-motion: reduce)');
    let i = 0, timer = 0;
    next.style.minWidth = '112px';   // ширина кнопки не прыгает при смене надписи
    const label = (anim) => {
      back.style.visibility = i === 0 ? 'hidden' : '';
      if (next.textContent !== LABELS[i]) { next.textContent = LABELS[i]; if (anim && !reduce.matches) next.animate({ opacity: [0, 1] }, { duration: 160, easing: 'ease-out' }) }
    };
    const check = () => {
      chk.classList.remove('ok'); next.disabled = true; clearTimeout(timer);
      const dom = form.elements.domain.value.trim();
      d.querySelector('[data-domain]').textContent = dom;
      timer = setTimeout(() => { chk.classList.add('ok'); next.disabled = false; next.focus() }, 1600);
    };
    const go = (n, anim = true) => {
      if (n === i && anim) return;
      const dir = n > i ? 1 : -1, from = d.getBoundingClientRect().height;
      panes[i].hidden = true; panes[n].hidden = false; i = n;
      ol.setStep(i, anim); label(anim);
      if (i === 1) { const dom = form.elements.domain.value.trim(); d.querySelector('[data-name]').textContent = dom.split('.').length > 2 ? dom.split('.')[0] : '@' }
      if (i === 2) check(); else { clearTimeout(timer); next.disabled = false }
      if (!anim || reduce.matches) return;
      const to = d.getBoundingClientRect().height;
      d.classList.add('wz-anim');
      d.animate({ height: [from + 'px', to + 'px'] }, { duration: 280, easing: 'cubic-bezier(.23, 1, .32, 1)' }).onfinish = () => d.classList.remove('wz-anim');
      panes[i].animate({ opacity: [0, 1], transform: [`translateX(${dir * 12}px)`, 'none'] }, { duration: 240, easing: 'cubic-bezier(.23, 1, .32, 1)' });
      panes[i].querySelector('input')?.focus();
    };
    form.addEventListener('submit', (e) => {
      e.preventDefault();
      if (i === 0 && !validate(panes[0])) return;
      if (i < panes.length - 1) return go(i + 1);
      if (chk.classList.contains('ok')) { d.close(); toast({ type: 'success', title: `Домен ${form.elements.domain.value.trim()} подключён` }) }
    });
    back.addEventListener('click', () => { if (i > 0) go(i - 1) });
    d.addEventListener('close', () => setTimeout(() => { if (!d.open) { go(0, false); resetLater(d) } }, 260));
    go(0, false);
  }

  /* ===== «?» в любом месте (не при наборе текста) — список горячих клавиш ===== */
  addEventListener('keydown', (e) => {
    if (e.key !== '?' || e.ctrlKey || e.metaKey || e.altKey) return;
    const t = e.target; if (t instanceof Element && t.closest('input, textarea, select, [contenteditable]')) return;
    if (document.querySelector('dialog:modal')) return;
    const k = $('dlg-keys'); if (k) { e.preventDefault(); k.showModal() }
  });

  const init = () => {
    document.querySelectorAll('dialog[data-guard]').forEach(formDlg);
    const inv = $('dlg-invite'); if (inv) invite(inv);
    document.querySelectorAll('dialog.dialog-wizard').forEach(wizard);
  };
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init); else init();
})();
