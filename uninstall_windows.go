package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Удаление. Программа появляется в «Приложениях» Windows, оттуда её и убирают: Windows запускает
// `monolit.exe --uninstall`. Убирается всё, что ставила установка: задача планировщика, ярлыки, запись
// в «Приложениях», папка в Program Files. Настройки и подписки — только если человек согласился.
const uninstallKey = `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\monolit`

// registerUninstall: запись в «Приложениях» Windows. Вызывается при установке и при каждом запуске
// с правами администратора: у копий, поставленных раньше, записи ещё нет
func registerUninstall(exe string) error {
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, uninstallKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	size := uint32(0)
	if st, err := os.Stat(exe); err == nil {
		size = uint32(st.Size() / 1024)
	}
	for name, v := range map[string]string{
		"DisplayName": "Monolit", "DisplayVersion": version, "Publisher": "Monolit", "DisplayIcon": exe,
		"InstallLocation": filepath.Dir(exe), "UninstallString": `"` + exe + `" --uninstall`,
	} {
		if err := k.SetStringValue(name, v); err != nil {
			return err
		}
	}
	for name, v := range map[string]uint32{"NoModify": 1, "NoRepair": 1, "EstimatedSize": size} {
		if err := k.SetDWordValue(name, v); err != nil {
			return err
		}
	}
	return nil
}

// safeInstallDir: папку удаляем целиком, поэтому она обязана быть именно «…\monolit» с нашим exe внутри
func safeInstallDir(dir string) bool {
	if !strings.EqualFold(filepath.Base(dir), "monolit") || len(filepath.Dir(dir)) < 4 {
		return false
	}
	st, err := os.Stat(filepath.Join(dir, "monolit.exe"))
	return err == nil && !st.IsDir()
}

// running: работает ли ещё копия программы (по её метке единственного запуска)
func running() bool {
	name, _ := syscall.UTF16PtrFromString("Local\\monolit-client")
	h, err := windows.OpenMutex(windows.SYNCHRONIZE, false, name)
	if err != nil {
		return false
	}
	windows.CloseHandle(h)
	return true
}

func uninstall() error {
	if !isAdmin() {
		return runElevated("--uninstall")
	}
	if !askBox("Удалить Monolit с этого компьютера?") {
		return nil
	}
	// работающая копия выходит сама и возвращает системе прокси и DNS, как при «Выходе» в трее
	if sendQuit() {
		for i := 0; i < 60 && running(); i++ {
			time.Sleep(250 * time.Millisecond)
		}
	}
	if running() {
		return errors.New("Monolit ещё работает. Закройте его через значок в трее и повторите удаление")
	}
	sysProxyOff() // копия могла упасть с включённым прокси: возвращаем настройки из сохранённых
	tunDown()

	var fails []string
	note := func(what string, err error) {
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			fails = append(fails, what+": "+err.Error())
		}
	}
	if taskExists() {
		note("задача планировщика", hidden("schtasks", "/Delete", "/TN", taskName, "/F").Run())
	}
	note("ярлык в «Пуске»", os.Remove(startMenuLnk()))
	if desk, err := windows.KnownFolderPath(windows.FOLDERID_Desktop, 0); err == nil {
		note("ярлык на рабочем столе", os.Remove(filepath.Join(desk, "monolit.lnk")))
	}
	if err := registry.DeleteKey(registry.LOCAL_MACHINE, uninstallKey); err != nil && !errors.Is(err, registry.ErrNotExist) {
		note("запись в «Приложениях»", err)
	}
	if askBox("Удалить также настройки и подписки?\n\nЕсли оставить, они подхватятся при следующей установке.") {
		note("настройки", os.RemoveAll(appDir()))
		note("служебные данные", os.RemoveAll(dataDir()))
	}
	// свою папку программа удалить не может, пока сама работает: это делает командная строка через пару секунд
	dir := installDir()
	if safeInstallDir(dir) {
		note("папка программы", hidden("cmd", "/c", `ping -n 4 127.0.0.1 >nul & rmdir /s /q "`+dir+`"`).Start())
	}
	if len(fails) > 0 {
		return fmt.Errorf("удалено не всё:\n%s", strings.Join(fails, "\n"))
	}
	infoBox("Monolit удалён.")
	return nil
}

func box(text string, flags uintptr) uintptr {
	t, _ := syscall.UTF16PtrFromString(text)
	c, _ := syscall.UTF16PtrFromString("Monolit")
	r, _, _ := messageBox.Call(0, uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(c)), flags)
	return r
}

// askBox: вопрос «Да / Нет»
func askBox(text string) bool { return box(text, 0x4|0x20) == 6 }

func infoBox(text string) { box(text, 0x40) }
