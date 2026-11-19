/* Цены Coreless — поведение страницы. Иконки и появление — в site.js, компоненты (меню, аккордеон, сегменты, копирование, тема) — из библиотеки js/. */
(() => {
  const $ = (s, r = document) => r.querySelector(s), $$ = (s, r = document) => [...r.querySelectorAll(s)];
  const reduce = matchMedia('(prefers-reduced-motion: reduce)');
  const ms = (v) => parseFloat(getComputedStyle(document.documentElement).getPropertyValue(v)) || 0;

  /* ===== Период оплаты: цены гаснут 200 мс, меняются невидимыми, проявляются 360 мс ===== */
  const seg = $('.pr-period .seg'), plans = $('.pr-plans');
  if (!seg || !plans) return;
  let want = plans.dataset.period, t = 0;
  const apply = () => {
    plans.dataset.period = want;
    $$('.pr-v', plans).forEach((v) => v.setAttribute('aria-hidden', v.dataset.v !== want));
  };
  seg.addEventListener('pick', (e) => {
    want = e.detail.dataset.period;
    if (t) return;                                   // во время затухания применится последний выбор
    if (want === plans.dataset.period) return;       // тот же период — ничего не мигает
    if (reduce.matches) return apply();
    plans.classList.add('pr-fading');
    t = setTimeout(() => { t = 0; apply(); plans.classList.remove('pr-fading') }, ms('--dur-swap-out'));
  });
})();
