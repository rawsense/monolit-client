package core

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/core"
	jsonconf "github.com/xtls/xray-core/infra/conf/serial"
)

// Ядро закрывается (перезапуск после обновления подписки), а запрос через AmneziaWG ещё в пути.
// Раньше такой запрос второй раз закрывал уже закрытую привязку, и падала вся программа
func TestWireGuardRequestAfterCoreClose(t *testing.T) {
	cfg := `{
 "log": {"loglevel": "none"},
 "outbounds": [{"tag": "px-0", "protocol": "wireguard", "settings": {
   "secretKey": "` + awgTestPriv + `", "address": ["10.13.0.2"], "mtu": 1280, "noKernelTun": true,
   "peers": [{"publicKey": "` + awgTestPub + `", "endpoint": "127.0.0.1:9", "allowedIPs": ["0.0.0.0/0"]}],
   "awg": {"Jc": "4", "Jmin": "10", "Jmax": "50", "S1": "12", "S2": "12", "S3": "12", "S4": "12", "HeaderProtectionKey": "` + awgTestHPK + `"}
 }}]
}`
	pb, err := jsonconf.LoadJSONConfig(bytes.NewReader([]byte(cfg)))
	if err != nil {
		t.Fatal(err)
	}
	inst, err := core.New(pb)
	if err != nil {
		t.Fatal(err)
	}
	if err := inst.Start(); err != nil {
		t.Fatal(err)
	}
	dial := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		ctx = session.ContextWithContent(ctx, &session.Content{SkipDNSResolve: true})
		ctx = session.SetForcedOutboundTagToContext(ctx, "px-0")
		if c, err := core.Dial(ctx, inst, net.TCPDestination(net.ParseAddress("192.0.2.1"), 80)); err == nil {
			c.Write([]byte("x")) // запрос уходит в подключение: здесь создаётся устройство
			time.AfterFunc(1500*time.Millisecond, func() { c.Close() })
		}
	}
	dial() // устройство создано
	time.Sleep(400 * time.Millisecond)
	inst.Close()
	time.Sleep(300 * time.Millisecond) // обработчик закрывает устройство в фоне
	for i := 0; i < 3; i++ {
		dial() // запросы, которые ещё шли в старое ядро
	}
	time.Sleep(2500 * time.Millisecond) // падение случилось бы здесь, в фоновой горутине ядра
}
