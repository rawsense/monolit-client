package core

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	jsonconf "github.com/xtls/xray-core/infra/conf/serial"
)

// примеры ссылок: адреса и ключи выдуманные
var sampleLinks = []string{
	"vless://11111111-2222-3333-4444-555555555555@nl.example.com:443?type=tcp&security=reality&pbk=AAAAbbbbCCCCddddEEEEffffGGGGhhhhIIIIjjjjKKK&sid=0a1b&sni=www.microsoft.com&fp=chrome&flow=xtls-rprx-vision#Netherlands%20Reality",
	"vless://11111111-2222-3333-4444-555555555555@nl.example.com:443?type=tcp&security=tls&sni=nl.example.com&fp=chrome&flow=xtls-rprx-vision#Netherlands%20Vision",
	"vless://11111111-2222-3333-4444-555555555555@nl.example.com:443?type=xhttp&security=tls&sni=nl.example.com&path=%2Fx&mode=stream-up#Netherlands%20XHTTP",
	"vless://11111111-2222-3333-4444-555555555555@de.example.com:443?type=ws&security=tls&path=%2Fws&host=de.example.com#Germany",
	"vless://11111111-2222-3333-4444-555555555555@de.example.com:443?type=grpc&security=tls&serviceName=gun&mode=multi#Germany%20gRPC",
	"trojan://secret@fi.example.com:443?security=tls&sni=fi.example.com#Finland",
	"ss://" + base64.RawURLEncoding.EncodeToString([]byte("chacha20-ietf-poly1305:pass")) + "@ss.example.com:8388#SS%20server",
	"hysteria2://authpass@hy.example.com:443?sni=hy.example.com#Hy%20server",
	"vmess://" + base64.StdEncoding.EncodeToString([]byte(`{"v":"2","ps":"VMess server","add":"vm.example.com","port":"443","id":"11111111-2222-3333-4444-555555555555","aid":"0","net":"ws","type":"none","host":"vm.example.com","path":"/vm","tls":"tls","sni":"vm.example.com"}`)),
}

func TestParseFormats(t *testing.T) {
	plain := strings.Join(sampleLinks, "\n")
	for name, body := range map[string]string{
		"список":     plain,
		"base64":     base64.StdEncoding.EncodeToString([]byte(plain)),
		"base64 url": base64.RawURLEncoding.EncodeToString([]byte(plain)),
	} {
		conns, err := parseSubBody([]byte(body))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(conns) != len(sampleLinks) {
			t.Fatalf("%s: %d подключений вместо %d", name, len(conns), len(sampleLinks))
		}
	}
	conns, _ := parseSubBody([]byte(plain))
	want := []struct{ server, proto string }{
		{"Netherlands", "Reality"}, {"Netherlands", "Vision"}, {"Netherlands", "XHTTP"}, {"Germany", "WS"}, {"Germany", "gRPC"},
		{"Finland", "Trojan"}, {"SS server", "Shadowsocks"}, {"Hy server", "Hysteria2"}, {"VMess server", "VMess"},
	}
	for i, w := range want {
		if conns[i].ServerName() != w.server || conns[i].Proto != w.proto {
			t.Errorf("%d: %q/%q, ждали %q/%q", i, conns[i].ServerName(), conns[i].Proto, w.server, w.proto)
		}
	}
	// одна ссылка — подписка без обновлений
	s, err := fetchSub(nil, nil, sampleLinks[5])
	if err != nil || len(s.Conns) != 1 || s.Title != "Finland" {
		t.Fatalf("одна ссылка: %v %+v", err, s.Title)
	}
}

func TestParseXrayJSON(t *testing.T) {
	arr := `[{"remarks":"A Vision","outbounds":[{"tag":"proxy","protocol":"vless","settings":{"vnext":[{"address":"a.example.com","port":443,"users":[{"id":"x","flow":"xtls-rprx-vision","encryption":"none"}]}]},"streamSettings":{"network":"raw","security":"tls"}},{"tag":"direct","protocol":"freedom"}]}]`
	one := `{"remarks":"B","outbounds":[{"tag":"direct","protocol":"freedom"},{"tag":"out","protocol":"trojan","settings":{"servers":[{"address":"b.example.com","port":443,"password":"p"}]}}]}`
	for _, body := range []string{arr, one} {
		c, err := parseSubBody([]byte(body))
		if err != nil || len(c) != 1 {
			t.Fatalf("%v %d", err, len(c))
		}
	}
	c, _ := parseSubBody([]byte(arr))
	if c[0].ServerName() != "A" || c[0].Proto != "Vision" || c[0].Server != "a.example.com" {
		t.Errorf("JSON: %q %q %q", c[0].ServerName(), c[0].Proto, c[0].Server)
	}
}

// конфиг из всех видов ссылок должен приниматься Xray
func TestConfigFromLinks(t *testing.T) {
	conns, err := parseSubBody([]byte(strings.Join(sampleLinks, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range conns {
		var ob map[string]any
		json.Unmarshal(c.Outbound, &ob)
		ob["tag"] = "px-0"
		cfg, _ := json.Marshal(map[string]any{"outbounds": []any{ob}})
		if _, err := jsonconf.LoadJSONConfig(bytes.NewReader(cfg)); err != nil {
			t.Errorf("%s (%s): %v", c.Name, c.Proto, err)
		}
	}
}

func TestBadLinks(t *testing.T) {
	vm := func(js string) string { return "vmess://" + base64.StdEncoding.EncodeToString([]byte(js)) }
	for name, link := range map[string]string{
		"порт 70000":         "trojan://secret@fi.example.com:70000?security=tls#X",
		"порт 0":             "trojan://secret@fi.example.com:0?security=tls#X",
		"ss порт 99999":      "ss://" + base64.RawURLEncoding.EncodeToString([]byte("chacha20-ietf-poly1305:pass")) + "@ss.example.com:99999#X",
		"vmess порт -1":      vm(`{"add":"vm.example.com","port":-1,"id":"11111111-2222-3333-4444-555555555555","net":"ws"}`),
		"vmess порт 1e20":    vm(`{"add":"vm.example.com","port":1e20,"id":"11111111-2222-3333-4444-555555555555","net":"ws"}`),
		"vmess порт дробный": vm(`{"add":"vm.example.com","port":443.5,"id":"11111111-2222-3333-4444-555555555555","net":"ws"}`),
		"vless без id":       "vless://@nl.example.com:443?type=tcp&security=tls#X",
		"vmess без id":       vm(`{"add":"vm.example.com","port":"443","id":"","net":"ws"}`),
		"reality без pbk":    "vless://11111111-2222-3333-4444-555555555555@nl.example.com:443?type=tcp&security=reality&sni=www.microsoft.com#X",
		"reality плохой pbk": "vless://11111111-2222-3333-4444-555555555555@nl.example.com:443?type=tcp&security=reality&pbk=abc&sni=www.microsoft.com#X",
		"устаревший h2":      "vless://11111111-2222-3333-4444-555555555555@nl.example.com:443?type=h2&security=tls#X",
		"устаревший quic":    "vless://11111111-2222-3333-4444-555555555555@nl.example.com:443?type=quic&security=tls#X",
		"неизвестная сеть":   "vless://11111111-2222-3333-4444-555555555555@nl.example.com:443?type=pigeon&security=tls#X",
	} {
		if c, err := parseSubBody([]byte(link)); err == nil {
			t.Errorf("%s: принята (%d подключений)", name, len(c))
		}
	}
}

// одна плохая ссылка не роняет остальные
func TestBadLinkSkipped(t *testing.T) {
	bad := []string{
		"vless://11111111-2222-3333-4444-555555555555@nl.example.com:443?type=tcp&security=reality&sni=x.com#Bad1",
		"trojan://secret@fi.example.com:70000?security=tls#Bad2",
	}
	body := strings.Join(append(append([]string{}, bad[0]), append(sampleLinks, bad[1])...), "\n")
	conns, err := parseSubBody([]byte(body))
	if err != nil || len(conns) != len(sampleLinks) {
		t.Fatalf("%v, %d подключений вместо %d", err, len(conns), len(sampleLinks))
	}
	for i, c := range conns {
		if c.Tag != fmt.Sprintf("px-%d", i) || strings.HasPrefix(c.Name, "Bad") {
			t.Errorf("%d: %s %s", i, c.Tag, c.Name)
		}
	}
	// в JSON-подписке так же
	arr := `[{"remarks":"Bad","outbounds":[{"tag":"proxy","protocol":"vless","settings":{"vnext":[{"address":"a.example.com","port":443,"users":[{"id":""}]}]}}]},` +
		`{"remarks":"Good","outbounds":[{"tag":"proxy","protocol":"trojan","settings":{"servers":[{"address":"b.example.com","port":443,"password":"p"}]}}]}]`
	c, err := parseSubBody([]byte(arr))
	if err != nil || len(c) != 1 || c[0].Name != "Good" {
		t.Fatalf("JSON: %v %+v", err, c)
	}
}

// allowInsecure ядро не принимает: без отпечатка — понятный отказ, с отпечатком — проверка по нему
func TestInsecureLinks(t *testing.T) {
	pin := strings.Repeat("ab", 32)
	for _, l := range []string{
		"vless://11111111-2222-3333-4444-555555555555@de.example.com:443?type=ws&security=tls&allowInsecure=1#a",
		"trojan://secret@fi.example.com:443?security=tls&insecure=1#b",
		"hysteria2://authpass@hy.example.com:443?insecure=1#c",
	} {
		_, err := parseSubBody([]byte(l))
		if err == nil || err.Error() != errInsecure.Error() {
			t.Fatalf("%s: ждали отказ про allowInsecure, а вышло %v", l, err)
		}
	}
	for _, l := range []string{
		"vless://11111111-2222-3333-4444-555555555555@de.example.com:443?type=ws&security=tls&allowInsecure=1&pcs=" + pin + "#a",
		"hysteria2://authpass@hy.example.com:443?insecure=1&pinSHA256=" + pin + "#c",
	} {
		conns, err := parseSubBody([]byte(l))
		if err != nil {
			t.Fatalf("%s: %v", l, err)
		}
		s := string(conns[0].Outbound)
		if strings.Contains(s, "allowInsecure") || !strings.Contains(s, pin) {
			t.Fatalf("%s: отпечаток не перенесён: %s", l, s)
		}
	}
	// в подписке с хорошей ссылкой небезопасная просто пропускается
	conns, err := parseSubBody([]byte(sampleLinks[4] + "\ntrojan://secret@fi.example.com:443?security=tls&insecure=1#b"))
	if err != nil || len(conns) != 1 {
		t.Fatalf("ждали одно подключение: %d, %v", len(conns), err)
	}
}

// JSON-подписка с allowInsecure: без отпечатка — понятный отказ, с отпечатком — allowInsecure убирается
func TestInsecureJSON(t *testing.T) {
	mk := func(extra string) string {
		return `{"remarks":"x","outbounds":[{"tag":"proxy","protocol":"trojan","settings":{"servers":[{"address":"fi.example.com","port":443,"password":"p"}]},` +
			`"streamSettings":{"network":"raw","security":"tls","tlsSettings":{"serverName":"fi.example.com","allowInsecure":true` + extra + `}}}]}`
	}
	if _, err := parseSubBody([]byte(mk(""))); err == nil || err.Error() != errInsecure.Error() {
		t.Fatalf("ждали отказ про allowInsecure, а вышло %v", err)
	}
	conns, err := parseSubBody([]byte(mk(`,"pinnedPeerCertSha256":"` + strings.Repeat("cd", 32) + `"`)))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(conns[0].Outbound), "allowInsecure") {
		t.Fatalf("allowInsecure остался: %s", conns[0].Outbound)
	}
}

// Одинаковые имена в подписке: выбор и замеры идут по имени, поэтому имена разводятся протоколом и номером
func TestUniqueNames(t *testing.T) {
	v := "vless://11111111-2222-3333-4444-555555555555@a.example.com:443?type=tcp&security=tls&sni=a.example.com&flow=xtls-rprx-vision#"
	r := "vless://11111111-2222-3333-4444-555555555555@b.example.com:443?type=tcp&security=reality&pbk=AAAAbbbbCCCCddddEEEEffffGGGGhhhhIIIIjjjjKKK&sid=0a1b&sni=b.example.com&flow=xtls-rprx-vision#"
	h := "hysteria2://authpass@a.example.com:443?sni=a.example.com#"
	conns, err := parseSubBody([]byte(strings.Join([]string{v + "Stockholm", h + "Stockholm", r + "Stockholm", r + "Stockholm", r + "Mobile", v + "Oslo%20Vision", v + "Oslo%20Vision"}, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ name, server, proto string }{
		{"Stockholm Vision", "Stockholm", "Vision"}, {"Stockholm Hysteria2", "Stockholm", "Hysteria2"},
		{"Stockholm Reality", "Stockholm", "Reality"}, {"Stockholm 2 Reality", "Stockholm 2", "Reality"},
		{"Mobile", "Mobile", "Reality"}, {"Oslo Vision", "Oslo", "Vision"}, {"Oslo Vision 2", "Oslo Vision 2", "Vision"},
	}
	seen := map[string]bool{}
	for i, w := range want {
		c := conns[i]
		if c.Name != w.name || c.ServerName() != w.server || c.Proto != w.proto {
			t.Errorf("%d: %q / %q / %q, ждали %q / %q / %q", i, c.Name, c.ServerName(), c.Proto, w.name, w.server, w.proto)
		}
		if seen[c.Name] {
			t.Errorf("имя %q повторяется", c.Name)
		}
		seen[c.Name] = true
	}
	// выбор по имени попадает ровно в своё подключение
	if got := pickConns(Settings{Conn: "Stockholm Hysteria2"}, Sub{Conns: conns}); len(got) != 1 || got[0].Proto != "Hysteria2" {
		t.Errorf("выбор Hysteria2 дал %+v", got)
	}
}

// Hysteria2 с обфускацией salamander: маска UDP в конфиге ядра; другая обфускация или без пароля — понятный отказ
func TestHysteriaObfs(t *testing.T) {
	conns, err := parseSubBody([]byte("hysteria2://authpass@hy.example.com:443?sni=hy.example.com&obfs=salamander&obfs-password=secret123#Hy"))
	if err != nil || len(conns) != 1 {
		t.Fatalf("salamander: %d, %v", len(conns), err)
	}
	if s := string(conns[0].Outbound); !strings.Contains(s, `"salamander"`) || !strings.Contains(s, `"secret123"`) || conns[0].Proto != "Hysteria2" {
		t.Errorf("маска не попала в подключение: %s", s)
	}
	for _, l := range []string{"hysteria2://a@hy.example.com:443?obfs=other&obfs-password=x#b", "hysteria2://a@hy.example.com:443?obfs=salamander#b"} {
		if _, err := parseSubBody([]byte(l)); err == nil || !strings.Contains(err.Error(), "обфускация") {
			t.Errorf("%s: ждали отказ про обфускацию, получили %v", l, err)
		}
	}
}

// Подписка с учётом устройств: клиент шлёт один и тот же случайный HWID и не шлёт имя компьютера
func TestSubHeadersHWID(t *testing.T) {
	old := hwidFile
	hwidFile = filepath.Join(t.TempDir(), "hwid")
	t.Cleanup(func() { hwidFile = old })
	oldPlat := plat
	plat = testDevice{}
	t.Cleanup(func() { plat = oldPlat })
	var got []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Clone())
		if r.Header.Get("x-hwid") == "" {
			w.Write([]byte("vless://00000000-0000-0000-0000-000000000000@0.0.0.0:1?type=tcp&security=none#need-hwid"))
			return
		}
		w.Write([]byte(sampleLinks[1]))
	}))
	defer srv.Close()
	for i := 0; i < 2; i++ {
		s, err := fetchSub(context.Background(), srv.Client(), srv.URL)
		if err != nil || len(s.Conns) != 1 || s.Conns[0].Proto != "Vision" {
			t.Fatalf("подписка %d: %+v, %v", i, s.Conns, err)
		}
	}
	a, b := got[0].Get("x-hwid"), got[1].Get("x-hwid")
	if !hwidRe.MatchString(a) || a != b {
		t.Errorf("HWID должен быть один и тот же: %q, %q", a, b)
	}
	if got[0].Get("x-device-os") != "Windows" || !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(got[0].Get("x-ver-os")) {
		t.Errorf("заголовки устройства: %v", got[0])
	}
	host, _ := os.Hostname()
	for k, v := range got[0] {
		if val := strings.ToLower(strings.Join(v, " ")); host != "" && (val == strings.ToLower(host) || (len(host) > 3 && strings.Contains(val, strings.ToLower(host)))) {
			t.Errorf("в заголовке %s имя компьютера", k)
		}
	}
}

// Заглушки панели (адрес самого компьютера) — не серверы: одни заглушки дают ошибку с их текстом,
// вперемешку с настоящими серверами они просто убираются
func TestSubPlaceholders(t *testing.T) {
	old := hwidFile
	hwidFile = filepath.Join(t.TempDir(), "hwid")
	t.Cleanup(func() { hwidFile = old })
	stub := func(name string) string {
		return "vless://99999999-9999-9999-9999-999999999999@127.0.0.1:443?type=tcp&security=none#" + name
	}
	body := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
	defer srv.Close()

	body = strings.Join([]string{stub("%E2%9A%A0%EF%B8%8F%20%D0%9F%D0%BE%D0%B4%D0%BF%D0%B8%D1%81%D0%BA%D0%B5%20%D0%BD%D1%83%D0%B6%D0%B5%D0%BD%20HWID"), stub("%E2%9A%A0%EF%B8%8F%20%D0%9E%D0%B1%D1%80%D0%B0%D1%82%D0%B8%D1%82%D0%B5%D1%81%D1%8C%20%D0%B2%20%D0%BF%D0%BE%D0%B4%D0%B4%D0%B5%D1%80%D0%B6%D0%BA%D1%83")}, "\n")
	s, err := fetchSub(context.Background(), srv.Client(), srv.URL)
	if err == nil || len(s.Conns) != 0 || err.Error() != "Подписке нужен HWID. Обратитесь в поддержку" {
		t.Errorf("одни заглушки: %d подключений, ошибка %q", len(s.Conns), err)
	}

	body = stub("info") + "\n" + sampleLinks[1] + "\n" + strings.Replace(stub("zero"), "127.0.0.1", "0.0.0.0", 1)
	s, err = fetchSub(context.Background(), srv.Client(), srv.URL)
	if err != nil || len(s.Conns) != 1 || s.Conns[0].Proto != "Vision" {
		t.Errorf("заглушки среди серверов: %+v, %v", s.Conns, err)
	}
	for host, want := range map[string]bool{"127.0.0.1": true, "0.0.0.0": true, "localhost": true, "[::1]": true, "": true, "nl.example.com": false, "8.8.8.8": false} {
		if got := (Conn{Server: host}).placeholder(); got != want {
			t.Errorf("placeholder(%q) = %v", host, got)
		}
	}
}

// Подписка, которая по основному адресу отвечает только знакомым приложениям (502 остальным),
// скачивается по тому же адресу с /json; исправная подписка второго запроса не делает
func TestSubJSONFallback(t *testing.T) {
	old := hwidFile
	hwidFile = filepath.Join(t.TempDir(), "hwid")
	t.Cleanup(func() { hwidFile = old })
	var paths []string
	strict := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if strict && !strings.HasSuffix(r.URL.Path, "/json") {
			w.WriteHeader(502)
			return
		}
		w.Write([]byte(sampleLinks[1]))
	}))
	defer srv.Close()
	s, err := fetchSub(context.Background(), srv.Client(), srv.URL+"/abcDEF123?x=1")
	if err != nil || len(s.Conns) != 1 || strings.Join(paths, " ") != "/abcDEF123 /abcDEF123/json" {
		t.Errorf("запасной адрес: %d подключений, %v, запросы %v", len(s.Conns), err, paths)
	}
	strict, paths = false, nil
	if _, err := fetchSub(context.Background(), srv.Client(), srv.URL+"/abcDEF123"); err != nil || len(paths) != 1 {
		t.Errorf("исправная подписка: %v, запросы %v", err, paths)
	}
	strict, paths = true, nil
	if _, err := fetchSub(context.Background(), srv.Client(), srv.URL+"/abcDEF123/json"); err != nil || len(paths) != 1 {
		t.Errorf("адрес уже с /json: %v, запросы %v", err, paths)
	}
}

// testDevice — оболочка системы для теста заголовков: только сведения об устройстве
type testDevice struct{ noPlatform }

func (testDevice) OSName() string    { return "Windows" }
func (testDevice) OSVersion() string { return "10.0.26200" }
func (testDevice) Device() string    { return "Desktop" }
