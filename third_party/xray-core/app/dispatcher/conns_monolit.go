package dispatcher

// monolit: журнал соединений для экрана «Соединения» — открытые сейчас и закрытые за последние 30 минут.
// Только в памяти: после перезапуска программы история начинается заново

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/features/routing"
	"github.com/xtls/xray-core/transport"
)

// ConnInfo — одно соединение, как его видит пользователь
type ConnInfo struct {
	ID       uint64
	Start    time.Time
	End      time.Time // нулевое — ещё открыто
	Network  string    // tcp, udp
	Domain   string    // имя сайта (из запроса или рукопожатия); пусто — известен только адрес
	Dest     string    // адрес:порт назначения
	Inbound  string    // тег входа: tun-in, mixed-in, probe-in
	Outbound string    // тег выхода: px-N, direct, direct-ru, block
	Rule     string    // тег сработавшего правила; пусто — путь по умолчанию
	Up, Down int64     // байт отправлено и получено
	FirstMs  int64     // от начала до первого ответа, мс; -1 — ответа ещё не было
	Proc     string    // программа без .exe
	ProcPath string
}

const (
	connKeep    = 30 * time.Minute
	connKeepMax = 5000 // закрытых не больше: при тысячах соединений в минуту память не растёт без края
)

type connRec struct {
	ConnInfo
	up, down atomic.Int64
	first    atomic.Int64 // мс до первого ответа + 1; 0 — не было
	proc     atomic.Pointer[[2]string]
	sw       atomic.Pointer[[2]string] // настоящий выход и причина, если путь сменила гонка (Switched)
	end      atomic.Int64              // unix нс; 0 — открыто
}

var connByLink sync.Map // обёрнутый link → *connRec, пока соединение открыто

var (
	connMu   sync.Mutex
	connSeq  uint64
	connOpen = map[*connRec]struct{}{}
	connDone []*connRec // по времени закрытия
	// FindProcess ищет программу по адресу источника; заменяется в тестах
	FindProcess = net.FindProcess
	// ConnsOn: журнал включён. Выключен — соединения не записываются и не замедляются
	ConnsOn atomic.Bool
)

func init() { ConnsOn.Store(true) }

// recordConn записывает соединение и оборачивает link счётчиками. Возвращает новый link и функцию закрытия
func recordConn(rc routing.Context, inTag, outTag, rule string, dest net.Destination, link *transport.Link) (*transport.Link, func()) {
	if !ConnsOn.Load() {
		return link, func() {}
	}
	r := &connRec{}
	r.Start = time.Now()
	r.Network = dest.Network.SystemString()
	r.Domain = rc.GetTargetDomain()
	if r.Domain == "" && dest.Address != nil && dest.Address.Family().IsDomain() {
		r.Domain = dest.Address.Domain()
	}
	if ips := rc.GetTargetIPs(); len(ips) > 0 {
		r.Dest = net.JoinHostPort(ips[0].String(), rc.GetTargetPort().String())
	} else if dest.Address != nil {
		r.Dest = dest.NetAddr()
	}
	r.Inbound, r.Outbound, r.Rule = inTag, outTag, rule
	connMu.Lock()
	connSeq++
	r.ID = connSeq
	connOpen[r] = struct{}{}
	connMu.Unlock()

	if src := rc.GetSourceIPs(); len(src) > 0 && (r.Network == "tcp" || r.Network == "udp") {
		nw := net.Network_TCP
		if r.Network == "udp" {
			nw = net.Network_UDP
		}
		d := net.Destination{Network: nw, Address: net.IPAddress(src[0]), Port: rc.GetSourcePort()}
		go func() { // таблица соединений Windows — не на пути данных
			defer func() { recover() }()
			if _, name, path, err := FindProcess(d); err == nil && name != "" {
				r.proc.Store(&[2]string{name, path})
			}
		}()
	}

	wrapped := &transport.Link{Reader: &countReader{Reader: link.Reader, n: &r.up}, Writer: &countWriter{Writer: link.Writer, r: r}}
	connByLink.Store(wrapped, r)
	return wrapped, func() {
		connByLink.Delete(wrapped)
		now := time.Now()
		r.end.Store(now.UnixNano())
		connMu.Lock()
		delete(connOpen, r)
		connDone = append(connDone, r)
		pruneLocked(now)
		connMu.Unlock()
	}
}

func pruneLocked(now time.Time) {
	i := 0
	for i < len(connDone) && (len(connDone)-i > connKeepMax || now.Sub(time.Unix(0, connDone[i].end.Load())) > connKeep) {
		i++
	}
	if i > 0 {
		connDone = append(connDone[:0], connDone[i:]...)
	}
}

func (r *connRec) info() ConnInfo {
	c := r.ConnInfo
	c.Up, c.Down = r.up.Load(), r.down.Load()
	c.FirstMs = r.first.Load() - 1
	if p := r.proc.Load(); p != nil {
		c.Proc, c.ProcPath = p[0], p[1]
	}
	if s := r.sw.Load(); s != nil {
		c.Outbound, c.Rule = s[0], s[1]
	}
	if e := r.end.Load(); e != 0 {
		c.End = time.Unix(0, e)
	}
	return c
}

// Conns — открытые соединения и закрытые за последние 30 минут
func Conns() []ConnInfo {
	connMu.Lock()
	pruneLocked(time.Now())
	list := make([]*connRec, 0, len(connOpen)+len(connDone))
	for r := range connOpen {
		list = append(list, r)
	}
	list = append(list, connDone...)
	connMu.Unlock()
	out := make([]ConnInfo, len(list))
	for i, r := range list {
		out[i] = r.info()
	}
	return out
}

// countReader считает отправленное (от программы к сайту)
type countReader struct {
	buf.Reader
	n *atomic.Int64
}

func (c *countReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	mb, err := c.Reader.ReadMultiBuffer()
	c.n.Add(int64(mb.Len()))
	return mb, err
}

func (c *countReader) ReadMultiBufferTimeout(t time.Duration) (buf.MultiBuffer, error) {
	tr, ok := c.Reader.(buf.TimeoutReader)
	if !ok {
		return c.ReadMultiBuffer()
	}
	mb, err := tr.ReadMultiBufferTimeout(t)
	c.n.Add(int64(mb.Len()))
	return mb, err
}

func (c *countReader) Interrupt()   { common.Interrupt(c.Reader) }
func (c *countReader) Close() error { return common.Close(c.Reader) }

// countWriter считает полученное и замечает первый ответ
type countWriter struct {
	buf.Writer
	r *connRec
}

func (c *countWriter) WriteMultiBuffer(mb buf.MultiBuffer) error {
	if n := int64(mb.Len()); n > 0 {
		c.r.down.Add(n)
		if c.r.first.Load() == 0 {
			c.r.first.CompareAndSwap(0, time.Since(c.r.Start).Milliseconds()+1)
		}
	}
	return c.Writer.WriteMultiBuffer(mb)
}

func (c *countWriter) Interrupt()   { common.Interrupt(c.Writer) }
func (c *countWriter) Close() error { return common.Close(c.Writer) }
