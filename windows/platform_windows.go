// Оболочка Windows для общей логики (пакет core): окно, трей, TUN-адаптер, системный прокси, установка
package main

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"monolit-client/core"
)

// имена общей логики, которыми пользуются файлы оболочки
type Settings = core.Settings

const (
	version = core.Version
	tunName = core.TunName
)

var (
	app     *core.App
	logLine = core.Log
	appDir  = core.AppDir
	safeGo  = core.Go
	safeRun = core.Run
)

func init() {
	core.SetPlatform(winPlatform{})
	go removeOldExe()
}

func dataDir() string {
	base, err := os.UserCacheDir() // %LOCALAPPDATA%
	if err != nil {
		base = os.TempDir()
	}
	d := filepath.Join(base, "monolit")
	os.MkdirAll(d, 0o755)
	return d
}

// winPlatform — то, чем Windows отличается для общей логики (core.Platform)
type winPlatform struct{}

func (winPlatform) Admin() bool       { return isAdmin() }
func (winPlatform) OSName() string    { return "Windows" }
func (winPlatform) OSVersion() string { return osVersion() }
func (winPlatform) Device() string    { return "Desktop" }

func (winPlatform) TunCheck() error {
	if !isAdmin() {
		return errors.New("нужны права администратора")
	}
	if err := ensureWintun(); err != nil {
		return err
	}
	if physIfName() == "" {
		return errors.New("не найдена сетевая карта с интернетом")
	}
	return nil
}

// TunBefore: адаптер пересоздаётся при каждом подключении, а Windows ещё какое-то время показывает старый
// с тем же именем. Его номер уже недействителен, и netsh отвечает «Элемент не найден». Ждём, пока старого не станет
func (winPlatform) TunBefore(ctx context.Context) error {
	for i := 0; i < 50; i++ {
		if _, err := net.InterfaceByName(tunName); err != nil {
			break
		}
		if !sleepCtx(ctx, 100*time.Millisecond) {
			return ctx.Err()
		}
	}
	return nil
}

// TunAfter — настройка адаптера: номер ищется заново на каждой попытке, пока Windows не закончит его заводить
func (winPlatform) TunAfter(ctx context.Context) error {
	deadline := time.Now().Add(8 * time.Second)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := setupTunIf()
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) || !sleepCtx(ctx, 300*time.Millisecond) {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
	}
}

func (winPlatform) TunDown()       { tunDown() }
func (winPlatform) PhysIf() string { return physIfName() }

// BindDirect привязывает соединение к сетевой карте, как выход direct ядра: иначе в TUN оно ушло бы в туннель
func (winPlatform) BindDirect(d *net.Dialer) {
	ifc, err := net.InterfaceByName(physIfName())
	if err != nil {
		return
	}
	idx := ifc.Index
	d.Control = func(network, address string, c syscall.RawConn) error {
		var serr error
		if err := c.Control(func(fd uintptr) { serr = bindIf(fd, address, idx) }); err != nil {
			return err
		}
		return serr
	}
}

const ipUnicastIf = 31 // IP_UNICAST_IF и IPV6_UNICAST_IF

// bindIf — как sockopt interface в xray: IPv4 — номер карты в порядке байт сети, IPv6 — как есть
func bindIf(fd uintptr, address string, idx int) error {
	if strings.Contains(address, ".") {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], uint32(idx))
		return syscall.SetsockoptInt(syscall.Handle(fd), syscall.IPPROTO_IP, ipUnicastIf, int(binary.NativeEndian.Uint32(b[:])))
	}
	return syscall.SetsockoptInt(syscall.Handle(fd), syscall.IPPROTO_IPV6, ipUnicastIf, idx)
}

func (winPlatform) SysProxyOn(port int) error { return sysProxyOn(port) }
func (winPlatform) SysProxyOff()              { sysProxyOff() }
func (winPlatform) SetAutoStart(on bool)      { setAutoStart(on) }
func (winPlatform) Restart()                  { restartSelf() }

// Обновление: monolit.exe из релиза скачивается рядом с запущенным и подменяет его. Установленная копия лежит
// в Program Files (installDir), туда пишет только процесс с правами. Копия из другого места (перенос
// не удался, сборка для проверки) обновляется на месте. Работает после перезапуска
func (winPlatform) UpdateAsset() string { return "monolit.exe" }

func (winPlatform) UpdateFile() (*os.File, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	out, err := os.Create(filepath.Join(filepath.Dir(exe), "monolit.exe.download"))
	if errors.Is(err, os.ErrPermission) && !isAdmin() {
		return nil, errors.New("нет прав на запись в " + filepath.Dir(exe) + ": откройте Monolit ярлыком из меню «Пуск» (с правами администратора) и повторите обновление")
	}
	return out, err
}

func (winPlatform) UpdateInstall(tmp string) error {
	if !isPE(tmp) {
		return errors.New("скачанный файл — не программа Windows")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	os.Remove(exe + ".old")
	if err := os.Rename(exe, exe+".old"); err != nil {
		return err
	}
	if err := os.Rename(tmp, exe); err != nil {
		os.Rename(exe+".old", exe)
		return err
	}
	return nil
}

// removeOldExe убирает monolit.exe.old после обновления. Старая копия ещё может работать
// несколько секунд (новая ждёт её выхода), поэтому пробуем несколько раз
func removeOldExe() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	for i := 0; i < 6; i++ {
		if err := os.Remove(exe + ".old"); err == nil || errors.Is(err, os.ErrNotExist) {
			return
		}
		time.Sleep(5 * time.Second)
	}
}

// isPE: файл начинается с «MZ», а по смещению из e_lfanew (0x3C) лежит «PE\0\0»
func isPE(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var hdr [64]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil || hdr[0] != 'M' || hdr[1] != 'Z' {
		return false
	}
	off := int64(binary.LittleEndian.Uint32(hdr[0x3C:]))
	var sig [4]byte
	if _, err := f.ReadAt(sig[:], off); err != nil {
		return false
	}
	return sig == [4]byte{'P', 'E', 0, 0}
}

func (winPlatform) AppsRunning() []string       { return runningApps() }
func (winPlatform) AppSkip(path string) bool    { return skipApp(path) }
func (winPlatform) AppTitle(path string) string { return exeTitle(path) }
func (winPlatform) AppIcon(path string) string  { return exeIcon(path) }
func (winPlatform) PickApp() (string, error)    { return pickExe() }

func (winPlatform) AppCheck(path string) error {
	if !strings.EqualFold(filepath.Ext(path), ".exe") {
		return errors.New("нужен файл программы .exe")
	}
	return nil
}
