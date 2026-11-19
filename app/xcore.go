package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xtls/xray-core/app/router"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/outbound"
	feature_routing "github.com/xtls/xray-core/features/routing"
	"github.com/xtls/xray-core/features/stats"
	"github.com/xtls/xray-core/infra/conf"
	jsonconf "github.com/xtls/xray-core/infra/conf/serial"
	_ "github.com/xtls/xray-core/main/distro/all"
)

// Списки по умолчанию: ИИ и то, что геоблокирует Россию, — всегда через VPN, российское — всегда напрямую
var (
	aiSites      = []string{"geosite:openai", "geosite:anthropic", "geosite:google-gemini", "domain:claude.ai", "domain:claude.com", "domain:anthropic.com", "domain:chatgpt.com", "domain:openai.com", "domain:oaistatic.com", "domain:oaiusercontent.com", "domain:gemini.google.com", "domain:aistudio.google.com", "domain:perplexity.ai", "domain:x.ai", "domain:grok.com", "domain:cursor.com", "domain:cursor.sh", "domain:githubcopilot.com"}
	ruDirect     = []string{"geosite:private", "geosite:category-ru", "geosite:yandex", "geosite:vk", "domain:ru", "domain:su", "domain:xn--p1ai", "geosite:apple"}
	blockedSeeds = []string{"geosite:ru-blocked"}
)

// adsDashboards — кабинеты, которые база рекламы блокирует вместе со счётчиками. Счётчики на чужих
// сайтах (google-analytics.com, googletagmanager.com) по-прежнему режутся
var adsDashboards = []string{"analytics.google.com", "tagmanager.google.com", "ads.google.com"}

// adsAllowRule: путь кабинетов тот же, что дали бы правила после блокировки: во «всё через VPN» — VPN,
// в умном — VPN, только если google.com в «найдено» (он не в базе заблокированных), иначе напрямую
func adsAllowRule(s Settings, via func(genRule) genRule) genRule {
	r := genRule{"domain": prefixed("domain:", adsDashboards), "ruleTag": "ads-allow"}
	vpn := s.Mode == "all"
	if s.Mode != "all" && s.Mode != "ai" {
		for _, f := range s.Found {
			for _, d := range adsDashboards {
				if f.Domain != "" && matchDomain(d, f.Domain) {
					vpn = true
				}
			}
		}
	}
	if vpn {
		return via(r)
	}
	r["outboundTag"] = "direct"
	return r
}

type Core struct {
	mu   sync.Mutex
	inst *core.Instance
	tun  bool
	gen  uint64 // растёт при каждом запуске и остановке: замер со старого ядра не в счёт
	// rgen растёт при смене маршрутов без перезапуска: она переподключает соединения, и замер, попавший
	// под неё, обрывается не по вине сервера. changed — когда ядро последний раз запускали или меняли маршруты
	rgen    uint64
	changed time.Time
}

// SettledFor: сколько прошло с запуска ядра или смены маршрутов. Сразу после них сеть ещё устаканивается
// (маршруты TUN, новые соединения), и неудачный замер ничего не говорит о сервере
func (c *Core) SettledFor() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.changed.IsZero() {
		return time.Hour
	}
	return time.Since(c.changed)
}

type genRule = map[string]any

// pickConns — выбранный конфиг подписки. Не выбран или пропал из подписки — первый в списке
func pickConns(s Settings, sub Sub) []Conn {
	for _, c := range sub.Conns {
		if c.Name == s.Conn {
			return []Conn{c}
		}
	}
	if len(sub.Conns) > 0 {
		return sub.Conns[:1]
	}
	return nil
}

// buildConfig собирает конфиг Xray из подписки и настроек
func buildConfig(s Settings, sub Sub, tun bool, physIf string) ([]byte, error) {
	var outbounds []any
	var tags []string
	// в ядре все подключения подписки с постоянными тегами px-N: у каждого есть задержка, а смена выбранного —
	// это только смена маршрута, без перезапуска ядра (замеры и соединения не теряются)
	picked := map[string]bool{}
	for _, c := range pickConns(s, sub) {
		picked[c.Tag] = true
	}
	for _, c := range sub.Conns {
		var ob map[string]any
		if err := json.Unmarshal(c.Outbound, &ob); err != nil || ob == nil { // "null" даёт пустую карту без ошибки
			continue
		}
		ob["tag"] = c.Tag
		ss, _ := ob["streamSettings"].(map[string]any)
		if ss == nil {
			ss = map[string]any{}
			ob["streamSettings"] = ss
		}
		if s.Fingerprint != "" {
			if tls, ok := ss["tlsSettings"].(map[string]any); ok {
				tls["fingerprint"] = s.Fingerprint
			}
		}
		if tun && physIf != "" { // в режиме TUN соединения к серверу идут мимо туннеля, иначе петля
			so, _ := ss["sockopt"].(map[string]any)
			if so == nil {
				so = map[string]any{}
			}
			so["interface"] = physIf
			ss["sockopt"] = so
		}
		outbounds = append(outbounds, ob)
		if picked[c.Tag] {
			tags = append(tags, c.Tag)
		}
	}
	if len(tags) == 0 {
		return nil, fmt.Errorf("нет подключений: добавьте подписку")
	}
	direct := genRule{"tag": "direct", "protocol": "freedom", "settings": genRule{"domainStrategy": "UseIPv4"}}
	if tun && physIf != "" {
		direct["streamSettings"] = genRule{"sockopt": genRule{"interface": physIf}}
	}
	// direct-ru — тот же прямой выход для российского: умный режим его не проверяет и не гоняет с VPN
	directRU := genRule{"tag": "direct-ru", "protocol": "freedom", "settings": genRule{"domainStrategy": "UseIPv4"}}
	if st, ok := direct["streamSettings"]; ok {
		directRU["streamSettings"] = st
	}
	// direct-local — к самому компьютеру (127.0.0.1, localhost) без привязки к сетевой карте: через карту петля
	// недоступна, и программы, идущие через наш прокси к локальным серверам, получали ошибку
	outbounds = append(outbounds, direct, directRU,
		genRule{"tag": "direct-local", "protocol": "freedom"},
		genRule{"tag": "block", "protocol": "blackhole"},
		genRule{"tag": "dns-out", "protocol": "dns"})

	// куда идёт VPN-трафик: выбранный конфиг
	vpn := genRule{"outboundTag": tags[0]}
	via := func(r genRule) genRule {
		for k, v := range vpn {
			r[k] = v
		}
		return r
	}
	domains := func(rs []Rule) []string {
		var d []string
		for _, r := range rs {
			if r.Domain != "" {
				d = append(d, "domain:"+r.Domain)
			}
		}
		return d
	}
	apps := func(rs []Rule) []string {
		var d []string
		for _, r := range rs {
			if r.Domain != "" {
				d = append(d, r.Domain)
			}
		}
		return d
	}

	rules := []any{}
	if tun {
		rules = append(rules, genRule{"inboundTag": []string{"tun-in"}, "port": "53", "outboundTag": "dns-out", "ruleTag": "dns"})
		// IPv6 из TUN — в никуда (сервер VPN без IPv6). Запросы DNS на IPv6-адреса выше уже ушли
		// в наш DNS: так система не спросит DNS провайдера мимо VPN. TCP отклоняет ещё стек TUN
		rules = append(rules, genRule{"inboundTag": []string{"tun-in"}, "ip": []string{"::/0"}, "outboundTag": "block", "ruleTag": "ipv6"})
	}
	rules = append(rules, via(genRule{"inboundTag": []string{"probe-in"}, "ruleTag": "probe"}))        // проверка сайта через VPN для умного режима
	rules = append(rules, via(genRule{"inboundTag": []string{"dns-internal"}, "ruleTag": "core-dns"})) // запросы DNS самого ядра — через VPN
	rules = append(rules, genRule{"ip": []string{"127.0.0.0/8", "::1"}, "outboundTag": "direct-local", "ruleTag": "local"},
		genRule{"domain": []string{"full:localhost"}, "outboundTag": "direct-local", "ruleTag": "local-name"}) // ruleTag у ядра уникален: повтор — и маршруты не применяются
	rules = append(rules, genRule{"ip": []string{"geoip:private"}, "outboundTag": "direct", "ruleTag": "private"})
	var servers []string // адреса самих серверов — всегда напрямую, одним правилом (ruleTag уникален)
	for _, c := range sub.Conns {
		if c.Server != "" && !slices.Contains(servers, "full:"+c.Server) {
			servers = append(servers, "full:"+c.Server)
		}
	}
	if len(servers) > 0 {
		rules = append(rules, genRule{"domain": servers, "outboundTag": "direct", "ruleTag": "server"})
	}
	// свои списки — раньше блокировки рекламы: сайт, который человек добавил сам, не режется как реклама
	if d := domains(s.Direct); len(d) > 0 {
		rules = append(rules, genRule{"domain": d, "outboundTag": "direct", "ruleTag": "my-direct"})
	}
	if d := domains(s.Always); len(d) > 0 {
		rules = append(rules, via(genRule{"domain": d, "ruleTag": "my-vpn"}))
	}
	if s.BlockAds {
		// кабинеты рекламы и аналитики — сайты, а не реклама, хотя в базе рекламы есть (analytics.google.com).
		// Правило с ними идёт раньше блокировки и ведёт тем путём, каким их повели бы правила ниже
		rules = append(rules, adsAllowRule(s, via), genRule{"domain": []string{"geosite:category-ads-all"}, "outboundTag": "block", "ruleTag": "ads"})
	}
	// программы — после своих списков сайтов: сайт, который человек повёл сам, идёт своим путём из любой программы
	if d := apps(s.Apps); len(d) > 0 {
		rules = append(rules, via(genRule{"process": d, "ruleTag": "apps"}))
	}
	if d := apps(s.AppsDirect); len(d) > 0 {
		rules = append(rules, genRule{"process": d, "outboundTag": "direct", "ruleTag": "apps-direct"})
	}
	rules = append(rules, via(genRule{"domain": aiSites, "ruleTag": "ai"}))
	// маршруты из подписки — ниже своих списков, программ и сайтов ИИ, выше правил режима
	var subDirect []string
	if s.SubRoutes {
		for _, c := range pickConns(s, sub) {
			var rs []any
			rs, subDirect = subRouteRules(c.Routes, via)
			rules = append(rules, rs...)
		}
	}
	// правила по адресам (ruleTag nameless-*) работают, только когда имя сайта неизвестно: в TUN имя из
	// рукопожатия решает так же, как в режиме прокси, а общие адреса Cloudflare из ru-blocked не уводят
	// в VPN сайты, которые открываются напрямую
	switch s.Mode {
	case "all":
		rules = append(rules, genRule{"protocol": []string{"bittorrent"}, "outboundTag": "direct", "ruleTag": "torrent"})
		rules = append(rules, genRule{"domain": ruDirect, "outboundTag": "direct", "ruleTag": "ru"}, genRule{"ip": []string{"geoip:ru"}, "outboundTag": "direct", "ruleTag": "nameless-ru"})
		rules = append(rules, via(genRule{"network": "tcp,udp", "ruleTag": "rest"}))
	case "ai":
		rules = append(rules, genRule{"network": "tcp,udp", "outboundTag": "direct", "ruleTag": "rest"})
	default: // smart
		rules = append(rules, genRule{"domain": ruDirect, "outboundTag": "direct-ru", "ruleTag": "ru"}, genRule{"ip": []string{"geoip:ru"}, "outboundTag": "direct-ru", "ruleTag": "nameless-ru"})
		rules = append(rules, svcRules(s, via)...) // YouTube, Discord: путь по проверке напрямую (svc.go), раньше «найдено» и базы
		if len(s.TempIP) > 0 {                     // продолжение зависшего QUIC без имени сайта — по адресу сервера (holdQUIC)
			rules = append(rules, via(genRule{"ip": s.TempIP, "network": "udp", "port": "443", "ruleTag": "temp-ip"}))
		}
		if d := domains(s.Found); len(d) > 0 {
			rules = append(rules, via(genRule{"domain": d, "ruleTag": "found"}))
		}
		rules = append(rules, via(genRule{"domain": blockedSeeds, "ruleTag": "blocked"}))
		rules = append(rules, via(genRule{"ip": []string{"geoip:ru-blocked"}, "ruleTag": "nameless-blocked"}))
		rules = append(rules, genRule{"network": "tcp,udp", "outboundTag": "direct", "ruleTag": "rest"})
	}

	// имя сайта из рукопожатия — только для выбора маршрута, адрес сервера не подменяется: обходчики блокировок
	// (zapret, GoodbyeDPI) подмешивают поддельное начало соединения с чужим именем (www.google.com), и подмена
	// адреса по нему уводила соединение на чужой сервер (так ломался QUIC). В журнал имя попадает и так
	sniff := genRule{"enabled": true, "destOverride": []string{"http", "tls", "quic"}, "routeOnly": true}
	inbounds := []any{
		genRule{"tag": "mixed-in", "protocol": "mixed", "listen": "127.0.0.1", "port": s.Port, "settings": genRule{"udp": true}, "sniffing": sniff},
		genRule{"tag": "probe-in", "protocol": "mixed", "listen": "127.0.0.1", "port": s.Port + 7, "sniffing": sniff},
	}
	if tun {
		inbounds = append(inbounds, genRule{"tag": "tun-in", "protocol": "tun", "settings": genRule{"name": tunName, "MTU": 1500}, "sniffing": sniff})
	}
	// DNS: российское и адреса самих серверов VPN — через Яндекс напрямую (+local), остальное — DoH через VPN
	// (правило dns-internal выше): 1.1.1.1 и 8.8.8.8 напрямую из России не отвечают
	local := append(append([]string{}, ruDirect...), subDirect...) // что подписка ведёт напрямую, то и ищется напрямую
	for _, c := range sub.Conns {
		if c.Server != "" && net.ParseIP(c.Server) == nil {
			local = append(local, "full:"+c.Server)
		}
	}
	dns := genRule{
		"tag": "dns-internal",
		"servers": []any{
			genRule{"address": "https+local://77.88.8.8/dns-query", "domains": local, "skipFallback": true},
			s.DNS, "https://8.8.8.8/dns-query",
		},
		"queryStrategy": "UseIPv4",
	}
	cfg := genRule{
		"log":    genRule{"loglevel": "warning"},
		"dns":    dns,
		"stats":  genRule{},
		"policy": genRule{"system": genRule{"statsOutboundUplink": true, "statsOutboundDownlink": true}},
		"routing": genRule{
			"domainStrategy": "IPIfNonMatch",
			"rules":          rules,
		},
		"inbounds":  inbounds,
		"outbounds": outbounds,
	}
	return json.MarshalIndent(cfg, "", " ")
}

func (c *Core) Start(cfgJSON []byte, tun bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	c.changed = time.Now()
	// новый конфиг разбираем, пока старое ядро работает: ошибка в конфиге не должна гасить VPN
	os.Setenv("XRAY_LOCATION_ASSET", geoDir())
	pb, err := jsonconf.LoadJSONConfig(bytes.NewReader(cfgJSON))
	if err != nil {
		return fmt.Errorf("конфиг: %w", err)
	}
	// адаптер TUN создаётся уже в core.New, второй с тем же именем не заведётся: старое ядро с TUN закрываем раньше
	if (tun || c.tun) && c.inst != nil {
		c.inst.Close()
		c.inst, c.tun = nil, false
	}
	inst, err := core.New(pb)
	if err != nil {
		return fmt.Errorf("ядро: %w", err)
	}
	if c.inst != nil { // порты занимаются в Start: старое ядро закрываем только теперь
		c.inst.Close()
		c.inst = nil
	}
	if err := inst.Start(); err != nil {
		inst.Close()
		return fmt.Errorf("запуск ядра: %w", err)
	}
	c.inst, c.tun = inst, tun
	hookLog() // журнал Xray — к нам, после Start (app/log ставит свой обработчик при запуске)
	return nil
}

// handler — выходной обработчик ядра по тегу (для гонки путей)
func (c *Core) handler(tag string) outbound.Handler {
	c.mu.Lock()
	inst := c.inst
	c.mu.Unlock()
	if inst == nil {
		return nil
	}
	m, ok := inst.GetFeature(outbound.ManagerType()).(outbound.Manager)
	if !ok {
		return nil
	}
	return m.GetHandler(tag)
}

func (c *Core) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	if c.inst != nil {
		c.inst.Close()
		c.inst, c.tun = nil, false
	}
}

func (c *Core) Running() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inst != nil
}

// Traffic: сумма байт через VPN (все px-*)
func (c *Core) Traffic(tags []string) (up, down int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inst == nil {
		return
	}
	m, _ := c.inst.GetFeature(stats.ManagerType()).(stats.Manager)
	if m == nil {
		return
	}
	for _, t := range tags {
		if ct := m.GetCounter("outbound>>>" + t + ">>>traffic>>>uplink"); ct != nil {
			up += ct.Value()
		}
		if ct := m.GetCounter("outbound>>>" + t + ">>>traffic>>>downlink"); ct != nil {
			down += ct.Value()
		}
	}
	return
}

type Probe struct {
	Tag   string `json:"tag"`
	Alive bool   `json:"alive"`
	Delay int64  `json:"delay"`
	Err   string `json:"err,omitempty"`
	Try   int64  `json:"-"` // когда была последняя проверка; 0 — ещё не проверяли
}

const probeURL = "https://www.gstatic.com/generate_204"

// Check проверяет каждое подключение сейчас: запрос probeURL строго через это подключение.
// Неудача — ещё одна попытка, чтобы случайный обрыв не записать в «нет ответа».
// Ядро перезапустилось во время проверки — итог пустой: неудачи там из-за перезапуска, а не серверов
func (c *Core) Check(tags []string) map[string]Probe {
	c.mu.Lock()
	inst, gen, rgen := c.inst, c.gen, c.rgen
	c.mu.Unlock()
	out := map[string]Probe{}
	if inst == nil {
		return out
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, tag := range tags {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := Probe{Tag: tag}
			for try := 0; try < 2; try++ {
				d, err := probeVia(inst, tag)
				if err == nil {
					p.Alive, p.Delay, p.Err = true, d, ""
					break
				}
				p.Err = err.Error()
			}
			p.Try = time.Now().Unix()
			mu.Lock()
			out[tag] = p
			mu.Unlock()
		}()
	}
	wg.Wait()
	c.mu.Lock()
	stale, rerouted := c.gen != gen, c.rgen != rgen
	c.mu.Unlock()
	if stale {
		return map[string]Probe{}
	}
	if rerouted { // маршруты сменились посреди замера: удачи настоящие, а неудачи — возможно, оборванные соединения
		for tag, p := range out {
			if !p.Alive {
				delete(out, tag)
			}
		}
	}
	return out
}

func probeVia(inst *core.Instance, tag string) (int64, error) {
	tr := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dest, err := xnet.ParseDestination(network + ":" + addr)
			if err != nil {
				return nil, err
			}
			ctx = session.ContextWithContent(ctx, &session.Content{SkipDNSResolve: true})
			ctx = session.SetForcedOutboundTagToContext(ctx, tag)
			return core.Dial(ctx, inst, dest)
		},
		TLSHandshakeTimeout: 8 * time.Second,
		MaxIdleConnsPerHost: 1,
	}
	defer tr.CloseIdleConnections()
	cl := &http.Client{Transport: tr, Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func() error {
		resp, err := cl.Get(probeURL)
		if err != nil {
			return err
		}
		io.Copy(io.Discard, resp.Body) // дочитать, иначе соединение не вернётся для второго запроса
		resp.Body.Close()
		return nil
	}
	// первый запрос открывает соединение (рукопожатия с сервером и сайтом), второй идёт по нему же:
	// его время — задержка, которую чувствуешь в игре или звонке, а не стоимость подключения
	if err := get(); err != nil {
		return 0, err
	}
	start := time.Now()
	if err := get(); err != nil {
		return 0, err
	}
	return max(time.Since(start).Milliseconds(), 1), nil
}

// ReloadRouting применяет маршруты из нового конфига без перезапуска ядра: соединения не рвутся
func (c *Core) ReloadRouting(cfgJSON []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inst == nil {
		return nil
	}
	r, _ := c.inst.GetFeature(feature_routing.RouterType()).(*router.Router)
	if r == nil {
		return fmt.Errorf("нет маршрутизатора")
	}
	c.rgen++
	c.changed = time.Now()
	var full struct {
		Routing *conf.RouterConfig `json:"routing"`
	}
	if err := json.Unmarshal(cfgJSON, &full); err != nil {
		return fmt.Errorf("маршруты: %w", err)
	}
	if full.Routing == nil {
		return fmt.Errorf("маршруты: в конфиге нет раздела routing")
	}
	pb, err := full.Routing.Build()
	if err != nil {
		return err
	}
	return r.AddRule(serial.ToTypedMessage(pb), false)
}

func tagsOf(sub Sub) []string {
	var t []string
	for _, c := range sub.Conns {
		t = append(t, c.Tag)
	}
	return t
}

func isVPNDetour(d string) bool { return strings.Contains(d, "px-") }

// buildConfigNoGeo — временное ядро без баз сайтов: всё через первое подключение. Нужно, чтобы скачать базы, если GitHub напрямую не открывается
func buildConfigNoGeo(s Settings, sub Sub) ([]byte, error) {
	var outbounds []any
	for _, c := range sub.Conns {
		var ob map[string]any
		if json.Unmarshal(c.Outbound, &ob) == nil && ob != nil {
			ob["tag"] = c.Tag
			outbounds = append(outbounds, ob)
		}
	}
	if len(outbounds) == 0 {
		return nil, fmt.Errorf("нет подключений")
	}
	return json.Marshal(genRule{
		"log":       genRule{"loglevel": "warning"},
		"inbounds":  []any{genRule{"tag": "probe-in", "protocol": "mixed", "listen": "127.0.0.1", "port": s.Port + 7}},
		"outbounds": outbounds,
	})
}
