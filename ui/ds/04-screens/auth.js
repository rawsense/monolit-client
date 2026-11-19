/* Вход и регистрация · Coreless. Режим в адресе: #signin, #signup, #reset — работают «назад» и прямые ссылки.
   Проверка полей — по уходу из поля и при отправке; после первой ошибки поле проверяется на каждый ввод. */
(() => {
  const root = document.documentElement;
  const $ = (s, el = document) => el.querySelector(s);
  const draw = () => window.lucide && lucide.createIcons();
  draw();

  const views = [...document.querySelectorAll('.view')];
  const titles = { signin: 'Вход в Coreless', signup: 'Регистрация в Coreless', reset: 'Сброс пароля в Coreless', sent: 'Проверьте почту' };
  const pw = $('#pw'), email = $('#email');
  let sentFrom = 'signup';

  function show(view, focus) {
    views.forEach((v) => {
      const on = v.dataset.view === view;
      v.classList.toggle('on', on);
      v.inert = !on;
    });
    if (focus) {
      // на сенсорном экране фокус в поле открывает клавиатуру и она закрывает форму:
      // там фокус идёт на заголовок (для чтения с экрана), а поле человек нажмёт сам
      const touch = matchMedia('(pointer: coarse)').matches;
      const h = $('.view.on h1');
      const t = touch ? h : $('.view.on .input') || h;
      setTimeout(() => {
        if (t) t.focus({ preventScroll: true });
        // ссылку внизу нажали, прокрутив страницу: возвращаем к заголовку нового режима
        if (h && h.getBoundingClientRect().top < 0) scrollTo({ top: 0, behavior: matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' });
      }, 60);
    }
  }

  function setMode(mode, focus) {
    if (mode === 'signin' || mode === 'signup') {
      root.dataset.mode = mode;
      // неактивные варианты текста скрыты и от чтения с экрана
      document.querySelectorAll('[data-m]').forEach((el) => el.setAttribute('aria-hidden', el.dataset.m !== mode));
      pw.autocomplete = mode === 'signup' ? 'new-password' : 'current-password';
      pw.placeholder = mode === 'signup' ? 'Минимум 8 символов' : '••••••••';
      // ошибки прошлого режима не переносим: требования к паролю разные
      clear(pw);
      show('auth', focus);
    } else if (mode === 'reset') {
      const r = $('#r-email');
      if (!r.value && email.value) r.value = email.value;
      show('reset', focus);
    } else if (mode === 'sent') {
      show('sent', focus);
    }
    document.title = titles[mode] || titles.signin;
  }

  const fromHash = () => {
    const h = location.hash.slice(1);
    return ['signin', 'signup', 'reset'].includes(h) ? h : 'signin';
  };
  // первая отрисовка без анимации
  root.classList.add('no-tr');
  setMode(fromHash(), false);
  requestAnimationFrame(() => requestAnimationFrame(() => root.classList.remove('no-tr')));
  addEventListener('hashchange', () => setMode(fromHash(), true));

  /* ---------- проверка полей ---------- */
  function setError(inp, text) {
    const msg = $('#' + inp.getAttribute('aria-describedby'));
    inp.setAttribute('aria-invalid', 'true');
    if (msg.className === 'error' && msg.dataset.t === text) return;
    msg.className = 'error'; msg.dataset.t = text;
    msg.innerHTML = '<i data-lucide="circle-alert"></i>' + text;
    draw();
  }
  function clear(inp) {
    const msg = $('#' + inp.getAttribute('aria-describedby'));
    inp.removeAttribute('aria-invalid');
    msg.className = 'msg'; msg.textContent = ''; delete msg.dataset.t;
  }
  const rules = {
    email: (v) => (!v ? 'Введите почту' : !/^[^\s@]+@[^\s@]+\.[^\s@]{2,}$/.test(v) ? 'Проверьте адрес почты' : ''),
    password: (v) => (!v ? 'Введите пароль' : root.dataset.mode === 'signup' && v.length < 8 ? 'Нужно не меньше 8 символов' : ''),
  };
  function check(inp) {
    const err = rules[inp.type === 'email' ? 'email' : 'password'](inp.value.trim());
    err ? setError(inp, err) : clear(inp);
    return !err;
  }
  document.querySelectorAll('.auth-form .input').forEach((inp) => {
    inp.addEventListener('blur', () => { if (inp.value) check(inp) });
    inp.addEventListener('input', () => { if (inp.getAttribute('aria-invalid') === 'true') check(inp) });
  });

  function busy(btn, on) {
    btn.classList.toggle('is-loading', on);
    btn.setAttribute('aria-busy', on);
    btn.closest('form, .view').querySelectorAll('.input, .btn').forEach((el) => { if (el !== btn) el.disabled = on });
  }

  /* ---------- вход / регистрация ---------- */
  $('#auth-form').addEventListener('submit', (e) => {
    e.preventDefault();
    const ok = [check(email), check(pw)];
    if (ok.includes(false)) { (ok[0] ? pw : email).focus(); return }
    const btn = e.submitter || $('#auth-form .submit');
    busy(btn, true);
    setTimeout(() => {
      busy(btn, false);
      if (root.dataset.mode === 'signup') {
        sentFrom = 'signup';
        $('#sent-to').textContent = email.value.trim();
        $('#sent-why').textContent = 'Перейдите по ссылке в нём, чтобы подтвердить адрес';
        startResendTimer();
        history.pushState(null, '', '#sent');
        setMode('sent', true);
      } else {
        location.href = 'landing.html';
      }
    }, 1200);
  });

  /* вход через сервисы: в макете только показываем загрузку на нажатой кнопке */
  document.querySelectorAll('[data-oauth]').forEach((b) => b.addEventListener('click', () => {
    busy(b, true);
    setTimeout(() => busy(b, false), 1200);
  }));

  /* ---------- сброс пароля ---------- */
  $('#reset-form').addEventListener('submit', (e) => {
    e.preventDefault();
    const inp = $('#r-email');
    if (!check(inp)) { inp.focus(); return }
    const btn = $('#reset-form .submit');
    busy(btn, true);
    setTimeout(() => {
      busy(btn, false);
      sentFrom = 'reset';
      $('#sent-to').textContent = inp.value.trim();
      $('#sent-why').textContent = 'Перейдите по ссылке в нём, чтобы задать новый пароль';
      startResendTimer();
      history.pushState(null, '', '#sent');
      setMode('sent', true);
    }, 1000);
  });

  /* ---------- повторная отправка: раз в минуту ---------- */
  const resend = $('#resend'), hint = $('#resend-hint');
  let timer;
  function startResendTimer() {
    let left = 60;
    resend.disabled = true;
    const tick = () => {
      if (left <= 0) { clearInterval(timer); resend.disabled = false; hint.textContent = ''; return }
      hint.textContent = 'Можно отправить снова через ' + Math.floor(left / 60) + ':' + String(left % 60).padStart(2, '0');
      left--;
    };
    clearInterval(timer); tick(); timer = setInterval(tick, 1000);
  }
  resend.addEventListener('click', () => {
    busy(resend, true);
    setTimeout(() => { busy(resend, false); startResendTimer(); hint.textContent = 'Письмо отправлено. ' + hint.textContent }, 900);
  });
})();
