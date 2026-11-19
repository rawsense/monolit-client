package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/xtls/xray-core/infra/conf"
)

// SubRoute — правило маршрута из конфига подписки. Берутся только правила по сайтам и адресам, которые ведут
// напрямую, в блокировку или в сам сервер этого конфига. Правила в другие серверы подписке не доверяем
type SubRoute struct {
	To      string   `json:"to"` // direct | block | vpn
	Domain  []string `json:"domain,omitempty"`
	IP      []string `json:"ip,omitempty"`
	Port    string   `json:"port,omitempty"`
	Network string   `json:"network,omitempty"`
}

// subRoutes разбирает routing.rules одного конфига. proxyTag — тег outbound, взятого как сервер.
// Правило с условием, которого мы не понимаем (программа, входящий тег, источник), пропускается целиком:
// без этого условия оно стало бы шире, чем задумано
func subRoutes(rules []map[string]any, outbounds []map[string]any, proxyTag string) []SubRoute {
	kind := map[string]string{}
	for _, ob := range outbounds {
		tag, _ := ob["tag"].(string)
		switch ob["protocol"] {
		case "freedom":
			kind[tag] = "direct"
		case "blackhole":
			kind[tag] = "block"
		}
	}
	if proxyTag != "" {
		kind[proxyTag] = "vpn"
	}
	var out []SubRoute
rules:
	for _, r := range rules {
		tag, _ := r["outboundTag"].(string)
		sr := SubRoute{To: kind[tag]}
		if tag == "" || sr.To == "" {
			continue
		}
		for k, v := range r {
			switch k {
			case "type", "outboundTag", "ruleTag":
			case "domain":
				sr.Domain = stringList(v)
			case "ip":
				sr.IP = stringList(v)
			case "port":
				switch p := v.(type) {
				case string:
					sr.Port = p
				case float64:
					sr.Port = fmt.Sprint(int(p))
				default:
					continue rules
				}
			case "network":
				n, ok := v.(string)
				if !ok {
					continue rules
				}
				sr.Network = n
			default:
				continue rules
			}
		}
		if len(sr.Domain) == 0 && len(sr.IP) == 0 { // «всё остальное» и правила только по порту не берём
			continue
		}
		out = append(out, sr)
	}
	return out
}

func stringList(v any) []string {
	var out []string
	switch l := v.(type) {
	case string:
		out = append(out, l)
	case []any:
		for _, x := range l {
			if s, ok := x.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
	}
	return out
}

// routeEntryOK: примет ли ядро запись правила. В подписке может быть группа, которой нет в наших базах
// (geosite:…), или файл ext:, которого у нас нет: одна такая запись не дала бы запуститься всему ядру.
// Итог запоминается до обновления баз
var routeEntryOK = func(key, entry string) bool {
	st, _ := os.Stat(filepath.Join(geoDir(), "geosite.dat"))
	var stamp int64
	if st != nil {
		stamp = st.ModTime().UnixNano()
	}
	routeMemo.Lock()
	defer routeMemo.Unlock()
	if routeMemo.ok == nil || routeMemo.stamp != stamp {
		routeMemo.stamp, routeMemo.ok = stamp, map[string]bool{}
	}
	id := key + "\x00" + entry
	if ok, seen := routeMemo.ok[id]; seen {
		return ok
	}
	ok := !strings.HasPrefix(strings.ToLower(entry), "ext") && regexpOK(entry) && buildsRule(genRule{key: []string{entry}, "outboundTag": "direct"})
	routeMemo.ok[id] = ok
	return ok
}

var routeMemo struct {
	sync.Mutex
	stamp int64
	ok    map[string]bool
}

// regexpOK: неверное регулярное выражение маршруты ядра молча пропускают, а список DNS — нет
func regexpOK(entry string) bool {
	re, ok := strings.CutPrefix(entry, "regexp:")
	if !ok {
		return true
	}
	_, err := regexp.Compile(re)
	return err == nil
}

func buildsRule(r genRule) bool {
	os.Setenv("XRAY_LOCATION_ASSET", geoDir())
	raw, err := json.Marshal(genRule{"rules": []any{r}})
	if err != nil {
		return false
	}
	var rc conf.RouterConfig
	if json.Unmarshal(raw, &rc) != nil {
		return false
	}
	cfg, err := rc.Build()
	if err != nil {
		return false
	}
	for _, rule := range cfg.Rule { // условия собираются уже при запуске ядра: неверное регулярное выражение всплывает здесь
		if _, err := rule.BuildCondition(); err != nil {
			return false
		}
	}
	return true
}

// subRouteRules — правила ядра из маршрутов подписки и сайты, которые подписка ведёт напрямую (для DNS).
// Напрямую — выходом direct-ru: умный режим такие соединения не проверяет и не гоняет с VPN
func subRouteRules(routes []SubRoute, via func(genRule) genRule) (rules []any, direct []string) {
	keep := func(key string, list []string) []string {
		var out []string
		for _, e := range list {
			if routeEntryOK(key, e) {
				out = append(out, e)
			}
		}
		return out
	}
	for i, sr := range routes {
		d, ip := keep("domain", sr.Domain), keep("ip", sr.IP)
		// условия правила складываются по «и»: опустевший список сделал бы правило шире — тогда его не берём
		if (len(sr.Domain) > 0 && len(d) == 0) || (len(sr.IP) > 0 && len(ip) == 0) || len(d)+len(ip) == 0 {
			continue
		}
		r := genRule{"ruleTag": fmt.Sprintf("sub-%d", i)}
		if len(d) == 0 {
			// правило только по адресам — как наши nameless-*: в TUN у соединения есть и имя, и адрес, и без этого
			// сайт с известным именем уходил бы по адресу мимо правил «найдено» и «заблокировано»
			r["ruleTag"] = fmt.Sprintf("nameless-sub-%d", i)
		}
		if len(d) > 0 {
			r["domain"] = d
		}
		if len(ip) > 0 {
			r["ip"] = ip
		}
		if sr.Port != "" {
			r["port"] = sr.Port
		}
		if sr.Network != "" {
			r["network"] = sr.Network
		}
		switch sr.To {
		case "direct":
			r["outboundTag"] = "direct-ru"
			if len(ip) == 0 && sr.Port == "" && sr.Network == "" {
				direct = append(direct, d...)
			}
		case "block":
			r["outboundTag"] = "block"
		case "vpn":
			r = via(r)
		default:
			continue
		}
		if sr.Port != "" || sr.Network != "" { // порт или сеть записаны так, что ядро их не примет
			if !buildsRule(genRule{"ip": []string{"10.0.0.0/8"}, "port": sr.Port, "network": sr.Network, "outboundTag": "direct"}) {
				continue
			}
		}
		rules = append(rules, r)
	}
	return rules, direct
}

func sameRoutes(a, b Sub) bool {
	x, _ := json.Marshal(routesOf(a))
	y, _ := json.Marshal(routesOf(b))
	return string(x) == string(y)
}

func routesFlat(s Sub) []SubRoute {
	var out []SubRoute
	for _, c := range s.Conns {
		out = append(out, c.Routes...)
	}
	return out
}

func routesOf(s Sub) [][]SubRoute {
	var out [][]SubRoute
	for _, c := range s.Conns {
		out = append(out, c.Routes)
	}
	return out
}
