package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/xtls/xray-core/core"
	jsonconf "github.com/xtls/xray-core/infra/conf/serial"
)

// MONOLIT_FRAG=1: сравнить прямой выход в обход TUN (через Ethernet) без дробления и с дроблением ClientHello
func TestFragDiag(t *testing.T) {
	if os.Getenv("MONOLIT_FRAG") == "" {
		t.Skip()
	}
	run := func(port int, frag map[string]any) {
		set := map[string]any{"domainStrategy": "UseIPv4"}
		if frag != nil {
			set["fragment"] = frag
		}
		cfg := fmt.Sprintf(`{"log":{"loglevel":"none"},"dns":{"servers":["https+local://77.88.8.8/dns-query"]},
"inbounds":[{"protocol":"mixed","listen":"127.0.0.1","port":%d}],
"outbounds":[{"protocol":"freedom","settings":%s,"streamSettings":{"sockopt":{"interface":"Ethernet"}}}]}`, port, mustJSON(set))
		pb, err := jsonconf.LoadJSONConfig(bytes.NewReader([]byte(cfg)))
		if err != nil {
			t.Fatal(err)
		}
		inst, err := core.New(pb)
		if err != nil {
			t.Fatal(err)
		}
		inst.Start()
		defer inst.Close()
		px, _ := url.Parse(fmt.Sprintf("socks5h://127.0.0.1:%d", port))
		cl := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(px)}}
		for _, u := range []string{"https://www.youtube.com/", "https://i.ytimg.com/vi/dQw4w9WgXcQ/hqdefault.jpg", "https://redirector.googlevideo.com/report_mapping", "https://yt3.ggpht.com/", "https://www.instagram.com/", "https://discord.com/"} {
			st := time.Now()
			res, err := cl.Get(u)
			if err != nil {
				t.Logf("frag=%v %s: ошибка", frag, u)
				continue
			}
			n, _ := io.Copy(io.Discard, res.Body)
			res.Body.Close()
			_ = n
			t.Logf("frag=%v %s: %d за %v", frag, u, res.StatusCode, time.Since(st).Round(time.Millisecond))
		}
	}
	for i := 0; i < 3; i++ {
		run(19090+i, map[string]any{"packets": "tlshello", "length": "10-30", "interval": "0"})
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
