package core

import (
	"encoding/json"
	"strings"
	"testing"
)

// ключи в тестах выдуманные: 32 байта в base64
const (
	awgTestPriv = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="
	awgTestPub  = "ICEiIyQlJicoKSorLC0uLzAxMjM0NTY3ODk6Ozw9Pj8="
	awgTestPSK  = "QEFCQ0RFRkdISUpLTE1OT1BRUlNUVVZXWFlaW1xdXl8="
	awgTestHPK  = "YGFiY2RlZmdoaWprbG1ub3BxcnN0dXZ3eHl6e3x9fn8="
)

const awgTestConf = `# выдан админкой
[Interface]
PrivateKey = ` + awgTestPriv + `
Address = 10.9.9.2/32, fd00::2/128
DNS = 1.1.1.1
MTU = 1280
Jc = 6
Jmin = 10
Jmax = 50
S1 = 12
S2 = 12
S3 = 12
S4 = 12
H1 = 1
H2 = 2
H3 = 3
H4 = 4
HeaderProtectionKey = ` + awgTestHPK + `
RandomTrailers = on
DisableCookies = on
RekeyAfterTime = 100-120

[Peer]
PublicKey = ` + awgTestPub + `
PresharedKey = ` + awgTestPSK + `
Endpoint = vpn.example.com:46110
AllowedIPs = 0.0.0.0/0, ::/0
PersistentKeepalive = 22-30
`

func TestParseAWGConf(t *testing.T) {
	conns, err := parseSubBody([]byte(awgTestConf))
	if err != nil || len(conns) != 1 {
		t.Fatalf("разбор: %v, подключений %d", err, len(conns))
	}
	c := conns[0]
	if c.Proto != "AmneziaWG" || c.Server != "vpn.example.com" || c.Name != "vpn.example.com" {
		t.Fatalf("подключение: %+v", c)
	}
	var ob struct {
		Protocol string `json:"protocol"`
		Settings struct {
			SecretKey string            `json:"secretKey"`
			Address   []string          `json:"address"`
			MTU       int               `json:"mtu"`
			AWG       map[string]string `json:"awg"`
			Peers     []struct {
				PublicKey, PreSharedKey, Endpoint string
				KeepAlive                         int
				AllowedIPs                        []string
			} `json:"peers"`
		} `json:"settings"`
	}
	if err := json.Unmarshal(c.Outbound, &ob); err != nil {
		t.Fatal(err)
	}
	s := ob.Settings
	if ob.Protocol != "wireguard" || s.SecretKey != awgTestPriv || strings.Join(s.Address, ",") != "10.9.9.2,fd00::2" || s.MTU != 1280 {
		t.Fatalf("настройки: %+v", s)
	}
	if len(s.Peers) != 1 || s.Peers[0].Endpoint != "vpn.example.com:46110" || s.Peers[0].KeepAlive != 22 || s.Peers[0].PreSharedKey != awgTestPSK {
		t.Fatalf("сервер: %+v", s.Peers)
	}
	if s.AWG["Jc"] != "6" || s.AWG["HeaderProtectionKey"] != awgTestHPK || s.AWG["RandomTrailers"] != "on" || s.AWG["RekeyAfterTime"] != "100-120" || len(s.AWG) != 15 {
		t.Fatalf("параметры маскировки: %v", s.AWG)
	}
}

// обычный WireGuard без параметров маскировки остаётся WireGuard
func TestParsePlainWGConf(t *testing.T) {
	conf := "[Interface]\nPrivateKey = " + awgTestPriv + "\nAddress = 10.0.0.2/32\n\n[Peer]\nPublicKey = " + awgTestPub + "\nEndpoint = 203.0.113.5:51820\n"
	conns, err := parseSubBody([]byte(conf))
	if err != nil || len(conns) != 1 || conns[0].Proto != "WireGuard" || conns[0].Server != "203.0.113.5" {
		t.Fatalf("%v %+v", err, conns)
	}
}

func TestBadWGConf(t *testing.T) {
	for name, conf := range map[string]string{
		"нет ключа":       "[Interface]\nAddress = 10.0.0.2/32\n[Peer]\nPublicKey = " + awgTestPub + "\nEndpoint = 1.2.3.4:1\n",
		"нет сервера":     "[Interface]\nPrivateKey = " + awgTestPriv + "\nAddress = 10.0.0.2/32\n",
		"нет адреса":      "[Interface]\nPrivateKey = " + awgTestPriv + "\n[Peer]\nPublicKey = " + awgTestPub + "\nEndpoint = 1.2.3.4:1\n",
		"плохой параметр": "[Interface]\nPrivateKey = " + awgTestPriv + "\nAddress = 10.0.0.2/32\nS1 = много\n[Peer]\nPublicKey = " + awgTestPub + "\nEndpoint = 1.2.3.4:1\n",
		"два сервера":     "[Interface]\nPrivateKey = " + awgTestPriv + "\nAddress = 10.0.0.2/32\n[Peer]\nPublicKey = " + awgTestPub + "\nEndpoint = 1.2.3.4:1\n[Peer]\nPublicKey = " + awgTestPub + "\nEndpoint = 1.2.3.5:1\n",
	} {
		if conns, err := parseSubBody([]byte(conf)); err == nil {
			t.Errorf("%s: принят как %+v", name, conns)
		}
	}
}

// подключение AmneziaWG из подписки (JSON) получает своё имя протокола и в конфиге ядра идёт без системного адаптера
func TestAWGFromSubscription(t *testing.T) {
	_, ob, err := parseWGConf(awgTestConf)
	if err != nil {
		t.Fatal(err)
	}
	ob["tag"] = "proxy"
	body, _ := json.Marshal([]any{genRule{"remarks": "Stockholm", "outbounds": []any{ob, genRule{"tag": "direct", "protocol": "freedom"}}}})
	conns, err := parseSubBody(body)
	if err != nil || len(conns) != 1 || conns[0].Proto != "AmneziaWG" || conns[0].Name != "Stockholm" {
		t.Fatalf("%v %+v", err, conns)
	}
	raw, err := buildConfig(Settings{Mode: "all", Port: 10808, DNS: "https://1.1.1.1/dns-query"}, Sub{Conns: conns}, true, "Ethernet")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Outbounds []struct {
			Tag      string `json:"tag"`
			Settings struct {
				NoKernelTun bool `json:"noKernelTun"`
			} `json:"settings"`
			StreamSettings struct {
				Sockopt struct {
					Interface string `json:"interface"`
				} `json:"sockopt"`
			} `json:"streamSettings"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	for _, o := range cfg.Outbounds {
		if o.Tag == "px-0" {
			if !o.Settings.NoKernelTun || o.StreamSettings.Sockopt.Interface != "Ethernet" {
				t.Fatalf("подключение в конфиге ядра: %+v", o)
			}
			return
		}
	}
	t.Fatal("подключения px-0 нет в конфиге ядра")
}
