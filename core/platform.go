package core

import (
	"context"
	"errors"
	"net"
	"os"
)

// Platform — всё, чем отличаются системы. Логика (подписки, конфиг ядра, маршруты, умный режим, замеры,
// настройки, мост к интерфейсу) живёт в этом пакете и одна на всех; оболочка системы (windows, android)
// даёт только то, что ниже, и задаёт себя через SetPlatform до New
type Platform interface {
	// Admin: есть права менять систему (в Windows — администратор)
	Admin() bool
	// OSName, OSVersion, Device — чем клиент представляется серверу подписки
	OSName() string
	OSVersion() string
	Device() string

	// TUN. Порядок: TunCheck → сборка конфига → TunBefore → запуск ядра → TunAfter. TunDown снимает то,
	// что поднял TunAfter. PhysIf — сетевая карта с интернетом: к ней привязаны выходы ядра мимо туннеля
	// (пусто, если система отделяет их иначе)
	TunCheck() error
	TunBefore(ctx context.Context) error
	TunAfter(ctx context.Context) error
	TunDown()
	PhysIf() string
	// BindDirect: своё соединение программы мимо туннеля (проверки умного режима напрямую)
	BindDirect(d *net.Dialer)

	// системный прокси — запасной способ, когда TUN не поднялся
	SysProxyOn(port int) error
	SysProxyOff()

	SetAutoStart(on bool)
	// Restart: перезапуск программы после обновления
	Restart()

	// обновление: имя файла в релизе, куда его скачивать, и установка скачанного
	UpdateAsset() string
	UpdateFile() (*os.File, error)
	UpdateInstall(path string) error

	// программы для списков «через VPN» и «напрямую»
	AppsRunning() []string       // пути программ, которые человек видит на экране
	AppSkip(path string) bool    // системное или сама программа: в список не предлагать
	AppCheck(path string) error  // годится ли путь для правила
	AppTitle(path string) string // понятное название
	AppIcon(path string) string  // data:image/png;base64,… или пусто
	PickApp() (string, error)    // выбор программы окном системы
}

var plat Platform = noPlatform{}

// SetPlatform задаёт оболочку системы. Вызывать до New
func SetPlatform(p Platform) { plat = p }

// noPlatform — пока оболочка не задана (тесты логики): систему не трогаем
type noPlatform struct{}

var errNoPlatform = errors.New("не поддерживается в этой сборке")

func (noPlatform) Admin() bool                     { return false }
func (noPlatform) OSName() string                  { return "" }
func (noPlatform) OSVersion() string               { return "" }
func (noPlatform) Device() string                  { return "" }
func (noPlatform) TunCheck() error                 { return errNoPlatform }
func (noPlatform) TunBefore(context.Context) error { return nil }
func (noPlatform) TunAfter(context.Context) error  { return nil }
func (noPlatform) TunDown()                        {}
func (noPlatform) PhysIf() string                  { return "" }
func (noPlatform) BindDirect(*net.Dialer)          {}
func (noPlatform) SysProxyOn(int) error            { return nil }
func (noPlatform) SysProxyOff()                    {}
func (noPlatform) SetAutoStart(bool)               {}
func (noPlatform) Restart()                        {}
func (noPlatform) UpdateAsset() string             { return "" }
func (noPlatform) UpdateFile() (*os.File, error)   { return nil, errNoPlatform }
func (noPlatform) UpdateInstall(string) error      { return errNoPlatform }
func (noPlatform) AppsRunning() []string           { return nil }
func (noPlatform) AppSkip(string) bool             { return false }
func (noPlatform) AppCheck(string) error           { return nil }
func (noPlatform) AppTitle(string) string          { return "" }
func (noPlatform) AppIcon(string) string           { return "" }
func (noPlatform) PickApp() (string, error)        { return "", errNoPlatform }
