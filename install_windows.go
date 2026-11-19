package main

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

//go:embed assets/wintun.dll
var wintunDLL []byte // драйвер TUN от WireGuard, подписан WireGuard LLC

const taskName = "monolit"

// installDir: %ProgramFiles%\monolit. Настройки и подписки остаются в %APPDATA%\monolit
func installDir() string {
	known, _ := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, 0)
	return filepath.Join(pickProgramFiles(known, os.Getenv("ProgramFiles")), "monolit")
}

// pickProgramFiles: путь от системы, затем переменная окружения, затем стандартный
func pickProgramFiles(known, env string) string {
	for _, p := range []string{known, env} {
		if p = strings.TrimSpace(p); p != "" && filepath.IsAbs(p) {
			return filepath.Clean(p)
		}
	}
	return `C:\Program Files`
}

// legacyDir: прежнее место установки, %LOCALAPPDATA%\Programs\monolit ("" — папку не узнать)
func legacyDir() string {
	base, err := os.UserCacheDir()
	if err != nil || base == "" {
		return ""
	}
	return filepath.Join(base, "Programs", "monolit")
}

// sameDir: пути к одной папке без учёта регистра и хвостовых разделителей
func sameDir(a, b string) bool {
	return a != "" && b != "" && strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

func installed() bool { return sameDir(exeDir(), installDir()) }

// inLegacyDir: запущена копия из старой папки — её надо перенести в Program Files
func inLegacyDir() bool { return sameDir(exeDir(), legacyDir()) }

// ensureWintun кладёт драйвер рядом с exe. Файл сверяется со встроенным по SHA-256: подменённый или битый перезаписывается
func ensureWintun() error {
	p := filepath.Join(exeDir(), "wintun.dll")
	have, rerr := os.ReadFile(p)
	if rerr == nil && sha256.Sum256(have) == sha256.Sum256(wintunDLL) {
		return nil
	}
	// через временный файл: занятый старым процессом драйвер так не испортится наполовину
	tmp := p + ".new"
	err := os.WriteFile(tmp, wintunDLL, 0o644)
	if err == nil {
		err = os.Rename(tmp, p)
	}
	if err == nil {
		return nil
	}
	os.Remove(tmp)
	if rerr == nil { // файл есть, но занят: работаем со старым, подключение из-за этого не срываем
		logLine("wintun.dll не обновился (занят?): " + err.Error())
		return nil
	}
	return err
}

// runElevated перезапускает программу с правами администратора (один запрос UAC)
func runElevated(args string) error {
	exe, _ := os.Executable()
	verb, _ := syscall.UTF16PtrFromString("runas")
	file, _ := syscall.UTF16PtrFromString(exe)
	params, _ := syscall.UTF16PtrFromString(args)
	return windows.ShellExecute(0, verb, file, params, nil, windows.SW_SHOWNORMAL)
}

// install: копирует exe и драйвер, создаёт задачу планировщика (запуск с правами без UAC) и ярлык в меню «Пуск»
func install() error {
	if !isAdmin() {
		return errors.New("нужны права администратора")
	}
	dir := installDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	src, _ := os.Executable()
	dst := filepath.Join(dir, "monolit.exe")
	if !strings.EqualFold(src, dst) {
		if err := copyFile(src, dst); err != nil {
			return fmt.Errorf("копирование: %w", err)
		}
		if err := verifyCopy(src, dst); err != nil {
			return err
		}
	}
	if err := writeVerified(filepath.Join(dir, "wintun.dll"), wintunDLL); err != nil {
		return fmt.Errorf("wintun.dll: %w", err)
	}
	if err := createTask(dst); err != nil {
		return err
	}
	if err := registerUninstall(dst); err != nil { // без записи в «Приложениях» программа работает, просто удалять её придётся командой
		logLine("запись в «Приложениях»: " + err.Error())
	}
	return shortcut(startMenuLnk(), dst)
}

// fileSum: размер и SHA-256 файла
func fileSum(p string) (int64, [sha256.Size]byte, error) {
	var sum [sha256.Size]byte
	f, err := os.Open(p)
	if err != nil {
		return 0, sum, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	copy(sum[:], h.Sum(nil))
	return n, sum, err
}

// verifyCopy: копия совпадает с оригиналом по размеру и SHA-256 и это программа Windows (MZ…PE)
func verifyCopy(src, dst string) error {
	n1, s1, err := fileSum(src)
	if err != nil {
		return fmt.Errorf("проверка копии: %w", err)
	}
	n2, s2, err := fileSum(dst)
	if err != nil {
		return fmt.Errorf("проверка копии: %w", err)
	}
	if n1 != n2 {
		return fmt.Errorf("размер копии не совпал: %d вместо %d", n2, n1)
	}
	if s1 != s2 {
		return errors.New("контрольная сумма копии не совпала")
	}
	if !isPE(dst) {
		return errors.New("копия — не программа Windows")
	}
	return nil
}

// writeVerified кладёт данные в файл через временный и сверяет записанное по SHA-256.
// Файл с тем же содержимым не трогаем: он может быть занят (загруженный драйвер)
func writeVerified(p string, data []byte) error {
	want := sha256.Sum256(data)
	if have, err := os.ReadFile(p); err == nil && sha256.Sum256(have) == want {
		return nil
	}
	tmp := p + ".new"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return err
	}
	have, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	if sha256.Sum256(have) != want {
		return errors.New("записанный файл не совпал")
	}
	return nil
}

// migrateFromLegacy переносит программу из старой папки в Program Files и запускает новую копию.
// true — новая копия запущена, этой надо выйти. При любой ошибке остаёмся работать из старой папки.
// Старую папку не удаляем: без согласия пользователя ничего не стираем
func migrateFromLegacy(fromTask bool) bool {
	old := exeDir()
	dst := filepath.Join(installDir(), "monolit.exe")
	if err := moveToInstallDir(dst); err != nil {
		logLine("перенос в Program Files не удался: " + err.Error() + "; работаю из " + old)
		return false
	}
	args := []string{"--wait-pid", fmt.Sprint(os.Getpid())}
	if fromTask { // запуск задачей при входе: новая копия тоже уходит в трей
		args = append(args, "--from-task")
	}
	if err := hidden(dst, args...).Start(); err != nil {
		logLine("перенос в Program Files: новая копия не запустилась: " + err.Error() + "; работаю из " + old)
		return false
	}
	logLine("перенос в Program Files: готово, " + dst + "; старая папка осталась: " + old)
	return true
}

// moveToInstallDir: копия exe и драйвера в Program Files, задача и ярлыки — на неё.
// Готовую копию там не перезаписываем: она может быть новее (обновилась сама), а старая папка — устаревшей
// и доступной на запись любой программе пользователя
func moveToInstallDir(dst string) error {
	if !isAdmin() {
		return errors.New("нужны права администратора")
	}
	src, err := os.Executable()
	if err != nil {
		return err
	}
	dir := filepath.Dir(dst)
	if isPE(dst) {
		logLine("в Program Files уже есть копия, не перезаписываю: " + dst)
	} else {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := copyFile(src, dst); err != nil {
			os.Remove(dst + ".new")
			return fmt.Errorf("копирование: %w", err)
		}
		if err := verifyCopy(src, dst); err != nil {
			os.Remove(dst) // битую копию, которую только что сделали сами, не оставляем
			return err
		}
	}
	if err := writeVerified(filepath.Join(dir, "wintun.dll"), wintunDLL); err != nil {
		return fmt.Errorf("wintun.dll: %w", err)
	}
	if err := createTask(dst); err != nil {
		return err
	}
	relinkShortcuts(dst) // ярлык на старый exe всё равно запускает задачу, поэтому его ошибка не мешает
	return nil
}

// relinkShortcuts: ярлык в «Пуске» — на новую копию; ярлык на рабочем столе — только если он уже есть
func relinkShortcuts(exe string) {
	if err := shortcut(startMenuLnk(), exe); err != nil {
		logLine("перенос: " + err.Error())
	}
	desk, err := windows.KnownFolderPath(windows.FOLDERID_Desktop, 0)
	if err != nil {
		return
	}
	lnk := filepath.Join(desk, "monolit.lnk")
	if _, err := os.Stat(lnk); err != nil {
		return
	}
	if err := shortcut(lnk, exe); err != nil {
		logLine("перенос, рабочий стол: " + err.Error())
	}
}

// taskExe: задача указывает на копию в Program Files; если её там нет (перенос не удался) — на запущенную
func taskExe() string {
	p := filepath.Join(installDir(), "monolit.exe")
	if isPE(p) {
		return p
	}
	exe, _ := os.Executable()
	return exe
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	out.Close()
	os.Remove(dst + ".old")
	os.Rename(dst, dst+".old") // работающий exe нельзя перезаписать, но можно переименовать
	return os.Rename(tmp, dst)
}

// createTask: задача «monolit» — запуск при входе с наивысшими правами. Ярлык запускает её же, поэтому UAC больше не появляется
func createTask(exe string) error {
	user := xmlEsc(os.Getenv("USERDOMAIN") + `\` + os.Getenv("USERNAME"))
	doc := `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo><Description>monolit VPN</Description></RegistrationInfo>
  <Triggers><LogonTrigger><Enabled>` + boolStr(loadSettings().AutoStart) + `</Enabled><UserId>` + user + `</UserId></LogonTrigger></Triggers>
  <Principals><Principal id="Author"><UserId>` + user + `</UserId><LogonType>InteractiveToken</LogonType><RunLevel>HighestAvailable</RunLevel></Principal></Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Priority>5</Priority>
  </Settings>
  <Actions Context="Author"><Exec><Command>"` + xmlEsc(exe) + `"</Command><Arguments>--from-task</Arguments></Exec></Actions>
</Task>`
	u := windows.StringToUTF16(doc)
	b := make([]byte, 2+len(u)*2)
	b[0], b[1] = 0xFF, 0xFE
	for i, c := range u {
		b[2+i*2], b[3+i*2] = byte(c), byte(c>>8)
	}
	// файл со случайным именем во временной папке пользователя: чужая программа не подложит свой заранее
	tf, err := os.CreateTemp("", "monolit-task-*.xml")
	if err != nil {
		return err
	}
	f := tf.Name()
	defer os.Remove(f)
	_, err = tf.Write(b[:len(b)-2])
	if cerr := tf.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if out, err := hidden("schtasks", "/Create", "/TN", taskName, "/XML", f, "/F").CombinedOutput(); err != nil {
		return fmt.Errorf("планировщик: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// xmlEsc экранирует текст для XML задачи (&, <, > и кавычки в пути или имени)
func xmlEsc(s string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func taskExists() bool { return hidden("schtasks", "/Query", "/TN", taskName).Run() == nil }

func runTask() error { return hidden("schtasks", "/Run", "/TN", taskName).Run() }

// setAutoStart включает или выключает запуск при входе (триггер задачи)
func setAutoStart(on bool) {
	if !taskExists() {
		return
	}
	if isAdmin() { // задача указывает на установленную копию в Program Files, а не на ту, что запущена сейчас
		if err := createTask(taskExe()); err != nil {
			logLine("автозапуск: " + err.Error())
		}
		return
	}
	flag := "/DISABLE"
	if on {
		flag = "/ENABLE"
	}
	hidden("schtasks", "/Change", "/TN", taskName, flag).Run()
}

func startMenuLnk() string {
	appdata, _ := os.UserConfigDir()
	return filepath.Join(appdata, `Microsoft\Windows\Start Menu\Programs\monolit.lnk`)
}

// shortcut: ярлык lnk на exe с --launch — запускает задачу планировщика
func shortcut(lnk, exe string) error {
	q := func(s string) string { return strings.ReplaceAll(s, "'", "''") } // кавычка в пути внутри '...' PowerShell
	ps := fmt.Sprintf(`$s=(New-Object -ComObject WScript.Shell).CreateShortcut('%s');$s.TargetPath='%s';$s.Arguments='--launch';$s.IconLocation='%s,0';$s.Description='monolit VPN';$s.Save()`,
		q(lnk), q(exe), q(exe))
	if out, err := hidden("powershell", "-NoProfile", "-NonInteractive", "-Command", ps).CombinedOutput(); err != nil {
		return fmt.Errorf("ярлык: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// ---------- один экземпляр ----------

var (
	findWindow          = user32.NewProc("FindWindowW")
	showWindow          = user32.NewProc("ShowWindow")
	setForegroundWindow = user32.NewProc("SetForegroundWindow")
)

// singleInstance: если программа уже запущена — показать её окно и вернуть false
func singleInstance() bool {
	name, _ := syscall.UTF16PtrFromString("Local\\monolit-client")
	_, err := windows.CreateMutex(nil, false, name)
	if err == nil {
		return true
	}
	title, _ := syscall.UTF16PtrFromString("Monolit") // заголовок окна, как в main.go
	if h, _, _ := findWindow.Call(0, uintptr(unsafe.Pointer(title))); h != 0 {
		showWindow.Call(h, 9) // SW_RESTORE
		setForegroundWindow.Call(h)
	}
	return false
}

// Выход по команде: `monolit --quit` просит работающую копию выйти так же, как «Выход» в трее.
// Нужно установке новой версии без участия пользователя. Событие создаёт работающая программа (она
// с правами администратора), поэтому попросить может только процесс с такими же правами
// var, а не const: тесты подменяют имя, иначе прогон закрыл бы работающую программу
var quitEvent = "Local\\monolit-client-quit"

func quitWatch(quit func()) {
	name, _ := syscall.UTF16PtrFromString(quitEvent)
	h, _ := windows.CreateEvent(nil, 0, 0, name) // само сбрасывается, сначала не выставлено
	if h == 0 {
		return
	}
	safeGo("quit", func() {
		if s, _ := windows.WaitForSingleObject(h, windows.INFINITE); s == windows.WAIT_OBJECT_0 {
			quit()
		}
	})
}

// sendQuit: false — программа не запущена или старой версии (без --quit)
func sendQuit() bool {
	name, _ := syscall.UTF16PtrFromString(quitEvent)
	h, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, name)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	return windows.SetEvent(h) == nil
}

// restartSelf запускает новую копию (она дождётся выхода этой) и закрывает текущую
func restartSelf() {
	exe, _ := os.Executable()
	c := hidden(exe, "--wait-pid", fmt.Sprint(os.Getpid()))
	if c.Start() == nil {
		logLine("выход: перезапуск после обновления")
		app.Disconnect()
		quitting.Store(true)
		os.Exit(0)
	}
}

// waitPid ждёт, пока старая копия закроется (после обновления)
func waitPid(pid int) {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return
	}
	windows.WaitForSingleObject(h, 15000)
	windows.CloseHandle(h)
}
