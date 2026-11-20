package main

import (
	"os"
	"strings"
	"testing"

	"monolit-client/core"
)

// запущенные программы: сама программа теста не показывается, у найденных есть название и имя файла
func TestProcList(t *testing.T) {
	a := core.New(true)
	rows, _ := a.Bindings()["mpProcs"].(func() ([]core.ProcRow, error))()
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
	a := core.New(true)
	b := a.Bindings()
	addApp := b["mpAppAdd"].(func(string) error)
	setApp := b["mpAppMode"].(func(string, string) error)
	settings := b["mpSettings"].(func() core.Settings)
	p := os.Getenv("SystemRoot") + `\explorer.exe`
	if err := addApp(p); err != nil {
		t.Fatal(err)
	}
	if err := addApp(strings.ToUpper(p)); err != nil {
		t.Fatal(err)
	}
	if err := addApp(`C:\x\readme.txt`); err == nil {
		t.Fatal("добавился не .exe")
	}
	apps := settings().Apps
	if len(apps) != 1 || !strings.EqualFold(apps[0].Domain, "explorer.exe") || apps[0].Path == "" || apps[0].App == "" {
		t.Fatalf("список программ: %+v", apps)
	}
	// путь «напрямую» переносит программу в другой список, «авто» убирает из обоих
	if err := setApp(p, "direct"); err != nil {
		t.Fatal(err)
	}
	nv, nd := len(settings().Apps), len(settings().AppsDirect)
	if nv != 0 || nd != 1 {
		t.Fatalf("после «напрямую»: через VPN %d, напрямую %d", nv, nd)
	}
	if err := setApp(p, "auto"); err != nil {
		t.Fatal(err)
	}
	nv, nd = len(settings().Apps), len(settings().AppsDirect)
	if nv != 0 || nd != 0 {
		t.Fatalf("после «авто»: через VPN %d, напрямую %d", nv, nd)
	}
	if err := setApp(p, "vpn"); err != nil {
		t.Fatal(err)
	}
	rule := b["mpRule"].(func(string, string, bool) error)
	if err := rule("apps", "EXPLORER.EXE", false); err != nil {
		t.Fatal(err)
	}
	n := len(settings().Apps)
	if n != 0 {
		t.Fatal("не удалилась программа, записанная в другом регистре")
	}
}
