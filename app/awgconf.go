package main

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"

	"github.com/xtls/xray-core/infra/conf"
)

// Файл .conf от WireGuard или AmneziaWG: разделы [Interface] и [Peer].
// Параметры маскировки AmneziaWG (Jc, S1, H1, HeaderProtectionKey…) уходят в settings.awg подключения wireguard

func looksLikeWGConf(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		return strings.EqualFold(line, "[Interface]")
	}
	return false
}

func parseWGConf(text string) (string, map[string]any, error) {
	var section string
	iface := map[string]string{}
	var peers []map[string]string
	awg := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			section = strings.ToLower(strings.Trim(line, "[] \t"))
			if section == "peer" {
				peers = append(peers, map[string]string{})
			}
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch section {
		case "interface":
			if conf.IsAWGParam(k) {
				awg[k] = v
			} else {
				iface[strings.ToLower(k)] = v
			}
		case "peer":
			peers[len(peers)-1][strings.ToLower(k)] = v
		}
	}
	if iface["privatekey"] == "" {
		return "", nil, errors.New("в конфиге WireGuard нет PrivateKey")
	}
	if len(peers) != 1 {
		return "", nil, errors.New("в конфиге WireGuard должен быть один раздел [Peer]")
	}
	p := peers[0]
	host, _, err := net.SplitHostPort(p["endpoint"])
	if p["publickey"] == "" || err != nil || host == "" {
		return "", nil, errors.New("в конфиге WireGuard нет ключа или адреса сервера")
	}
	var addrs []any
	for _, a := range strings.Split(iface["address"], ",") {
		a, _, _ = strings.Cut(strings.TrimSpace(a), "/")
		if net.ParseIP(a) != nil {
			addrs = append(addrs, a)
		}
	}
	if len(addrs) == 0 {
		return "", nil, errors.New("в конфиге WireGuard нет адреса (Address)")
	}
	peer := genRule{"publicKey": p["publickey"], "endpoint": p["endpoint"], "allowedIPs": []any{"0.0.0.0/0", "::/0"}}
	if v := p["presharedkey"]; v != "" {
		peer["preSharedKey"] = v
	}
	// у AmneziaWG PersistentKeepalive может быть диапазоном «22-30»: берём нижнюю границу
	if ka, _, _ := strings.Cut(p["persistentkeepalive"], "-"); ka != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(ka)); err == nil && n > 0 && n < 65536 {
			peer["keepAlive"] = n
		}
	}
	st := genRule{"secretKey": iface["privatekey"], "address": addrs, "peers": []any{peer}}
	if n, err := strconv.Atoi(iface["mtu"]); err == nil && n >= 576 && n <= 1500 {
		st["mtu"] = n
	}
	if len(awg) > 0 {
		m := genRule{}
		for k, v := range awg {
			m[k] = v
		}
		st["awg"] = m
	}
	return host, genRule{"protocol": "wireguard", "settings": st}, nil
}

// isAWG: подключение wireguard с параметрами маскировки AmneziaWG
func isAWG(ob map[string]any) bool {
	st, _ := ob["settings"].(map[string]any)
	m, _ := st["awg"].(map[string]any)
	return len(m) > 0
}

// parseVPNLink: ключ vpn:// приложения AmneziaVPN. Внутри base64url — JSON приложения, сжатый как qCompress
// (4 байта длины и поток zlib), либо сам текст .conf. Берётся только WireGuard и AmneziaWG: из JSON достаётся
// исходный текст .conf (last_config.config) и разбирается как обычный файл
func parseVPNLink(link string) (string, map[string]any, error) {
	bad := errors.New("ключ vpn:// не читается")
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(strings.TrimPrefix(strings.TrimSpace(link), "vpn://"), "="))
	if err != nil || len(raw) == 0 {
		return "", nil, bad
	}
	if len(raw) > 4 {
		if zr, err := zlib.NewReader(bytes.NewReader(raw[4:])); err == nil {
			if plain, err := io.ReadAll(io.LimitReader(zr, 1<<20)); err == nil && len(plain) > 0 {
				raw = plain
			}
		}
	}
	text := string(raw)
	if looksLikeWGConf(text) {
		return parseWGConf(text)
	}
	var cfg struct {
		Description string
		Containers  []map[string]json.RawMessage
	}
	if json.Unmarshal(raw, &cfg) != nil {
		return "", nil, bad
	}
	for _, c := range cfg.Containers {
		for _, key := range []string{"awg", "wireguard"} {
			var box struct {
				LastConfig string `json:"last_config"`
			}
			var last struct {
				Config string
			}
			if json.Unmarshal(c[key], &box) != nil || json.Unmarshal([]byte(box.LastConfig), &last) != nil || !looksLikeWGConf(last.Config) {
				continue
			}
			name, ob, err := parseWGConf(last.Config)
			if err == nil && strings.TrimSpace(cfg.Description) != "" {
				name = strings.TrimSpace(cfg.Description)
			}
			return name, ob, err
		}
	}
	return "", nil, errors.New("в ключе vpn:// нет подключения WireGuard или AmneziaWG")
}
