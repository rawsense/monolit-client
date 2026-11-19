package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// живая проверка прямого пути (рукопожатие и страница как у Chrome):
// MONOLIT_LIVE=mailflare.co,ya.ru go test -count=1 -run TestChromeLive -v
func TestChromeLive(t *testing.T) {
	list := os.Getenv("MONOLIT_LIVE")
	if list == "" {
		t.Skip("только вручную")
	}
	for _, d := range strings.Split(list, ",") {
		t0 := time.Now()
		t.Logf("%s: %q за %v", d, chromeHandshake(d), time.Since(t0).Round(time.Millisecond))
	}
}
