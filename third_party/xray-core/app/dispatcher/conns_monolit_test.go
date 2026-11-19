package dispatcher

import (
	"testing"
	"time"

	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/features/routing"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/pipe"
)

type connCtx struct{ routing.Context }

func (connCtx) GetTargetDomain() string { return "example.com" }
func (connCtx) GetTargetIPs() []net.IP  { return nil }
func (connCtx) GetSourceIPs() []net.IP  { return []net.IP{net.ParseIP("127.0.0.1")} }
func (connCtx) GetSourcePort() net.Port { return 50000 }
func (connCtx) GetTargetPort() net.Port { return 443 }

// соединение записывается с трафиком, задержкой, программой и правилом; после закрытия остаётся в истории
func TestRecordConn(t *testing.T) {
	FindProcess = func(net.Destination) (int, string, string, error) { return 1, "discord", "C:/d/discord.exe", nil }
	upR, upW := pipe.New()
	downR, downW := pipe.New()
	dest := net.TCPDestination(net.DomainAddress("example.com"), 443)
	link, done := recordConn(connCtx{}, "tun-in", "px-1", "found", dest, &transport.Link{Reader: upR, Writer: downW})

	upW.WriteMultiBuffer(buf.MultiBuffer{buf.FromBytes(make([]byte, 100))})
	mb, _ := link.Reader.ReadMultiBuffer()
	buf.ReleaseMulti(mb)
	time.Sleep(20 * time.Millisecond)
	link.Writer.WriteMultiBuffer(buf.MultiBuffer{buf.FromBytes(make([]byte, 300))})
	mb, _ = downR.ReadMultiBuffer()
	buf.ReleaseMulti(mb)
	time.Sleep(50 * time.Millisecond) // программа ищется в фоне

	find := func() *ConnInfo {
		for _, c := range Conns() {
			if c.Rule == "found" && c.Domain == "example.com" {
				return &c
			}
		}
		return nil
	}
	c := find()
	if c == nil || c.Up != 100 || c.Down != 300 || c.FirstMs < 15 || c.Proc != "discord" || c.Outbound != "px-1" || !c.End.IsZero() {
		t.Fatalf("открытое: %+v", c)
	}
	done()
	if c = find(); c == nil || c.End.IsZero() {
		t.Fatalf("закрытое: %+v", c)
	}
}
