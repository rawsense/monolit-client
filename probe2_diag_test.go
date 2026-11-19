package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/core"
)

// MONOLIT_PROBE2=1: почему второй запрос по одному соединению не проходит у Hysteria2 и XHTTP
func TestZZProbe2(t *testing.T) {
	if os.Getenv("MONOLIT_PROBE2") == "" {
		t.Skip()
	}
	app = newApp(true)
	app.push = func(string) {}
	app.Connect()
	time.Sleep(2 * time.Second)
	inst := app.core.inst
	for i := 0; i < 3; i++ {
		res := app.core.Check(tagsOf(app.sub))
		for _, c := range app.sub.Conns {
			p := res[c.Tag]
			t.Logf("Check #%d %s: alive=%v delay=%d err=%s", i, c.Name, p.Alive, p.Delay, p.Err)
		}
	}
	for _, c := range app.sub.Conns {
		tag := c.Tag
		tr := &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				dest, _ := xnet.ParseDestination(network + ":" + addr)
				ctx = session.ContextWithContent(ctx, &session.Content{SkipDNSResolve: true})
				ctx = session.SetForcedOutboundTagToContext(ctx, tag)
				t.Logf("%s: dial %s", c.Name, addr)
				return core.Dial(ctx, inst, dest)
			},
			MaxIdleConnsPerHost: 1,
		}
		cl := &http.Client{Transport: tr, Timeout: 10 * time.Second}
		for i := 0; i < 3; i++ {
			st := time.Now()
			resp, err := cl.Get(probeURL)
			if err != nil {
				t.Logf("%s #%d: %v", c.Name, i, err)
				continue
			}
			n, _ := io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			t.Logf("%s #%d: %s, %d байт, %s, proto=%s close=%v", c.Name, i, resp.Status, n, time.Since(st).Round(time.Millisecond), resp.Proto, resp.Close)
		}
		tr.CloseIdleConnections()
	}
	app.Disconnect()
}
