//go:build android

// Package mobile — оболочка Android для общей логики (пакет core). Собирается gomobile в библиотеку,
// которую вызывает приложение: сервис VPN, окно с интерфейсом, уведомление. Здесь только то, чем Android
// отличается; подписки, конфиг ядра, маршруты и умный режим — в core, те же, что в Windows
package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"
	"syscall"

	"monolit-client/core"

	"github.com/xtls/xray-core/common/platform"
	"github.com/xtls/xray-core/transport/internet"
)

// Host — что приложение даёт библиотеке. Реализуется на стороне Android
type Host interface {
	// Protect выводит сокет из VPN (VpnService.protect): соединения ядра к серверу и проверки «напрямую»
	// иначе возвращались бы в туннель
	Protect(fd int32) bool
	// OpenTun поднимает VPN системы с этими адресами и отдаёт дескриптор (отсоединённый: дальше им владеет
	// библиотека) или -1, если пользователь не дал разрешение или сервис не запустился
	OpenTun(mtu int32, addr4 string, prefix4 int32, dns4 string, addr6 string, prefix6 int32) int32
	// CloseTun: VPN снят, сервису можно завершаться
	CloseTun()
	// Push: новое состояние для окна (JSON), то же, что получает окно Windows
	Push(state string)
	OSVersion() string
	Device() string
}

// адреса TUN те же, что в Windows: DNS на 172.19.0.2 ядро перехватывает правилом, IPv6 заводится в туннель
// и там отклоняется, чтобы программы шли по IPv4
const (
	tunMTU     = 1500
	tunAddr4   = "172.19.0.1"
	tunPrefix4 = 30
	tunDNS4    = "172.19.0.2"
	tunAddr6   = "fdfe:dcba:9876::1"
	tunPrefix6 = 126
)

var (
	mu   sync.Mutex
	host Host
	app  *core.App
	ui   string
)

// Start готовит программу: папка данных приложения, настройки и подписки, раздача интерфейса.
// Возвращает адрес интерфейса для окна. Повторный вызов (окно открыли заново) отдаёт тот же адрес
func Start(dataDir string, h Host) string {
	mu.Lock()
	defer mu.Unlock()
	host = h
	if app != nil {
		return ui
	}
	core.SetDataDir(dataDir)
	core.SetPlatform(droid{})
	// журнал программы — ещё и в журнал Android (adb logcat -s GoLog): файл в закрытой папке приложения снаружи не прочитать
	core.SetLogSink(func(s string) { fmt.Fprintln(os.Stderr, s) })
	protect := func(network, address string, c syscall.RawConn) error {
		return c.Control(func(fd uintptr) {
			if !h.Protect(int32(fd)) {
				core.Log("сокет к " + address + " не выведен из VPN")
			}
		})
	}
	os.Setenv("MONOLIT_LOGLEVEL", "debug")      // ВРЕМЕННО: проба на телефоне
	os.Setenv("MONOLIT_LOGFILTER", "wireguard") // ВРЕМЕННО
	internet.RegisterDialerController(protect)
	internet.RegisterListenerController(protect)
	app = core.New(false)
	app.SetPush(func(js string) { current().Push(js) })
	core.HookIntercept()
	core.Go("loop", app.Loop)
	ui = core.ServeUI()
	return ui
}

func current() Host {
	mu.Lock()
	defer mu.Unlock()
	return host
}

// Call — вызов из окна по имени (те же привязки, что у окна Windows). args — JSON-массив аргументов.
// Ответ — JSON результата; ошибка возвращается исключением на стороне Android
func Call(name, args string) (string, error) {
	res, err := app.Call(name, []byte(args))
	return string(res), err
}

// Names — имена вызовов в JSON: окно заводит по ним функции window.mpXxx
func Names() string {
	b, _ := json.Marshal(app.BindingNames())
	return string(b)
}

// Connect, Disconnect, Toggle — для плитки в шторке и уведомления, без окна
func Connect()    { app.Connect() }
func Disconnect() { app.Disconnect() }
func Toggle()     { app.Toggle() }

// State — состояние в JSON
func State() string {
	res, _ := app.Call("mpState", nil)
	return string(res)
}

// droid — то, чем Android отличается для общей логики (core.Platform)
type droid struct{}

func (droid) Admin() bool       { return true } // права на VPN даёт система по согласию пользователя
func (droid) OSName() string    { return "Android" }
func (droid) OSVersion() string { return current().OSVersion() }
func (droid) Device() string    { return current().Device() }

func (droid) TunCheck() error { return nil }

// TunBefore: сервис поднимает VPN системы, дескриптор уходит ядру через окружение (его читает вход TUN ядра)
func (droid) TunBefore(ctx context.Context) error {
	fd := current().OpenTun(tunMTU, tunAddr4, tunPrefix4, tunDNS4, tunAddr6, tunPrefix6)
	if fd <= 0 {
		return errors.New("Android не дал поднять VPN")
	}
	return os.Setenv(platform.TunFdKey, strconv.Itoa(int(fd)))
}

func (droid) TunAfter(context.Context) error { return nil }

// TunDown: дескриптор закрывает ядро при остановке, здесь — только отпустить сервис
func (droid) TunDown() { current().CloseTun() }

// PhysIf пуст: выходы ядра мимо туннеля отделяет Protect, а не привязка к сетевой карте
func (droid) PhysIf() string { return "" }

func (droid) BindDirect(d *net.Dialer) {
	d.Control = func(network, address string, c syscall.RawConn) error {
		return c.Control(func(fd uintptr) { current().Protect(int32(fd)) })
	}
}

// системного прокси на Android нет: только VPN
func (droid) SysProxyOn(int) error { return errors.New("на Android только VPN") }
func (droid) SysProxyOff()         {}

func (droid) SetAutoStart(bool) {}
func (droid) Restart()          {}

// обновление и программы — следующим шагом (установка APK, список приложений системы)
func (droid) UpdateAsset() string { return "monolit.apk" }
func (droid) UpdateFile() (*os.File, error) {
	return nil, errors.New("обновление на Android ещё не сделано")
}
func (droid) UpdateInstall(string) error {
	return errors.New("обновление на Android ещё не сделано")
}
func (droid) AppsRunning() []string  { return nil }
func (droid) AppSkip(string) bool    { return false }
func (droid) AppCheck(string) error  { return nil }
func (droid) AppTitle(string) string { return "" }
func (droid) AppIcon(string) string  { return "" }
func (droid) PickApp() (string, error) {
	return "", errors.New("выбор приложений на Android ещё не сделан")
}
