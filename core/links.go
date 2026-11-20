package core

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/xtls/xray-core/infra/conf"
)

// Разбор подписок любого вида: JSON Xray (массив конфигов или один конфиг),
// список ссылок vless:// vmess:// trojan:// ss:// hysteria2:// — как есть или в base64, одна ссылка

// parseSubBody превращает тело подписки (или вставленную ссылку) в подключения
// parseSubBody разбирает подписку и разводит одинаковые имена подключений
func parseSubBody(body []byte) ([]Conn, error) {
	conns, err := parseSubRaw(body)
	return uniqueNames(conns), err
}

// uniqueNames: подключение выбирают, замеряют и запоминают по имени, поэтому одинаковых имён быть не должно.
// «Stockholm» трижды с разными протоколами → «Stockholm Vision», «Stockholm Hysteria2», «Stockholm XHTTP»:
// на экране это по-прежнему сервер «Stockholm» с плашкой протокола. Совпали и имя, и протокол — добавляется номер
func uniqueNames(conns []Conn) []Conn {
	count := map[string]int{}
	for _, c := range conns {
		count[c.Name]++
	}
	seen := map[string]bool{}
	for i := range conns {
		c := &conns[i]
		base, name := c.Name, c.Name
		hasProto := strings.HasSuffix(base, " "+c.Proto) || base == c.Proto
		if count[base] > 1 && !hasProto {
			name = base + " " + c.Proto
		}
		for n := 2; seen[name]; n++ {
			if hasProto {
				name = fmt.Sprintf("%s %d", base, n)
			} else {
				name = fmt.Sprintf("%s %d %s", base, n, c.Proto)
			}
		}
		seen[name] = true
		c.Name = name
	}
	return conns
}

func parseSubRaw(body []byte) ([]Conn, error) {
	text := strings.TrimSpace(strings.TrimPrefix(string(body), string([]byte{0xef, 0xbb, 0xbf})))
	if text == "" {
		return nil, errors.New("подписка пустая")
	}
	if looksLikeWGConf(text) { // файл .conf от WireGuard или AmneziaWG — тоже начинается с «[»
		name, ob, err := parseWGConf(text)
		if err == nil {
			err = checkOutbound(ob)
		}
		if err != nil {
			return nil, err
		}
		return []Conn{newConn(0, name, ob)}, nil
	}
	if text[0] == '[' || text[0] == '{' {
		return parseXrayJSON([]byte(text))
	}
	if !strings.Contains(text, "://") {
		if dec, ok := unbase64(text); ok {
			text = dec
		}
	}
	var conns []Conn
	var bad []string
	for _, line := range strings.FieldsFunc(text, func(r rune) bool { return r == '\n' || r == '\r' }) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		name, ob, err := parseLink(line)
		if err == nil {
			err = checkOutbound(ob)
		}
		if err != nil { // одна плохая ссылка не должна ломать весь конфиг: её пропускаем
			bad = append(bad, err.Error())
			continue
		}
		conns = append(conns, newConn(len(conns), name, ob))
	}
	if len(conns) == 0 {
		if len(bad) > 0 {
			return nil, errors.New(bad[0])
		}
		return nil, errors.New("в подписке нет подключений")
	}
	return conns, nil
}

func unbase64(s string) (string, bool) {
	s = strings.Join(strings.Fields(s), "")
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return string(b), true
		}
	}
	return "", false
}

// parseXrayJSON: массив полных конфигов (monolit, Remnawave, Marzban) или один конфиг.
// Из каждого берётся outbound с тегом proxy, а если такого нет — первый настоящий прокси
func parseXrayJSON(body []byte) ([]Conn, error) {
	type cfg struct {
		Remarks   string           `json:"remarks"`
		Outbounds []map[string]any `json:"outbounds"`
		Routing   struct {
			Rules []map[string]any `json:"rules"`
		} `json:"routing"`
	}
	var cfgs []cfg
	if body[0] == '{' {
		var one cfg
		if err := json.Unmarshal(body, &one); err != nil {
			return nil, errors.New("не получилось прочитать JSON")
		}
		cfgs = []cfg{one}
	} else if err := json.Unmarshal(body, &cfgs); err != nil {
		return nil, errors.New("не получилось прочитать JSON")
	}
	var conns []Conn
	var bad []string
	for _, c := range cfgs {
		var pick map[string]any
		for _, ob := range c.Outbounds {
			if ob["tag"] == "proxy" {
				pick = ob
				break
			}
		}
		if pick == nil {
			for _, ob := range c.Outbounds {
				switch ob["protocol"] {
				case "vless", "vmess", "trojan", "shadowsocks", "hysteria", "socks", "http", "wireguard":
					pick = ob
				}
				if pick != nil {
					break
				}
			}
		}
		if pick == nil {
			continue
		}
		if err := checkOutbound(pick); err != nil {
			bad = append(bad, err.Error())
			continue
		}
		ptag, _ := pick["tag"].(string)
		conn := newConn(len(conns), c.Remarks, pick)
		conn.Routes = subRoutes(c.Routing.Rules, c.Outbounds, ptag)
		conns = append(conns, conn)
	}
	if len(conns) == 0 {
		if len(bad) > 0 {
			return nil, errors.New(bad[0])
		}
		return nil, errors.New("в подписке нет подключений")
	}
	return conns, nil
}

// checkOutbound: собирает outbound так же, как Xray при запуске. Ошибка здесь — ошибка запуска всего конфига,
// поэтому такое подключение отбрасывается заранее
func checkOutbound(ob map[string]any) error {
	if err := checkUsers(ob); err != nil {
		return err
	}
	raw, err := json.Marshal(ob)
	if err != nil {
		return err
	}
	var c conf.OutboundDetourConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return fmt.Errorf("подключение не читается: %v", err)
	}
	c.Tag = "check"
	if _, err := c.Build(); err != nil {
		return fmt.Errorf("подключение не годится: %v", err)
	}
	return nil
}

// checkUsers: понятные ошибки для частых поломок ссылок (Xray сказал бы то же, но непонятнее)
func checkUsers(ob map[string]any) error {
	p, _ := ob["protocol"].(string)
	st, _ := ob["settings"].(map[string]any)
	if p == "vless" || p == "vmess" {
		ok := false
		if l, _ := st["vnext"].([]any); len(l) > 0 {
			if m, _ := l[0].(map[string]any); m != nil {
				if us, _ := m["users"].([]any); len(us) > 0 {
					if u, _ := us[0].(map[string]any); u != nil {
						id, _ := u["id"].(string)
						ok = strings.TrimSpace(id) != ""
					}
				}
			}
		}
		if !ok {
			return errors.New("в ссылке " + p + " нет идентификатора пользователя")
		}
	}
	if ss, _ := ob["streamSettings"].(map[string]any); ss != nil {
		// JSON-подписка с allowInsecure: с отпечатком сертификата он лишний, без отпечатка ядро откажет
		if t, _ := ss["tlsSettings"].(map[string]any); t != nil && t["allowInsecure"] == true {
			if pin, _ := t["pinnedPeerCertSha256"].(string); strings.TrimSpace(pin) != "" {
				delete(t, "allowInsecure")
			} else {
				return errInsecure
			}
		}
	}
	if ss, _ := ob["streamSettings"].(map[string]any); ss != nil && ss["security"] == "reality" {
		rs, _ := ss["realitySettings"].(map[string]any)
		pk, _ := rs["publicKey"].(string)
		pw, _ := rs["password"].(string)
		if pk == "" && pw == "" {
			return errors.New("в ссылке Reality нет ключа (pbk)")
		}
	}
	return nil
}

// newConn: имя из подписи, протокол и адрес — из самого outbound
func newConn(i int, name string, ob map[string]any) Conn {
	raw, _ := json.Marshal(ob)
	addr := obAddress(ob)
	if strings.TrimSpace(name) == "" {
		name = addr
	}
	return Conn{Tag: fmt.Sprintf("px-%d", i), Name: strings.TrimSpace(name), Server: addr, Proto: protoLabel(ob), Outbound: raw}
}

func obAddress(ob map[string]any) string {
	st, _ := ob["settings"].(map[string]any)
	if st == nil {
		return ""
	}
	if a, ok := st["address"].(string); ok {
		return a
	}
	for _, k := range []string{"vnext", "servers", "peers"} {
		if l, ok := st[k].([]any); ok && len(l) > 0 {
			if m, ok := l[0].(map[string]any); ok {
				if a, ok := m["address"].(string); ok {
					return a
				}
				if e, ok := m["endpoint"].(string); ok {
					h, _, _ := net.SplitHostPort(e)
					return h
				}
			}
		}
	}
	return ""
}

// protoLabel — короткое имя для переключателя протоколов: Vision, XHTTP, Reality, WS, gRPC, Hysteria2, Trojan…
func protoLabel(ob map[string]any) string {
	p, _ := ob["protocol"].(string)
	ss, _ := ob["streamSettings"].(map[string]any)
	network, _ := ss["network"].(string)
	security, _ := ss["security"].(string)
	flow := ""
	if st, ok := ob["settings"].(map[string]any); ok {
		if f, ok := st["flow"].(string); ok {
			flow = f
		}
		if l, ok := st["vnext"].([]any); ok && len(l) > 0 {
			if m, ok := l[0].(map[string]any); ok {
				if us, ok := m["users"].([]any); ok && len(us) > 0 {
					if u, ok := us[0].(map[string]any); ok {
						flow, _ = u["flow"].(string)
					}
				}
			}
		}
	}
	byNet := map[string]string{"xhttp": "XHTTP", "splithttp": "XHTTP", "ws": "WS", "grpc": "gRPC", "httpupgrade": "HTTPUpgrade", "kcp": "mKCP"}
	switch p {
	case "vless":
		if l, ok := byNet[network]; ok {
			return l
		}
		// Reality раньше Vision: внутри Reality тот же поток Vision, но человек выбирал именно Reality
		if security == "reality" {
			return "Reality"
		}
		if strings.Contains(flow, "vision") {
			return "Vision"
		}
		return "VLESS"
	case "vmess":
		return "VMess"
	case "trojan":
		return "Trojan"
	case "shadowsocks":
		return "Shadowsocks"
	case "hysteria":
		return "Hysteria2"
	case "wireguard":
		if isAWG(ob) {
			return "AmneziaWG"
		}
		return "WireGuard"
	case "socks":
		return "SOCKS"
	case "http":
		return "HTTP"
	}
	return strings.ToUpper(p)
}

// parseLink: одна ссылка → имя и outbound Xray
func parseLink(link string) (string, map[string]any, error) {
	scheme, _, ok := strings.Cut(link, "://")
	if !ok {
		return "", nil, errors.New("это не ссылка на сервер")
	}
	switch strings.ToLower(scheme) {
	case "vmess":
		return parseVmess(link)
	case "ss":
		return parseSS(link)
	case "vpn": // ключ приложения AmneziaVPN
		return parseVPNLink(link)
	}
	u, err := url.Parse(link)
	if err != nil {
		return "", nil, fmt.Errorf("ссылка не читается: %v", err)
	}
	name := u.Fragment
	host, port := u.Hostname(), portOf(u.Port())
	if host == "" || port == 0 {
		return "", nil, errors.New("в ссылке нет адреса или порта")
	}
	q := u.Query()
	switch strings.ToLower(u.Scheme) {
	case "vless":
		user := genRule{"id": u.User.Username(), "encryption": def(q.Get("encryption"), "none")}
		if f := q.Get("flow"); f != "" {
			user["flow"] = f
		}
		ob := genRule{"protocol": "vless", "settings": genRule{"vnext": []any{genRule{"address": host, "port": port, "users": []any{user}}}}}
		st, err := stream(q, host, "none")
		if err != nil {
			return "", nil, err
		}
		ob["streamSettings"] = st
		return name, ob, nil
	case "trojan":
		pw := u.User.Username()
		ob := genRule{"protocol": "trojan", "settings": genRule{"servers": []any{genRule{"address": host, "port": port, "password": pw}}}}
		st, err := stream(q, host, "tls")
		if err != nil {
			return "", nil, err
		}
		ob["streamSettings"] = st
		return name, ob, nil
	case "hysteria2", "hy2":
		// обфускация Hysteria2 бывает одна — salamander с паролем; в Xray это маска UDP
		var mask genRule
		if o := q.Get("obfs"); o != "" {
			pw := q.Get("obfs-password")
			if o != "salamander" || pw == "" {
				return "", nil, fmt.Errorf("Hysteria2: обфускация %q не поддерживается, нужна salamander с паролем", o)
			}
			mask = genRule{"udp": []any{genRule{"type": "salamander", "settings": genRule{"password": pw}}}}
		}
		auth := u.User.Username()
		if pw, ok := u.User.Password(); ok {
			auth += ":" + pw
		}
		tls := genRule{"serverName": def(q.Get("sni"), host), "alpn": []string{"h3"}}
		if err := tlsVerify(q, tls); err != nil {
			return "", nil, err
		}
		ob := genRule{"protocol": "hysteria", "settings": genRule{"address": host, "port": port, "version": 2},
			"streamSettings": genRule{"network": "hysteria", "security": "tls", "tlsSettings": tls, "hysteriaSettings": genRule{"version": 2, "auth": auth}}}
		if mask != nil {
			ob["streamSettings"].(genRule)["finalmask"] = mask
		}
		return name, ob, nil
	}
	return "", nil, fmt.Errorf("протокол %s не поддерживается", u.Scheme)
}

// portOf: номер порта 1…65535, иначе 0
func portOf(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 1 || n > 65535 {
		return 0
	}
	return n
}

func def(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

// errInsecure: ядро больше не принимает allowInsecure (убран после 2026-06-01), а без проверки
// сертификата такое подключение и небезопасно
var errInsecure = errors.New("сервер без проверки сертификата (allowInsecure) больше не поддерживается ядром")

// tlsVerify: ссылка просит не проверять сертификат. С отпечатком сертификата (pcs, pinSHA256)
// проверяем по нему, без отпечатка — отказ
func tlsVerify(q url.Values, t genRule) error {
	if pin := def(q.Get("pcs"), q.Get("pinSHA256")); pin != "" {
		t["pinnedPeerCertSha256"] = pin
		return nil
	}
	if q.Get("allowInsecure") == "1" || q.Get("insecure") == "1" {
		return errInsecure
	}
	return nil
}

// stream собирает streamSettings из параметров ссылки (общие для VLESS и Trojan)
func stream(q url.Values, host, defSec string) (genRule, error) {
	network := def(q.Get("type"), "tcp")
	if network == "tcp" {
		network = "raw"
	}
	if network == "splithttp" {
		network = "xhttp"
	}
	sec := def(q.Get("security"), defSec)
	ss := genRule{"network": network, "security": sec}
	sni := def(q.Get("sni"), def(q.Get("peer"), host))
	switch sec {
	case "tls":
		t := genRule{"serverName": sni}
		if fp := q.Get("fp"); fp != "" {
			t["fingerprint"] = fp
		}
		if a := q.Get("alpn"); a != "" {
			t["alpn"] = strings.Split(a, ",")
		}
		if err := tlsVerify(q, t); err != nil {
			return nil, err
		}
		ss["tlsSettings"] = t
	case "reality":
		ss["realitySettings"] = genRule{"serverName": sni, "fingerprint": def(q.Get("fp"), "chrome"), "publicKey": q.Get("pbk"), "shortId": q.Get("sid"), "spiderX": q.Get("spx")}
	}
	path, h := q.Get("path"), q.Get("host")
	switch network {
	case "ws":
		ss["wsSettings"] = genRule{"path": def(path, "/"), "host": h}
	case "httpupgrade":
		ss["httpupgradeSettings"] = genRule{"path": def(path, "/"), "host": h}
	case "grpc":
		ss["grpcSettings"] = genRule{"serviceName": q.Get("serviceName"), "multiMode": q.Get("mode") == "multi"}
	case "xhttp":
		x := genRule{"path": def(path, "/"), "host": h, "mode": def(q.Get("mode"), "auto")}
		if extra := q.Get("extra"); extra != "" {
			var e map[string]any
			if json.Unmarshal([]byte(extra), &e) == nil {
				x["extra"] = e
			}
		}
		ss["xhttpSettings"] = x
	case "raw":
		if q.Get("headerType") == "http" {
			ss["rawSettings"] = genRule{"header": genRule{"type": "http", "request": genRule{"path": []string{def(path, "/")}, "headers": genRule{"Host": []string{h}}}}}
		}
	}
	return ss, nil
}

// vmess://base64(JSON в формате v2rayN)
func parseVmess(link string) (string, map[string]any, error) {
	dec, ok := unbase64(strings.TrimPrefix(link, link[:strings.Index(link, "://")+3]))
	if !ok {
		return "", nil, errors.New("ссылка vmess не читается")
	}
	var v map[string]any
	if json.Unmarshal([]byte(dec), &v) != nil {
		return "", nil, errors.New("ссылка vmess не читается")
	}
	str := func(k string) string {
		switch x := v[k].(type) {
		case string:
			return x
		case float64:
			if x == math.Trunc(x) && math.Abs(x) < 1e9 { // дробное или огромное число не превращаем в порт
				return strconv.Itoa(int(x))
			}
			return strconv.FormatFloat(x, 'f', -1, 64)
		}
		return ""
	}
	host, port := str("add"), portOf(str("port"))
	if host == "" || port == 0 {
		return "", nil, errors.New("в ссылке нет адреса или порта")
	}
	q := url.Values{}
	q.Set("type", str("net"))
	q.Set("security", str("tls"))
	q.Set("sni", str("sni"))
	q.Set("fp", str("fp"))
	q.Set("alpn", str("alpn"))
	q.Set("path", str("path"))
	q.Set("host", str("host"))
	q.Set("headerType", str("type"))
	if str("net") == "grpc" {
		q.Set("serviceName", str("path"))
	}
	user := genRule{"id": str("id"), "alterId": portOf(str("aid")), "security": def(str("scy"), "auto")}
	ob := genRule{"protocol": "vmess", "settings": genRule{"vnext": []any{genRule{"address": host, "port": port, "users": []any{user}}}}}
	st, err := stream(q, host, "none")
	if err != nil {
		return "", nil, err
	}
	ob["streamSettings"] = st
	return str("ps"), ob, nil
}

// ss://base64(method:password)@host:port#name или ss://base64(method:password@host:port)#name
func parseSS(link string) (string, map[string]any, error) {
	rest := link[strings.Index(link, "://")+3:]
	name := ""
	if i := strings.Index(rest, "#"); i >= 0 {
		name, _ = url.PathUnescape(rest[i+1:])
		rest = rest[:i]
	}
	if i := strings.Index(rest, "?"); i >= 0 {
		rest = rest[:i]
	}
	rest = strings.TrimSuffix(rest, "/")
	var cred, hostport string
	if at := strings.LastIndex(rest, "@"); at >= 0 {
		cred, hostport = rest[:at], rest[at+1:]
		if d, ok := unbase64(cred); ok {
			cred = d
		} else if d, err := url.PathUnescape(cred); err == nil {
			cred = d
		}
	} else {
		d, ok := unbase64(rest)
		if !ok {
			return "", nil, errors.New("ссылка ss не читается")
		}
		at := strings.LastIndex(d, "@")
		if at < 0 {
			return "", nil, errors.New("ссылка ss не читается")
		}
		cred, hostport = d[:at], d[at+1:]
	}
	method, pw, ok := strings.Cut(cred, ":")
	host, p, err := net.SplitHostPort(hostport)
	if !ok || err != nil || portOf(p) == 0 {
		return "", nil, errors.New("ссылка ss не читается")
	}
	ob := genRule{"protocol": "shadowsocks", "settings": genRule{"servers": []any{genRule{"address": host, "port": portOf(p), "method": method, "password": pw}}}}
	return name, ob, nil
}
