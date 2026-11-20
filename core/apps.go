package core

// Программы для списков «через VPN» и «напрямую»: что сейчас работает, с понятным названием и значком.
// Какие программы запущены, как они называются и какой у них значок, знает оболочка системы (Platform)

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"

	"github.com/xtls/xray-core/app/dispatcher"
)

// ProcRow — программа в окне выбора
type ProcRow struct {
	Exe    string `json:"exe"`    // VALORANT-Win64-Shipping.exe: так программа попадает в правило
	Name   string `json:"name"`   // VALORANT: из описания файла, иначе имя файла без .exe
	Path   string `json:"path"`   // полный путь: по нему значок и название
	Icon   string `json:"icon"`   // data:image/png;base64,… или пусто
	Net    bool   `json:"net"`    // ходила в сеть через Monolit за последние 30 минут
	Listed bool   `json:"listed"` // уже в списке «через VPN»
}

// procList: программы, которые ходили в сеть через Monolit, и те, что человек видит на экране.
// Системные и саму программу не показываем: их в список «через VPN» не добавляют
func (a *App) procList() []ProcRow {
	traffic := map[string]int64{} // путь в нижнем регистре → байт через Monolit
	paths := map[string]string{}  // путь в нижнем регистре → как есть
	for _, c := range dispatcher.Conns() {
		if c.ProcPath == "" {
			continue
		}
		p := filepath.FromSlash(c.ProcPath)
		k := strings.ToLower(p)
		traffic[k] += c.Up + c.Down
		paths[k] = p
	}
	for _, p := range plat.AppsRunning() {
		if k := strings.ToLower(p); paths[k] == "" {
			paths[k] = p
		}
	}

	a.mu.Lock()
	listed := map[string]bool{}
	for _, r := range a.set.Apps {
		listed[strings.ToLower(r.Domain)] = true
	}
	a.mu.Unlock()

	var rows []ProcRow
	for k, p := range paths {
		if plat.AppSkip(p) {
			continue
		}
		exe := filepath.Base(p)
		_, net := traffic[k]
		rows = append(rows, ProcRow{Exe: exe, Name: plat.AppTitle(p), Path: p, Icon: plat.AppIcon(p), Net: net, Listed: listed[strings.ToLower(exe)]})
	}
	// сначала то, что ходит в сеть, по трафику; потом остальное по названию
	slices.SortFunc(rows, func(x, y ProcRow) int {
		tx, ty := traffic[strings.ToLower(x.Path)], traffic[strings.ToLower(y.Path)]
		if x.Net != y.Net {
			if x.Net {
				return -1
			}
			return 1
		}
		if tx != ty {
			if tx > ty {
				return -1
			}
			return 1
		}
		return strings.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name))
	})
	// одна программа из двух папок (обновилась): оставить первую, в правило всё равно идёт имя файла
	seen := map[string]bool{}
	return slices.DeleteFunc(rows, func(r ProcRow) bool {
		k := strings.ToLower(r.Exe)
		if seen[k] {
			return true
		}
		seen[k] = true
		return false
	})
}

// addApp: программа по пути к файлу — в список «через VPN»
func (a *App) addApp(path string) error { return a.setApp(path, "vpn") }

// setApp: путь программы — vpn (список «через VPN»), direct (список «напрямую») или auto (ни в одном списке).
// В правило идёт имя файла (так его узнаёт ядро), название и путь — для списка. Программа только в одном списке
func (a *App) setApp(path, mode string) error {
	path = filepath.FromSlash(strings.Trim(strings.TrimSpace(path), `"`)) // из «Соединений» путь приходит с прямыми чертами
	if err := plat.AppCheck(path); err != nil {
		return err
	}
	if mode != "vpn" && mode != "direct" && mode != "auto" {
		return errors.New("нет такого пути: " + mode)
	}
	exe := filepath.Base(path)
	name := ""
	if mode != "auto" {
		name = plat.AppTitle(path)
	}
	a.mu.Lock()
	same := func(r Rule) bool { return strings.EqualFold(r.Domain, exe) }
	a.set.Apps = slices.DeleteFunc(slices.Clone(a.set.Apps), same)
	a.set.AppsDirect = slices.DeleteFunc(slices.Clone(a.set.AppsDirect), same)
	r := Rule{Domain: exe, App: name, Path: path, When: now()}
	switch mode {
	case "vpn":
		a.set.Apps = append(a.set.Apps, r)
	case "direct":
		a.set.AppsDirect = append(a.set.AppsDirect, r)
	}
	a.mu.Unlock()
	safeGo("apply", func() { a.apply(false) })
	return nil
}
