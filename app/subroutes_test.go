package main

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const subRoutesCfg = `[{
 "remarks": "Stockholm",
 "outbounds": [
  {"tag": "proxy", "protocol": "vless", "settings": {"vnext": [{"address": "vpn.example.com", "port": 443, "users": [{"id": "11111111-1111-4111-a111-111111111111", "encryption": "none"}]}]}, "streamSettings": {"network": "tcp", "security": "tls", "tlsSettings": {"serverName": "vpn.example.com"}}},
  {"tag": "other", "protocol": "vless", "settings": {"vnext": [{"address": "evil.example.com", "port": 443, "users": [{"id": "11111111-1111-4111-a111-111111111111", "encryption": "none"}]}]}},
  {"tag": "direct", "protocol": "freedom"},
  {"tag": "block", "protocol": "blackhole"}
 ],
 "routing": {"domainStrategy": "IPIfNonMatch", "rules": [
  {"type": "field", "domain": ["geosite:category-ads-all"], "outboundTag": "block"},
  {"type": "field", "domain": ["domain:habr.com"], "outboundTag": "proxy"},
  {"type": "field", "domain": ["domain:bank.com"], "outboundTag": "other"},
  {"type": "field", "domain": ["domain:ifconfig.me", "geosite:no-such-group", "domain:ru"], "outboundTag": "direct"},
  {"type": "field", "ip": ["geoip:ru"], "outboundTag": "direct"},
  {"type": "field", "domain": ["geosite:no-such-group"], "outboundTag": "direct"},
  {"type": "field", "inboundTag": ["x"], "domain": ["domain:a.com"], "outboundTag": "direct"},
  {"type": "field", "network": "udp", "port": 443, "outboundTag": "block"},
  {"type": "field", "domain": ["domain:q.com"], "network": "udp", "port": 443, "outboundTag": "block"},
  {"type": "field", "network": "tcp,udp", "outboundTag": "direct"}
 ]}
}]`

func subRoutesConns(t *testing.T) []Conn {
	t.Helper()
	conns, err := parseSubBody([]byte(subRoutesCfg))
	if err != nil || len(conns) != 1 {
		t.Fatalf("разбор: %v, подключений %d", err, len(conns))
	}
	return conns
}

// из конфига берутся только правила по сайтам и адресам: напрямую, блокировка и сам сервер конфига
func TestSubRoutesParse(t *testing.T) {
	got := subRoutesConns(t)[0].Routes
	want := []SubRoute{
		{To: "block", Domain: []string{"geosite:category-ads-all"}},
		{To: "vpn", Domain: []string{"domain:habr.com"}},
		{To: "direct", Domain: []string{"domain:ifconfig.me", "geosite:no-such-group", "domain:ru"}},
		{To: "direct", IP: []string{"geoip:ru"}},
		{To: "direct", Domain: []string{"geosite:no-such-group"}},
		{To: "block", Domain: []string{"domain:q.com"}, Port: "443", Network: "udp"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("маршруты:\n%+v\nждали:\n%+v", got, want)
	}
}

type subTestRule struct {
	RuleTag     string   `json:"ruleTag"`
	OutboundTag string   `json:"outboundTag"`
	Domain      []string `json:"domain"`
	IP          []string `json:"ip"`
}

func subTestConfig(t *testing.T, s Settings) (rules []subTestRule, dnsLocal []string) {
	t.Helper()
	old := routeEntryOK
	routeEntryOK = func(_, e string) bool { return e != "geosite:no-such-group" }
	defer func() { routeEntryOK = old }()
	s.Port, s.DNS = 10808, "https://1.1.1.1/dns-query"
	raw, err := buildConfig(s, Sub{Conns: subRoutesConns(t)}, false, "")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Routing struct {
			Rules []subTestRule `json:"rules"`
		} `json:"routing"`
		DNS struct {
			Servers []json.RawMessage `json:"servers"`
		} `json:"dns"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	var first struct {
		Domains []string `json:"domains"`
	}
	json.Unmarshal(cfg.DNS.Servers[0], &first)
	return cfg.Routing.Rules, first.Domains
}

func TestSubRoutesOff(t *testing.T) {
	rules, local := subTestConfig(t, Settings{Mode: "all"})
	for _, r := range rules {
		if strings.HasPrefix(r.RuleTag, "sub-") {
			t.Fatalf("настройка выключена, а правило есть: %+v", r)
		}
	}
	if slices.Contains(local, "domain:ifconfig.me") {
		t.Fatal("настройка выключена, а сайт подписки в прямом DNS")
	}
}

func TestSubRoutesOn(t *testing.T) {
	for _, mode := range []string{"all", "smart", "ai"} {
		rules, local := subTestConfig(t, Settings{Mode: mode, SubRoutes: true, BlockAds: true,
			Direct: []Rule{{Domain: "b.com"}}, Apps: []Rule{{Domain: "x.exe"}}})
		var got []string
		pos := map[string]int{}
		seen := map[string]bool{}
		for i, r := range rules {
			if seen[r.RuleTag] {
				t.Fatalf("%s: повтор ruleTag %s", mode, r.RuleTag)
			}
			seen[r.RuleTag] = true
			pos[r.RuleTag] = i
			if strings.Contains(r.RuleTag, "sub-") {
				got = append(got, r.RuleTag+">"+r.OutboundTag+" "+strings.Join(append(r.Domain, r.IP...), ","))
			}
		}
		want := []string{
			"sub-0>block geosite:category-ads-all",
			"sub-1>px-0 domain:habr.com",
			"sub-2>direct-ru domain:ifconfig.me,domain:ru", // неизвестная группа выброшена, остальное осталось
			"nameless-sub-3>direct-ru geoip:ru",            // только адреса: решает, лишь когда имя сайта неизвестно
			"sub-5>block domain:q.com",                     // sub-4 целиком из неизвестной группы — правила нет
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s:\n%s\nждали:\n%s", mode, strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
		// свои списки, программы и сайты ИИ — выше подписки, правила режима — ниже
		for _, above := range []string{"my-direct", "apps", "ai"} {
			if pos[above] > pos["sub-0"] {
				t.Errorf("%s: %s ниже правил подписки", mode, above)
			}
		}
		if pos["rest"] < pos["sub-5"] {
			t.Errorf("%s: правила подписки ниже правила «остальное»", mode)
		}
		if !slices.Contains(local, "domain:ifconfig.me") || slices.Contains(local, "domain:habr.com") || slices.Contains(local, "domain:q.com") {
			t.Errorf("%s: прямой DNS: %v", mode, local)
		}
	}
}

// конфиг с правилами подписки ядро принимает целиком
func TestSubRoutesCoreAccepts(t *testing.T) {
	routes := []SubRoute{
		{To: "direct", Domain: []string{"domain:ifconfig.me", "full:checkip.amazonaws.com", "regexp:("}},
		{To: "block", Domain: []string{"domain:q.com"}, Port: "443", Network: "udp"},
		{To: "block", Domain: []string{"domain:w.com"}, Port: "nope"},
		{To: "direct", IP: []string{"10.1.0.0/16", "not-an-ip"}},
	}
	rules, direct := subRouteRules(routes, func(r genRule) genRule { r["outboundTag"] = "px-0"; return r })
	if len(rules) != 3 {
		t.Fatalf("правил %d: %+v", len(rules), rules)
	}
	if !reflect.DeepEqual(direct, []string{"domain:ifconfig.me", "full:checkip.amazonaws.com"}) {
		t.Fatalf("прямые сайты: %v", direct)
	}
	for _, r := range rules {
		if !buildsRule(r.(genRule)) {
			t.Errorf("ядро не принимает %+v", r)
		}
	}
}

func TestSameRoutes(t *testing.T) {
	a := Sub{Conns: subRoutesConns(t)}
	b := Sub{Conns: subRoutesConns(t)}
	if !sameRoutes(a, b) {
		t.Fatal("одинаковые подписки считаются разными")
	}
	b.Conns[0].Routes = b.Conns[0].Routes[1:]
	if sameRoutes(a, b) {
		t.Fatal("разные маршруты считаются одинаковыми")
	}
	if !sameConns(a, b) {
		t.Fatal("маршруты не должны влиять на сравнение серверов")
	}
}
