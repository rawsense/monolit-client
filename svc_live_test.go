package main

import (
	"os"
	"testing"
)

// MONOLIT_SVC=1: проверить YouTube и Discord напрямую, как умный режим (в обход TUN — через сетевую карту)
func TestSvcLive(t *testing.T) {
	if os.Getenv("MONOLIT_SVC") == "" {
		t.Skip()
	}
	old := app
	defer func() { app = old }()
	app = newApp(true)
	app.on, app.via = true, "tun" // probeDialer привяжет проверку к сетевой карте
	for i := range services {
		sv := &services[i]
		for _, h := range sv.Probe {
			r := probe(h, nil)
			if r == "" {
				r = "открылся"
			}
			t.Logf("%s %s: %s", sv.Name, h, r)
		}
	}
}
