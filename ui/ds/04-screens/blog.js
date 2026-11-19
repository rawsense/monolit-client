/* Блог и статья · Coreless */
(() => {
  try { lucide.createIcons() } catch (e) {}
  const $ = (s, el = document) => el.querySelector(s), $$ = (s, el = document) => [...el.querySelectorAll(s)];
  const reduce = matchMedia('(prefers-reduced-motion: reduce)');
  const KEY = (k) => (document.documentElement.dataset.store ? document.documentElement.dataset.store + ':' : '') + k;

  // обложки проявляются, когда фото загрузилось, а не построчно
  $$('.cover img').forEach((img) => {
    const ok = () => img.classList.add('ok');
    if (img.complete && img.naturalWidth) ok(); else { img.addEventListener('load', ok, { once: true }); img.addEventListener('error', ok, { once: true }) }
  });

  /* ================= страница со статьями ================= */
  const posts = $('#posts');
  if (posts) {
    const tabs = $('.cats'), cats = $$('.tab', tabs), q = $('#q'), empty = $('#empty');
    const items = $$('.post-row, .post-card', posts);
    let cat = ['product', 'dev', 'guide'].includes(location.hash.slice(1)) ? location.hash.slice(1) : 'all';

    // вкладки — системные (inputs.js): линия, стрелки, фокус. Здесь только выбор рубрики из кода (адрес, «Сбросить»)
    const slide = (anim) => tabs.select(cats.find((x) => x.dataset.cat === cat), anim);
    // смена содержимого без спешки: старое гаснет, меняется невидимым, новое проявляется, высота едет плавно.
    // Новый запрос во время затухания не перезапускает его — применится последний.
    const ms = (v) => parseFloat(getComputedStyle(document.documentElement).getPropertyValue(v)) || 0;
    let swapT = 0, pending = null, grow = null;
    const swap = (change) => {
      if (reduce.matches) { change(); return }
      pending = change;
      if (swapT) return;
      posts.classList.add('fading');
      swapT = setTimeout(() => {
        grow?.cancel();
        const h0 = posts.offsetHeight;
        pending(); pending = null; swapT = 0;
        const h1 = posts.offsetHeight;
        posts.classList.remove('fading');
        if (h0 !== h1) grow = posts.animate([{ height: h0 + 'px' }, { height: h1 + 'px' }], { duration: ms('--dur-swap-in'), easing: 'cubic-bezier(.77, 0, .175, 1)' });
      }, ms('--dur-swap-out'));
    };
    const match = (el, words) => (cat === 'all' || el.dataset.cat === cat) && words.every((w) => el.dataset.q.includes(w));
    function apply(anim) {
      const words = q.value.trim().toLowerCase().split(/\s+/).filter(Boolean);
      slide(anim);
      const next = items.map((el) => match(el, words));
      if (next.every((ok, i) => ok === !items[i].hidden) && anim) return;   // результаты те же — ничего не трогаем
      const write = () => {
        items.forEach((el, i) => { el.hidden = !next[i] });
        empty.classList.toggle('on', !items.some((el, i) => next[i] && el.classList.contains('post-row')));
      };
      anim ? swap(write) : write();
    }
    tabs.addEventListener('pick', (e) => {
      cat = e.detail.dataset.cat;
      history.replaceState(null, '', cat === 'all' ? location.pathname : '#' + cat);
      apply(true);
    });
    let t;
    q.addEventListener('input', () => { clearTimeout(t); t = setTimeout(() => apply(true), 250) });   // ждём, пока перестанут печатать
    $('#reset').addEventListener('click', () => { q.value = ''; q.dispatchEvent(new Event('input')); cat = 'all'; history.replaceState(null, '', location.pathname); apply(true) });
    apply(false);

    // вид: списком или карточками; бегунок переключателя едет, список меняется через короткое затухание
    // переключатель — системная группа .tgl (data-mode=single): выбор отмечает inputs.js, здесь только смена вида
    const vb = $$('.view-tgl button');
    const setView = (v, anim) => {
      vb.forEach((b) => b.setAttribute('aria-pressed', b.dataset.view === v));
      try { localStorage.setItem(KEY('blog-view'), v) } catch (e) {}
      if (!anim) { posts.dataset.view = v; return }
      swap(() => { posts.dataset.view = v });
    };
    let saved = 'list';
    try { saved = localStorage.getItem(KEY('blog-view')) || 'list' } catch (e) {}
    vb.forEach((b) => b.addEventListener('click', () => { if (posts.dataset.view !== b.dataset.view) setView(b.dataset.view, true) }));
    setView(saved === 'grid' ? 'grid' : 'list', false);
  }

  /* ================= статья ================= */
  const prose = $('#prose');
  if (prose) {
    // полоса прочитанного: только transform, раз в кадр
    const bar = $('#progress');
    let raf = 0;
    const progress = () => {
      raf = 0;
      const r = prose.getBoundingClientRect();
      const total = r.height - innerHeight * .6;
      const p = Math.min(1, Math.max(0, (-r.top + 96) / Math.max(total, 1)));
      bar.style.setProperty('--p', p.toFixed(4));
    };
    addEventListener('scroll', () => { if (!raf) raf = requestAnimationFrame(progress) }, { passive: true });
    progress();

    // оглавление справа — системное .onpage (layers.js), копирование ссылки — системные data-copy / data-done (inputs.js)

    // мобильное содержание закрывается после перехода к разделу:
    // системный аккордеон закрываем его же кликом, чтобы высота ушла плавно
    $$('.mtoc a').forEach((a) => a.addEventListener('click', () => { const d = a.closest('details'); if (d.classList.contains('is-open')) d.querySelector('summary').click() }));

    // кнопка копирования в блоке кода берёт текст самого кода
    $$('.prose .code').forEach((c) => { const b = $('[data-copy]', c); if (b) b.dataset.copy = $('pre', c).innerText.trim() });
  }
})();
