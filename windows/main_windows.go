// monolit client: VPN-клиент под Windows на ядре Xray. Окно WebView2, интерфейс встроен в exe
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"sync"
	"time"

	"monolit-client/core"

	"github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

func main() {
	test := flag.Bool("test", false, "тестовый запуск: порт 10818, система не меняется")
	doInstall := flag.Bool("install", false, "установка (с правами администратора)")
	fromTask := flag.Bool("from-task", false, "запуск задачей при входе в Windows")
	launch := flag.Bool("launch", false, "запуск ярлыком: через задачу, без UAC")
	headless := flag.Bool("headless", false, "без окна: подключиться и ждать (для проверки)")
	tunTest := flag.Int("tun-test", 0, "проверка TUN: подключиться на N секунд и выйти (с правами администратора)")
	wait := flag.Int("wait-pid", 0, "дождаться выхода старой копии (после обновления)")
	doUninstall := flag.Bool("uninstall", false, "удаление программы (её запускает Windows из «Приложений»)")
	quit := flag.Bool("quit", false, "попросить работающую копию выйти, как «Выход» в трее")
	flag.Parse()

	if *quit { // код 0 — просьба передана, 1 — программа не запущена или старой версии
		if sendQuit() {
			os.Exit(0)
		}
		os.Exit(1)
	}

	// авария (panic в любом потоке) — в crash.log рядом с настройками: окно без консоли, иначе причина пропадает
	crashPath := filepath.Join(appDir(), "crash.log")
	if st, err := os.Stat(crashPath); err == nil && st.Size() > 2<<20 { // больше 2 МБ — в crash.log.1, начать заново
		os.Rename(crashPath, crashPath+".1")
	}
	if f, err := os.OpenFile(crashPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		core.SetCrashFile(f)
		fmt.Fprintln(f, "=== запуск", time.Now().Format("2006-01-02 15:04:05"), "===")
		debug.SetCrashOutput(f, debug.CrashOptions{})
		// туда же всё, что пишется в stderr (log.Fatal, сообщения библиотек): иначе выход без аварии не объяснить
		windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(f.Fd()))
		os.Stderr = f
		log.SetOutput(f)
	}

	if *wait > 0 {
		waitPid(*wait)
	}
	if *doUninstall {
		if err := uninstall(); err != nil {
			logLine("удаление: " + err.Error())
			fatalBox(err.Error())
			os.Exit(1)
		}
		return
	}
	if *doInstall {
		if err := install(); err != nil {
			logLine("установка: " + err.Error())
			os.Exit(1)
		}
		showOnce()
		runTask()
		return
	}
	if *launch && taskExists() && !isAdmin() {
		showOnce()
		if runTask() == nil {
			return
		}
	}
	// новая версия запущена из старой папки (%LOCALAPPDATA%\Programs\monolit): перенести в Program Files.
	// Без прав — сначала через задачу (она с правами, UAC нет), иначе один запрос UAC. Отказ — работаем отсюда
	if !*test && !*headless && *tunTest == 0 && inLegacyDir() {
		switch {
		case isAdmin():
			if migrateFromLegacy(*fromTask) {
				return
			}
		case *fromTask: // задача сама запустила без прав (учётная запись не администратор): не зацикливаемся
			logLine("перенос в Program Files: нет прав администратора, работаю из " + exeDir())
		case taskExists():
			showOnce()
			if runTask() == nil {
				return
			}
		default:
			if runElevated("") == nil {
				return
			}
		}
	}
	// первый запуск скачанного exe: установить один раз с правами администратора, дальше без UAC
	if !*test && !*headless && *tunTest == 0 && !installed() && !taskExists() && !isAdmin() {
		if runElevated("--install") == nil {
			return
		}
	}
	// запись в «Приложениях» Windows: у копий, поставленных до появления удаления, её ещё нет
	if !*test && installed() && isAdmin() {
		if err := registerUninstall(filepath.Join(installDir(), "monolit.exe")); err != nil {
			logLine("запись в «Приложениях»: " + err.Error())
		}
	}
	if !*test && *tunTest == 0 && !singleInstance() {
		return
	}

	if *tunTest > 0 {
		app = core.New(true)
		app.ForceTun()
		app.SetPush(func(string) {})
		safeGo("loop", app.Loop)
		app.Connect()
		v := app.View()
		logLine(fmt.Sprintf("проверка TUN: on=%v via=%s err=%q", v.On, v.Via, v.Err))
		time.Sleep(time.Duration(*tunTest) * time.Second)
		app.Disconnect()
		logLine("проверка TUN: отключено")
		return
	}
	app = core.New(*test)
	if *test {
		backupPath = filepath.Join(appDir(), "test", "system-backup.json")
	}
	// копия системного прокси осталась — прошлый запуск не отключился (сбой, снятие задачи, питание):
	// вернуть прокси как было, иначе без запущенного ядра интернет не работает
	if !*test {
		if _, err := os.Stat(backupPath); err == nil {
			logLine("прошлый запуск не выключил системный прокси: возвращаю как было")
			sysProxyOff()
		}
	}
	core.HookIntercept()
	if *headless {
		app.SetPush(func(string) {})
		safeGo("loop", app.Loop)
		app.Connect()
		v := app.View()
		log.Printf("on=%v via=%s err=%q conns=%d", v.On, v.Via, v.Err, len(v.Conns))
		select {}
	}

	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     os.Getenv("MONOLIT_DEBUG") != "",
		AutoFocus: true,
		DataPath:  filepath.Join(dataDir(), "webview"),
		WindowOptions: webview2.WindowOptions{
			Title:  "Monolit",
			Width:  1040,
			Height: 680,
			Center: true,
		},
	})
	if w == nil {
		// на Windows 10 среды WebView2 может не быть: без сообщения программа закрылась бы молча
		logLine("окно: WebView2 не запустился")
		fatalBox("Не получилось открыть окно: на компьютере нет Microsoft Edge WebView2.\n\nУстановите WebView2 Runtime с сайта Microsoft (developer.microsoft.com/microsoft-edge/webview2) и запустите Monolit снова.")
		os.Exit(1)
	}
	defer w.Destroy()
	hwnd := uintptr(w.Window())
	darkTitle(hwnd)
	setWindowIcon(hwnd)
	hideOnClose(hwnd)
	w.SetSize(880, 600, webview2.HintMin)
	for name, fn := range app.Bindings() {
		fn = core.SafeBinding(name, fn)
		var err error
		if core.AsyncBinding(name) {
			err = bindAsync(w, name, fn)
		} else {
			err = w.Bind(name, fn)
		}
		if err != nil {
			log.Fatal(name, err)
		}
	}
	var (
		pushMu  sync.Mutex
		last    time.Time
		pending bool // отложенное обновление уже назначено
	)
	app.SetPush(func(js string) {
		trayState(app.View().On, "")
		pushMu.Lock()
		if wait := 5*time.Second - time.Since(last); !windowVisible() && wait > 0 {
			// окно скрыто — реже обновляем, но последнее состояние всё равно дойдёт по истечении паузы
			if !pending {
				pending = true
				time.AfterFunc(wait, func() {
					pushMu.Lock()
					pending = false
					pushMu.Unlock()
					safeRun("notify", app.Notify)
				})
			}
			pushMu.Unlock()
			return
		}
		last = time.Now()
		pushMu.Unlock()
		w.Dispatch(func() { w.Eval("window.mp && mp.update(" + js + ")") })
	})
	safeGo("loop", app.Loop) // после app.push: цикл сразу шлёт состояние
	exit := func(why string) {
		logLine("выход: " + why)
		app.Disconnect()
		quitting.Store(true)
		w.Dispatch(w.Terminate)
	}
	startTray(func() { exit("«Выход» в трее") })
	if !*test {
		quitWatch(func() { exit("команда --quit") })
	}
	w.Navigate(core.ServeUI())
	// задачей запускают и вход в Windows (тогда сразу в трей), и установка с ярлыком (тогда окно нужно).
	// Различаем по отметке, которую ставят установка и ярлык перед запуском задачи
	if *fromTask && !takeShowOnce() {
		showWindow.Call(hwnd, 0)
	}
	if app.AutoOn() {
		safeGo("connect", app.Connect)
	}
	w.Run()
	logLine(fmt.Sprintf("выход: окно закрылось (quitting=%v)", quitting.Load()))
	app.Disconnect()
}

// отметка «показать окно при следующем запуске задачей»: задача одна, а запускают её по разным причинам
func showOncePath() string { return filepath.Join(appDir(), "show-once") }

func showOnce() { os.WriteFile(showOncePath(), nil, 0o644) }

func takeShowOnce() bool {
	st, err := os.Stat(showOncePath())
	if err != nil {
		return false
	}
	os.Remove(showOncePath())
	return time.Since(st.ModTime()) < time.Minute // старая отметка (запуск задачи не случился) не в счёт
}

// bindAsync — привязка, которая не держит поток окна: go-webview2 ждёт ответ привязки прямо в нём.
// В окно уходит «__имя» с номером своего Promise, работа идёт в фоне, итог — resolve/reject этого Promise.
// Для страницы вызов прежний: window.имя(...) возвращает Promise. fn — уже из safeBinding (последним error)
func bindAsync(w webview2.WebView, name string, fn any) error {
	v := reflect.ValueOf(fn)
	t := v.Type()
	// tok — метка документа: после перезагрузки страницы номера Promise начинаются заново, и запоздалый
	// ответ старой страницы не должен закрыть новый вызов с тем же номером
	done := func(tok string, seq int, res any, err error) {
		id := fmt.Sprint(seq)
		var js string
		if b, e := json.Marshal(res); err == nil && e == nil {
			js = "window.__mpRpc[" + id + "].resolve(" + string(b) + ")"
		} else {
			if err == nil {
				err = e
			}
			msg, _ := json.Marshal(err.Error())
			js = "window.__mpRpc[" + id + "].reject(" + string(msg) + ")"
		}
		js = "if (window.__mpRpc && window.__mpRpc.tok === " + jsonStr(tok) + " && window.__mpRpc[" + id + "]) { " + js + "; window.__mpRpc[" + id + "] = undefined }"
		w.Dispatch(func() { w.Eval(js) })
	}
	err := w.Bind("__"+name, core.SafeBinding(name, func(tok string, seq int, raw ...json.RawMessage) {
		if len(raw) != t.NumIn() {
			done(tok, seq, nil, fmt.Errorf("function arguments mismatch"))
			return
		}
		args := make([]reflect.Value, len(raw))
		for i := range raw {
			p := reflect.New(t.In(i))
			if err := json.Unmarshal(raw[i], p.Interface()); err != nil {
				done(tok, seq, nil, err)
				return
			}
			args[i] = p.Elem()
		}
		safeGo(name, func() {
			out := v.Call(args)
			var res any
			err, _ := out[len(out)-1].Interface().(error)
			if len(out) == 2 {
				res = out[0].Interface()
			}
			done(tok, seq, res, err)
		})
	}))
	if err != nil {
		return err
	}
	// метка своя у каждого документа: скрипт Init выполняется заново при каждой загрузке страницы.
	// Имя своё, не _rpc: _rpc занят самим go-webview2 (там нет tok, и ответы молча отбрасывались — кнопка крутилась вечно)
	w.Init(`(function() { var name = ` + jsonStr(name) + `, inner = ` + jsonStr("__"+name) + `;
		var RPC = window.__mpRpc = (window.__mpRpc || {nextSeq: 1, tok: Date.now().toString(36) + Math.random().toString(36).slice(2)});
		window[name] = function() {
			var seq = RPC.nextSeq++;
			var promise = new Promise(function(resolve, reject) { RPC[seq] = {resolve: resolve, reject: reject}; });
			window[inner].apply(null, [RPC.tok, seq].concat(Array.prototype.slice.call(arguments)));
			return promise;
		};
	})()`)
	return nil
}

func jsonStr(s string) string { b, _ := json.Marshal(s); return string(b) }
