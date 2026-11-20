package core

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	utls "github.com/refraction-networking/utls"
	xlog "github.com/xtls/xray-core/common/log"
	xnet "github.com/xtls/xray-core/common/net"
	"golang.org/x/net/http2"
	"golang.org/x/net/publicsuffix"
)

// ---------- журнал Xray ----------

type logHandler struct{}

var (
	logMu     sync.Mutex
	logRing   []string // последние строки для раздела «Журнал»
	logFile   *os.File
	logWrites int       // строк с последней проверки размера
	logRetry  time.Time // файл не открылся: до этого времени не пробуем снова
	logPath   = func() string { return filepath.Join(appDir(), "monolit.log") }
)

const (
	logMaxSize    = 4 << 20 // больше 4 МБ — в monolit.log.1, начать заново
	logCheckEvery = 200     // размер проверяем раз в столько строк
)

func hookLog() { xlog.RegisterHandler(logHandler{}) }

func logLine(s string) {
	line := time.Now().Format("15:04:05 ") + s
	if logSink != nil {
		logSink(line)
	}
	logMu.Lock()
	defer logMu.Unlock()
	logRing = append(logRing, line)
	if len(logRing) > 400 {
		logRing = logRing[len(logRing)-400:]
	}
	if logFile == nil && time.Now().After(logRetry) {
		f, err := os.OpenFile(logPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			logRetry = time.Now().Add(30 * time.Second) // не открывать на каждой строке
		} else {
			logFile, logWrites = f, logCheckEvery // сразу проверить размер
		}
	}
	if logFile == nil {
		return
	}
	if logWrites++; logWrites >= logCheckEvery {
		logWrites = 0
		logRotate()
		if logFile == nil {
			return
		}
	}
	fmt.Fprintln(logFile, time.Now().Format("2006-01-02 ")+line)
}

// logRotate: большой журнал уходит в monolit.log.1 (старый .1 заменяется), пишем в новый. Под logMu
func logRotate() {
	st, err := logFile.Stat()
	if err != nil || st.Size() <= logMaxSize {
		return
	}
	logFile.Close()
	logFile = nil
	p := logPath()
	os.Remove(p + ".1")
	if os.Rename(p, p+".1") != nil {
		os.Truncate(p, 0) // переименовать не дали (файл открыт другим): хотя бы начать заново
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		logRetry = time.Now().Add(30 * time.Second)
		return
	}
	logFile = f
}

var notices sync.Map

// firstNotice: true, если такое сообщение встретилось впервые
func firstNotice(c string) bool {
	_, seen := notices.LoadOrStore(c, struct{}{})
	return !seen
}

func (logHandler) Handle(msg xlog.Message) {
	switch m := msg.(type) {
	case *xlog.AccessMessage:
		if m.Status != xlog.AccessAccepted {
			return
		}
		dest, ok := m.To.(xnet.Destination)
		if !ok || !dest.Address.Family().IsDomain() {
			return
		}
		d := strings.ToLower(dest.Address.Domain())
		if strings.HasSuffix(m.Detour, "direct") && (dest.Port == 443 || dest.Port == 80) {
			smart.candidate(d)
		}
	case *xlog.GeneralMessage:
		if m.Severity <= xlog.Severity_Warning {
			c := fmt.Sprint(m.Content)
			if strings.Contains(c, "deprecated") && !firstNotice(c) {
				return // предупреждение об устаревшем протоколе: проверка каждой ссылки подписки повторяет его, в журнал — один раз
			}
			logLine(c)
		}
	}
}

// ---------- умный режим: сайт не открылся напрямую — проверить и перевести на VPN ----------

type smartDetector struct {
	mu      sync.Mutex
	checked map[string]time.Time // когда домен проверяли последний раз
	busy    map[string]bool      // проверяется сейчас: HTTP/3 к нему напрямую не пускаем (race.go)
	slots   chan struct{}
}

var smart = &smartDetector{checked: map[string]time.Time{}, busy: map[string]bool{}, slots: make(chan struct{}, 8)}

// checking: домен сейчас проверяется. Пока идёт проверка, HTTP/3 напрямую к нему не пускаем: браузер сразу
// идёт по HTTPS, а такие соединения при попадании в «найдено» обрываются чисто и повторяются через VPN.
// Оборванное соединение HTTP/3 браузер продолжал пакетами без имени сайта и ждал по нему 10-15 с
func (s *smartDetector) checking(domain string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.busy[domain]
}

// pending: сайт новый и его проверка идёт (запускает её, если пора). Пока true, соединения сайта
// идут через VPN (race.go, настройка vpnFirst): проверка найдёт обрыв — браузер уже на VPN и ничего
// не надо обрывать; не найдёт — следующие соединения пойдут напрямую
func (s *smartDetector) pending(domain string) bool {
	s.candidate(domain)
	return s.checking(domain)
}

// forget: пользователь убрал сайт из списка — проверить заново при следующем заходе
func (s *smartDetector) forget(domain string) {
	s.mu.Lock()
	for d := range s.checked {
		if matchDomain(d, domain) {
			delete(s.checked, d)
		}
	}
	s.mu.Unlock()
	races.forget(rootOf(domain))
}

func (s *smartDetector) done(domain string) {
	s.mu.Lock()
	delete(s.busy, domain)
	s.mu.Unlock()
}

const (
	smartRecheck    = 6 * time.Hour // проверенный домен не проверяем снова столько
	smartCheckedMax = 8192          // отметок больше — убрать устаревшие
)

// prune: отметки старше smartRecheck уже ничего не решают; если все свежие — начать заново. Под s.mu
func (s *smartDetector) prune() {
	if len(s.checked) < smartCheckedMax {
		return
	}
	for d, t := range s.checked {
		if time.Since(t) >= smartRecheck {
			delete(s.checked, d)
		}
	}
	if len(s.checked) >= smartCheckedMax {
		s.checked = map[string]time.Time{}
	}
}

// rootOf: www.linkedin.com → linkedin.com, news.bbc.co.uk → bbc.co.uk — по списку публичных суффиксов.
// Адрес IP и одиночное имя — как есть; если список не помог — прежнее правило rootDomain
func rootOf(host string) string {
	h := strings.TrimSuffix(strings.ToLower(host), ".")
	if h == "" || net.ParseIP(strings.Trim(h, "[]")) != nil || !strings.Contains(h, ".") {
		return h
	}
	if r, err := publicsuffix.EffectiveTLDPlusOne(h); err == nil {
		return r
	}
	return rootDomain(h)
}

// домены, которые не проверяем: российские и служебные
var skipSuffix = []string{".ru", ".su", ".xn--p1ai", ".local", ".lan", "localhost", ".arpa", ".msftconnecttest.com", ".windowsupdate.com"}

func skipDomain(domain string) bool {
	for _, suf := range skipSuffix {
		if strings.HasSuffix(domain, suf) || domain == strings.TrimPrefix(suf, ".") {
			return true
		}
	}
	return false
}

func (s *smartDetector) candidate(domain string) {
	if app == nil || !app.smartOn() {
		return
	}
	if skipDomain(domain) || svcOf(domain) != nil { // сервисы проверяются целиком (svc.go)
		return
	}
	if app.listed(domain) {
		return
	}
	s.mu.Lock()
	if t, ok := s.checked[domain]; ok && time.Since(t) < smartRecheck {
		s.mu.Unlock()
		return
	}
	s.prune()
	first := time.Now()
	s.checked[domain] = first
	s.busy[domain] = true
	s.mu.Unlock()
	go func() {
		defer s.done(domain)
		select {
		case s.slots <- struct{}{}:
		case <-time.After(45 * time.Second):
			// очередь не дошла: снять отметку, чтобы сайт проверился, когда попадётся снова
			s.mu.Lock()
			delete(s.checked, domain)
			s.mu.Unlock()
			return
		}
		defer func() { <-s.slots }()
		st := time.Now()
		reason := probe(domain, nil)
		if reason == "" {
			return
		}
		if dnsFail(reason) {
			logLine(domain + ": напрямую " + reason + ", это сбой DNS, а не блокировка: оставлен как есть")
			return
		}
		// проверка файлов (assets.go) уже сравнила оба пути: файл через VPN скачался целиком.
		// Повторная проверка главной через VPN только отнимала время
		if !strings.HasPrefix(reason, "файл ") {
			logLine(domain + ": напрямую " + reason + ", проверяю через VPN")
			// напрямую не открылся: проверить через VPN, иначе сайт просто не работает
			if r := probe(domain, app.probeProxy()); r != "" {
				logLine(domain + ": напрямую " + reason + ", через VPN тоже " + r + ", оставлен как есть")
				return
			}
		}
		// проверка двумя путями уже прошла (напрямую не открылся, через VPN открылся): сразу в «найдено».
		// Повторная проверка через report могла проскочить напрямую и сайт не добавлялся (mailflare.co, 05:49)
		logLine(fmt.Sprintf("%s: напрямую %s, через VPN открылся (проверка %.1f с, от первого соединения %.1f с)",
			domain, reason, time.Since(st).Seconds(), time.Since(first).Seconds()))
		if sv := svcOf(domain); sv != nil {
			app.svcFailed(sv, domain, reason)
			return
		}
		app.found(domain, reason)
	}()
}

// probeDialer: в TUN своё соединение «напрямую» ушло бы в сам туннель (и в гонку путей, где его выручит VPN),
// поэтому проверку напрямую оболочка системы пускает мимо туннеля, как выход direct ядра
func probeDialer(proxy *url.URL) *net.Dialer {
	d := &net.Dialer{Timeout: 6 * time.Second}
	if proxy != nil || app == nil || !app.viaTun() {
		return d
	}
	plat.BindDirect(d)
	return d
}

// probe открывает https://домен и возвращает причину неудачи или "" если открылся
func probe(domain string, proxy *url.URL) string {
	if proxy == nil {
		// напрямую сначала рукопожатие как у Chrome: провайдер узнаёт браузер по нему и ломает
		// только такие соединения (так с YouTube), а обычное рукопожатие Go пропускает
		if r := chromeHandshake(domain); r != "" {
			return r
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Second)
	defer cancel()
	tr := &http.Transport{
		Proxy:               func(*http.Request) (*url.URL, error) { return proxy, nil },
		DialContext:         probeDialer(proxy).DialContext,
		TLSHandshakeTimeout: 6 * time.Second,
		TLSClientConfig:     &tls.Config{ServerName: domain},
		DisableKeepAlives:   true,
	}
	defer tr.CloseIdleConnections()
	cl := &http.Client{Transport: tr, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if h := r.URL.Hostname(); strings.Contains(h, "warning.rt.ru") || strings.Contains(h, "blocklist") || strings.Contains(h, "zapret-info") {
			return errBlockPage
		}
		if len(via) > 3 {
			return http.ErrUseLastResponse
		}
		return nil
	}}
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://"+domain+"/", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36")
	res, err := cl.Do(req)
	if err == nil {
		var html bytes.Buffer
		r := readBody(capBody{res.Body, &html}, probeRead, probeIdle)
		res.Body.Close()
		if res.StatusCode == 451 {
			return "ответ 451, доступ ограничен"
		}
		if r == "" && proxy == nil { // главная прошла: проверить её крупные файлы (assets.go)
			r = assetProbe(res.Request.URL.Hostname(), html.Bytes())
		}
		return r
	}
	r := reasonOf(err)
	if r == "" {
		logLine(domain + ": непонятная ошибка проверки: " + err.Error())
	}
	return r
}

// Провайдер замедляет иначе, чем блокирует: соединение и первые 16–24 КБ проходят, потом загрузка встаёт
// без обрыва. Поэтому страницу читаем дальше этого порога: встала посреди — сайт замедлен
const (
	probeRead = 160 << 10       // сколько читать: порог замедления плавает (видели 24 и 64 КБ), берём с запасом
	probeIdle = 4 * time.Second // сколько ждать новых данных, прежде чем считать, что загрузка встала
)

// readBody читает до limit байт; конец страницы раньше — открылся. Данные перестали идти дольше idle
// или соединение оборвалось посреди — причина для умного режима
func readBody(body io.ReadCloser, limit int64, idle time.Duration) string {
	_, r := readBodyN(body, limit, idle)
	return r
}

// readBodyN — readBody, ещё и сколько прочитано
func readBodyN(body io.ReadCloser, limit int64, idle time.Duration) (int64, string) {
	var stalled atomic.Bool
	t := time.AfterFunc(idle, func() { stalled.Store(true); body.Close() })
	defer t.Stop()
	buf := make([]byte, 16<<10)
	var n int64
	for n < limit {
		k, err := body.Read(buf)
		n += int64(k)
		if k > 0 {
			t.Reset(idle)
		}
		if err == io.EOF {
			return n, ""
		}
		if err != nil {
			if stalled.Load() {
				return n, fmt.Sprintf("загрузка встала после %d КБ", n>>10)
			}
			return n, fmt.Sprintf("обрыв после %d КБ", n>>10)
		}
	}
	return n, ""
}

func chromeHandshake(domain string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Second)
	defer cancel()
	c, err := probeDialer(nil).DialContext(ctx, "tcp", net.JoinHostPort(domain, "443"))
	if err != nil {
		return reasonOf(err)
	}
	defer c.Close()
	u := utls.UClient(c, &utls.Config{ServerName: domain}, utls.HelloChrome_Auto)
	if err := u.HandshakeContext(ctx); err != nil {
		if r := reasonOf(err); r != "" {
			return r
		}
		logLine(domain + ": рукопожатие напрямую: " + err.Error())
		return "обрыв при установке защищённого соединения"
	}
	// страницу читаем по этому же соединению: провайдер замедляет именно соединения, похожие на Chrome,
	// а обычный клиент Go получает страницу целиком, и замедленный сайт выглядел рабочим (mailflare.co)
	return chromePage(ctx, u, domain)
}

// chromePage запрашивает главную страницу по готовому соединению (HTTP/2 или HTTP/1.1, как договорились)
// и читает её через readBody. Сбой самого запроса — не признак блокировки: рукопожатие уже прошло
func chromePage(ctx context.Context, u *utls.UConn, domain string) string {
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://"+domain+"/", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
	var res *http.Response
	var err error
	if u.ConnectionState().NegotiatedProtocol == "h2" {
		var cc *http2.ClientConn
		if cc, err = (&http2.Transport{}).NewClientConn(u); err == nil {
			defer cc.Close()
			res, err = cc.RoundTrip(req)
		}
	} else if err = req.Write(u); err == nil {
		res, err = http.ReadResponse(bufio.NewReader(u), req)
	}
	if err != nil {
		if r := reasonOf(err); r == "сброс соединения" {
			return r
		}
		return ""
	}
	defer res.Body.Close()
	return readBody(connBody{res.Body, u}, probeRead, probeIdle) // connBody: тишину обрывает закрытием соединения (assets.go)
}

var errBlockPage = errors.New("заглушка провайдера")

func reasonOf(err error) string {
	var dnsErr *net.DNSError
	var opErr *net.OpError
	switch {
	case errors.Is(err, errBlockPage):
		return "заглушка провайдера"
	case errors.As(err, &dnsErr):
		return dnsReason
	case errors.Is(err, syscall.ECONNRESET) || strings.Contains(err.Error(), "forcibly closed") || strings.Contains(err.Error(), "reset"):
		return "сброс соединения"
	case strings.Contains(err.Error(), "handshake") || strings.Contains(err.Error(), "EOF"):
		return "обрыв при установке защищённого соединения"
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "timeout"):
		return "нет ответа"
	case errors.As(err, &opErr):
		return "соединение не устанавливается"
	}
	var cert *tls.CertificateVerificationError
	if errors.As(err, &cert) {
		return "подменён сертификат"
	}
	return ""
}

// ---------- умный режим: надёжность признака и проверка двумя путями ----------

// reliableReasons — признаки, которые случайный сбой не даёт: такой сайт сразу в «найдено».
// Остальное (замедление, нет ответа, адрес не находится, обрыв, победа VPN в гонке) — сначала проверка
var reliableReasons = map[string]bool{
	"подменён сертификат":         true,
	"сброс соединения":            true, // сброс в рукопожатии: подложенный провайдером RST
	"заглушка провайдера":         true,
	"ответ 451, доступ ограничен": true,
	midReason: true, // встал посреди записи TLS: так не тормозит ни сервер, ни сеть (watch.go)
}

const midReason = "обрыв посреди ответа"

// dnsReason — напрямую не нашёлся адрес. Не признак блокировки: адрес ищется через зашифрованный DNS, подменить
// ответ для одного сайта провайдер не может, а сбои у этого DNS бывают у всех сайтов разом (riotgames.com попал
// в «найдено» в 01:00, YouTube и Discord раз в 20 минут уходили на VPN). Такая проверка не в счёт: ни в «найдено»,
// ни из него, путь сервиса не меняется
const dnsReason = "адрес не находится"

func dnsFail(r string) bool { return r == dnsReason }

func reliableReason(r string) bool { return reliableReasons[r] }

// sharedClouds — общие облака и CDN: на одном корне тысячи чужих сайтов, в список идёт конкретное имя.
// Площадки из частной части списка суффиксов (s3.amazonaws.com, appspot.com…) rootOf и так не склеивает
var sharedClouds = map[string]bool{
	"amazonaws.com": true, "cloudfront.net": true, "awsglobalaccelerator.com": true, "elasticbeanstalk.com": true,
	"googleusercontent.com": true, "googleapis.com": true, "googlehosted.com": true, "ggpht.com": true,
	"windows.net": true, "cloudapp.net": true, "trafficmanager.net": true, "msedge.net": true,
	"edgekey.net": true, "edgesuite.net": true, "fastly.net": true, "fastlylb.net": true,
	"cloudflare.net": true, "cdn77.org": true, "b-cdn.net": true, "digitaloceanspaces.com": true,
}

func sharedCloud(root string) bool {
	if sharedClouds[root] {
		return true
	}
	name, _, _ := strings.Cut(root, ".")
	return strings.HasPrefix(name, "akamai") || strings.HasPrefix(name, "azure")
}

// ruleDomain — что записать в список: корень сайта, а у общего облака — само имя
func ruleDomain(host string) string {
	h := strings.TrimSuffix(strings.ToLower(host), ".")
	if r := rootOf(h); !sharedCloud(r) {
		return r
	}
	return h
}

const (
	verifyEvery = 10 * time.Minute // один и тот же сайт не проверяем чаще
	recheckGap  = 3 * time.Minute  // между удачными перепроверками найденного — не меньше
	recheckNeed = 2                // столько удачных подряд — и сайт уходит из «найдено»
	verifyMax   = 4096             // отметок больше — убрать устаревшие
)

type goodMark struct {
	n  int       // удачных проверок напрямую подряд
	at time.Time // последней удачной
}

// verifier: ненадёжный признак проверяется напрямую и через VPN одновременно. Пока идёт проверка,
// соединения сайта идут как раньше (гонка путей, VPN при зависании)
type verifier struct {
	mu      sync.Mutex
	pending map[string]bool      // сейчас проверяются
	last    map[string]time.Time // когда проверяли последний раз
	good    map[string]goodMark  // найденные, открывшиеся напрямую
	slots   chan struct{}
	running atomic.Bool // идёт перепроверка списка
	gap     time.Duration

	// подменяются в тестах
	on     func() bool
	direct func(host string) string // "" — открылся
	vpn    func(host string) string
	add    func(host, reason string)
	drop   func(domain string) bool
}

func newVerifier() *verifier {
	return &verifier{
		pending: map[string]bool{}, last: map[string]time.Time{}, good: map[string]goodMark{},
		slots: make(chan struct{}, 4), gap: recheckGap,
		on:     func() bool { return app != nil && app.smartOn() },
		direct: func(h string) string { return probe(h, nil) },
		vpn:    func(h string) string { return probe(h, app.probeProxy()) },
		add:    func(h, r string) { app.found(h, r) },
		drop:   func(d string) bool { return app.unfound(d) },
	}
}

var verify *verifier

func init() { verify = newVerifier() } // не в объявлении: проверка через app ссылается на verify

// begin: false, если сайт уже проверяется или проверялся меньше every назад
func (v *verifier) begin(key string, every time.Duration) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.pending[key] {
		return false
	}
	if t, ok := v.last[key]; ok && time.Since(t) < every {
		return false
	}
	if len(v.last) >= verifyMax {
		for k, t := range v.last {
			if time.Since(t) >= verifyEvery {
				delete(v.last, k)
			}
		}
		if len(v.last) >= verifyMax {
			v.last = map[string]time.Time{}
		}
	}
	v.pending[key], v.last[key] = true, time.Now()
	return true
}

// end: проверка закончилась. forget — она не состоялась (очередь), сайт можно проверить снова
func (v *verifier) end(key string, forget bool) {
	v.mu.Lock()
	delete(v.pending, key)
	if forget {
		delete(v.last, key)
	}
	v.mu.Unlock()
}

func (v *verifier) busy(key string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.pending[key]
}

// take: место в очереди проверок; не дождались — false
func (v *verifier) take() bool {
	select {
	case v.slots <- struct{}{}:
		return true
	case <-time.After(45 * time.Second):
		return false
	}
}

// dual: проверка напрямую и через VPN одновременно. У каждой свой срок (probe)
func (v *verifier) dual(host string) (direct, vpn string) {
	direct, vpn = "сбой проверки", "сбой проверки"
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer recoverLog("проверке напрямую")
		direct = v.direct(host)
	}()
	go func() {
		defer wg.Done()
		defer recoverLog("проверке через VPN")
		vpn = v.vpn(host)
	}()
	wg.Wait()
	return
}

// suspect: ненадёжный признак блокировки — сайт в «найдено» только после проверки двумя путями
func (v *verifier) suspect(host, reason string) {
	if skipDomain(host) || !v.on() {
		return
	}
	key := ruleDomain(host)
	if !v.begin(key, verifyEvery) {
		return
	}
	safeGo("проверке сайта", func() {
		if !v.take() {
			v.end(key, true)
			return
		}
		defer func() { <-v.slots }()
		defer v.end(key, false)
		v.decide(host, reason)
	})
}

// decide: напрямую не открылся, а через VPN открылся — в список; напрямую открылся — не записываем
func (v *verifier) decide(host, reason string) {
	d, p := v.dual(host)
	key := ruleDomain(host)
	switch {
	case d == "":
		if firstNotice("напрямую открылся " + key) {
			logLine(key + ": напрямую открылся, в список не добавлен")
		}
	case dnsFail(d):
		logLine(host + ": напрямую " + d + ", это сбой DNS, а не блокировка: в список не добавлен")
	case p != "":
		logLine(host + ": напрямую " + d + ", через VPN тоже " + p + ", оставлен как есть")
	default:
		logLine(host + ": " + reason + "; проверка: напрямую " + d + ", через VPN открылся")
		v.add(host, reason)
	}
}

// recheck: найденные по ненадёжному признаку проверить снова. Открылся напрямую recheckNeed раз подряд
// (не чаще gap) — убрать из «найдено». Надёжные причины и списки пользователя не трогаем
func (v *verifier) recheck(list []Rule) {
	if !v.on() || !v.running.CompareAndSwap(false, true) {
		return
	}
	defer v.running.Store(false)
	var wg sync.WaitGroup
	for _, r := range list {
		if r.Domain == "" || reliableReason(r.Reason) {
			continue
		}
		if dnsFail(r.Reason) { // добавлен по сбою DNS, до того как он перестал быть признаком: убрать без проверки
			v.drop(r.Domain)
			continue
		}
		if !v.begin(r.Domain, v.gap) {
			continue
		}
		if !v.take() {
			v.end(r.Domain, true)
			continue
		}
		wg.Add(1)
		go func(dom string) {
			defer wg.Done()
			defer func() { <-v.slots }()
			defer v.end(dom, false)
			defer recoverLog("перепроверке найденного")
			if !v.on() {
				return
			}
			d, _ := v.dual(dom)
			if dnsFail(d) { // сбой DNS не говорит ни за, ни против: счёт не сбрасываем
				return
			}
			if v.count(dom, d == "") {
				v.drop(dom)
			}
		}(r.Domain)
	}
	wg.Wait()
}

// count учитывает перепроверку; true — пора убрать из списка
func (v *verifier) count(dom string, ok bool) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	if !ok {
		delete(v.good, dom) // не открылся — счёт заново
		return false
	}
	m := v.good[dom]
	if m.n > 0 && time.Since(m.at) < v.gap {
		return false // слишком скоро после прошлой удачной: не в счёт
	}
	m.n++
	m.at = time.Now()
	if m.n < recheckNeed {
		v.good[dom] = m
		return false
	}
	delete(v.good, dom)
	return true
}
