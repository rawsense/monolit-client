package main

import (
	"strings"

	"github.com/xtls/xray-core/app/dispatcher"
)

// ConnRow — соединение для экрана «Соединения». Подписи причин и путей делает интерфейс
type ConnRow struct {
	ID      uint64 `json:"id"`
	Start   int64  `json:"start"` // unix мс
	End     int64  `json:"end"`   // 0 — открыто
	Net     string `json:"net"`
	Domain  string `json:"domain"`
	Dest    string `json:"dest"`
	In      string `json:"in"`
	Out     string `json:"out"`     // тег выхода
	Path    string `json:"path"`    // vpn, direct, block
	Via     string `json:"via"`     // «Stockholm Hysteria2» для VPN
	Rule    string `json:"rule"`    // тег правила (xcore.go)
	Up      int64  `json:"up"`      // байт
	Down    int64  `json:"down"`    // байт
	First   int64  `json:"first"`   // мс до первого ответа, -1 — не было
	Proc    string `json:"proc"`    // программа
	ProcDir string `json:"procDir"` // путь к ней
}

// conns — открытые и закрытые за 30 минут соединения ядра
func (a *App) conns() []ConnRow {
	a.mu.Lock()
	names := map[string]string{}
	for _, c := range a.sub.Conns {
		names[c.Tag] = strings.TrimSpace(c.ServerName() + " " + c.Proto)
	}
	a.mu.Unlock()
	list := dispatcher.Conns()
	out := make([]ConnRow, 0, len(list))
	for _, c := range list {
		r := ConnRow{ID: c.ID, Start: c.Start.UnixMilli(), Net: c.Network, Domain: c.Domain, Dest: c.Dest, In: c.Inbound,
			Out: c.Outbound, Rule: c.Rule, Up: c.Up, Down: c.Down, First: c.FirstMs, Proc: c.Proc, ProcDir: c.ProcPath}
		if !c.End.IsZero() {
			r.End = c.End.UnixMilli()
		}
		switch g := routeGroup(c.Outbound); g {
		case "vpn":
			r.Path, r.Via = "vpn", names[c.Outbound]
		case "direct", "block":
			r.Path = g
		default:
			r.Path = "direct"
		}
		if r.Proc == "" && c.Inbound == "probe-in" {
			r.Proc = "monolit"
		}
		out = append(out, r)
	}
	return out
}
