package main

import (
	"encoding/json"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func svcTestTags(t *testing.T, s Settings) []string {
	t.Helper()
	sub := Sub{Conns: []Conn{{Tag: "px-0", Name: "a", Server: "vpn.example.com", Outbound: json.RawMessage(`{"protocol":"vless"}`)}}}
	s.Port, s.DNS = 10808, "https://1.1.1.1/dns-query"
	raw, err := buildConfig(s, sub, false, "")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Routing struct {
			Rules []struct {
				RuleTag     string `json:"ruleTag"`
				OutboundTag string `json:"outboundTag"`
			} `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	var tags []string
	for _, r := range cfg.Routing.Rules {
		tags = append(tags, r.RuleTag+">"+r.OutboundTag)
	}
	return tags
}

// ядро не принимает повторяющийся ruleTag (так не применялись маршруты): все режимы, оба способа, много серверов
func TestRuleTagsUnique(t *testing.T) {
	conns := []Conn{
		{Tag: "px-0", Name: "a", Server: "vpn.example.com", Outbound: json.RawMessage(`{"protocol":"vless"}`)},
		{Tag: "px-1", Name: "b", Server: "vpn.example.com", Outbound: json.RawMessage(`{"protocol":"vless"}`)},
		{Tag: "px-2", Name: "c", Server: "vpn2.example.com", Outbound: json.RawMessage(`{"protocol":"vless"}`)},
	}
	for _, mode := range []string{"smart", "all", "ai"} {
		for _, svc := range []map[string]string{nil, {"youtube": svcDirect, "discord": svcDirect}, {"youtube": svcVPN, "discord": svcVPN}, {"youtube": svcDirect}} {
			for _, tun := range []bool{false, true} {
				s := Settings{Mode: mode, Port: 10808, DNS: "https://1.1.1.1/dns-query", BlockAds: true, Svc: svc,
					Always: []Rule{{Domain: "a.com"}}, Direct: []Rule{{Domain: "b.com"}}, Found: []Rule{{Domain: "c.com"}}, Apps: []Rule{{Domain: "x.exe"}}, TempIP: []string{"104.21.46.95"}}
				raw, err := buildConfig(s, Sub{Conns: conns}, tun, "Ethernet")
				if err != nil {
					t.Fatal(err)
				}
				var cfg struct {
					Routing struct {
						Rules []struct {
							RuleTag string `json:"ruleTag"`
						} `json:"rules"`
					} `json:"routing"`
				}
				json.Unmarshal(raw, &cfg)
				seen := map[string]bool{}
				for _, r := range cfg.Routing.Rules {
					if r.RuleTag == "" || seen[r.RuleTag] {
						t.Fatalf("%s tun=%v svc=%v: ruleTag %q пуст или повторяется", mode, tun, svc, r.RuleTag)
					}
					seen[r.RuleTag] = true
				}
			}
		}
	}
}

// правила сервисов — в умном режиме, раньше «найдено» и базы заблокированных; путь — по итогу проверки
func TestSvcRules(t *testing.T) {
	tags := svcTestTags(t, Settings{Mode: "smart", Found: []Rule{{Domain: "youtube.com"}}, Svc: map[string]string{"youtube": svcDirect, "discord": svcVPN}})
	idx := func(tag string) int {
		return slices.IndexFunc(tags, func(s string) bool { return strings.HasPrefix(s, tag+">") })
	}
	d, v, f, b := idx("svc-direct"), idx("svc-vpn"), idx("found"), idx("blocked")
	if d < 0 || v < 0 || f < 0 || b < 0 || d > f || v > f || f > b {
		t.Fatalf("порядок правил: %v", tags)
	}
	if tags[d] != "svc-direct>direct" || strings.HasSuffix(tags[v], ">direct") {
		t.Fatalf("пути сервисов: %s, %s", tags[d], tags[v])
	}
	if idx("svc-wait") >= 0 {
		t.Fatal("svc-wait при известных итогах")
	}
	// без итога — через VPN, как раньше
	tags = svcTestTags(t, Settings{Mode: "smart"})
	if n := len(slices.DeleteFunc(slices.Clone(tags), func(s string) bool { return !strings.HasPrefix(s, "svc-wait>") })); n != 1 {
		t.Fatalf("непроверенных правил %d: %v", n, tags)
	}
	// в других режимах правил сервисов нет
	for _, m := range []string{"all", "ai"} {
		for _, s := range svcTestTags(t, Settings{Mode: m, Svc: map[string]string{"youtube": svcDirect}}) {
			if strings.HasPrefix(s, "svc-") {
				t.Fatalf("режим %s: %v", m, s)
			}
		}
	}
}

func TestSvcOf(t *testing.T) {
	for d, want := range map[string]string{
		"rr3---sn-abc.googlevideo.com": "youtube", "www.youtube.com": "youtube", "i.ytimg.com": "youtube", "yt3.ggpht.com": "youtube",
		"gateway.discord.gg": "discord", "cdn.discordapp.com": "discord", "media.discordapp.net": "discord", "abc.discord.media": "discord",
		"google.com": "", "notyoutube.com": "", "lh3.ggpht.com": "",
	} {
		got := ""
		if sv := svcOf(d); sv != nil {
			got = sv.Key
		}
		if got != want {
			t.Errorf("svcOf(%s) = %q, ждали %q", d, got, want)
		}
	}
}

// проверка: всё открылось — напрямую; один адрес не открылся — VPN; сбой прямого — перепроверка не чаще svcRetry
func TestSvcCheck(t *testing.T) {
	old := app
	defer func() { app = old }()
	a := newApp(true)
	app = a
	a.mu.Lock()
	a.on, a.set.Mode = true, "smart"
	a.mu.Unlock()

	var fail atomic.Value
	fail.Store("")
	var dnsDown atomic.Value
	dnsDown.Store("")
	var calls atomic.Int32
	prev := svcs.direct
	defer func() { svcs.direct = prev }()
	svcs.direct = func(h string) string {
		if svcOf(h) == svcOf("youtube.com") { // Discord проверяется сам после apply — не в счёт
			calls.Add(1)
		}
		if h == fail.Load().(string) {
			return "нет ответа"
		}
		if h == dnsDown.Load().(string) {
			return dnsReason
		}
		return ""
	}
	a.mu.Lock()
	found0 := len(a.set.Found)
	a.mu.Unlock()
	yt := svcOf("youtube.com")
	wait := func(want string) {
		t.Helper()
		for i := 0; i < 200; i++ {
			svcs.mu.Lock()
			busy := svcs.busy[yt.Key]
			svcs.mu.Unlock()
			if !busy && a.svcState(yt) == want {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("итог %q, ждали %q", a.svcState(yt), want)
	}
	svcs.mu.Lock()
	delete(svcs.last, yt.Key)
	svcs.mu.Unlock()
	a.svcCheck(yt, 0)
	wait(svcDirect)

	// сбой DNS у адреса сервиса: путь не меняется
	dnsDown.Store(yt.Probe[0])
	a.svcCheck(yt, 0)
	wait(svcDirect)
	dnsDown.Store("")

	fail.Store("redirector.googlevideo.com")
	a.report("rr1---sn-x.googlevideo.com", "не отвечает напрямую")
	wait(svcDirect) // перепроверка не раньше svcRetry после прошлой
	svcs.mu.Lock()
	svcs.last[yt.Key] = time.Now().Add(-2 * svcRetry)
	svcs.mu.Unlock()
	a.report("rr1---sn-x.googlevideo.com", "не отвечает напрямую")
	wait(svcVPN)

	a.mu.Lock()
	found := len(a.set.Found)
	a.mu.Unlock()
	if found != found0 {
		t.Fatal("сервис попал в «найдено»")
	}
	// через VPN: сбой прямого уже ни при чём, перепроверки нет
	n := calls.Load()
	svcs.mu.Lock()
	svcs.last[yt.Key] = time.Now().Add(-2 * svcRetry)
	svcs.mu.Unlock()
	a.report("www.youtube.com", "обрыв напрямую")
	time.Sleep(50 * time.Millisecond)
	if calls.Load() != n {
		t.Fatal("перепроверка при пути через VPN")
	}
}

// завис HTTP/3: адрес сервера ненадолго через VPN — правило temp-ip, в «найдено» не пишется, повтор не дублирует
func TestHoldQUIC(t *testing.T) {
	old := app
	defer func() { app = old }()
	a := newApp(true)
	app = a
	a.mu.Lock()
	found0 := len(a.set.Found)
	a.mu.Unlock()
	a.holdQUIC("104.21.46.95")
	a.holdQUIC("104.21.46.95")
	a.holdQUIC("")
	a.mu.Lock()
	ips, found := slices.Clone(a.set.TempIP), len(a.set.Found)
	a.mu.Unlock()
	if !slices.Equal(ips, []string{"104.21.46.95"}) || found != found0 {
		t.Fatalf("TempIP %v, найдено %d→%d", ips, found0, found)
	}
	tags := svcTestTags(t, Settings{Mode: "smart", TempIP: ips})
	if !slices.ContainsFunc(tags, func(s string) bool { return strings.HasPrefix(s, "temp-ip>") && !strings.HasSuffix(s, ">direct") }) {
		t.Fatalf("нет правила temp-ip через VPN: %v", tags)
	}
}

// «Новые сайты сначала через VPN» включено по умолчанию, и у старого файла настроек без этого поля тоже
func TestVpnFirstDefault(t *testing.T) {
	s := defaults()
	if err := json.Unmarshal([]byte(`{"mode":"smart"}`), &s); err != nil || !s.VpnFirst {
		t.Fatalf("старый файл: vpnFirst %v, %v", s.VpnFirst, err)
	}
	if err := json.Unmarshal([]byte(`{"vpnFirst":false}`), &s); err != nil || s.VpnFirst {
		t.Fatalf("выключено в файле: vpnFirst %v, %v", s.VpnFirst, err)
	}
}

// убрали сайт из списка: отметки проверки сайта и его поддоменов сняты, следующий заход проверит заново
func TestSmartForget(t *testing.T) {
	s := &smartDetector{checked: map[string]time.Time{"vinfo-api.com": time.Now(), "test.vinfo-api.com": time.Now(), "other.com": time.Now()}, busy: map[string]bool{}}
	s.forget("vinfo-api.com")
	if len(s.checked) != 1 || s.checked["other.com"].IsZero() {
		t.Fatalf("осталось %v", s.checked)
	}
}
