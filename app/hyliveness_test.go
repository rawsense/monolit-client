package main

import (
	"encoding/json"
	"testing"
)

func hyTestConfig(t *testing.T, link string) map[string]any {
	t.Helper()
	conns, err := parseSubBody([]byte(link))
	if err != nil || len(conns) != 1 {
		t.Fatalf("разбор: %v, подключений %d", err, len(conns))
	}
	raw, err := buildConfig(Settings{Mode: "all", Port: 10808, DNS: "https://1.1.1.1/dns-query"}, Sub{Conns: conns}, false, "")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	for _, o := range cfg.Outbounds {
		if o["tag"] == "px-0" {
			return o
		}
	}
	t.Fatal("нет подключения px-0")
	return nil
}

func hyQuic(ob map[string]any) map[string]any {
	ss, _ := ob["streamSettings"].(map[string]any)
	fm, _ := ss["finalmask"].(map[string]any)
	qp, _ := fm["quicParams"].(map[string]any)
	return qp
}

// Hysteria2 без своих сроков получает поддержку соединения и короткий срок молчания; ядро такой конфиг принимает
func TestHysteriaLivenessDefaults(t *testing.T) {
	ob := hyTestConfig(t, "hysteria2://authpass@hy.example.com:443?sni=hy.example.com#Hy")
	qp := hyQuic(ob)
	if qp["keepAlivePeriod"] != float64(hyKeepAlive) || qp["maxIdleTimeout"] != float64(hyMaxIdle) {
		t.Fatalf("сроки: %v", qp)
	}
	ob["tag"] = "proxy"
	if err := checkOutbound(ob); err != nil {
		t.Fatalf("ядро не принимает: %v", err)
	}
}

// сроки и прочие параметры из подписки остаются как заданы
func TestHysteriaLivenessKeepsSubscription(t *testing.T) {
	body := `[{"remarks":"Hy","outbounds":[{"tag":"proxy","protocol":"hysteria","settings":{"address":"hy.example.com","port":443,"version":2},
	 "streamSettings":{"network":"hysteria","security":"tls","tlsSettings":{"serverName":"hy.example.com","alpn":["h3"]},
	 "hysteriaSettings":{"version":2,"auth":"authpass"},"finalmask":{"quicParams":{"congestion":"bbr","maxIdleTimeout":40}}}}]}]`
	qp := hyQuic(hyTestConfig(t, body))
	if qp["congestion"] != "bbr" || qp["maxIdleTimeout"] != float64(40) || qp["keepAlivePeriod"] != float64(hyKeepAlive) {
		t.Fatalf("параметры подписки изменены: %v", qp)
	}
}

// другим протоколам эти параметры не добавляются
func TestLivenessOnlyForHysteria(t *testing.T) {
	ob := hyTestConfig(t, "vless://11111111-1111-4111-a111-111111111111@v.example.com:443?security=tls&type=tcp&flow=xtls-rprx-vision&sni=v.example.com#V")
	if qp := hyQuic(ob); qp != nil {
		t.Fatalf("у Vision появились параметры QUIC: %v", qp)
	}
}
