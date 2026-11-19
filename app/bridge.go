package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"
)

// ---------- состояние для интерфейса ----------

type ConnView struct {
	Tag   string `json:"tag"`
	Name  string `json:"name"`
	Srv   string `json:"srv"` // название сервера без протокола
	Proto string `json:"proto"`
	Host  string `json:"host"`
	Known bool   `json:"known"` // была хоть одна проверка
	Alive bool   `json:"alive"`
	Delay int64  `json:"delay"`
}

// SubView — подписка в списке: название и сколько осталось
type SubView struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Used    int64  `json:"used"`
	Total   int64  `json:"total"`
	Expire  int64  `json:"expire"`
	Updated int64  `json:"updated"`
}

type View struct {
	Subs       []SubView  `json:"subs"`
	Active     string     `json:"active"`
	Conn       string     `json:"conn"` // выбранный конфиг
	Checking   bool       `json:"checking"`
	Refreshing bool       `json:"refreshing"`
	RefreshErr string     `json:"refreshErr"`
	RefreshMsg string     `json:"refreshNote"` // обновлять было нечего: показать как сообщение, а не ошибку
	On         bool       `json:"on"`
	Busy       string     `json:"busy"`
	Err        string     `json:"err"`
	Via        string     `json:"via"`
	Since      int64      `json:"since"`
	Mode       string     `json:"mode"`
	ShowAI     bool       `json:"showAI"`
	Current    string     `json:"current"`
	Title      string     `json:"title"`
	Conns      []ConnView `json:"conns"`
	SpeedDown  int64      `json:"speedDown"`
	SpeedUp    int64      `json:"speedUp"`
	Today      int64      `json:"today"`
	HasSub     bool       `json:"hasSub"`
	Sub        Sub        `json:"sub"`
	Counts     [3]int     `json:"counts"` // всегда VPN, всегда напрямую, найдено
	Version    string     `json:"version"`
	Admin      bool       `json:"admin"`
	Update     string     `json:"update"`
	Geo        int64      `json:"geo"`
}

func (a *App) view() View {
	a.mu.Lock()
	defer a.mu.Unlock()
	v := View{On: a.on, Busy: a.busy, Err: a.err, Via: a.via, Mode: a.set.Mode, ShowAI: a.set.ShowAI, Active: a.set.Active,
		Title: a.sub.Title, SpeedDown: a.speedDown, SpeedUp: a.speedUp, Today: a.todayDown + a.todayUp,
		HasSub: len(a.set.Subs) > 0, Version: version, Admin: isAdmin(),
		Counts: [3]int{len(a.set.Always), len(a.set.Direct), len(a.set.Found)}}
	v.Sub = a.sub
	v.Checking, v.Refreshing, v.RefreshErr, v.RefreshMsg = a.checking, a.refreshing, a.refreshErr, a.refreshNote
	if a.upd != nil {
		v.Update = a.upd.Tag
	}
	if st, err := os.Stat(filepath.Join(geoDir(), "geosite.dat")); err == nil {
		v.Geo = st.ModTime().Unix()
	}
	v.Sub.Conns = nil
	if a.on {
		v.Since = a.since.Unix()
	}
	for _, c := range a.sub.Conns {
		h := a.health[c.Name]
		cv := ConnView{Tag: c.Tag, Name: c.Name, Srv: c.ServerName(), Proto: c.Proto, Host: c.Server}
		// «нет ответа» — только после двух неудач подряд; до первого удачного замера одна неудача ещё ничего не значит
		if h != nil && h.known && (h.delay > 0 || h.fails >= 2) {
			cv.Known, cv.Alive, cv.Delay = true, h.alive(), h.delay
		}
		v.Conns = append(v.Conns, cv)
	}
	v.Current = a.lastTag
	for _, r := range a.set.Subs {
		x := a.subs[r.ID]
		t := x.Title
		if t == "" {
			t = "Подписка " + strings.TrimPrefix(r.ID, "s")
		}
		v.Subs = append(v.Subs, SubView{ID: r.ID, Title: t, Used: x.Used, Total: x.Total, Expire: x.Expire, Updated: x.Updated})
	}
	if p := pickConns(a.set, a.sub); len(p) > 0 {
		v.Conn = p[0].Name
	}
	return v
}

func (a *App) notify() {
	if a.push == nil {
		return
	}
	b, _ := json.Marshal(a.view())
	a.push(string(b))
}

// ---------- вызовы из интерфейса ----------

func (a *App) bindings() map[string]any {
	return map[string]any{
		"mpState":  func() View { return a.view() },
		"mpToggle": func() { safeGo("toggle", a.Toggle) },
		"mpMode": func(m string) error {
			if !slices.Contains([]string{"smart", "ai", "all"}, m) {
				return errors.New("нет такого режима")
			}
			a.mu.Lock()
			if m == "ai" && !a.set.ShowAI {
				a.mu.Unlock()
				return errors.New("режим «Только ИИ» выключен в настройках")
			}
			a.set.Mode = m
			a.mu.Unlock()
			safeGo("apply", func() { a.apply(false) })
			return nil
		},
		"mpConn": func(name string) {
			a.mu.Lock()
			a.set.Conn = name
			a.mu.Unlock()
			safeGo("apply", func() { a.apply(false) }) // только маршрут: ядро не перезапускается, замеры и соединения остаются
		},
		// mpSub добавляет подписку и сразу выбирает её. Идёт в фоне (asyncBindings): скачивание не держит окно
		"mpSub": func(url string) error {
			url = strings.TrimSpace(url)
			if url == "" {
				return errors.New("вставьте ссылку")
			}
			a.mu.Lock()
			for _, r := range a.set.Subs {
				if r.URL == url {
					a.mu.Unlock()
					return errors.New("эта подписка уже добавлена")
				}
			}
			a.mu.Unlock()
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			ns, err := a.fetchSubAny(ctx, url)
			if err != nil {
				return err
			}
			a.mu.Lock()
			for _, r := range a.set.Subs { // пока скачивали, могли добавить ту же ссылку
				if r.URL == url {
					a.mu.Unlock()
					return errors.New("эта подписка уже добавлена")
				}
			}
			id := newSubID(a.set.Subs)
			a.set.Subs = append(slices.Clip(a.set.Subs), SubRef{ID: id, URL: url})
			a.set.Active, a.set.Conn, a.lastTag = id, "", ""
			a.mu.Unlock()
			a.setSub(id, ns)
			safeGo("apply", func() { a.apply(true) })
			return nil
		},
		"mpSubUse": func(id string) error {
			a.mu.Lock()
			if !a.hasSub(id) {
				a.mu.Unlock()
				return errors.New("нет такой подписки")
			}
			// без серверов ядру не на что переключиться: VPN остался бы на старой подписке, а выбрана новая
			if a.on && len(a.subs[id].Conns) == 0 {
				a.mu.Unlock()
				return errors.New("в этой подписке пока нет серверов: обновите её или отключите VPN")
			}
			a.set.Active, a.set.Conn = id, ""
			a.sub = a.subs[id]
			a.lastTag = ""
			// выбранную подписку сразу скачиваем заново: список в кэше мог устареть, пока была выбрана другая
			refresh := !a.refreshing
			if refresh {
				a.refreshing, a.refreshErr, a.refreshNote = true, "", ""
			}
			a.mu.Unlock()
			safeGo("apply", func() {
				a.apply(true)
				if refresh {
					a.refreshSub()
				}
			})
			return nil
		},
		"mpSubDel": func(id string) {
			a.mu.Lock()
			// новый срез, а не правка на месте: снимки настроек читают старый без a.mu
			a.set.Subs = slices.DeleteFunc(slices.Clone(a.set.Subs), func(r SubRef) bool { return r.ID == id })
			delete(a.subs, id)
			// под замком файла: запись кэша, начатая до удаления, не вернёт файл. Поздние записи setSub
			// отбрасывает сам (подписки уже нет в списке)
			m := subLock(id)
			m.Lock()
			os.Remove(subFile(id))
			m.Unlock()
			empty := len(a.set.Subs) == 0
			active := a.set.Active == id
			noServers := false
			if active {
				a.set.Active, a.set.Conn, a.sub, a.lastTag = "", "", Sub{}, ""
				if !empty {
					next := a.set.Subs[0].ID
					if a.on { // VPN включён: переходим на подписку, где есть серверы, как в mpSubUse
						for _, r := range a.set.Subs {
							if len(a.subs[r.ID].Conns) > 0 {
								next = r.ID
								break
							}
						}
						noServers = len(a.subs[next].Conns) == 0
					}
					a.set.Active = next
					a.sub = a.subs[next]
				}
			}
			on, snap := a.on, a.set.clone()
			a.mu.Unlock()
			switch {
			case on && (empty || noServers):
				storeSettings(snap)
				safeGo("disconnect", func() {
					a.Disconnect()
					if noServers {
						a.mu.Lock()
						a.err = "Нет подписки с серверами"
						a.mu.Unlock()
					}
					a.notify()
				})
			case !active || !on || empty:
				// удалили невыбранную или VPN выключен: ядро трогать незачем
				storeSettings(snap)
			default:
				safeGo("apply", func() { a.apply(true) })
				return
			}
			a.notify()
		},
		"mpSettings": func() Settings {
			a.mu.Lock()
			defer a.mu.Unlock()
			return a.set.clone() // ответ кодируется уже без a.mu
		},
		// mpSave принимает изменённые поля настроек. Списки сайтов меняются через mpRule
		"mpSave": func(patch map[string]any) error {
			a.mu.Lock()
			cur, _ := json.Marshal(a.set)
			var m map[string]any
			json.Unmarshal(cur, &m)
			restart := false
			for k, v := range patch {
				switch k {
				case "always", "direct", "found", "apps", "appsDirect", "subUrl", "subs", "active", "conn":
					continue
				case "port", "connect", "dns", "fingerprint":
					restart = true
				}
				m[k] = v
			}
			raw, _ := json.Marshal(m)
			var ns Settings
			err := json.Unmarshal(raw, &ns)
			if _, ok := patch["port"]; ok && err == nil && (ns.Port < 1024 || ns.Port > 65528) {
				err = errors.New("порт — число от 1024 до 65528") // +7 — порт проверки сайтов
			}
			if err == nil && !ns.ShowAI && ns.Mode == "ai" {
				ns.Mode = "smart" // карточку спрятали: режим, которого не видно на главном, не оставляем
			}
			if err == nil {
				a.set = ns
				a.port.Store(int64(ns.Port))
			}
			a.mu.Unlock()
			if err != nil {
				return err
			}
			if _, ok := patch["autoStart"]; ok {
				safeGo("autostart", a.syncAutoStart) // schtasks — секунды, окно не ждёт
			}
			safeGo("apply", func() { a.apply(restart) })
			if on, _ := patch["subRoutes"].(bool); on {
				// подписка скачана до этой настройки или её кэш от прошлой версии: маршрутов в нём нет
				a.mu.Lock()
				need := len(routesFlat(a.sub)) == 0 && !a.refreshing && !isLink(a.activeURL())
				if need {
					a.refreshing, a.refreshErr, a.refreshNote = true, "", ""
				}
				a.mu.Unlock()
				if need {
					safeGo("refresh", a.refreshSub)
				}
			}
			return nil
		},
		// mpRule: list — always | direct | found | apps; add=false удаляет
		"mpRule": func(list, value string, add bool) error {
			value = strings.TrimSpace(value)
			apps := strings.HasPrefix(list, "apps")
			if !apps { // у программы имя файла как есть: ядро сравнивает его с именем процесса
				value = strings.ToLower(value)
				value = strings.TrimPrefix(strings.TrimPrefix(value, "https://"), "http://")
				if i := strings.IndexAny(value, "/:"); i > 0 {
					value = value[:i]
				}
			}
			if value == "" {
				return errors.New("пусто")
			}
			a.mu.Lock()
			l := a.listPtr(list)
			if l == nil {
				a.mu.Unlock()
				return errors.New("нет такого списка")
			}
			// новые срезы, а не правка на месте: снимки настроек читают старые без a.mu
			*l = slices.DeleteFunc(slices.Clone(*l), func(r Rule) bool { return r.Domain == value || apps && strings.EqualFold(r.Domain, value) })
			if add {
				// сайт может быть только в одном списке, программа — тоже
				others := []string{"always", "direct", "found"}
				if apps {
					others = []string{"apps", "appsDirect"}
				}
				for _, other := range others {
					if o := a.listPtr(other); o != nil && other != list {
						*o = slices.DeleteFunc(slices.Clone(*o), func(r Rule) bool { return strings.EqualFold(r.Domain, value) })
					}
				}
				*l = append(*l, Rule{Domain: value, When: now()})
			}
			a.mu.Unlock()
			if !add { // убрали из списка: сайт снова непроверенный, при следующем заходе проверится заново
				smart.forget(value)
			}
			safeGo("apply", func() { a.apply(false) })
			return nil
		},
		// mpUpdate и mpGeo скачивают файлы: идут в фоне (asyncBindings)
		"mpUpdate": func() error {
			a.mu.Lock()
			r := a.upd
			a.mu.Unlock()
			if r == nil {
				return errors.New("обновлений нет")
			}
			ctx, cancel := context.WithTimeout(context.Background(), downloadTimeout)
			defer cancel()
			if err := applyUpdate(ctx, a.dlClient(), r); err != nil {
				return err
			}
			safeGo("restart", restartSelf)
			return nil
		},
		// mpGeo: true — скачаны новые базы, false — в релизе те же, перезапуск ядра не нужен
		"mpGeo": func() (bool, error) {
			ctx, cancel := context.WithTimeout(context.Background(), downloadTimeout)
			defer cancel()
			changed, err := updateGeo(ctx, a.dlClient())
			if err != nil {
				return false, err
			}
			if changed {
				safeGo("apply", func() { a.apply(true) })
			}
			return changed, nil
		},
		// программы для списка «через VPN» (procs_windows.go): запущенные со значками, окно выбора файла, добавление по пути
		"mpProcs":   func() ([]ProcRow, error) { return a.procList(), nil }, // фоновые вызовы отвечают парой «значение, ошибка»
		"mpPickExe": func() (string, error) { return pickExe() },
		"mpAppAdd":  func(path string) error { return a.addApp(path) },
		// mpAppMode: путь программы из шторки соединений — auto | vpn | direct
		"mpAppMode": func(path, mode string) error { return a.setApp(path, mode) },
		// mpIcons: значки программ из списка по путям; нет файла или значка — пусто
		"mpIcons": func(paths []string) (map[string]string, error) {
			m := make(map[string]string, len(paths))
			for _, p := range paths {
				if p != "" {
					m[p] = exeIcon(p)
				}
			}
			return m, nil
		},
		// mpConns — для экрана «Соединения»: открытые и закрытые за 30 минут (conns.go)
		"mpConns": func() []ConnRow { return a.conns() },
		"mpLog": func() []string {
			logMu.Lock()
			defer logMu.Unlock()
			return slices.Clone(logRing)
		},
		// mpCheck: обновить подписку и проверить каждый конфиг. Возвращается сразу, итог придёт в состоянии
		"mpCheck": func() {
			a.mu.Lock()
			if a.checking {
				a.mu.Unlock()
				return
			}
			a.checking = true
			a.mu.Unlock()
			a.notify()
			safeGo("recheck", a.recheck)
		},
		// mpRefresh: обновить выбранную подписку
		"mpRefresh": func() {
			a.mu.Lock()
			if a.refreshing {
				a.mu.Unlock()
				return
			}
			a.refreshing, a.refreshErr, a.refreshNote = true, "", ""
			a.mu.Unlock()
			a.notify()
			safeGo("refreshSub", a.refreshSub)
		},
	}
}

func (a *App) listPtr(name string) *[]Rule {
	switch name {
	case "always":
		return &a.set.Always
	case "direct":
		return &a.set.Direct
	case "found":
		return &a.set.Found
	case "apps":
		return &a.set.Apps
	case "appsDirect":
		return &a.set.AppsDirect
	}
	return nil
}

// asyncBindings — привязки с долгой работой (сеть). go-webview2 вызывает привязки в потоке окна,
// поэтому они идут в фоне, а ответ приходит в тот же Promise (bindAsync в main.go)
// Окно выбора файла и перебор программ со значками тоже в фоне: окно не должно замирать
var asyncBindings = map[string]bool{"mpSub": true, "mpUpdate": true, "mpGeo": true, "mpProcs": true, "mpPickExe": true, "mpIcons": true}

var autoMu sync.Mutex

// syncAutoStart ставит задачу автозапуска по текущей настройке. По очереди: быстрые щелчки не перепутают порядок
func (a *App) syncAutoStart() {
	autoMu.Lock()
	defer autoMu.Unlock()
	a.mu.Lock()
	on := a.set.AutoStart
	a.mu.Unlock()
	setAutoStart(on)
}

var errType = reflect.TypeOf((*error)(nil)).Elem()

// safeBinding оборачивает привязку: авария в ней — ошибка в Promise, а не падение программы.
// У обёртки последним всегда идёт error, так go-webview2 передаёт её в reject
func safeBinding(name string, fn any) any {
	v := reflect.ValueOf(fn)
	t := v.Type()
	in := make([]reflect.Type, t.NumIn())
	for i := range in {
		in[i] = t.In(i)
	}
	var out []reflect.Type
	for i := 0; i < t.NumOut(); i++ {
		out = append(out, t.Out(i))
	}
	withErr := len(out) > 0 && out[len(out)-1] == errType
	if !withErr {
		out = append(out, errType)
	}
	ft := reflect.FuncOf(in, out, t.IsVariadic())
	return reflect.MakeFunc(ft, func(args []reflect.Value) (res []reflect.Value) {
		defer func() {
			if r := recover(); r != nil {
				noteCrash(name, r)
				res = make([]reflect.Value, len(out))
				for i, o := range out {
					res[i] = reflect.Zero(o)
				}
				e := reflect.New(errType).Elem()
				e.Set(reflect.ValueOf(errors.New("внутренняя ошибка, подробности в журнале")))
				res[len(out)-1] = e
			}
		}()
		if t.IsVariadic() {
			res = v.CallSlice(args)
		} else {
			res = v.Call(args)
		}
		if !withErr {
			res = append(res, reflect.Zero(errType))
		}
		return res
	}).Interface()
}
