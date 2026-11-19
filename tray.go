package main

import (
	"runtime"
	"sync"

	"fyne.io/systray"
)

var (
	icoOn, icoOff []byte
	trayToggle    *systray.MenuItem
	trayReady     = make(chan struct{})
)

// startTray: значок у часов. Щелчок — окно, меню — включить, открыть, выйти
func startTray(onQuit func()) {
	icoOn, icoOff = icoBytes(colOn), icoBytes(colOff)
	go func() {
		runtime.LockOSThread()
		systray.Run(func() {
			systray.SetIcon(icoOff)
			systray.SetTooltip("Monolit: отключено")
			systray.SetOnTapped(showMain)
			open := systray.AddMenuItem("Открыть", "")
			trayToggle = systray.AddMenuItem("Подключить", "")
			systray.AddSeparator()
			quit := systray.AddMenuItem("Выход", "")
			close(trayReady)
			for {
				select {
				case <-open.ClickedCh:
					showMain()
				case <-trayToggle.ClickedCh:
					safeGo("toggle", app.Toggle)
				case <-quit.ClickedCh:
					onQuit()
					return
				}
			}
		}, nil)
	}()
}

var (
	trayMu    sync.Mutex // trayState зовут из разных потоков
	trayWasOn = -1
)

func trayState(on bool, busy string) {
	select {
	case <-trayReady:
	default:
		return
	}
	trayMu.Lock()
	defer trayMu.Unlock()
	v := 0
	if on {
		v = 1
	}
	if busy != "" {
		systray.SetTooltip("Monolit: " + busy)
	}
	if v == trayWasOn {
		return
	}
	trayWasOn = v
	if on {
		systray.SetIcon(icoOn)
		systray.SetTooltip("Monolit: подключено")
		trayToggle.SetTitle("Отключить")
	} else {
		systray.SetIcon(icoOff)
		systray.SetTooltip("Monolit: отключено")
		trayToggle.SetTitle("Подключить")
	}
}
