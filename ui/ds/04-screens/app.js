// Экран «Заявки»: демо-данные и поведение рабочего экрана (references/app.md)
// — строки обновляются на месте по ключу, а не innerHTML всей таблицы;
// — смена вида (вкладка, поиск, сайт, страница): список гаснет 200 мс и проявляется 360 мс;
// — форматтеры чисел и дат создаются один раз.
(() => {
  const $ = (id) => document.getElementById(id)
  const NF = new Intl.NumberFormat('ru-RU'), num = (n) => NF.format(n)
  const TF = new Intl.DateTimeFormat('ru-RU', { hour: '2-digit', minute: '2-digit' })
  const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]))

  const people = [
    ['Мария Соколова', 'Запись на консультацию', 'studio-lumen.ru', 0, 'new'], ['Игорь Белов', 'Расчет стоимости', 'studio-lumen.ru', 84000, 'new'],
    ['Елена Крылова', 'Прием терапевта', 'vetclinic-ural.ru', 1900, 'new'], ['ООО «Северный двор»', 'Корпоративный заказ', 'kofe-na-gorkah.ru', 23500, 'work'],
    ['Дмитрий Орлов', 'Вызов врача на дом', 'vetclinic-ural.ru', 3500, 'work'], ['Ольга Миронова', 'Бронь зала на субботу', 'kofe-na-gorkah.ru', 12000, 'new'],
    ['Павел Зуев', 'Вопрос по проекту', 'studio-lumen.ru', 0, 'done'], ['Анастасия Лебедева', 'Вакцинация', 'vetclinic-ural.ru', 2400, 'done'],
    ['Сергей Никитин', 'Доставка зерна', 'kofe-na-gorkah.ru', 4700, 'work'], ['Ксения Фомина', 'Расчет стоимости', 'studio-lumen.ru', 126000, 'new']
  ]
  const now = Date.now()
  let rows = people.map((p, i) => ({ id: 'Z-' + (1048 - i), name: p[0], form: p[1], site: p[2], sum: p[3], st: p[4], time: now - i * 23 * 60000, phone: '+7 912 ' + (300 + i * 7) + '-41-0' + i }))
  const ST = { new: ['Новая', 'badge-accent'], work: ['В работе', 'badge-warning'], done: ['Закрыта', ''] }
  let view = 'new', site = '', query = ''

  const badge = (r) => `<span class="badge ${ST[r.st][1]}">${ST[r.st][0]}</span>`
  const rowHtml = (r) => `<th scope="row"><span class="ap-who">${esc(r.name)}</span><span class="ap-what">${esc(r.form)}<span class="ap-site-in">, ${esc(r.site)}</span></span><span class="ap-row-st">${badge(r)}</span></th>`
    + `<td class="ap-st-col">${badge(r)}</td><td class="ap-site-col">${esc(r.site)}</td><td class="num">${r.sum ? num(r.sum) + ' ₽' : '<span class="ap-when">Без суммы</span>'}</td><td><span class="ap-when">${TF.format(r.time)}</span></td>`

  const tbody = $('ap-rows')
  function render() {
    const q = query.toLowerCase()
    const list = rows.filter((r) => (view === 'all' || r.st === view) && (!site || r.site === site) && (!q || (r.name + r.phone).toLowerCase().includes(q)))
    const byId = new Map([...tbody.children].map((tr) => [tr.dataset.id, tr]))
    let prev = null
    for (const r of list) {
      let tr = byId.get(r.id)
      if (tr) byId.delete(r.id)
      else { tr = document.createElement('tr'); tr.dataset.id = r.id; tr.tabIndex = 0 }
      const html = rowHtml(r)
      if (tr._html !== html) { tr.innerHTML = html; tr._html = html }
      const want = prev ? prev.nextSibling : tbody.firstChild
      if (tr !== want) tbody.insertBefore(tr, want)
      prev = tr
    }
    byId.forEach((tr) => tr.remove())
    tbody.closest('.table-wrap').hidden = !list.length
    $('ap-empty').hidden = !!list.length
  }

  const listEl = $('ap-list')
  let t
  const change = () => { listEl.classList.add('out'); clearTimeout(t); t = setTimeout(() => { render(); listEl.classList.remove('out') }, 200) }
  $('ap-tabs').addEventListener('pick', (e) => { view = e.detail.dataset.view; change() })
  $('ap-site').addEventListener('change', () => { site = $('ap-site').value; change() })
  let typing
  $('ap-search').addEventListener('input', () => { clearTimeout(typing); typing = setTimeout(() => { query = $('ap-search').value.trim(); change() }, 250) })
  $('ap-pager').addEventListener('page', () => change())

  // Карточка строки — шторка справа, список остается виден
  const open = (tr) => {
    const r = rows.find((x) => x.id === tr.dataset.id); if (!r) return
    $('ap-d-id').textContent = r.id
    $('ap-d-title').textContent = r.name
    $('ap-d-kv').innerHTML = [['Форма', r.form], ['Сайт', r.site], ['Телефон', r.phone], ['Сумма', r.sum ? num(r.sum) + ' ₽' : 'Без суммы'], ['Пришла', TF.format(r.time)]]
      .map(([k, v]) => `<div><dt>${k}</dt><dd>${esc(v)}</dd></div>`).join('')
    $('ap-d-raw').textContent = JSON.stringify({ id: r.id, name: r.name, phone: r.phone, form: r.form, page: 'https://' + r.site + '/kontakty' }, null, 2)
    $('ap-detail').showModal()
  }
  tbody.addEventListener('click', (e) => { const tr = e.target.closest('tr'); if (tr) open(tr) })
  tbody.addEventListener('keydown', (e) => { if (e.key === 'Enter' && e.target.matches('tr')) open(e.target) })
  $('ap-info-open').addEventListener('click', (e) => { e.preventDefault(); $('ap-info').showModal() })

  // События в шторке «Тариф и лимиты»
  $('ap-events').innerHTML = [['check', 'Форма на kofe-na-gorkah.ru приняла заявку', '14:32'], ['check', 'Списание за ноябрь прошло', '1 ноября'], ['circle-alert', 'Форма записи на vetclinic-ural.ru отвечала с ошибкой 6 минут', '29 октября']]
    .map(([ic, m, tm]) => `<li><span class="tl-mark ${ic === 'check' ? 'ok' : ''}"><i data-lucide="${ic}"></i></span><div class="tl-body"><p>${m}</p><time>${tm}</time></div></li>`).join('')

  // Живые данные: новая заявка встает сверху, остальные строки не перерисовываются
  let next = 1049
  setInterval(() => {
    if (document.hidden) return
    rows = [{ id: 'Z-' + next++, name: 'Новый клиент ' + (next - 1049), form: 'Запись на консультацию', site: 'studio-lumen.ru', sum: 0, st: 'new', time: Date.now(), phone: '+7 900 000-00-0' + (next % 10) }, ...rows]
    $('ap-new').textContent = num(rows.filter((r) => r.st === 'new').length)
    render()
  }, 15000)

  render()
  try { lucide.createIcons() } catch (e) {}
})()
