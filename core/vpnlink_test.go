package core

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"testing"
)

// qCompress из Qt: длина несжатого в 4 байтах и поток zlib
func qCompress(data []byte) []byte {
	var b bytes.Buffer
	binary.Write(&b, binary.BigEndian, uint32(len(data)))
	w := zlib.NewWriter(&b)
	w.Write(data)
	w.Close()
	return b.Bytes()
}

func vpnLinkOf(data []byte) string {
	return "vpn://" + base64.RawURLEncoding.EncodeToString(data)
}

// ключ приложения AmneziaVPN в том виде, в каком его выдаёт админка monolit: JSON приложения с текстом .conf внутри
func amneziaKeyJSON(t *testing.T, key string) []byte {
	t.Helper()
	last, _ := json.Marshal(genRule{"config": awgTestConf, "hostName": "vpn.example.com", "port": 46110})
	raw, err := json.Marshal(genRule{
		"containers":       []any{genRule{"container": "amnezia-" + key, key: genRule{"last_config": string(last), "isThirdPartyConfig": true, "port": "46110", "transport_proto": "udp"}}},
		"defaultContainer": "amnezia-" + key, "description": "Stockholm", "hostName": "vpn.example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestParseVPNLink(t *testing.T) {
	for name, link := range map[string]string{
		"сжатый JSON":        vpnLinkOf(qCompress(amneziaKeyJSON(t, "awg"))),
		"JSON без сжатия":    vpnLinkOf(amneziaKeyJSON(t, "awg")),
		"сжатый текст .conf": vpnLinkOf(qCompress([]byte(awgTestConf))),
		"с дополнением =":    vpnLinkOf(qCompress(amneziaKeyJSON(t, "awg"))) + "==",
	} {
		conns, err := parseSubBody([]byte(link))
		if err != nil || len(conns) != 1 {
			t.Fatalf("%s: %v, подключений %d", name, err, len(conns))
		}
		c := conns[0]
		if c.Proto != "AmneziaWG" || c.Server != "vpn.example.com" {
			t.Errorf("%s: %+v", name, c)
		}
		wantName := "Stockholm"
		if name == "сжатый текст .conf" {
			wantName = "vpn.example.com"
		}
		if c.Name != wantName {
			t.Errorf("%s: имя %q", name, c.Name)
		}
		var ob struct {
			Settings struct {
				SecretKey string            `json:"secretKey"`
				AWG       map[string]string `json:"awg"`
			} `json:"settings"`
		}
		json.Unmarshal(c.Outbound, &ob)
		if ob.Settings.SecretKey != awgTestPriv || ob.Settings.AWG["HeaderProtectionKey"] != awgTestHPK || ob.Settings.AWG["S3"] != "12" {
			t.Errorf("%s: ключи или параметры не те", name)
		}
	}
}

func TestBadVPNLink(t *testing.T) {
	other, _ := json.Marshal(genRule{"containers": []any{genRule{"container": "amnezia-openvpn", "openvpn": genRule{"last_config": "{}"}}}})
	for name, link := range map[string]string{
		"не base64":          "vpn://%%%",
		"пусто":              "vpn://",
		"мусор":              vpnLinkOf([]byte("hello")),
		"нет WireGuard":      vpnLinkOf(qCompress(other)),
		"пустой last_config": vpnLinkOf(qCompress([]byte(`{"containers":[{"container":"amnezia-awg","awg":{"last_config":"{}"}}]}`))),
	} {
		if conns, err := parseSubBody([]byte(link)); err == nil {
			t.Errorf("%s: принят как %+v", name, conns)
		}
	}
}
