package core

// Проверка файлов с главной. Провайдер обрывает только большие ответы (~16 КБ и дальше): у сайта с маленькой
// главной (vinfo-api.com, 1,7 КБ) проверка главной напрямую проходит, а картинки не грузятся. Что грузит
// браузер, monolit не видит (адрес внутри HTTPS), поэтому берём файлы, на которые ссылается сама главная:
// картинки, стили, скрипты того же сайта. Самые подходящие качаем одновременно напрямую и через VPN:
// напрямую встало, а через VPN скачалось целиком — провайдер режет сайт

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"
)

const (
	assetMax     = 3         // файлов на проверку
	assetHTMLMax = 256 << 10 // сколько главной разбирать
)

var assetRe = regexp.MustCompile(`(?i)(?:src|href|srcset|data-src)\s*=\s*["']\s*([^"'\s>]+)`)

// assetKind: чем выше, тем лучше файл для проверки (картинки обычно крупнее и не кэшируются в общих CDN)
func assetKind(p string) int {
	switch strings.ToLower(path.Ext(p)) {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif", ".avif":
		return 3
	case ".js", ".css", ".woff2", ".woff":
		return 2
	case ".svg", ".ico":
		return 1
	}
	return 0
}

// assetURLs — файлы того же сайта из главной, лучшие первыми, не больше assetMax
func assetURLs(domain string, html []byte) []*url.URL {
	base := &url.URL{Scheme: "https", Host: domain, Path: "/"}
	type cand struct {
		u    *url.URL
		kind int
	}
	var list []cand
	seen := map[string]bool{}
	for _, m := range assetRe.FindAllSubmatch(html, -1) {
		raw := string(m[1])
		if strings.HasPrefix(raw, "data:") {
			continue
		}
		u, err := base.Parse(raw)
		if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Hostname(), domain) {
			continue
		}
		k := assetKind(u.Path)
		if k == 0 || seen[u.Path] {
			continue
		}
		seen[u.Path] = true
		u.Fragment = ""
		list = append(list, cand{u, k})
	}
	slices.SortStableFunc(list, func(a, b cand) int { return b.kind - a.kind })
	var out []*url.URL
	for _, c := range list {
		if len(out) == assetMax {
			break
		}
		out = append(out, c.u)
	}
	return out
}

// capBody запоминает начало прочитанного (главную — для поиска файлов)
type capBody struct {
	io.ReadCloser
	buf *bytes.Buffer
}

func (c capBody) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	if room := assetHTMLMax - c.buf.Len(); room > 0 && n > 0 {
		c.buf.Write(p[:min(n, room)])
	}
	return n, err
}

// chromeGet: файл напрямую с рукопожатием как у Chrome (провайдер ломает именно такие соединения).
// Возвращает сколько прочитано и причину; "" — скачался целиком
// progress — сколько прочитано напрямую и когда пришёл последний байт (для решения без долгой тишины)
type progress struct {
	n, last atomic.Int64
}

type countBody struct {
	io.ReadCloser
	pr *progress
}

func (b countBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.pr.n.Add(int64(n))
		b.pr.last.Store(time.Now().UnixNano())
	}
	return n, err
}

func chromeGet(ctx context.Context, u *url.URL, pr *progress) (int64, string) {
	c, err := assetDial(ctx, u.Host)
	if err != nil {
		return 0, reasonOf(err)
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.Close() }) // отмена проверки обрывает и висящее чтение
	defer stop()
	uc := utls.UClient(c, &utls.Config{ServerName: u.Hostname(), InsecureSkipVerify: assetInsecure}, utls.HelloChrome_Auto)
	if err := uc.HandshakeContext(ctx); err != nil {
		return 0, "обрыв при установке защищённого соединения"
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36")
	req.Header.Set("Accept", "*/*")
	var res *http.Response
	if uc.ConnectionState().NegotiatedProtocol == "h2" {
		var cc *http2.ClientConn
		if cc, err = (&http2.Transport{}).NewClientConn(uc); err == nil {
			defer cc.Close()
			res, err = cc.RoundTrip(req)
		}
	} else if err = req.Write(uc); err == nil {
		res, err = http.ReadResponse(bufio.NewReader(uc), req)
	}
	if err != nil {
		if r := reasonOf(err); r == "нет ответа" {
			return 0, "загрузка встала после 0 КБ"
		}
		return 0, "" // отказ сервера на запрос — не признак
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return 0, ""
	}
	// при тишине readBodyN закрывает тело. У HTTP/1.1 закрытие тела ждёт висящее чтение —
	// поэтому закрывается само соединение, и чтение сразу возвращается
	return readBodyN(connBody{countBody{res.Body, pr}, uc}, probeRead, probeIdle)
}

type connBody struct {
	io.ReadCloser
	conn net.Conn
}

func (b connBody) Close() error {
	b.conn.Close()
	return b.ReadCloser.Close()
}

// vpnGet: тот же файл через VPN
func vpnGet(ctx context.Context, u *url.URL, proxy *url.URL) (int64, string) {
	tr := &http.Transport{Proxy: http.ProxyURL(proxy), TLSHandshakeTimeout: 6 * time.Second, DisableKeepAlives: true}
	defer tr.CloseIdleConnections()
	req, _ := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36")
	res, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return 0, reasonOf(err) + " "
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return 0, "ответ " + res.Status
	}
	return readBodyN(res.Body, probeRead, probeIdle)
}

func stalledReason(r string) bool {
	return strings.HasPrefix(r, "загрузка встала") || strings.HasPrefix(r, "обрыв после")
}

// assetQuiet: тишины напрямую хватает, если через VPN тот же файл уже пришёл целиком. Провайдер
// обрывает намертво (после ~14-25 КБ ни байта), а через VPN картинка приходит за доли секунды —
// ждать полные probeIdle (4 с) незачем
const assetQuiet = 1500 * time.Millisecond

// assetProbe: файлы с главной одновременно напрямую и через VPN. "" — обрыва нет (или проверить нечем)
func assetProbe(domain string, html []byte) string {
	urls := assetURLs(domain, html)
	if len(urls) == 0 {
		return ""
	}
	var proxy *url.URL
	if app != nil {
		proxy = app.probeProxy()
	}
	return assetProbeVia(urls, proxy)
}

func assetProbeVia(urls []*url.URL, proxy *url.URL) string {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	res := make(chan string, len(urls))
	for _, u := range urls {
		go func() {
			defer recoverLog("проверке файла")
			res <- assetOne(ctx, u, proxy)
		}()
	}
	for range urls {
		if r := <-res; r != "" {
			return r // cancel обрывает остальные проверки
		}
	}
	return ""
}

// assetOne: один файл напрямую и через VPN
func assetOne(ctx context.Context, u *url.URL, proxy *url.URL) string {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type got struct {
		n int64
		r string
	}
	var pr progress
	pr.last.Store(time.Now().UnixNano())
	dc, vc := make(chan got, 1), make(chan got, 1)
	go func() {
		defer recoverLog("проверке файла напрямую")
		n, r := chromeGet(ctx, u, &pr)
		dc <- got{n, r}
	}()
	if proxy != nil {
		go func() {
			defer recoverLog("проверке файла через VPN")
			n, r := assetVPN(ctx, u, proxy)
			vc <- got{n, r}
		}()
	}
	debug := func(f string, a ...any) {
		if probeDebug != nil {
			probeDebug(u.Path + ": " + fmt.Sprintf(f, a...))
		}
	}
	var v *got
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case d := <-dc:
			if ctx.Err() != nil {
				return "" // проверка кончилась по времени: не признак
			}
			debug("напрямую %d Б %q", d.n, d.r)
			if !stalledReason(d.r) {
				return ""
			}
			if proxy == nil { // без VPN достаточно обрыва напрямую: сайт начал отдавать файл и встал
				return "файл " + u.Path + ": " + d.r
			}
			if v == nil { // дождаться VPN: открылся ли файл там
				select {
				case x := <-vc:
					v = &x
				case <-ctx.Done():
					return ""
				}
			}
			debug("через VPN %d Б %q", v.n, v.r)
			if v.r == "" && v.n > d.n {
				return "файл " + u.Path + ": " + d.r
			}
			return ""
		case x := <-vc:
			v = &x
			debug("через VPN %d Б %q", v.n, v.r)
			vc = nil
		case <-tick.C:
			// через VPN файл уже целиком, напрямую начал отдавать и затих: решение без полной тишины
			if v == nil || v.r != "" {
				continue
			}
			n := pr.n.Load()
			if n > 0 && n < v.n && time.Since(time.Unix(0, pr.last.Load())) >= assetQuiet {
				debug("напрямую встало на %d Б, через VPN %d Б", n, v.n)
				return fmt.Sprintf("файл %s: загрузка встала после %d КБ", u.Path, n>>10)
			}
		case <-ctx.Done():
			return ""
		}
	}
}

var probeDebug func(string) // тесты: что показала проверка файлов

var assetVPN = vpnGet // тесты подменяют

// прямое соединение в обход VPN; тесты подменяют на свой сервер
var assetDial = func(ctx context.Context, host string) (net.Conn, error) {
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, "443")
	}
	return probeDialer(nil).DialContext(ctx, "tcp", host)
}

var assetInsecure bool // тесты: свой сертификат
