package main

import (
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

var (
	dwmSetWindowAttribute  = syscall.NewLazyDLL("dwmapi.dll").NewProc("DwmSetWindowAttribute")
	createIconFromResource = user32.NewProc("CreateIconFromResourceEx")
	sendMessage            = user32.NewProc("SendMessageW")
	setWindowLongPtr       = user32.NewProc("SetWindowLongPtrW")
	callWindowProc         = user32.NewProc("CallWindowProcW")
	isWindowVisible        = user32.NewProc("IsWindowVisible")
)

// darkTitle: тёмная системная полоса заголовка (Windows 10 20H1+ и 11) в цвет фона
func darkTitle(hwnd uintptr) {
	const useDarkMode, captionColor = 20, 35
	on := int32(1)
	dwmSetWindowAttribute.Call(hwnd, useDarkMode, uintptr(unsafe.Pointer(&on)), 4)
	bg := uint32(0x000a0908) // #08090a в COLORREF (0x00BBGGRR)
	dwmSetWindowAttribute.Call(hwnd, captionColor, uintptr(unsafe.Pointer(&bg)), 4)
}

// hicon из PNG нужного размера
func hicon(png []byte, size int) uintptr {
	h, _, _ := createIconFromResource.Call(uintptr(unsafe.Pointer(&png[0])), uintptr(len(png)), 1, 0x00030000, uintptr(size), uintptr(size), 0)
	return h
}

func setWindowIcon(hwnd uintptr) {
	const wmSetIcon = 0x0080
	sendMessage.Call(hwnd, wmSetIcon, 0, hicon(markPNG(16, colOn), 16))
	sendMessage.Call(hwnd, wmSetIcon, 1, hicon(markPNG(32, colOn), 32))
}

var (
	mainHwnd uintptr
	origProc uintptr
	quitting atomic.Bool
	// endingSession: Windows завершает сеанс, поток окна занят WM_ENDSESSION — рассылки окнам не ждём
	endingSession atomic.Bool
)

// hideOnClose: крестик прячет окно в трей, VPN продолжает работать. Выход — из меню трея
func hideOnClose(hwnd uintptr) {
	mainHwnd = hwnd
	cb := syscall.NewCallback(func(h, msg, wp, lp uintptr) uintptr {
		const wmClose, wmEndSession = 0x0010, 0x0016
		if msg == wmClose && !quitting.Load() {
			showWindow.Call(h, 0) // SW_HIDE
			return 0
		}
		// выход из Windows или перезагрузка: после ответа процесс убьют, системный прокси надо вернуть сейчас
		if msg == wmEndSession && wp != 0 && app != nil {
			quitting.Store(true)
			endingSession.Store(true)
			logLine("выход: завершение сеанса Windows")
			done := make(chan struct{})
			safeGo("disconnect", func() {
				defer close(done)
				app.Disconnect()
			})
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				logLine("выход: отключение не уложилось в 3 с")
			}
			return 0
		}
		r, _, _ := callWindowProc.Call(origProc, h, msg, wp, lp)
		return r
	})
	gwlpWndProc := -4
	origProc, _, _ = setWindowLongPtr.Call(hwnd, uintptr(gwlpWndProc), cb)
}

func showMain() {
	if mainHwnd != 0 {
		showWindow.Call(mainHwnd, 9) // SW_RESTORE
		showWindow.Call(mainHwnd, 5) // SW_SHOW
		setForegroundWindow.Call(mainHwnd)
		if app != nil {
			safeGo("notify", app.Notify) // пока окно было скрыто, обновления шли реже
		}
	}
}

func windowVisible() bool {
	r, _, _ := isWindowVisible.Call(mainHwnd)
	return r != 0
}

var messageBox = user32.NewProc("MessageBoxW")

// fatalBox: у программы нет консоли, поэтому причину, по которой окно не открылось, показываем системным сообщением
func fatalBox(text string) {
	t, _ := syscall.UTF16PtrFromString(text)
	c, _ := syscall.UTF16PtrFromString("Monolit")
	const iconError = 0x10
	messageBox.Call(0, uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(c)), iconError)
}
