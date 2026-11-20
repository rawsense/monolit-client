package core

import (
	"encoding/json"
	"reflect"
	"testing"
)

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
