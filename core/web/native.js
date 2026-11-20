// Окно без привязок Go (Android): те же вызовы window.mpXxx поверх одного моста MonolitNative.
// В Windows моста нет — файл ничего не делает
(function () {
  var n = window.MonolitNative;
  if (!n) return;
  document.documentElement.classList.add('os-android'); // прячет настройки, которые есть только в Windows
  var seq = 0, wait = {};
  window.__mpDone = function (id, ok, val) {
    var w = wait[id];
    if (!w) return;
    delete wait[id];
    if (ok) w.resolve(val); else w.reject(new Error(val));
  };
  JSON.parse(n.names()).forEach(function (name) {
    window[name] = function () {
      var id = ++seq, args = JSON.stringify(Array.prototype.slice.call(arguments));
      return new Promise(function (resolve, reject) {
        wait[id] = { resolve: resolve, reject: reject };
        n.call(id, name, args);
      });
    };
  });
})();

// Телефон: режимы живут в настройках («Маршруты»), а не на главном — тот же блок переезжает туда
addEventListener('DOMContentLoaded', function () {
  if (!matchMedia('(max-width: 640px)').matches) return;
  var modes = document.querySelector('#v-home .mc-modes'), set = document.querySelector('#v-s-routes .mc-set');
  if (modes && set) set.insertBefore(modes, set.firstChild);
});

// Телефон: список серверов, когда не помещается, обрезается посередине строки — нижнее затемнение
// ложится на текст, и видно, что дальше есть ещё
addEventListener('DOMContentLoaded', function () {
  if (!matchMedia('(max-width: 640px)').matches) return;
  var l = document.querySelector('#servers');
  if (!l) return;
  var snap = function () {
    l.style.maxHeight = '';
    var row = l.firstElementChild ? l.firstElementChild.offsetHeight : 56, h = l.clientHeight;
    if (l.scrollHeight > h + 1 && h % row < row * 0.35) l.style.maxHeight = Math.max(1, Math.floor((h - row / 2) / row)) * row + row / 2 + 'px';
  };
  addEventListener('resize', snap);
  new MutationObserver(snap).observe(l, { childList: true });
  snap();
});
