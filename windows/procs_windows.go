package main

// Программы для списка «через VPN»: что сейчас работает, с понятным названием и значком, и выбор файла
// окном Windows. Вписывать имя exe руками не нужно: у игры оно бывает вроде VALORANT-Win64-Shipping.exe

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32p            = syscall.NewLazyDLL("user32.dll")
	gdi32p             = syscall.NewLazyDLL("gdi32.dll")
	shell32p           = syscall.NewLazyDLL("shell32.dll")
	comdlg32p          = syscall.NewLazyDLL("comdlg32.dll")
	pEnumWindows       = user32p.NewProc("EnumWindows")
	pIsWindowVisible   = user32p.NewProc("IsWindowVisible")
	pGetWindowTextLen  = user32p.NewProc("GetWindowTextLengthW")
	pGetWindowPid      = user32p.NewProc("GetWindowThreadProcessId")
	pGetIconInfo       = user32p.NewProc("GetIconInfo")
	pDestroyIcon       = user32p.NewProc("DestroyIcon")
	pGetDC             = user32p.NewProc("GetDC")
	pReleaseDC         = user32p.NewProc("ReleaseDC")
	pGetObject         = gdi32p.NewProc("GetObjectW")
	pGetDIBits         = gdi32p.NewProc("GetDIBits")
	pDeleteObject      = gdi32p.NewProc("DeleteObject")
	pExtractIconEx     = shell32p.NewProc("ExtractIconExW")
	pGetOpenFileName   = comdlg32p.NewProc("GetOpenFileNameW")
	pCommDlgExtendedEr = comdlg32p.NewProc("CommDlgExtendedError")
)

// runningApps — программы этого сеанса с окном на экране
func runningApps() []string {
	var sess uint32
	windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &sess)
	var paths []string
	for pid := range windowPids() {
		var s uint32
		if windows.ProcessIdToSessionId(pid, &s) != nil || s != sess {
			continue
		}
		if p := pidPath(pid); p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}

// skipApp: системное (из папки Windows), сама программа и не exe — в список «через VPN» не предлагаем
func skipApp(p string) bool {
	self, _ := os.Executable()
	sysDir := strings.ToLower(os.Getenv("SystemRoot"))
	if sysDir == "" {
		sysDir = `c:\windows`
	}
	k := strings.ToLower(p)
	return strings.HasPrefix(k, sysDir+`\`) || strings.EqualFold(p, self) || !strings.HasSuffix(k, ".exe")
}

// windowPids — процессы с видимым окном, у которого есть заголовок: то, что человек видит на экране.
// Обратный вызов один на программу: syscall.NewCallback не освобождается, новый на каждый вызов — утечка
var (
	winMu   sync.Mutex
	winPids map[uint32]bool
	winCB   = syscall.NewCallback(func(h, _ uintptr) uintptr {
		if v, _, _ := pIsWindowVisible.Call(h); v == 0 {
			return 1
		}
		if n, _, _ := pGetWindowTextLen.Call(h); n == 0 {
			return 1
		}
		var pid uint32
		pGetWindowPid.Call(h, uintptr(unsafe.Pointer(&pid)))
		if pid != 0 {
			winPids[pid] = true
		}
		return 1
	})
)

func windowPids() map[uint32]bool {
	winMu.Lock()
	defer winMu.Unlock()
	winPids = map[uint32]bool{}
	pEnumWindows.Call(winCB, 0)
	return winPids
}

func pidPath(pid uint32) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}

var (
	exeMu    sync.Mutex
	titleMem = map[string]string{}
	iconMem  = map[string]string{}
)

// exeTitle — описание файла («VALORANT», «Discord»), как в диспетчере задач; нет описания — имя без .exe
func exeTitle(path string) string {
	k := strings.ToLower(path)
	exeMu.Lock()
	t, ok := titleMem[k]
	exeMu.Unlock()
	if ok {
		return t
	}
	t = fileDescription(path)
	if t == "" {
		t = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	exeMu.Lock()
	titleMem[k] = t
	exeMu.Unlock()
	return t
}

func fileDescription(path string) string {
	size, err := windows.GetFileVersionInfoSize(path, nil)
	if err != nil || size == 0 {
		return ""
	}
	buf := make([]byte, size)
	if windows.GetFileVersionInfo(path, 0, size, unsafe.Pointer(&buf[0])) != nil {
		return ""
	}
	var tr unsafe.Pointer
	var trLen uint32
	langs := []string{"040904b0", "040904e4", "041904b0"}
	if windows.VerQueryValue(unsafe.Pointer(&buf[0]), `\VarFileInfo\Translation`, unsafe.Pointer(&tr), &trLen) == nil && trLen >= 4 {
		p := (*[2]uint16)(tr)
		langs = append([]string{fmt.Sprintf("%04x%04x", p[0], p[1])}, langs...)
	}
	for _, l := range langs {
		var v unsafe.Pointer
		var vLen uint32
		if windows.VerQueryValue(unsafe.Pointer(&buf[0]), `\StringFileInfo\`+l+`\FileDescription`, unsafe.Pointer(&v), &vLen) != nil || vLen == 0 {
			continue
		}
		if s := strings.TrimSpace(windows.UTF16PtrToString((*uint16)(v))); s != "" {
			return s
		}
	}
	return ""
}

// exeIcon — значок программы 32×32 в PNG (data URL). Нет значка — пусто, интерфейс рисует общий
func exeIcon(path string) string {
	k := strings.ToLower(path)
	exeMu.Lock()
	s, ok := iconMem[k]
	exeMu.Unlock()
	if ok {
		return s
	}
	if img := iconImage(path); img != nil {
		var b bytes.Buffer
		if png.Encode(&b, img) == nil {
			s = "data:image/png;base64," + base64.StdEncoding.EncodeToString(b.Bytes())
		}
	}
	exeMu.Lock()
	iconMem[k] = s
	exeMu.Unlock()
	return s
}

type iconInfo struct {
	fIcon          int32
	xHot, yHot     uint32
	hbmMask, hbmCl uintptr
}

type bitmapObj struct {
	typ, width, height, widthBytes int32
	planes, bitsPixel              uint16
	bits                           uintptr
}

type bitmapInfoHeader struct {
	size                        uint32
	width, height               int32
	planes, bitCount            uint16
	compression, sizeImage      uint32
	xPels, yPels, clrUsed, clrI int32
}

func iconImage(path string) image.Image {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil
	}
	var large uintptr
	if n, _, _ := pExtractIconEx.Call(uintptr(unsafe.Pointer(p)), 0, uintptr(unsafe.Pointer(&large)), 0, 1); n == 0 || large == 0 {
		return nil
	}
	defer pDestroyIcon.Call(large)
	var ii iconInfo
	if r, _, _ := pGetIconInfo.Call(large, uintptr(unsafe.Pointer(&ii))); r == 0 {
		return nil
	}
	defer pDeleteObject.Call(ii.hbmMask)
	if ii.hbmCl == 0 {
		return nil // одноцветный значок: редкость, рисуем общий
	}
	defer pDeleteObject.Call(ii.hbmCl)
	var bm bitmapObj
	if r, _, _ := pGetObject.Call(ii.hbmCl, unsafe.Sizeof(bm), uintptr(unsafe.Pointer(&bm))); r == 0 || bm.width <= 0 || bm.height <= 0 || bm.width > 256 || bm.height > 256 {
		return nil
	}
	w, h := int(bm.width), int(bm.height)
	dc, _, _ := pGetDC.Call(0)
	defer pReleaseDC.Call(0, dc)
	bits := func(hbm uintptr) []byte {
		hdr := bitmapInfoHeader{size: 40, width: int32(w), height: -int32(h), planes: 1, bitCount: 32}
		buf := make([]byte, w*h*4)
		// BITMAPINFO = заголовок + таблица цветов; для 32 бит таблицы нет, но место под неё GetDIBits пишет
		info := make([]byte, 40+256*4)
		*(*bitmapInfoHeader)(unsafe.Pointer(&info[0])) = hdr
		if r, _, _ := pGetDIBits.Call(dc, hbm, 0, uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&info[0])), 0); r == 0 {
			return nil
		}
		return buf
	}
	cl := bits(ii.hbmCl)
	if cl == nil {
		return nil
	}
	hasAlpha := false
	for i := 3; i < len(cl); i += 4 {
		if cl[i] != 0 {
			hasAlpha = true
			break
		}
	}
	var mask []byte
	if !hasAlpha {
		mask = bits(ii.hbmMask) // старый значок без прозрачности: прозрачное — по маске (белое в маске)
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w*h; i++ {
		b, g, r, al := cl[i*4], cl[i*4+1], cl[i*4+2], cl[i*4+3]
		if !hasAlpha {
			al = 255
			if mask != nil && mask[i*4] != 0 {
				al = 0
			}
		}
		img.SetNRGBA(i%w, i/w, color.NRGBA{r, g, b, al})
	}
	return img
}

type openFileName struct {
	structSize    uint32
	owner         uintptr
	instance      uintptr
	filter        *uint16
	customFilter  *uint16
	maxCustFilter uint32
	filterIndex   uint32
	file          *uint16
	maxFile       uint32
	fileTitle     *uint16
	maxFileTitle  uint32
	initialDir    *uint16
	title         *uint16
	flags         uint32
	fileOffset    uint16
	fileExtension uint16
	defExt        *uint16
	custData      uintptr
	hook          uintptr
	templateName  *uint16
	reserved      uintptr
	reserved2     uint32
	flagsEx       uint32
}

// pickExe — окно Windows «Открыть» с фильтром по программам. "" — закрыли без выбора
func pickExe() (string, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	u := func(s string) []uint16 { return utf16z(s) }
	filter := append(append(u("Программы (*.exe)"), u("*.exe")...), 0)
	title := u("Программа, которая пойдёт через VPN")
	buf := make([]uint16, 32768)
	ofn := openFileName{filter: &filter[0], filterIndex: 1, file: &buf[0], maxFile: uint32(len(buf)), title: &title[0],
		flags: 0x00080000 | 0x00001000 | 0x00000800 | 0x00000008, owner: mainHwnd} // EXPLORER, FILEMUSTEXIST, PATHMUSTEXIST, NOCHANGEDIR
	ofn.structSize = uint32(unsafe.Sizeof(ofn))
	if r, _, _ := pGetOpenFileName.Call(uintptr(unsafe.Pointer(&ofn))); r == 0 {
		if code, _, _ := pCommDlgExtendedEr.Call(); code != 0 {
			return "", fmt.Errorf("окно выбора файла не открылось (код %d)", code)
		}
		return "", nil
	}
	return windows.UTF16ToString(buf), nil
}

func utf16z(s string) []uint16 {
	r, _ := windows.UTF16FromString(s)
	return r
}
