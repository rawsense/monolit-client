package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xtls/xray-core/app/dispatcher"
	"github.com/xtls/xray-core/features/outbound"
)

const version = "0.1.0"

type App struct {
	mu                 sync.Mutex
	set                Settings
	sub                Sub            // выбранная подписка
	subs               map[string]Sub // все добавленные подписки по id
	core               Core
	on                 bool   // VPN включён
	busy               string // идёт подключение/отключение
	err                string
	since              time.Time
	via                string                 // tun | proxy — как подключены к системе сейчас
	lastTag            string                 // через какое подключение идём сейчас
	health             map[string]*connHealth // замеры по названию конфига, переживают перезапуск ядра
	checking           bool                   // идёт проверка конфигов по кнопке
	measuring          atomic.Bool            // идёт фоновый замер задержки
	refreshing         bool                   // идёт обновление подписки
	refreshErr         string                 // чем закончилось последнее обновление подписки
	refreshNote        string                 // обновление не понадобилось: почему (не ошибка)
	prevUp, prevDown   int64
	speedDown, speedUp int64
	todayDown, todayUp int64
	today              string
	test               bool // тестовый запуск: свой порт, систему не трогаем
	tunTest            bool // проверка TUN: тестовые файлы и порт, но с TUN
	upd                *Release
	updChecked         time.Time
	push               func(string)
	op                 sync.Mutex         // одна операция за раз: подключение, отключение, применение настроек
	stopGen            uint64             // растёт при каждом «Отключить»: подключение из очереди после него не идёт
	cancel             context.CancelFunc // отмена идущего подключения
	connMode           string             // способ подключения из настроек, с которым подключились
	connPort           int                // порт, на котором включён системный прокси
	port               atomic.Int64       // порт прокси для клиентов HTTP: читается без a.mu
}

var app *App

func newApp(test bool) *App {
	if test { // тест хранит всё отдельно, чтобы не испортить настоящие настройки
		d := filepath.Join(appDir(), "test")
		os.MkdirAll(d, 0o755)
		setPath, subsDir, backupPath = filepath.Join(d, "settings.json"), filepath.Join(d, "subs"), filepath.Join(d, "system-backup.json")
	}
	a := &App{set: loadSettings(), subs: map[string]Sub{}, test: test}
	// одна подписка из прошлой версии: ссылка и кэш переезжают в список подписок
	if a.set.SubURL != "" && len(a.set.Subs) == 0 {
		a.set.Subs = []SubRef{{ID: "s1", URL: a.set.SubURL}}
		a.set.Active = "s1"
		old := filepath.Join(filepath.Dir(setPath), "subscription.json")
		if b, err := os.ReadFile(old); err == nil {
			os.MkdirAll(subsDir, 0o755)
			if os.WriteFile(subFile("s1"), b, 0o600) == nil {
				os.Rename(old, old+".old")
			}
		}
		a.set.SubURL = ""
		storeSettings(a.set)
	}
	for _, r := range a.set.Subs {
		a.subs[r.ID] = loadSub(r.ID)
	}
	if a.activeURL() == "" && len(a.set.Subs) > 0 {
		a.set.Active = a.set.Subs[0].ID
	}
	a.sub = a.subs[a.set.Active]
	if test {
		a.set.Port = 10818
		a.set.Connect = "proxy"
	}
	a.port.Store(int64(a.set.Port))
	return a
}

// hasSub: подписка ещё в списке. Вызывать под a.mu
func (a *App) hasSub(id string) bool {
	return slices.ContainsFunc(a.set.Subs, func(r SubRef) bool { return r.ID == id })
}

// activeURL — ссылка выбранной подписки. Вызывать под a.mu или до запуска
func (a *App) activeURL() string {
	for _, r := range a.set.Subs {
		if r.ID == a.set.Active {
			return r.URL
		}
	}
	return ""
}

// setSub кладёт свежие данные подписки в кэш и, если она выбрана, делает текущей
// Подписку, удалённую, пока она скачивалась, не возвращаем
func (a *App) setSub(id string, s Sub) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.hasSub(id) {
		return
	}
	saveSub(id, s)
	a.subs[id] = s
	if id == a.set.Active {
		a.sub = s
	}
}

// fetchSubAny: при включённом VPN подписку скачиваем через него (адрес подписки провайдер мог закрыть),
// а не вышло — мимо VPN, через сетевую карту. Иначе подписку с нерабочим сервером нельзя было бы обновить:
// запрос уходил бы в тот самый сервер, который не отвечает
func (a *App) fetchSubAny(ctx context.Context, url string) (Sub, error) {
	a.mu.Lock()
	on := a.on
	a.mu.Unlock()
	if !on {
		return fetchSub(ctx, directClient(), url)
	}
	vctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	s, err := fetchSub(vctx, a.vpnClient(), url)
	cancel()
	if err == nil {
		return s, nil
	}
	t := newTransport(nil)
	t.DialContext = probeDialer(nil).DialContext // в TUN обычное соединение ушло бы в туннель
	defer t.CloseIdleConnections()
	d, derr := fetchSub(ctx, &http.Client{Timeout: 25 * time.Second, Transport: t}, url)
	if derr == nil {
		logLine("подписка: через VPN не скачалась (" + err.Error() + "), скачана напрямую")
		return d, nil
	}
	return s, derr
}

// refreshSub: скачать выбранную подписку заново. Ядро перезапускается, только если конфиги в ней поменялись.
// Идёт в фоне: привязки вызываются в потоке окна, долгая работа в них подвешивает интерфейс
func (a *App) refreshSub() {
	a.mu.Lock()
	url, id, on, old := a.activeURL(), a.set.Active, a.on, a.sub
	a.mu.Unlock()
	var errText, note string
	changed := false
	if url != "" && isLink(url) { // вставлена ссылка на один сервер: скачивать нечего
		note = "Это одна ссылка, обновлять нечего"
	} else if url != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		ns, err := a.fetchSubAny(ctx, url)
		cancel()
		if err != nil {
			errText = "Подписка не обновилась: " + err.Error()
		} else {
			a.setSub(id, ns)
			changed = !sameConns(old, ns)
			if on && changed {
				logLine("подписка обновлена: конфиги поменялись, ядро перезапущено")
				a.apply(true) // набор конфигов поменялся — ядру нужны новые подключения
			} else if on && !sameRoutes(old, ns) {
				a.apply(false) // поменялись только маршруты подписки: ядро не перезапускаем
			}
		}
	}
	a.mu.Lock()
	a.refreshing, a.refreshErr, a.refreshNote = false, errText, note
	a.mu.Unlock()
	a.notify()
	if changed && on {
		safeGo("measure", a.measure) // новые конфиги — сразу замер, не ждать полминуты
	}
}

// recheck: заново проверить каждый конфиг выбранной подписки, по две попытки
func (a *App) recheck() {
	a.mu.Lock()
	sub := a.sub
	a.mu.Unlock()
	res := a.core.Check(tagsOf(sub))
	a.mu.Lock()
	a.record(sub, res, true)
	a.checking = false
	a.mu.Unlock()
	a.notify()
}

func sameConns(a, b Sub) bool {
	if len(a.Conns) != len(b.Conns) {
		return false
	}
	for i := range a.Conns {
		if a.Conns[i].Tag != b.Conns[i].Tag || !sameJSON(a.Conns[i].Outbound, b.Conns[i].Outbound) {
			return false
		}
	}
	return true
}

// sameJSON сравнивает конфиги по смыслу: копия подписки на диске сохранена с отступами, а свежая — без,
// и побайтное сравнение считало их разными — ядро перезапускалось на каждом первом обновлении
func sameJSON(x, y []byte) bool {
	var bx, by bytes.Buffer
	if json.Compact(&bx, x) != nil || json.Compact(&by, y) != nil {
		return bytes.Equal(x, y)
	}
	return bytes.Equal(bx.Bytes(), by.Bytes())
}

// ---------- события ----------

// event: решения клиента и ошибки — в журнал
func (a *App) event(kind, title, detail string) {
	logLine(title + ": " + detail)
	a.notify()
}

// ---------- аварии ----------

// safeRun: авария в фоновой задаче — в журнал и crash.log, а не падение всей программы с VPN
func safeRun(name string, f func()) {
	defer recoverLog(name)
	f()
}

func safeGo(name string, f func()) { go safeRun(name, f) }

// recoverLog — только через defer
func recoverLog(name string) {
	if r := recover(); r != nil {
		noteCrash(name, r)
	}
}

func noteCrash(name string, r any) {
	msg := fmt.Sprintf("авария в %s: %v\n%s", name, r, debug.Stack())
	logLine(msg)
	crashNote(msg)
}

// ---------- клиенты HTTP ----------

// Клиенты общие: свой Transport на каждый запрос копил открытые соединения
var (
	direct     = &http.Client{Timeout: 30 * time.Second, Transport: newTransport(nil)}
	clientMu   sync.Mutex
	viaClients = map[string]*http.Client{} // по адресу прокси и виду клиента
)

const downloadTimeout = 10 * time.Minute // скачивание exe и баз сайтов целиком

func newTransport(proxy *url.URL) *http.Transport {
	t := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConnsPerHost:   2,
	}
	if proxy != nil {
		t.Proxy = http.ProxyURL(proxy)
	}
	return t
}

func cachedClient(key string, mk func() *http.Client) *http.Client {
	clientMu.Lock()
	defer clientMu.Unlock()
	cl := viaClients[key]
	if cl == nil {
		cl = mk()
		viaClients[key] = cl
	}
	return cl
}

func directClient() *http.Client { return direct }

// downloadClient — для больших файлов: без общего предела в 30 с, срок задаёт контекст (downloadTimeout).
// proxy nil — напрямую
func downloadClient(proxy *url.URL) *http.Client {
	key := "dl direct"
	if proxy != nil {
		key = "dl " + proxy.String()
	}
	return cachedClient(key, func() *http.Client { return &http.Client{Transport: newTransport(proxy)} })
}

// dlClient — клиент для скачивания: через VPN, если он включён
func (a *App) dlClient() *http.Client {
	a.mu.Lock()
	on := a.on
	a.mu.Unlock()
	if on {
		return downloadClient(a.probeProxy())
	}
	return downloadClient(nil)
}

func (a *App) proxyURL() *url.URL {
	return &url.URL{Scheme: "socks5", Host: fmt.Sprintf("127.0.0.1:%d", a.port.Load())}
}

func (a *App) probeProxy() *url.URL {
	return &url.URL{Scheme: "socks5", Host: fmt.Sprintf("127.0.0.1:%d", a.port.Load()+7)}
}

// vpnHandler — обработчик выбранного VPN-подключения
func (a *App) vpnHandler() outbound.Handler {
	a.mu.Lock()
	tag := a.lastTag
	a.mu.Unlock()
	if tag == "" {
		return nil
	}
	return a.core.handler(tag)
}

func (a *App) vpnClient() *http.Client {
	p := a.probeProxy()
	return cachedClient("vpn "+p.String(), func() *http.Client {
		return &http.Client{Timeout: 60 * time.Second, Transport: newTransport(p)}
	})
}

// ---------- подключение ----------

// Connect, Disconnect и apply идут по очереди под a.op: иначе отключение посреди подключения оставляло
// включённый системный прокси, а два применения настроек обгоняли друг друга
func (a *App) Connect() {
	a.mu.Lock()
	gen := a.stopGen
	a.mu.Unlock()
	a.op.Lock()
	defer a.op.Unlock()
	a.mu.Lock()
	stopped := a.stopGen != gen // пока ждали очереди, нажали «Отключить»
	a.mu.Unlock()
	if !stopped {
		a.connectLocked()
	}
}

// connectLocked — подключение под a.op
func (a *App) connectLocked() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.mu.Lock()
	if a.on {
		a.mu.Unlock()
		return
	}
	a.busy, a.err, a.cancel = "Подключение", "", cancel
	a.mu.Unlock()
	a.notify()
	err := a.connect(ctx)
	if ctx.Err() != nil {
		err = nil // отменили кнопкой: это не ошибка, отключит Disconnect
	}
	a.mu.Lock()
	a.busy, a.cancel = "", nil
	if err != nil {
		a.err = err.Error()
	}
	a.mu.Unlock()
	if err != nil {
		a.event("error", "Не удалось подключиться", err.Error())
	}
	a.notify()
}

func (a *App) connect(ctx context.Context) error {
	a.mu.Lock()
	s, sub, url, id := a.set.clone(), a.sub, a.activeURL(), a.set.Active
	a.mu.Unlock()
	if url == "" && len(sub.Conns) == 0 {
		return errors.New("добавьте подписку")
	}
	if len(sub.Conns) == 0 || time.Since(time.Unix(sub.Updated, 0)) > time.Duration(s.UpdateHours)*time.Hour {
		if ns, err := fetchSub(ctx, directClient(), url); err == nil {
			sub = ns
			a.setSub(id, ns)
		} else if len(sub.Conns) == 0 {
			return fmt.Errorf("подписка: %w", err)
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if !geoReady() { // базы сайтов: напрямую, а если GitHub не открылся — через временное ядро
		gctx, cancel := context.WithTimeout(ctx, downloadTimeout)
		_, err := updateGeo(gctx, downloadClient(nil))
		cancel()
		if err != nil && ctx.Err() == nil {
			if err := a.geoViaTemp(ctx, s, sub); err != nil {
				return fmt.Errorf("базы сайтов: %w", err)
			}
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	via := "proxy"
	if (!a.test || a.tunTest) && s.Connect != "proxy" {
		if err := a.startTun(ctx, s, sub); err == nil {
			via = "tun"
			if ctx.Err() != nil { // отменили сразу после подъёма TUN: снять, что подняли
				tunDown()
				a.core.Stop()
				return ctx.Err()
			}
		} else if ctx.Err() != nil {
			return ctx.Err()
		} else if s.Connect == "tun" {
			return fmt.Errorf("TUN: %w", err)
		} else {
			a.event("switch", "Режим прокси вместо TUN", err.Error())
		}
	}
	if via == "proxy" {
		cfg, err := buildConfig(s, sub, false, "")
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := a.core.Start(cfg, false); err != nil {
			return err
		}
		if ctx.Err() != nil { // отменили, пока запускалось ядро: прокси не включаем
			a.core.Stop()
			return ctx.Err()
		}
		if !a.test {
			if err := sysProxyOn(s.Port); err != nil {
				a.event("error", "Системный прокси не включился", err.Error())
			}
		}
	}
	safeGo("measure", func() { // первый замер задержки сразу, а не на ближайшем общем замере (до 30 с)
		time.Sleep(2 * time.Second)
		a.measure()
	})
	up, down := a.core.Traffic(tagsOf(sub)) // до a.mu: Traffic берёт замок ядра
	a.mu.Lock()
	a.on, a.via, a.since = true, via, time.Now()
	a.connMode, a.connPort = s.Connect, s.Port
	a.prevUp, a.prevDown = up, down
	a.mu.Unlock()
	safeGo("svcCheck", func() { // YouTube и Discord: напрямую или через VPN — сразу после подключения
		time.Sleep(3 * time.Second)
		a.svcCheckAll(0)
	})
	safeGo("recheckFound", func() { // после подключения сеть устаканивается (маршруты TUN), потом перепроверка найденного
		time.Sleep(20 * time.Second)
		a.recheckFound()
	})
	return nil
}

func (a *App) geoViaTemp(ctx context.Context, s Settings, sub Sub) error {
	s.Mode = "all"
	cfg, err := buildConfigNoGeo(s, sub)
	if err != nil {
		return err
	}
	if err := a.core.Start(cfg, false); err != nil {
		return err
	}
	defer a.core.Stop()
	time.Sleep(time.Second)
	gctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	p := &url.URL{Scheme: "socks5", Host: fmt.Sprintf("127.0.0.1:%d", s.Port+7)}
	_, err = updateGeo(gctx, downloadClient(p))
	return err
}

// Disconnect отменяет идущее подключение, дожидается его и снимает VPN. Два вызова подряд снимают один раз
func (a *App) Disconnect() {
	a.mu.Lock()
	a.stopGen++
	if a.cancel != nil {
		a.cancel()
	}
	a.mu.Unlock()
	a.op.Lock()
	defer a.op.Unlock()
	a.teardown()
}

// teardown — отключение под a.op: системный прокси или маршруты TUN, затем ядро
func (a *App) teardown() {
	a.mu.Lock()
	if !a.on {
		a.mu.Unlock()
		return
	}
	via := a.via
	a.busy = "Отключение"
	a.mu.Unlock()
	a.notify()
	if via == "tun" {
		tunDown()
	} else if !a.test {
		sysProxyOff()
	}
	a.core.Stop()
	a.mu.Lock()
	a.on, a.busy, a.via = false, "", ""
	a.mu.Unlock()
	a.notify()
}

// Toggle: во время подключения — отмена
func (a *App) Toggle() {
	a.mu.Lock()
	on := a.on || a.busy == "Подключение"
	a.mu.Unlock()
	if on {
		a.Disconnect()
	} else {
		a.Connect()
	}
}

// apply пересобирает маршруты после смены режима или списков. Соединения не рвутся
func (a *App) apply(restart bool) {
	a.op.Lock()
	defer a.op.Unlock()
	a.mu.Lock()
	s, sub, on, via, mode, port := a.set.clone(), a.sub, a.on, a.via, a.connMode, a.connPort
	gen := a.stopGen // нажали «Отключить» во время переподключения — заново не подключаемся
	a.mu.Unlock()
	storeSettings(s)
	if !on {
		a.notify()
		return
	}
	// сменился способ подключения (TUN ↔ прокси) или порт системного прокси: перезапуска ядра мало, нужно
	// снять старый способ и поднять новый — то есть переподключиться. В «авто» на прокси (TUN не поднялся)
	// TUN заново пробуем, только если пользователь сам сменил способ, а не на каждом обновлении
	if restart && !a.test {
		need := (s.Connect == "tun" && via != "tun") || (s.Connect == "proxy" && via != "proxy") ||
			(s.Connect == "auto" && mode != "auto" && via != "tun") || (via == "proxy" && s.Port != port)
		if need {
			a.teardown()
			a.mu.Lock()
			stopped := a.stopGen != gen
			a.mu.Unlock()
			if !stopped {
				a.connectLocked()
			}
			return
		}
	}
	var err error
	if restart && via == "tun" {
		// перезапуск ядра закрывает виртуальную карту вместе с адресом и маршрутами: поднять TUN целиком,
		// иначе трафик молча пойдёт мимо VPN
		err = a.startTun(context.Background(), s, sub)
	} else if cfg, e := buildConfig(s, sub, via == "tun", physIfName()); e != nil {
		err = e
	} else if restart {
		err = a.core.Start(cfg, false)
	} else {
		err = a.core.ReloadRouting(cfg)
		if err == nil {
			// открытые соединения остаются на старом пути: те, кому теперь положен другой, обрываем,
			// и программа переподключается уже правильным путём (игра после «Всё» → «Умный»)
			safeGo("reroute", func() {
				if n := dispatcher.Reroute(sameRoute); n > 0 {
					logLine(fmt.Sprintf("новые маршруты: %d соединений переподключены другим путём", n))
				}
			})
		}
	}
	if err == nil && s.Mode == "smart" {
		safeGo("svcCheck", func() { a.svcCheckAll(svcEvery) }) // перешли в умный режим после долгого перерыва
	}
	if err != nil {
		switch {
		case !restart || a.core.Running():
			// ошибка в конфиге до остановки ядра: старое ядро работает, VPN остаётся как был
		case via == "tun" && s.Connect == "auto":
			// «авто»: TUN не поднялся заново — дальше через системный прокси, а не отключение
			perr := a.proxyFallback(s, sub)
			if perr == nil {
				a.event("switch", "Режим прокси вместо TUN", err.Error())
				return
			}
			err = perr
			fallthrough
		default:
			// ядро уже закрыто (или TUN без адреса): честно отключиться, а не показывать «подключено» без VPN
			a.teardown()
			a.mu.Lock()
			a.err = "Настройки не применились: " + err.Error()
			a.mu.Unlock()
		}
		a.event("error", "Настройки не применились", err.Error())
	}
	a.notify()
}

// proxyFallback — под a.op: TUN не поднялся при перезапуске, подключаемся через системный прокси
func (a *App) proxyFallback(s Settings, sub Sub) error {
	tunDown()
	cfg, err := buildConfig(s, sub, false, "")
	if err != nil {
		return err
	}
	if err := a.core.Start(cfg, false); err != nil {
		return err
	}
	if !a.test {
		if err := sysProxyOn(s.Port); err != nil {
			a.event("error", "Системный прокси не включился", err.Error())
		}
	}
	a.mu.Lock()
	a.via, a.connPort = "proxy", s.Port
	a.mu.Unlock()
	return nil
}

// routeGroup: путь, который видит пользователь. Два прямых выхода — один путь, все серверы VPN — тоже
func routeGroup(tag string) string {
	switch {
	case tag == "direct" || tag == "direct-ru" || tag == "direct-local":
		return "direct"
	case strings.HasPrefix(tag, "px-"):
		return "vpn"
	}
	return tag
}

func sameRoute(old, new string) bool { return routeGroup(old) == routeGroup(new) }

// ---------- умный режим ----------

func (a *App) smartOn() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.on && a.set.Mode == "smart"
}

// vpnFirst: новый сайт на время проверки идёт через VPN (настройка в «Маршрутах»)
func (a *App) vpnFirst() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.set.VpnFirst
}

func matchDomain(d, rule string) bool { return d == rule || strings.HasSuffix(d, "."+rule) }

func (a *App) listed(d string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.listedLocked(d)
}

// listedLocked — под a.mu
func (a *App) listedLocked(d string) bool {
	for _, l := range [][]Rule{a.set.Always, a.set.Direct, a.set.Found} {
		for _, r := range l {
			if matchDomain(d, r.Domain) {
				return true
			}
		}
	}
	return false
}

// rootDomain: www.linkedin.com → linkedin.com, чтобы правило покрыло весь сайт
func rootDomain(d string) string {
	p := strings.Split(d, ".")
	if len(p) <= 2 {
		return d
	}
	two := strings.Join(p[len(p)-2:], ".")
	if len(p[len(p)-2]) <= 3 && len(p) >= 3 { // co.uk, com.br
		return strings.Join(p[len(p)-3:], ".")
	}
	return two
}

// viaTun: подключены через TUN
func (a *App) viaTun() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.on && a.via == "tun"
}

// report: признак блокировки. Надёжный — сразу в «найдено», ненадёжный — сначала проверка двумя путями
func (a *App) report(domain, reason string) {
	if sv := svcOf(domain); sv != nil { // сервис не попадает в «найдено»: его путь решает своя проверка
		a.svcFailed(sv, domain, reason)
		return
	}
	if reliableReason(reason) {
		a.found(domain, reason)
		return
	}
	verify.suspect(domain, reason)
}

// recheckFound: перепроверить найденное по ненадёжным признакам (после подключения и в housekeeping)
func (a *App) recheckFound() {
	a.mu.Lock()
	list := slices.Clone(a.set.Found)
	a.mu.Unlock()
	verify.recheck(list)
}

// unfound убирает сайт из «найдено», если он там по ненадёжному признаку. Списки пользователя не трогаем
func (a *App) unfound(domain string) bool {
	a.mu.Lock()
	i := slices.IndexFunc(a.set.Found, func(r Rule) bool { return r.Domain == domain })
	if i < 0 || reliableReason(a.set.Found[i].Reason) {
		a.mu.Unlock()
		return false
	}
	// новый срез, а не правка на месте: снимки настроек без clone смотрят в тот же массив
	a.set.Found = append(slices.Clone(a.set.Found[:i]), a.set.Found[i+1:]...)
	a.mu.Unlock()
	safeGo("apply", func() { a.apply(false) })
	a.event("found", domain+" снова напрямую", "открывается без VPN")
	return true
}

func (a *App) found(domain, reason string) {
	d := ruleDomain(domain) // корень сайта, у общих облаков — само имя
	// проверка и добавление разом: иначе два потока добавляли один сайт дважды
	a.mu.Lock()
	if a.listedLocked(d) {
		a.mu.Unlock()
		return
	}
	a.set.Found = append(a.set.Found, Rule{Domain: d, Reason: reason, When: now()})
	a.mu.Unlock()
	// вызывают из потоков ядра: не ждать в них очереди a.op. После новых маршрутов оборвать прямые
	// соединения сайта: браузер иначе ещё долго ждёт по зависшему, а повтор пойдёт уже через VPN
	safeGo("apply", func() { a.apply(false); stalls.cut(d) })
	a.event("found", d+" теперь через VPN", reason)
}

// holdQUIC: адрес сервера зависшего HTTP/3 ненадолго через VPN (UDP 443). Браузер продолжает старое
// соединение QUIC пакетами без имени сайта, правило по имени их не узнаёт, и браузер ждал ~30 с. Через VPN
// сервер принимает продолжение с нового адреса (смена адреса в QUIC). Новые соединения HTTP/3 к сайту
// закрыты (quicOff), так что через этот адрес идёт только хвост старого
const holdFor = 2 * time.Minute

func (a *App) holdQUIC(ip string) {
	if ip == "" {
		return
	}
	a.mu.Lock()
	if slices.Contains(a.set.TempIP, ip) {
		a.mu.Unlock()
		return
	}
	a.set.TempIP = append(slices.Clone(a.set.TempIP), ip)
	a.mu.Unlock()
	safeGo("apply", func() { a.apply(false) })
	time.AfterFunc(holdFor, func() {
		a.mu.Lock()
		a.set.TempIP = slices.DeleteFunc(slices.Clone(a.set.TempIP), func(x string) bool { return x == ip })
		a.mu.Unlock()
		safeGo("apply", func() { a.apply(false) })
	})
}

// ---------- фоновые задачи ----------

func (a *App) loop() {
	t := time.NewTicker(time.Second)
	n := 0
	for range t.C {
		n++
		safeRun("tick", a.tick)
		if n%measureEvery == 3 {
			safeGo("measure", a.measure)
		}
		if n%600 == 0 {
			safeGo("housekeeping", a.housekeeping)
		}
	}
}

func (a *App) tick() {
	a.mu.Lock()
	on, sub, set := a.on, a.sub, a.set
	a.mu.Unlock()
	if !on {
		return
	}
	up, down := a.core.Traffic(tagsOf(sub))
	picked := pickConns(set, sub)
	day := time.Now().Format("2006-01-02")
	a.mu.Lock()
	if a.today != day {
		a.today, a.todayUp, a.todayDown = day, 0, 0
	}
	if up >= a.prevUp && down >= a.prevDown {
		a.speedUp, a.speedDown = up-a.prevUp, down-a.prevDown
		a.todayUp += a.speedUp
		a.todayDown += a.speedDown
	}
	a.prevUp, a.prevDown = up, down
	if len(picked) > 0 {
		a.lastTag = picked[0].Tag
	}
	a.mu.Unlock()
	a.notify()
}

// connHealth — сглаженное состояние конфига. Одна неудачная проверка — случайность (сеть моргнула, ядро
// перезапускалось), «нет ответа» показываем только после двух подряд. Пока проверок не было — состояние неизвестно
type connHealth struct {
	known   bool
	delay   int64   // показываемая задержка: среднее последних замеров
	samples []int64 // последние удачные замеры, не больше delaySamples
	fails   int     // неудачных проверок подряд
}

const (
	measureEvery = 30 // секунд между замерами задержки
	// settleTime: сколько после запуска ядра или смены маршрутов неудачный замер не считается
	settleTime   = 20 * time.Second
	delaySamples = 3 // замеров в среднем: один замер прыгает от случайных задержек сети
)

func (h *connHealth) alive() bool { return h != nil && h.known && h.delay > 0 && h.fails < 2 }

func (h *connHealth) add(d int64) {
	h.samples = append(h.samples, d)
	if len(h.samples) > delaySamples {
		h.samples = h.samples[len(h.samples)-delaySamples:]
	}
	var sum int64
	for _, v := range h.samples {
		sum += v
	}
	h.delay, h.fails = sum/int64(len(h.samples)), 0
}

// record учитывает проверки. manual — проверка по кнопке: там уже две попытки, неудача сразу честная.
// Вызывать под a.mu
func (a *App) record(sub Sub, res map[string]Probe, manual bool) {
	if a.health == nil {
		a.health = map[string]*connHealth{}
	}
	for _, c := range sub.Conns {
		p, ok := res[c.Tag]
		if !ok {
			continue
		}
		h := a.health[c.Name]
		if h == nil {
			h = &connHealth{}
			a.health[c.Name] = h
		}
		h.known = true
		switch {
		case p.Alive:
			if h.fails > 0 && !manual {
				logLine(fmt.Sprintf("замер: %s снова отвечает, %d мс", c.Name, p.Delay))
			}
			h.add(p.Delay)
		case manual:
			h.fails = 2
		default:
			if h.fails == 0 { // первая неудача подряд — в журнал причину, дальше не повторяем
				logLine("замер: " + c.Name + " не ответил: " + p.Err)
			}
			h.fails++
		}
	}
}

// measure: фоновый замер задержки всех конфигов подписки, пока VPN включён
func (a *App) measure() {
	if !a.measuring.CompareAndSwap(false, true) {
		return
	}
	defer a.measuring.Store(false)
	a.mu.Lock()
	on, sub := a.on, a.sub
	a.mu.Unlock()
	if !on {
		return
	}
	tags := tagsOf(sub)
	res := a.core.Check(tags)
	// Ядро только что запустили или сменили маршруты (выбрали другой сервер или подписку): сеть ещё
	// устаканивается, и неудача замера в эти секунды — не про сервер. Такие неудачи не записываем,
	// иначе рабочий сервер на полминуты становится «нет ответа»; замер повторяем через 5 с
	if a.core.SettledFor() < settleTime {
		for tag, p := range res {
			if !p.Alive {
				delete(res, tag)
			}
		}
	}
	a.mu.Lock()
	if a.on && sameConns(sub, a.sub) {
		a.record(sub, res, false)
	}
	on = a.on
	a.mu.Unlock()
	a.notify()
	if on && len(res) < len(tags) { // часть серверов осталась без итога — не ждём общего замера через полминуты
		time.AfterFunc(5*time.Second, func() { safeRun("measure", a.measure) })
	}
}

// housekeeping: подписка и базы сайтов обновляются сами
func (a *App) housekeeping() {
	a.mu.Lock()
	s, on, checked := a.set.clone(), a.on, a.updChecked
	subs := maps.Clone(a.subs)
	a.mu.Unlock()
	if on {
		safeGo("recheckFound", a.recheckFound) // найденное по ненадёжным признакам: не открывается ли снова напрямую
		a.svcCheckAll(svcEvery - time.Minute)  // housekeeping раз в 10 минут: запас, чтобы плановая не пропускалась
	}
	// все подписки обновляются по расписанию: у невыбранных тоже видны трафик и срок
	for _, r := range s.Subs {
		old := subs[r.ID]
		if time.Since(time.Unix(old.Updated, 0)) <= time.Duration(s.UpdateHours)*time.Hour {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		ns, err := a.fetchSubAny(ctx, r.URL)
		cancel()
		if err == nil {
			a.setSub(r.ID, ns)
			if r.ID == s.Active && on && !sameConns(old, ns) {
				a.apply(true)
			}
		}
	}
	if time.Since(checked) > 6*time.Hour {
		cl := directClient()
		if on {
			cl = a.vpnClient()
		}
		if r, err := checkUpdate(context.Background(), cl); err == nil {
			a.mu.Lock()
			a.upd, a.updChecked = r, time.Now()
			a.mu.Unlock()
		}
	}
	if geoAge() > 72*time.Hour && on {
		ctx, cancel := context.WithTimeout(context.Background(), downloadTimeout)
		changed, err := updateGeo(ctx, downloadClient(a.probeProxy()))
		cancel()
		if err == nil && changed { // те же базы — перезапускать ядро незачем
			a.apply(true)
		}
	}
}
