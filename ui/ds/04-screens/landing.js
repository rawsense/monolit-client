/* Лендинг Coreless — поведение страницы. Компоненты (меню, аккордеон, копирование, тема) — из библиотеки js/. */
(() => {
  const $ = (s, r = document) => r.querySelector(s), $$ = (s, r = document) => [...r.querySelectorAll(s)];
  const reduce = matchMedia('(prefers-reduced-motion: reduce)');
  try { lucide.createIcons() } catch (e) {}

  /* ===== Демо: поле слева → место на «сайте» справа, изменённое коротко подсвечивается ===== */
  const out = (k) => $$(`[data-out="${k}"]`);
  const flash = (el) => { el.classList.add('flash'); requestAnimationFrame(() => requestAnimationFrame(() => el.classList.remove('flash'))) };
  const fallback = { title: 'Без заголовка', hours: '', price: '0', price2: '0' };
  $$('[data-bind]').forEach((inp) => {
    const k = inp.dataset.bind;
    inp.addEventListener(inp.type === 'checkbox' ? 'change' : 'input', () => {
      stopAuto();
      if (k === 'promo') { out(k).forEach((el) => el.classList.toggle('on', inp.checked)); return }
      let v = inp.value;
      if (k.startsWith('price')) { v = v.replace(/\D/g, '').slice(0, 5); if (inp.value !== v) inp.value = v }
      out(k).forEach((el) => { el.textContent = v.trim() || fallback[k]; flash(el) });
    });
  });

  /* Один раз, когда демо видно: само меняет цену 260 → 290, чтобы идея читалась без действий.
     Любое касание панели останавливает показ. При «меньше движения» — не запускается. */
  let autoT = [], autoDone = false;
  const stopAuto = () => { autoT.forEach(clearTimeout); autoT = []; autoDone = true };
  const autoPlay = () => {
    if (autoDone || reduce.matches) return; autoDone = true;
    const inp = $('[data-bind=price]'), target = '290'; let t = 400;
    const step = (fn, d) => autoT.push(setTimeout(fn, (t += d)));
    const set = (v) => { inp.value = v; out('price').forEach((el) => { el.textContent = v || '0'; flash(el) }) };
    step(() => inp.classList.add('auto'), 0);
    step(() => set('26'), 160); step(() => set('2'), 110); step(() => set('29'), 220); step(() => set(target), 160);
    step(() => { inp.classList.remove('auto'); const p = $('[data-bind=promo]'); p.checked = true; out('promo').forEach((el) => el.classList.add('on')) }, 900);
  };
  $('.demo-panel')?.addEventListener('pointerdown', stopAuto);
  $('.demo-panel')?.addEventListener('focusin', stopAuto);

  /* появление .rv — в site.js; здесь только шаги «Как это работает» */
  const io = new IntersectionObserver((es) => es.forEach((e) => { if (e.isIntersecting) { e.target.classList.add('in'); io.unobserve(e.target) } }), { rootMargin: '0px 0px -8% 0px' });
  $$('.how li').forEach((li) => io.observe(li));

  const demo = $('#demo');
  if (demo) new IntersectionObserver((es, o) => { if (es[0].isIntersecting) { setTimeout(autoPlay, 700); o.disconnect() } }, { threshold: .6 }).observe(demo);

  /* ===== Вкладки «Для кого»: линия едет к выбранной, панели меняются кроссфейдом ===== */
  const tabs = $('.aud .tabs');   // линию, выбор и стрелки ведёт inputs.js
  if (tabs) {
    const items = $$('.tab', tabs);
    const show = () => items.forEach((t) => $('#' + t.getAttribute('aria-controls')).classList.toggle('on', t.getAttribute('aria-selected') === 'true'));
    tabs.addEventListener('pick', show); show();
  }
})();
