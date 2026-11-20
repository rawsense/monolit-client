package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// системные DLL — только из System32: DLL с тем же именем рядом с exe не подхватится
var (
	wininet             = windows.NewLazySystemDLL("wininet.dll")
	internetSetOption   = wininet.NewProc("InternetSetOptionW")
	internetQueryOption = wininet.NewProc("InternetQueryOptionW")
	user32              = windows.NewLazySystemDLL("user32.dll")
	sendMessageTimeout  = user32.NewProc("SendMessageTimeoutW")
	iphlpapi            = windows.NewLazySystemDLL("iphlpapi.dll")
	getBestInterface    = iphlpapi.NewProc("GetBestInterface")
	kernel32            = windows.NewLazySystemDLL("kernel32.dll")
	globalFree          = kernel32.NewProc("GlobalFree")
)

func hidden(name string, args ...string) *exec.Cmd {
	c := exec.Command(name, args...)
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	return c
}

func isAdmin() bool { return windows.GetCurrentProcessToken().IsElevated() }

// ---------- что было до нас: восстанавливаем при отключении ----------

type sysBackup struct {
	ProxyEnable   uint32            `json:"proxyEnable"`
	ProxyServer   string            `json:"proxyServer"`
	ProxyOverride string            `json:"proxyOverride"`
	Env           map[string]string `json:"env"`    // "" — переменной не было
	Exempt        []string          `json:"exempt"` // SID приложений Store, которым мы выдали доступ к 127.0.0.1
	// WinINet: автоопределение и PAC. В старых копиях полей нет — тогда восстанавливаем как раньше
	Inet          bool   `json:"inet,omitempty"`
	InetFlags     uint32 `json:"inetFlags,omitempty"`
	AutoConfigURL string `json:"autoConfigURL,omitempty"`
}

// наш обход прокси и NO_PROXY: при снимке их значения считаются «не было»
const (
	ourBypass  = "localhost;127.*;10.*;172.16.*;172.17.*;172.18.*;172.19.*;172.20.*;172.21.*;172.22.*;172.23.*;172.24.*;172.25.*;172.26.*;172.27.*;172.28.*;172.29.*;172.30.*;172.31.*;192.168.*;<local>"
	ourNoProxy = "localhost,127.0.0.1,::1"
)

// флаги INTERNET_PER_CONN_FLAGS
const (
	proxyDirect     = 1
	proxyManual     = 2
	proxyAutoURL    = 4
	proxyAutoDetect = 8
)

var backupPath = filepath.Join(appDir(), "system-backup.json")

const inetKey = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

var proxyEnv = []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY"}

func refreshInet() {
	internetSetOption.Call(0, 39, 0, 0) // INTERNET_OPTION_SETTINGS_CHANGED
	internetSetOption.Call(0, 37, 0, 0) // INTERNET_OPTION_REFRESH
}

// setInetProxy пишет прокси через WinINet: так обновляется и DefaultConnectionSettings,
// откуда прокси берут Chromium, Electron и WinHTTP (одних значений реестра им мало)
// раскладка INTERNET_PER_CONN_OPTIONW и INTERNET_PER_CONN_OPTION_LISTW для amd64:
// объединение Value — 8 байт со смещением 8, DWORD лежит в младших байтах
type inetOpt struct {
	option uint32
	_      uint32
	value  uintptr
}

type inetOptList struct {
	size   uint32
	conn   *uint16
	count  uint32
	errIdx uint32
	opts   *inetOpt
}

// setInetProxy: прямое подключение или наш прокси; автоопределение и PAC при этом выключены
func setInetProxy(on bool, server, bypass string) error {
	flags := uint32(proxyDirect)
	if on {
		flags |= proxyManual
	}
	return setInetProxyFull(flags, server, bypass, "", false)
}

// setInetProxyFull пишет все флаги, а с withPAC — и адрес PAC (пустой — убрать)
func setInetProxyFull(flags uint32, server, bypass, pac string, withPAC bool) error {
	sv, _ := syscall.UTF16PtrFromString(server)
	bv, _ := syscall.UTF16PtrFromString(bypass)
	var pv *uint16
	if pac != "" {
		pv, _ = syscall.UTF16PtrFromString(pac)
	}
	opts := []inetOpt{
		{option: 1, value: uintptr(flags)},              // INTERNET_PER_CONN_FLAGS
		{option: 2, value: uintptr(unsafe.Pointer(sv))}, // INTERNET_PER_CONN_PROXY_SERVER
		{option: 3, value: uintptr(unsafe.Pointer(bv))}, // INTERNET_PER_CONN_PROXY_BYPASS
	}
	if withPAC {
		opts = append(opts, inetOpt{option: 4, value: uintptr(unsafe.Pointer(pv))}) // INTERNET_PER_CONN_AUTOCONFIG_URL
	}
	l := inetOptList{count: uint32(len(opts)), opts: &opts[0]}
	l.size = uint32(unsafe.Sizeof(l))
	r, _, e := internetSetOption.Call(0, 75, uintptr(unsafe.Pointer(&l)), uintptr(l.size)) // INTERNET_OPTION_PER_CONNECTION_OPTION
	runtime.KeepAlive(sv)
	runtime.KeepAlive(bv)
	runtime.KeepAlive(pv)
	runtime.KeepAlive(opts)
	if r == 0 {
		return fmt.Errorf("InternetSetOption: %v", e)
	}
	refreshInet()
	return nil
}

// queryInet читает флаги WinINet и адрес PAC. Флаги — из FLAGS_UI (как в настройках Windows),
// а если система его не знает — из FLAGS
func queryInet() (flags uint32, pac string, ok bool) {
	for _, fo := range []uint32{10, 1} { // INTERNET_PER_CONN_FLAGS_UI, INTERNET_PER_CONN_FLAGS
		opts := []inetOpt{{option: fo}, {option: 4}}
		l := inetOptList{count: uint32(len(opts)), opts: &opts[0]}
		l.size = uint32(unsafe.Sizeof(l))
		size := l.size
		r, _, _ := internetQueryOption.Call(0, 75, uintptr(unsafe.Pointer(&l)), uintptr(unsafe.Pointer(&size)))
		runtime.KeepAlive(opts)
		if r == 0 {
			continue
		}
		if p := *(**uint16)(unsafe.Pointer(&opts[1].value)); p != nil { // строку выделил WinINet: копируем и освобождаем
			pac = windows.UTF16PtrToString(p)
			globalFree.Call(uintptr(unsafe.Pointer(p)))
		}
		return uint32(opts[0].value), pac, true
	}
	return 0, "", false
}

// broadcastEnv сообщает программам о новых переменных. При завершении сеанса не шлём: рассылка ждёт и наше
// окно, а его поток занят WM_ENDSESSION — отключение теряло бы 3 с. Значения в реестре и так записаны
func broadcastEnv() {
	if endingSession.Load() {
		return
	}
	p, _ := syscall.UTF16PtrFromString("Environment")
	var res uintptr
	sendMessageTimeout.Call(0xffff, 0x001A, 0, uintptr(unsafe.Pointer(p)), 0x0002, 3000, uintptr(unsafe.Pointer(&res)))
}

// sysProxyOn: системный прокси, переменные для консольных программ и доступ к 127.0.0.1 для приложений Store
func sysProxyOn(port int) error {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	k, err := registry.OpenKey(registry.CURRENT_USER, inetKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	b, err := loadBackup(k, addr)
	if err != nil {
		return err
	}
	// копия ложится на диск раньше, чем мы что-то меняем: не записалась — прокси не включаем
	if err := writeBackup(b); err != nil {
		return fmt.Errorf("копия настроек: %w", err)
	}
	k.SetDWordValue("ProxyEnable", 1)
	k.SetStringValue("ProxyServer", addr)
	k.SetStringValue("ProxyOverride", ourBypass)
	if err := setInetProxy(true, addr, ourBypass); err != nil {
		logLine("системный прокси: " + err.Error())
	}
	if ek, err := registry.OpenKey(registry.CURRENT_USER, "Environment", registry.SET_VALUE); err == nil {
		ek.SetStringValue("HTTP_PROXY", "http://"+addr)
		ek.SetStringValue("HTTPS_PROXY", "http://"+addr)
		ek.SetStringValue("NO_PROXY", ourNoProxy)
		ek.Close()
		broadcastEnv()
	}
	if isAdmin() {
		if added := exemptStoreApps(b.Exempt); len(added) > 0 {
			b.Exempt = append(b.Exempt, added...)
			if err := writeBackup(b); err != nil { // прокси уже включён: это не ошибка включения
				logLine("копия настроек (приложения Store): " + err.Error())
			}
		}
	}
	return nil
}

// loadBackup: копия с диска, а если её нет — снимок текущих настроек.
// Наши же значения (остались после сбоя) в снимок идут как «не было», иначе мёртвый прокси станет «исходным»
func loadBackup(k registry.Key, addr string) (sysBackup, error) {
	var b sysBackup
	raw, err := os.ReadFile(backupPath)
	if err == nil {
		if err := json.Unmarshal(raw, &b); err == nil {
			return b, nil
		}
		logLine("копия настроек испорчена, снимаю заново: " + err.Error())
		os.Remove(backupPath + ".bad")
		os.Rename(backupPath, backupPath+".bad")
		b = sysBackup{}
	} else if !errors.Is(err, os.ErrNotExist) {
		return b, fmt.Errorf("копия настроек: %w", err)
	}
	pe, _, _ := k.GetIntegerValue("ProxyEnable")
	b.ProxyEnable = uint32(pe)
	b.ProxyServer, _, _ = k.GetStringValue("ProxyServer")
	b.ProxyOverride, _, _ = k.GetStringValue("ProxyOverride")
	// наш — текущий порт или любой порт 127.0.0.1 вместе с нашим обходом (сбой на другом порту из настроек)
	ours := b.ProxyEnable != 0 && (b.ProxyServer == addr ||
		strings.HasPrefix(b.ProxyServer, "127.0.0.1:") && b.ProxyOverride == ourBypass)
	if ours {
		b.ProxyEnable = 0
	}
	if b.ProxyOverride == ourBypass {
		b.ProxyOverride = ""
	}
	if flags, pac, ok := queryInet(); ok {
		if ours {
			flags = flags&^proxyManual | proxyDirect
		}
		b.Inet, b.InetFlags, b.AutoConfigURL = true, flags, pac
	}
	b.Env = map[string]string{}
	if ek, err := registry.OpenKey(registry.CURRENT_USER, "Environment", registry.QUERY_VALUE); err == nil {
		np, _, _ := ek.GetStringValue("NO_PROXY")
		for _, n := range proxyEnv {
			v, _, _ := ek.GetStringValue(n)
			// наши: текущий адрес или любой порт 127.0.0.1 при нашем NO_PROXY
			if v == "http://"+addr || (np == ourNoProxy && (n == "NO_PROXY" || strings.HasPrefix(v, "http://127.0.0.1:"))) {
				v = ""
			}
			b.Env[n] = v
		}
		ek.Close()
	}
	return b, nil
}

// writeBackup пишет копию целиком или никак: временный файл рядом, сброс на диск, переименование
func writeBackup(b sysBackup) error {
	raw, err := json.MarshalIndent(b, "", " ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(backupPath), filepath.Base(backupPath)+".tmp*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, backupPath)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// sysProxyOff возвращает настройки из копии. Копии нет — ничего не делает; повторный вызов безвреден.
// Копия удаляется, только когда всё вернулось: иначе она нужна для следующей попытки
func sysProxyOff() {
	raw, err := os.ReadFile(backupPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logLine("копия настроек: " + err.Error())
		}
		return
	}
	var b sysBackup
	if err := json.Unmarshal(raw, &b); err != nil {
		logLine("копия настроек испорчена, прокси не восстановлен: " + err.Error())
		return
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, inetKey, registry.SET_VALUE)
	if err != nil {
		logLine("системный прокси не восстановлен, копия сохранена: " + err.Error())
		return
	}
	ok := k.SetDWordValue("ProxyEnable", b.ProxyEnable) == nil
	ok = setOrDelete(k, "ProxyServer", b.ProxyServer) == nil && ok
	ok = setOrDelete(k, "ProxyOverride", b.ProxyOverride) == nil && ok
	k.Close()
	if b.Inet {
		err = setInetProxyFull(b.InetFlags, b.ProxyServer, b.ProxyOverride, b.AutoConfigURL, true)
	} else {
		// старая копия без снимка WinINet: как раньше — реестр и оповещение. setInetProxy снял бы
		// автоопределение и PAC, которых в копии нет
		refreshInet()
	}
	if err != nil {
		logLine("системный прокси: " + err.Error())
		ok = false
	}
	if ek, err := registry.OpenKey(registry.CURRENT_USER, "Environment", registry.SET_VALUE); err == nil {
		for _, n := range proxyEnv {
			ok = setOrDelete(ek, n, b.Env[n]) == nil && ok
		}
		ek.Close()
		broadcastEnv()
	} else {
		ok = false
	}
	if !ok {
		logLine("системный прокси восстановлен не полностью, копия сохранена")
		return
	}
	// доступ к 127.0.0.1 приложениям Store оставляем: он безвреден и нужен при следующем включении
	os.Remove(backupPath)
}

// setOrDelete: пустое значение — удалить; если его и так нет, это не ошибка
func setOrDelete(k registry.Key, name, v string) error {
	if v != "" {
		return k.SetStringValue(name, v)
	}
	if err := k.DeleteValue(name); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

// exemptStoreApps выдаёт приложениям Microsoft Store доступ к 127.0.0.1 (иначе они не видят прокси)
func exemptStoreApps(done []string) []string {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Classes\Local Settings\Software\Microsoft\Windows\CurrentVersion\AppContainer\Mappings`, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return nil
	}
	sids, _ := k.ReadSubKeyNames(-1)
	k.Close()
	have := map[string]bool{}
	for _, s := range done {
		have[s] = true
	}
	out, _ := hidden("CheckNetIsolation.exe", "LoopbackExempt", "-s").Output()
	for _, line := range strings.Split(string(out), "\n") {
		if i := strings.Index(line, "S-1-15-"); i >= 0 {
			have[strings.TrimSpace(line[i:])] = true
		}
	}
	var added []string
	for _, sid := range sids {
		if have[sid] {
			continue
		}
		if hidden("CheckNetIsolation.exe", "LoopbackExempt", "-a", "-p="+sid).Run() == nil {
			added = append(added, sid)
		}
	}
	return added
}

// ---------- TUN ----------

// physIfName: сетевая карта, через которую идёт интернет (по ней соединения к серверу идут мимо туннеля)
// lastPhys — сетевая карта, найденная до подъёма TUN: пока TUN работает, лучший путь к 8.8.8.8 идёт через него
var (
	physMu   sync.Mutex
	lastPhys string
)

// physIfName не следит за сменой сети (Wi-Fi ↔ кабель) при поднятом TUN: отдаёт карту, найденную до подъёма
func physIfName() string {
	var idx uint32
	ip := uint32(8) | uint32(8)<<8 | uint32(8)<<16 | uint32(8)<<24 // 8.8.8.8 в порядке байт сети
	if r, _, _ := getBestInterface.Call(uintptr(ip), uintptr(unsafe.Pointer(&idx))); r != 0 {
		return ""
	}
	ifc, err := net.InterfaceByIndex(int(idx))
	if err != nil {
		return ""
	}
	physMu.Lock()
	defer physMu.Unlock()
	if ifc.Name == tunName {
		return lastPhys
	}
	lastPhys = ifc.Name
	return ifc.Name
}

func exeDir() string {
	p, _ := os.Executable()
	return filepath.Dir(p)
}

// sleepCtx: пауза, прерываемая отменой. false — отменили
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// адрес TUN для IPv6: свой ULA. С ним Windows ставит IPv4 первым в ответах getaddrinfo (метки RFC 6724
// не совпадают), а IPv6 по маршрутам ниже уходит в TUN и там отклоняется
const (
	tunV6Addr   = "fdfe:dcba:9876::1"
	tunV6Prefix = "/126"
)

// tunV6Routes — два маршрута вместо ::/0: они уже ::/0 сетевой карты и потому главнее
var tunV6Routes = []string{"::/1", "8000::/1"}

// tunV4Cmds — настройка IPv4 адаптера (как раньше)
func tunV4Cmds(idx string) [][]string {
	return [][]string{
		{"netsh", "interface", "ipv4", "set", "address", "name=" + idx, "source=static", "address=172.19.0.1", "mask=255.255.255.252"},
		{"netsh", "interface", "ipv4", "set", "dnsservers", "name=" + idx, "source=static", "address=172.19.0.2", "register=none", "validate=no"},
		{"netsh", "interface", "ipv4", "set", "interface", idx, "metric=1"},
		{"route", "add", "0.0.0.0", "mask", "128.0.0.0", "172.19.0.2", "metric", "1", "if", idx},
		{"route", "add", "128.0.0.0", "mask", "128.0.0.0", "172.19.0.2", "metric", "1", "if", idx},
	}
}

// tunV6Cmds — весь IPv6 в TUN. Всё store=active: после сбоя или перезагрузки не остаётся ничего,
// а с адаптером исчезает и так. Проверку дублей (DAD) выключаем: gVisor с подменой адресов
// ответил бы на неё, и Windows сочла бы адрес занятым
func tunV6Cmds(idx string) [][]string {
	c := [][]string{
		{"netsh", "interface", "ipv6", "set", "interface", "interface=" + idx, "dadtransmits=0", "routerdiscovery=disabled", "metric=1", "store=active"},
		{"netsh", "interface", "ipv6", "add", "address", "interface=" + idx, "address=" + tunV6Addr + tunV6Prefix, "store=active"},
	}
	for _, r := range tunV6Routes {
		c = append(c, []string{"netsh", "interface", "ipv6", "add", "route", "prefix=" + r, "interface=" + idx, "metric=1", "store=active"})
	}
	return c
}

// tunDownCmds — снять маршруты и адрес. Ошибки не важны: чего-то может и не быть
func tunDownCmds(idx string) [][]string {
	c := [][]string{
		{"route", "delete", "0.0.0.0", "mask", "128.0.0.0", "172.19.0.2", "if", idx},
		{"route", "delete", "128.0.0.0", "mask", "128.0.0.0", "172.19.0.2", "if", idx},
	}
	for _, r := range tunV6Routes {
		c = append(c, []string{"netsh", "interface", "ipv6", "delete", "route", "prefix=" + r, "interface=" + idx, "store=active"})
	}
	return append(c, []string{"netsh", "interface", "ipv6", "delete", "address", "interface=" + idx, "address=" + tunV6Addr, "store=active"})
}

func setupTunIf() error {
	ifc, err := net.InterfaceByName(tunName)
	if err != nil {
		return errors.New("адаптер TUN не появился")
	}
	idx := fmt.Sprint(ifc.Index)
	run := func(st []string) error {
		if out, err := hidden(st[0], st[1:]...).CombinedOutput(); err != nil {
			return fmt.Errorf("%s: %s", strings.Join(st[:3], " "), strings.TrimSpace(string(out)))
		}
		return nil
	}
	for _, st := range tunV4Cmds(idx) {
		if err := run(st); err != nil {
			tunDown()
			return err
		}
	}
	for _, st := range tunV6Cmds(idx) {
		if err := run(st); err != nil {
			// IPv6 выключен в системе — обходить VPN нечем, работаем на IPv4. Иначе — ошибка и повтор
			if !hasGlobalIPv6(tunName) {
				logLine("IPv6 в TUN не настроен, у сетевых карт IPv6 нет: " + err.Error())
				return nil
			}
			tunDown()
			return err
		}
	}
	return nil
}

// hasGlobalIPv6: есть ли у какой-то карты, кроме skip, IPv6-адрес не из локальной ссылки (fe80::) и не петля
func hasGlobalIPv6(skip string) bool {
	ifs, err := net.Interfaces()
	if err != nil {
		return true // не знаем — считаем, что есть
	}
	for _, ifc := range ifs {
		if ifc.Name == skip || ifc.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && isGlobalV6(ipn.IP) {
				return true
			}
		}
	}
	return false
}

func isGlobalV6(ip net.IP) bool {
	return ip.To4() == nil && ip.To16() != nil && !ip.IsLoopback() && !ip.IsLinkLocalUnicast()
}

func tunDown() {
	if ifc, err := net.InterfaceByName(tunName); err == nil {
		for _, st := range tunDownCmds(fmt.Sprint(ifc.Index)) {
			hidden(st[0], st[1:]...).Run()
		}
	}
}

// osVersion: версия Windows вида 10.0.26200 — для заголовков запроса подписки
func osVersion() string {
	v := windows.RtlGetVersion()
	return fmt.Sprintf("%d.%d.%d", v.MajorVersion, v.MinorVersion, v.BuildNumber)
}
