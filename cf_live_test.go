package main

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"os"
	"testing"
	"time"
)

// MONOLIT_CF=1: скачать напрямую (в обход TUN) большие ответы с адресов Cloudflare — режет ли провайдер после ~16 КБ,
// и по чему режет: по адресу или по имени сайта
func TestCFLive(t *testing.T) {
	if os.Getenv("MONOLIT_CF") == "" {
		t.Skip()
	}
	old := app
	defer func() { app = old }()
	app = newApp(true)
	app.on, app.via = true, "tun"
	get := func(name, u, ip string) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		d := probeDialer(nil)
		tr := &http.Transport{TLSClientConfig: &tls.Config{}, ForceAttemptHTTP2: true,
			DialContext: func(ctx context.Context, nw, addr string) (net.Conn, error) {
				if ip != "" {
					addr = net.JoinHostPort(ip, "443")
				}
				return d.DialContext(ctx, nw, addr)
			}}
		req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
		req.Header.Set("User-Agent", "Mozilla/5.0 Chrome/140.0")
		t0 := time.Now()
		res, err := (&http.Client{Transport: tr}).Do(req)
		if err != nil {
			t.Logf("%s: %v", name, err)
			return
		}
		r := readBody(res.Body, 300<<10, 4*time.Second)
		res.Body.Close()
		if r == "" {
			r = "скачалось"
		}
		t.Logf("%s: %d, %s, %s", name, res.StatusCode, r, time.Since(t0).Round(time.Millisecond))
	}
	img := "https://vinfo-api.com/media/WeaponSkins/092A25A4-422F-F577-37AC-26A5D489C155.png"
	big := "https://speed.cloudflare.com/__down?bytes=300000"
	get("картинка vinfo", img, "")
	get("speed.cloudflare.com на своём адресе", big, "")
	get("speed.cloudflare.com на адресе vinfo 104.21.46.95", big, "104.21.46.95")
	get("speed.cloudflare.com на адресе vinfo 172.67.137.85", big, "172.67.137.85")
	if ips, err := net.LookupIP("speed.cloudflare.com"); err == nil {
		for _, ip := range ips {
			if ip.To4() != nil {
				get("картинка vinfo на адресе speed "+ip.String(), img, ip.String())
				break
			}
		}
	}
}

type recConn struct {
	net.Conn
	r *tlsRecs
}

func (c recConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	c.r.feed(b[:n])
	return n, err
}

// MONOLIT_CF=1: при настоящем обрыве у сайта на Cloudflare поток встаёт посреди записи TLS
func TestMidLive(t *testing.T) {
	if os.Getenv("MONOLIT_CF") == "" {
		t.Skip()
	}
	old := app
	defer func() { app = old }()
	app = newApp(true)
	app.on, app.via = true, "tun"
	for _, u := range []string{"https://vinfo-api.com/media/WeaponSkins/092A25A4-422F-F577-37AC-26A5D489C155.png", "https://speed.cloudflare.com/__down?bytes=300000"} {
		var r tlsRecs
		d := probeDialer(nil)
		tr := &http.Transport{TLSClientConfig: &tls.Config{}, ForceAttemptHTTP2: true,
			DialContext: func(ctx context.Context, nw, addr string) (net.Conn, error) {
				c, err := d.DialContext(ctx, nw, addr)
				if err != nil {
					return nil, err
				}
				return recConn{c, &r}, nil
			}}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
		res, err := (&http.Client{Transport: tr}).Do(req)
		if err != nil {
			t.Logf("%s: %v", u, err)
			cancel()
			continue
		}
		why := readBody(res.Body, 300<<10, 4*time.Second)
		res.Body.Close()
		cancel()
		t.Logf("%s: %q, посреди записи TLS: %v, не TLS: %v", u, why, r.mid.Load(), r.broken)
	}
}

// MONOLIT_CF=1: проверка сайта вместе с файлами главной — режущие провайдером сайты ловятся, обычные нет
func TestAssetLive(t *testing.T) {
	if os.Getenv("MONOLIT_CF") == "" {
		t.Skip()
	}
	old := app
	defer func() { app = old; probeDebug = nil }()
	app = newApp(true)
	app.on, app.via = true, "tun"
	probeDebug = func(s string) { t.Log("  " + s) }
	for _, d := range []string{"vinfo-api.com", "mailflare.co", "github.com", "example.com", "habr.com", "www.wikipedia.org"} {
		t0 := time.Now()
		r := probe(d, nil)
		t.Logf("%s: %q за %.1f с", d, r, time.Since(t0).Seconds())
	}
}
