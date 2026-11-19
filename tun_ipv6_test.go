package main

import (
	"encoding/json"
	"net"
	"reflect"
	"strings"
	"testing"
)

// команды IPv6: только store=active и на номер адаптера TUN, маршруты ::/1 и 8000::/1
func TestTunV6Cmds(t *testing.T) {
	got := tunV6Cmds("42")
	want := [][]string{
		{"netsh", "interface", "ipv6", "set", "interface", "interface=42", "dadtransmits=0", "routerdiscovery=disabled", "metric=1", "store=active"},
		{"netsh", "interface", "ipv6", "add", "address", "interface=42", "address=fdfe:dcba:9876::1/126", "store=active"},
		{"netsh", "interface", "ipv6", "add", "route", "prefix=::/1", "interface=42", "metric=1", "store=active"},
		{"netsh", "interface", "ipv6", "add", "route", "prefix=8000::/1", "interface=42", "metric=1", "store=active"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tunV6Cmds:\n%q\nждали\n%q", got, want)
	}
	for _, c := range got {
		if c[len(c)-1] != "store=active" {
			t.Errorf("не store=active: %q", c)
		}
	}
	// два маршрута вместе покрывают всё ::/0
	_, a, _ := net.ParseCIDR(tunV6Routes[0])
	_, b, _ := net.ParseCIDR(tunV6Routes[1])
	for _, ip := range []string{"2a00:1450::1", "::1", "fe80::1", "ffff::1", "2001:db8::1"} {
		x := net.ParseIP(ip)
		if !a.Contains(x) && !b.Contains(x) {
			t.Errorf("%s не попадает в маршруты", ip)
		}
	}
}

// снятие: те же маршруты и адрес, что добавили; IPv4 — как раньше
func TestTunDownCmds(t *testing.T) {
	got := tunDownCmds("7")
	want := [][]string{
		{"route", "delete", "0.0.0.0", "mask", "128.0.0.0", "172.19.0.2", "if", "7"},
		{"route", "delete", "128.0.0.0", "mask", "128.0.0.0", "172.19.0.2", "if", "7"},
		{"netsh", "interface", "ipv6", "delete", "route", "prefix=::/1", "interface=7", "store=active"},
		{"netsh", "interface", "ipv6", "delete", "route", "prefix=8000::/1", "interface=7", "store=active"},
		{"netsh", "interface", "ipv6", "delete", "address", "interface=7", "address=fdfe:dcba:9876::1", "store=active"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tunDownCmds:\n%q\nждали\n%q", got, want)
	}
}

// IPv4 не изменился
func TestTunV4Cmds(t *testing.T) {
	got := tunV4Cmds("3")
	if len(got) != 5 || got[0][4] != "address" || got[3][0] != "route" || got[4][len(got[4])-1] != "3" {
		t.Fatalf("tunV4Cmds: %q", got)
	}
	for _, c := range got {
		if strings.Contains(strings.Join(c, " "), "ipv6") {
			t.Errorf("IPv6 в командах IPv4: %q", c)
		}
	}
}

func TestIsGlobalV6(t *testing.T) {
	for ip, want := range map[string]bool{
		"2a00:1450::1": true, "fd00::5": true, "fe80::1": false, "::1": false, "192.168.1.1": false, "::ffff:1.2.3.4": false,
	} {
		if got := isGlobalV6(net.ParseIP(ip)); got != want {
			t.Errorf("isGlobalV6(%s) = %v, ждали %v", ip, got, want)
		}
	}
}

type testRule struct {
	InboundTag  []string `json:"inboundTag"`
	IP          []string `json:"ip"`
	Port        string   `json:"port"`
	OutboundTag string   `json:"outboundTag"`
}

func ipv6TestRules(t *testing.T, tun bool, mode string) []testRule {
	t.Helper()
	sub := Sub{Conns: []Conn{{Tag: "px-0", Name: "a", Server: "vpn.example.com", Outbound: json.RawMessage(`{"protocol":"vless"}`)}}}
	raw, err := buildConfig(Settings{Mode: mode, Port: 10808, DNS: "https://1.1.1.1/dns-query"}, sub, tun, "Ethernet")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Routing struct {
			Rules []testRule `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg.Routing.Rules
}

// правило блокировки IPv6: в TUN сразу за DNS, до всех остальных; в режиме прокси его нет
func TestBuildConfigIPv6Block(t *testing.T) {
	for _, mode := range []string{"smart", "all", "ai"} {
		rs := ipv6TestRules(t, true, mode)
		if len(rs) < 2 {
			t.Fatalf("%s: мало правил", mode)
		}
		if rs[0].Port != "53" || rs[0].OutboundTag != "dns-out" {
			t.Errorf("%s: первое правило не DNS: %+v", mode, rs[0])
		}
		r := rs[1]
		if !reflect.DeepEqual(r.InboundTag, []string{"tun-in"}) || !reflect.DeepEqual(r.IP, []string{"::/0"}) || r.OutboundTag != "block" {
			t.Errorf("%s: второе правило не блок IPv6: %+v", mode, r)
		}
		for i, x := range rs {
			if i != 1 && reflect.DeepEqual(x.IP, []string{"::/0"}) {
				t.Errorf("%s: лишнее правило ::/0 под номером %d", mode, i)
			}
		}
	}
	for _, r := range ipv6TestRules(t, false, "smart") {
		if reflect.DeepEqual(r.IP, []string{"::/0"}) {
			t.Errorf("в режиме прокси правило IPv6: %+v", r)
		}
	}
}
