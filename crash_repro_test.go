package main

import (
	"os"
	"testing"
	"time"
)

// MONOLIT_REPRO=1: тестовый клиент (прокси на 10818, система не меняется) — смена режима и обновление подписки
func TestCrashRepro(t *testing.T) {
	if os.Getenv("MONOLIT_REPRO") == "" {
		t.Skip()
	}
	app = newApp(true)
	app.push = func(string) {}
	hookIntercept()
	go app.loop()
	app.Connect()
	time.Sleep(3 * time.Second)
	v := app.view()
	t.Logf("on=%v via=%s err=%q", v.On, v.Via, v.Err)
	for _, m := range []string{"all", "ai", "smart"} {
		app.mu.Lock()
		app.set.Mode = m
		app.mu.Unlock()
		app.apply(false)
		t.Logf("режим %s: ок", m)
		time.Sleep(time.Second)
	}
	app.refreshSub()
	app.recheck()
	t.Logf("обновление подписки: ок, err=%q", app.view().RefreshErr)
	app.apply(true)
	t.Logf("перезапуск: ок")
	time.Sleep(2 * time.Second)
	app.Disconnect()
}
