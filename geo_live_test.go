package main

import (
	"context"
	"net/url"
	"os"
	"testing"
	"time"
)

// живой замер обновления баз в отдельную папку:
// MONOLIT_GEO=socks5h://127.0.0.1:10815 go test -count=1 -run TestGeoLive -v
func TestGeoLive(t *testing.T) {
	p := os.Getenv("MONOLIT_GEO")
	if p == "" {
		t.Skip("только вручную")
	}
	t.Setenv("APPDATA", t.TempDir())
	var proxy *url.URL
	if p != "direct" {
		proxy, _ = url.Parse(p)
	}
	cl := downloadClient(proxy)
	ctx := context.Background()
	t0 := time.Now()
	base := geoTagBase(ctx, cl)
	t.Logf("тег: %q за %v", base, time.Since(t0).Round(time.Millisecond))
	for _, f := range geoFiles {
		t1 := time.Now()
		_, err := fetchSum(ctx, cl, base+f+".sha256sum")
		t.Logf("%s сумма: %v за %v", f, err, time.Since(t1).Round(time.Millisecond))
		t1 = time.Now()
		err = geoFetch(ctx, cl, base, f)
		t.Logf("%s целиком: %v за %v", f, err, time.Since(t1).Round(time.Millisecond))
	}
	t.Logf("всего %v", time.Since(t0).Round(time.Millisecond))
	for i := 0; i < 2; i++ { // первый — обе базы сразу, второй — «уже свежие»
		t1 := time.Now()
		ch, err := updateGeo(ctx, cl)
		t.Logf("updateGeo: изменились=%v ошибка=%v за %v", ch, err, time.Since(t1).Round(time.Millisecond))
	}
}
