package core

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestAssetURLs(t *testing.T) {
	html := `<link href="/css/a.css"><img src="https://x.com/i/b.png"><img src="//cdn.other.com/c.png">
	<script src="s.js"></script><img srcset="/i/d.webp 2x"><img src="data:image/png;base64,AA"><a href="/about">
	<img src="/i/b.png"><img data-src="/i/e.jpg">`
	var got []string
	for _, u := range assetURLs("x.com", []byte(html)) {
		got = append(got, u.Path)
	}
	if strings.Join(got, " ") != "/i/b.png /i/d.webp /i/e.jpg" {
		t.Fatalf("%v", got)
	}
}

// сервер отдаёт картинку целиком или начинает и встаёт посреди (как обрыв у провайдера)
func TestAssetProbe(t *testing.T) {
	stall := make(chan struct{})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok.png":
			w.Write(make([]byte, 60<<10))
		case "/cut.png":
			w.Header().Set("Content-Length", "61440")
			w.Write(make([]byte, 20<<10))
			w.(http.Flusher).Flush()
			select {
			case <-stall:
			case <-r.Context().Done():
			}
		}
	}))
	defer func() { close(stall); srv.Close() }() // сначала отпустить висящий ответ: Close ждёт обработчики
	addr := strings.TrimPrefix(srv.URL, "https://")
	oldDial, oldIns, oldApp := assetDial, assetInsecure, app
	defer func() { assetDial, assetInsecure, app = oldDial, oldIns, oldApp }()
	assetDial = func(ctx context.Context, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	}
	assetInsecure, app = true, nil
	if r := assetProbe("x.test", []byte(`<img src="/ok.png">`)); r != "" {
		t.Fatalf("целый файл: %q", r)
	}
	t0 := time.Now()
	r := assetProbe("x.test", []byte(`<img src="/ok.png"><img src="/cut.png">`))
	if !strings.Contains(r, "/cut.png") || !strings.Contains(r, "встала после 20 КБ") {
		t.Fatalf("обрыв не найден: %q", r)
	}
	if d := time.Since(t0); d > 8*time.Second {
		t.Fatalf("долго: %v", d)
	}
}

// через VPN файл целиком, напрямую встал: решение по тишине assetQuiet, не по полной (probeIdle)
func TestAssetQuiet(t *testing.T) {
	stall := make(chan struct{})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "61440")
		w.Write(make([]byte, 20<<10))
		w.(http.Flusher).Flush()
		select {
		case <-stall:
		case <-r.Context().Done():
		}
	}))
	defer func() { close(stall); srv.Close() }()
	addr := strings.TrimPrefix(srv.URL, "https://")
	oldDial, oldIns, oldVPN := assetDial, assetInsecure, assetVPN
	defer func() { assetDial, assetInsecure, assetVPN = oldDial, oldIns, oldVPN }()
	assetDial = func(ctx context.Context, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	}
	assetInsecure = true
	assetVPN = func(context.Context, *url.URL, *url.URL) (int64, string) { return 61440, "" }
	t0 := time.Now()
	r := assetProbeVia(assetURLs("x.test", []byte(`<img src="/cut.png">`)), &url.URL{Scheme: "socks5", Host: "127.0.0.1:1"})
	d := time.Since(t0)
	if !strings.Contains(r, "встала после 20 КБ") {
		t.Fatalf("обрыв не найден: %q", r)
	}
	if d > 3*time.Second {
		t.Fatalf("долго: %v", d)
	}
	t.Logf("%q за %v", r, d.Round(100*time.Millisecond))
}
