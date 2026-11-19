package main

// Слежка за прямыми соединениями: сайт, который напрямую отвечает с замедлением (запрос ушёл, ответа нет
// несколько секунд), умный режим проверяет через VPN. Если там он отвечает быстро, зависшие соединения
// обрываются, и браузер повторяет запрос гонкой путей (при зависании прямого — через VPN). В «найдено» сайт
// попадает только после проверки двумя путями (verifier в smart.go).

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/transport"
)

const (
	handWait     = time.Second             // ответ на начало соединения (ServerHello): сервер его не обдумывает
	stallWait    = 2500 * time.Millisecond // ответ на запрос: сервер может думать, ждём дольше
	stallMinReq  = 120                     // байт запроса без ответа: меньше — служебные кадры (подтверждения, окна)
	stallMaxAge  = 60 * time.Second        // долгоживущие соединения (чаты, push) не считаем
	stallMaxDown = 1 << 20                 // уже получил мегабайт — это не замедление на старте
	vpnFastLimit = 3 * time.Second         // через VPN сайт должен ответить за это время, иначе VPN тоже не помогает
	midWait      = 3 * time.Second         // ответ встал посреди записи TLS: столько ждём продолжения
	midMaxAge    = 30 * time.Minute        // соединение TCP старше — больше не следим (память)
)

// Обрыв посреди ответа: провайдер пропускает первые ~16 КБ ответа (так режут сайты на Cloudflare), дальше
// пакеты сайта не доходят, а соединение висит. Страница сайта маленькая и проходит целиком, запрос ушёл
// и ответ пришёл — обычная слежка этого не видит. Видно по записям TLS: размер каждой — в её заголовке,
// и сервер не останавливается посреди записи. Ответ встал посреди записи дольше midWait — это обрыв

// tlsRecs — где мы в потоке записей TLS от сайта. Пишет один поток (выход direct), читает слежка
type tlsRecs struct {
	left   int     // байт тела текущей записи ещё не пришло
	hdr    [5]byte // заголовок, пришедший не целиком
	hdrN   int
	broken bool        // поток не похож на TLS — дальше не разбираем
	mid    atomic.Bool // стоим посреди записи
}

func (t *tlsRecs) feed(b []byte) {
	for len(b) > 0 && !t.broken {
		if t.left > 0 {
			k := min(t.left, len(b))
			t.left -= k
			b = b[k:]
			continue
		}
		k := copy(t.hdr[t.hdrN:], b)
		t.hdrN += k
		b = b[k:]
		if t.hdrN < 5 {
			break
		}
		t.hdrN = 0
		n := int(t.hdr[3])<<8 | int(t.hdr[4])
		if t.hdr[0] < 20 || t.hdr[0] > 24 || t.hdr[1] != 3 || n == 0 || n > 18432 {
			t.broken = true
			break
		}
		t.left = n
	}
	t.mid.Store(!t.broken && (t.left > 0 || t.hdrN > 0))
}

type watchConn struct {
	domain  string
	network string
	ip      string // адрес сервера (в TUN известен; через прокси — пусто)
	start   time.Time
	pending atomic.Int64 // байт отправлено с последнего ответа
	lastUp  atomic.Int64 // UnixNano последней отправки
	down    atomic.Int64
	done    atomic.Bool
	// гонка путей (race.go)
	vpnFirst bool            // сайт проверяется: VPN главный, прямой путь запасной
	orig     *transport.Link // link, каким его получил Intercept: по нему ядро отмечает настоящий выход
	flagged  atomic.Bool
	link     *transport.Link // исходный, чтобы оборвать
	recs     tlsRecs
	lastDn   atomic.Int64 // UnixNano последнего ответа
	pendAt   atomic.Int64 // UnixNano первой отправки, оставшейся без ответа
}

// cutMid: ответ встал посреди записи TLS дольше midWait (только TCP: у QUIC записей TLS в потоке нет)
func (c *watchConn) cutMid(now time.Time) bool {
	return !c.done.Load() && c.network == "tcp" && c.recs.mid.Load() &&
		now.Sub(time.Unix(0, c.lastDn.Load())) >= midWait
}

// wait: сколько ждать ответа. Пока от сайта не пришло ни байта, идёт рукопожатие — ждём меньше
func (c *watchConn) wait() time.Duration {
	if c.down.Load() == 0 {
		return handWait
	}
	return stallWait
}

// stuck: запрос ушёл, ответа нет дольше wait(), а соединение свежее и почти ничего не получило
// Отсчёт: у TCP — от последней отправки (долгая выгрузка файла шлёт данные без ответа, это не зависание);
// у UDP (HTTP/3) — от первой отправки без ответа: QUIC без ответа повторяет пакеты с растущими паузами
// (0,3, 0,6, 1,2, 2,4 с), и отсчёт от последней набирал 2,5 с тишины только через ~7 с. Выгрузка по QUIC
// так не ошибётся: сервер всё время подтверждает полученное, а это ответ
func (c *watchConn) stuck(now time.Time) bool {
	ref := c.lastUp.Load()
	if c.network == "udp" {
		ref = c.pendAt.Load()
	}
	return !c.done.Load() && now.Sub(c.start) <= stallMaxAge && c.down.Load() <= stallMaxDown &&
		c.pending.Load() >= stallMinReq && now.Sub(time.Unix(0, ref)) >= c.wait()
}

type watchReader struct { // от программы к сайту
	buf.Reader
	c *watchConn
}

func (r *watchReader) note(mb buf.MultiBuffer, err error) {
	if n := mb.Len(); n > 0 {
		t := time.Now().UnixNano()
		if r.c.pending.Add(int64(n)) == int64(n) { // до этого всё было отвечено: отсюда отсчёт тишины у UDP
			r.c.pendAt.Store(t)
		}
		r.c.lastUp.Store(t)
	}
	if err != nil {
		r.c.done.Store(true)
	}
}

func (r *watchReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	mb, err := r.Reader.ReadMultiBuffer()
	r.note(mb, err)
	return mb, err
}

func (r *watchReader) ReadMultiBufferTimeout(d time.Duration) (buf.MultiBuffer, error) {
	var mb buf.MultiBuffer
	var err error
	if tr, ok := r.Reader.(buf.TimeoutReader); ok {
		mb, err = tr.ReadMultiBufferTimeout(d)
	} else {
		mb, err = r.Reader.ReadMultiBuffer()
	}
	r.note(mb, err)
	return mb, err
}

func (r *watchReader) Interrupt() { common.Interrupt(r.Reader) }

type watchWriter struct { // от сайта к программе
	buf.Writer
	c *watchConn
}

func (w *watchWriter) WriteMultiBuffer(mb buf.MultiBuffer) error {
	if n := mb.Len(); n > 0 {
		w.c.down.Add(int64(n))
		w.c.pending.Store(0)
		w.c.lastDn.Store(time.Now().UnixNano())
		if w.c.network == "tcp" {
			for _, b := range mb {
				w.c.recs.feed(b.Bytes())
			}
		}
	}
	return w.Writer.WriteMultiBuffer(mb)
}

func (w *watchWriter) Close() error {
	w.c.done.Store(true)
	return common.Close(w.Writer)
}

func (w *watchWriter) Interrupt() {
	w.c.done.Store(true)
	common.Interrupt(w.Writer)
}

type stallWatch struct {
	mu      sync.Mutex
	conns   map[*watchConn]struct{}
	testing map[string]bool      // сайты (ruleDomain), которые сейчас проверяются через VPN
	noQUIC  map[string]time.Time // сайт (rootOf) → до какого времени HTTP/3 напрямую не пускаем
}

var stalls = &stallWatch{conns: map[*watchConn]struct{}{}, testing: map[string]bool{}, noQUIC: map[string]time.Time{}}

// HTTP/3 (QUIC) шифрует всё, записей TLS в потоке нет: обрыв посреди ответа в нём не виден, а проверка
// главной страницы напрямую проходит. Поэтому, если QUIC к сайту завис, а через VPN сайт отвечает, HTTP/3
// напрямую к нему на noQUICFor не пускаем: браузер сразу переходит на обычный HTTPS (TCP), где обрыв
// ловится по записям TLS, гонка и слежка работают
const noQUICFor = 30 * time.Minute

func (s *stallWatch) quicOff(root string) {
	s.mu.Lock()
	now := time.Now()
	for k, t := range s.noQUIC { // просроченные не копим
		if now.After(t) {
			delete(s.noQUIC, k)
		}
	}
	s.noQUIC[root] = now.Add(noQUICFor)
	s.mu.Unlock()
}

func (s *stallWatch) quicBlocked(root string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.noQUIC[root]
	return ok && time.Now().Before(t)
}

// wrap ставит слежку на прямое соединение сайта domain
func (s *stallWatch) wrap(domain, network, ip string, link *transport.Link) (*transport.Link, *watchConn) {
	c := &watchConn{domain: domain, network: network, ip: ip, start: time.Now(), link: link}
	s.mu.Lock()
	s.conns[c] = struct{}{}
	s.mu.Unlock()
	return &transport.Link{Reader: &watchReader{link.Reader, c}, Writer: &watchWriter{link.Writer, c}}, c
}

func (s *stallWatch) loop() {
	for range time.Tick(200 * time.Millisecond) {
		now := time.Now()
		var hit, mid []*watchConn
		s.mu.Lock()
		for c := range s.conns {
			age := now.Sub(c.start)
			// обрыв посреди ответа ловим всё время жизни соединения TCP: браузер грузит картинки и по старому,
			// открытому раньше; зависание без ответа — только у свежих (долгие чаты и push молчат законно)
			if c.done.Load() || age > stallMaxAge && (c.network != "tcp" || age > midMaxAge) {
				delete(s.conns, c)
				continue
			}
			if c.flagged.Load() {
				continue
			}
			switch {
			case c.cutMid(now):
				c.flagged.Store(true)
				mid = append(mid, c)
			case c.stuck(now):
				c.flagged.Store(true)
				hit = append(hit, c)
			}
		}
		s.mu.Unlock()
		for _, c := range hit {
			s.stalled(c, "")
		}
		for _, c := range mid {
			s.stalled(c, fmt.Sprintf("обрыв посреди ответа после %d КБ (тишина %.1f с, соединению %.0f с)", c.down.Load()>>10,
				now.Sub(time.Unix(0, c.lastDn.Load())).Seconds(), now.Sub(c.start).Seconds()))
		}
	}
}

// stalled: прямое соединение сайта встало. why пусто — нет ответа на запрос (замедление, сначала проверка
// двумя путями); иначе — обрыв посреди ответа: признак надёжный, после ответа через VPN сайт сразу в «найдено»
func (s *stallWatch) stalled(c *watchConn, why string) {
	key := ruleDomain(c.domain)
	s.mu.Lock()
	if s.testing[key] {
		// сайт уже проверяется по другому соединению: это не терять, проверить на следующем круге
		c.flagged.Store(false)
		s.mu.Unlock()
		return
	}
	s.testing[key] = true
	s.mu.Unlock()
	safeGo("слежке за зависанием", func() {
		defer func() {
			s.mu.Lock()
			delete(s.testing, key)
			s.mu.Unlock()
		}()
		if app.listed(c.domain) {
			s.cut(key)
			return
		}
		if verify.busy(key) { // сайт уже проверяется двумя путями: оборвать, повтор пойдёт гонкой
			races.forget(rootOf(c.domain))
			s.cut(key)
			return
		}
		st := time.Now()
		r := probe(c.domain, app.probeProxy())
		took := time.Since(st)
		if why != "" {
			if r != "" {
				logLine(fmt.Sprintf("%s: напрямую %s, через VPN тоже %s, оставлен как есть", c.domain, why, r))
				return
			}
			logLine(fmt.Sprintf("%s: напрямую %s, через VPN открылся", c.domain, why))
			races.forget(rootOf(c.domain))
			app.report(c.domain, midReason)
			s.cut(key)
			return
		}
		if r != "" || took > vpnFastLimit {
			logLine(fmt.Sprintf("%s: напрямую нет ответа %.1f с (%s), через VPN тоже не ответил, оставлен как есть", c.domain, c.wait().Seconds(), c.network))
			return
		}
		logLine(fmt.Sprintf("%s: напрямую нет ответа %.1f с (%s), через VPN ответ за %d мс", c.domain, c.wait().Seconds(), c.network, took.Milliseconds()))
		if c.network == "udp" {
			// HTTP/3 завис. Дальше сайт напрямую без HTTP/3 (quicOff): по HTTPS обрыв посреди ответа виден.
			// Висящее соединение QUIC браузер продолжает пакетами без имени сайта — их ненадолго через VPN
			// по адресу (holdQUIC). А сайт проверяется вместе с файлами главной (assetProbe): напрямую встало,
			// через VPN скачалось — в «найдено» насовсем
			races.forget(rootOf(c.domain))
			s.quicOff(rootOf(c.domain))
			app.holdQUIC(c.ip)
			app.report(c.domain, "завис HTTP/3")
			s.cut(key)
			return
		}
		// замедление — признак ненадёжный: в список только после проверки. А пока повтор соединения
		// идёт гонкой путей — при зависании прямого его подхватит VPN
		races.forget(rootOf(c.domain))
		app.report(c.domain, "замедление")
		s.cut(key)
	})
}

// cut обрывает прямые соединения сайта: браузер повторит запрос, и он пойдёт уже через VPN
func (s *stallWatch) cut(root string) {
	s.mu.Lock()
	var list []*watchConn
	for c := range s.conns {
		if matchDomain(c.domain, root) {
			delete(s.conns, c)
			if !c.done.Load() { // done — соединение гонка уже отдала VPN: обрывать нечего
				list = append(list, c)
			}
		}
	}
	s.mu.Unlock()
	udp := 0
	for _, c := range list {
		common.Interrupt(c.link.Reader)
		common.Interrupt(c.link.Writer)
		// HTTP/3: браузер продолжает оборванное соединение пакетами без имени сайта, они шли бы по адресу
		// напрямую, и браузер ждал бы по мёртвому соединению. Адрес сервера ненадолго через VPN (holdQUIC)
		if c.network == "udp" && app != nil {
			udp++
			app.holdQUIC(c.ip)
		}
	}
	if len(list) > 0 {
		logLine(fmt.Sprintf("%s: оборвано прямых соединений: %d (HTTP/3: %d)", root, len(list), udp))
	}
}
