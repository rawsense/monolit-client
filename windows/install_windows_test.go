package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Только чистые помощники и временные папки: настоящий Program Files и планировщик не трогаем

func TestPickProgramFiles(t *testing.T) {
	cases := []struct{ known, env, want string }{
		{`D:\Program Files`, `C:\Program Files`, `D:\Program Files`},
		{"", `E:\Apps`, `E:\Apps`},
		{"", "", `C:\Program Files`},
		{"  ", `relative\dir`, `C:\Program Files`}, // относительный путь из окружения не берём
		{`C:\Program Files\`, "", `C:\Program Files`},
	}
	for _, c := range cases {
		if got := pickProgramFiles(c.known, c.env); got != c.want {
			t.Errorf("pickProgramFiles(%q, %q) = %q, want %q", c.known, c.env, got, c.want)
		}
	}
}

func TestSameDir(t *testing.T) {
	if !sameDir(`C:\Program Files\monolit`, `c:\program files\MONOLIT\`) {
		t.Error("регистр и хвостовой разделитель не должны мешать")
	}
	if sameDir(`C:\Program Files\monolit`, `C:\Program Files\monolit2`) {
		t.Error("разные папки совпали")
	}
	if sameDir("", "") {
		t.Error("пустые пути не считаются одной папкой")
	}
}

func TestInstallDirIsProgramFiles(t *testing.T) {
	d := installDir()
	if filepath.Base(d) != "monolit" || !filepath.IsAbs(d) {
		t.Fatalf("installDir() = %q", d)
	}
	if l := legacyDir(); sameDir(d, l) {
		t.Fatalf("installDir совпал со старой папкой %q", l)
	}
}

// fakePE: минимальный заголовок MZ…PE, которого хватает isPE
func fakePE(extra string) []byte {
	b := make([]byte, 0x80)
	b[0], b[1] = 'M', 'Z'
	b[0x3C] = 0x40
	copy(b[0x40:], "PE\x00\x00")
	return append(b, extra...)
}

func TestVerifyCopy(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.exe")
	dst := filepath.Join(dir, "dst.exe")
	os.WriteFile(src, fakePE("payload"), 0o644)

	if err := copyFile(src, dst); err != nil {
		t.Fatal(err)
	}
	if err := verifyCopy(src, dst); err != nil {
		t.Fatalf("верная копия не прошла: %v", err)
	}

	os.WriteFile(dst, fakePE("payloaX"), 0o644) // тот же размер, другое содержимое
	if verifyCopy(src, dst) == nil {
		t.Error("другая сумма прошла")
	}
	os.WriteFile(dst, fakePE("pay"), 0o644)
	if verifyCopy(src, dst) == nil {
		t.Error("другой размер прошёл")
	}

	// одинаковые файлы, но не exe
	txt := filepath.Join(dir, "a.txt")
	txt2 := filepath.Join(dir, "b.txt")
	os.WriteFile(txt, []byte("просто текст, не программа Windows................................"), 0o644)
	os.WriteFile(txt2, []byte("просто текст, не программа Windows................................"), 0o644)
	if verifyCopy(txt, txt2) == nil {
		t.Error("не-exe прошёл")
	}
	if verifyCopy(src, filepath.Join(dir, "нет.exe")) == nil {
		t.Error("отсутствующая копия прошла")
	}
}

func TestWriteVerified(t *testing.T) {
	p := filepath.Join(t.TempDir(), "wintun.dll")
	if err := writeVerified(p, []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := writeVerified(p, []byte("two")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "two" {
		t.Fatalf("содержимое %q", b)
	}
	if _, err := os.Stat(p + ".new"); err == nil {
		t.Error("временный файл остался")
	}
	if err := writeVerified(p, []byte("two")); err != nil { // то же содержимое — без перезаписи
		t.Fatal(err)
	}
}

// --quit: работающая копия получает просьбу выйти; без неё просить некого
func TestQuitEvent(t *testing.T) {
	old := quitEvent
	defer func() { quitEvent = old }()
	quitEvent = fmt.Sprintf(`Local\monolit-test-quit-%d`, os.Getpid())
	if sendQuit() {
		t.Fatal("просьба ушла, хотя программа не запущена")
	}
	got := make(chan struct{})
	quitWatch(func() { close(got) })
	if !sendQuit() {
		t.Fatal("просьба не ушла")
	}
	select {
	case <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("программа не получила просьбу выйти")
	}
}

// Папку программы удаляем целиком, поэтому чужую или пустую папку удаление трогать не должно
func TestSafeInstallDir(t *testing.T) {
	root := t.TempDir()
	good := filepath.Join(root, "Program Files", "monolit")
	os.MkdirAll(good, 0o755)
	if safeInstallDir(good) {
		t.Error("папка без monolit.exe не должна считаться нашей")
	}
	os.WriteFile(filepath.Join(good, "monolit.exe"), []byte("x"), 0o644)
	if !safeInstallDir(good) {
		t.Error("папка monolit с monolit.exe — наша")
	}
	other := filepath.Join(root, "Program Files", "other")
	os.MkdirAll(other, 0o755)
	os.WriteFile(filepath.Join(other, "monolit.exe"), []byte("x"), 0o644)
	if safeInstallDir(other) || safeInstallDir(`C:\`) || safeInstallDir(`C:\monolit`) {
		t.Error("чужая папка или корень диска не должны считаться нашими")
	}
}
