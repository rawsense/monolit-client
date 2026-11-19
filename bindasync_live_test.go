package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	webview2 "github.com/jchv/go-webview2"
)

// живая проверка bindAsync в настоящем WebView2: Promise фонового вызова без аргументов должен закрыться.
// MONOLIT_WV=1 go test -count=1 -run TestBindAsyncLive -v
func TestBindAsyncLive(t *testing.T) {
	if os.Getenv("MONOLIT_WV") == "" {
		t.Skip("только вручную")
	}
	runtime.LockOSThread()
	w := webview2.NewWithOptions(webview2.WebViewOptions{DataPath: filepath.Join(os.TempDir(), "monolit-wv-test"),
		WindowOptions: webview2.WindowOptions{Title: "test", Width: 300, Height: 200}})
	if w == nil {
		t.Fatal("нет WebView2")
	}
	defer w.Destroy()
	var got string
	var steps []string
	if err := bindAsync(w, "mpX", safeBinding("mpX", func() error { time.Sleep(300 * time.Millisecond); return nil })); err != nil {
		t.Fatal(err)
	}
	w.Bind("report", func(s string) { got = s; w.Dispatch(w.Terminate) })
	w.Bind("step", func(s string) { steps = append(steps, s) })
	time.AfterFunc(15*time.Second, func() { w.Dispatch(w.Terminate) })
	w.SetHtml(`<script>setTimeout(() => { step(typeof mpX + ' ' + typeof __mpX + ' ' + JSON.stringify(Object.keys(window.__mpRpc||{}))); window.onerror = (m) => step('err ' + m); }, 100); setTimeout(() => mpX().then(() => report('ok'), (e) => report('err ' + e)), 200)</script>`)
	w.Run()
	t.Logf("шаги: %q", steps)
	if got != "ok" {
		t.Fatalf("Promise не закрылся: %q", got)
	}
}
