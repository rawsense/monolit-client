package core

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// Всё, что видно оболочке системы (windows, android). Остальное в пакете — для самой логики

const (
	Version = version
	// TunName — имя виртуальной карты TUN
	TunName = tunName
)

// New создаёт программу: настройки и подписки с диска. test — тестовый запуск: свои файлы и порт, систему не трогаем
func New(test bool) *App {
	app = newApp(test)
	return app
}

func AppDir() string { return appDir() }

// LoadSettings читает настройки с диска (установке нужны до создания программы)
func LoadSettings() Settings { return loadSettings() }

func Log(s string)              { logLine(s) }
func Go(name string, f func())  { safeGo(name, f) }
func Run(name string, f func()) { safeRun(name, f) }

// HookIntercept включает гонку путей умного режима в ядре
func HookIntercept() { hookIntercept() }

// SafeBinding оборачивает вызов из интерфейса: авария в нём — ошибка, а не падение программы
func SafeBinding(name string, fn any) any { return safeBinding(name, fn) }

// AsyncBinding: вызов с долгой работой (сеть) — оболочка выполняет его в фоне
func AsyncBinding(name string) bool { return asyncBindings[name] }

func (a *App) Bindings() map[string]any { return a.bindings() }
func (a *App) View() View               { return a.view() }
func (a *App) Notify()                  { a.notify() }
func (a *App) Loop()                    { a.loop() }

// SetPush: куда отдавать состояние для интерфейса (JSON). Задать до Loop
func (a *App) SetPush(f func(string)) { a.push = f }

// AutoOn: подключаться при запуске
func (a *App) AutoOn() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.set.AutoOn && len(a.set.Subs) > 0
}

// ForceTun: проверка TUN — тестовые файлы и порт, но с настоящим TUN
func (a *App) ForceTun() {
	a.tunTest = true
	a.set.Connect = "tun"
}

var (
	crashMu   sync.Mutex
	crashFile *os.File
)

// SetCrashFile: файл, куда пишутся перехваченные аварии (необработанные туда пишет сам рантайм)
func SetCrashFile(f *os.File) {
	crashMu.Lock()
	crashFile = f
	crashMu.Unlock()
}

func crashNote(msg string) {
	crashMu.Lock()
	defer crashMu.Unlock()
	if crashFile != nil {
		fmt.Fprintln(crashFile, time.Now().Format("2006-01-02 15:04:05"), msg)
	}
}
