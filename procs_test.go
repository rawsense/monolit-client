package main

import (
	"os"
	"strings"
	"testing"
)

// запущенные программы: сама программа теста не показывается, у найденных есть название и имя файла
func TestProcList(t *testing.T) {
	a := newApp(true)
	rows := a.procList()
	self, _ := os.Executable()
	icons := 0
	for _, r := range rows {
		if strings.EqualFold(r.Path, self) {
			t.Fatalf("в списке сама программа: %s", r.Path)
		}
		if r.Name == "" || !strings.HasSuffix(strings.ToLower(r.Exe), ".exe") {
			t.Fatalf("строка без названия или имени файла: %+v", r)
		}
		if strings.HasPrefix(r.Icon, "data:image/png;base64,") {
			icons++
		}
	}
	t.Logf("программ %d, со значком %d", len(rows), icons)
	for i, r := range rows {
		if i < 12 {
			t.Logf("%s | %s | сеть %v | значок %d байт", r.Name, r.Exe, r.Net, len(r.Icon))
		}
	}
}

// значок и название берутся из самого файла: у explorer.exe они есть всегда
func TestExeIconTitle(t *testing.T) {
	p := os.Getenv("SystemRoot") + `\explorer.exe`
	if !strings.HasPrefix(exeIcon(p), "data:image/png;base64,") {
		t.Fatal("нет значка у explorer.exe")
	}
	if n := exeTitle(p); n == "" || strings.EqualFold(n, "explorer") {
		t.Logf("название explorer.exe: %q (описания нет?)", n)
	}
}

// добавление по пути: имя файла как есть, повтор в другом регистре не дублирует, удаление без учёта регистра
func TestAddApp(t *testing.T) {
	old := app
	defer func() { app = old }()
	a := newApp(true)
	app = a
	p := os.Getenv("SystemRoot") + `\explorer.exe`
	if err := a.addApp(p); err != nil {
		t.Fatal(err)
	}
	if err := a.addApp(strings.ToUpper(p)); err != nil {
		t.Fatal(err)
	}
	if err := a.addApp(`C:\x\readme.txt`); err == nil {
		t.Fatal("добавился не .exe")
	}
	a.mu.Lock()
	apps := append([]Rule(nil), a.set.Apps...)
	a.mu.Unlock()
	if len(apps) != 1 || !strings.EqualFold(apps[0].Domain, "explorer.exe") || apps[0].Path == "" || apps[0].App == "" {
		t.Fatalf("список программ: %+v", apps)
	}
	// путь «напрямую» переносит программу в другой список, «авто» убирает из обоих
	if err := a.setApp(p, "direct"); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	nv, nd := len(a.set.Apps), len(a.set.AppsDirect)
	a.mu.Unlock()
	if nv != 0 || nd != 1 {
		t.Fatalf("после «напрямую»: через VPN %d, напрямую %d", nv, nd)
	}
	if err := a.setApp(p, "auto"); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	nv, nd = len(a.set.Apps), len(a.set.AppsDirect)
	a.mu.Unlock()
	if nv != 0 || nd != 0 {
		t.Fatalf("после «авто»: через VPN %d, напрямую %d", nv, nd)
	}
	if err := a.setApp(p, "vpn"); err != nil {
		t.Fatal(err)
	}
	rule := a.bindings()["mpRule"].(func(string, string, bool) error)
	if err := rule("apps", "EXPLORER.EXE", false); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	n := len(a.set.Apps)
	a.mu.Unlock()
	if n != 0 {
		t.Fatal("не удалилась программа, записанная в другом регистре")
	}
}
