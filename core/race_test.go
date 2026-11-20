package core

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/pipe"
)

// fakePath: получает запрос, через delay отвечает reply (или молчит, если delay < 0), дальше эхо
type fakePath struct {
	tag   string
	delay time.Duration
	reply string
	mu    sync.Mutex
	got   []byte
}

func (f *fakePath) Start() error                         { return nil }
func (f *fakePath) Close() error                         { return nil }
func (f *fakePath) Tag() string                          { return f.tag }
func (f *fakePath) SenderSettings() *serial.TypedMessage { return nil }
func (f *fakePath) ProxySettings() *serial.TypedMessage  { return nil }
func (f *fakePath) Dispatch(ctx context.Context, l *transport.Link) {
	first := true
	for {
		mb, err := l.Reader.ReadMultiBuffer()
		f.mu.Lock()
		for _, b := range mb {
			f.got = append(f.got, b.Bytes()...)
		}
		f.mu.Unlock()
		if err != nil {
			common.Close(l.Writer)
			return
		}
		if first {
			first = false
			if f.delay < 0 {
				buf.ReleaseMulti(mb)
				<-ctx.Done()
				common.Close(l.Writer)
				return
			}
			select {
			case <-time.After(f.delay):
			case <-ctx.Done():
				return
			}
			l.Writer.WriteMultiBuffer(buf.MergeBytes(nil, []byte(f.reply)))
		}
		buf.ReleaseMulti(mb)
	}
}

func runRace(t *testing.T, direct, vpn *fakePath) (string, []string) {
	t.Helper()
	var mu sync.Mutex
	var found []string
	raceFound = func(d, r string) { mu.Lock(); found = append(found, d); mu.Unlock() }
	races = &raceMemo{direct: map[string]time.Time{}}
	cR, cW := pipe.New(pipe.WithoutSizeLimit()) // программа → клиент
	sR, sW := pipe.New(pipe.WithoutSizeLimit()) // клиент → программа
	wc := &watchConn{}
	go race(context.Background(), "a.example.com", direct, vpn, &transport.Link{Reader: cR, Writer: sW}, wc)
	cW.WriteMultiBuffer(buf.MergeBytes(nil, []byte("hello")))
	got := make(chan string, 1)
	go func() {
		mb, err := sR.ReadMultiBuffer()
		if err != nil {
			got <- "ERR"
			return
		}
		got <- mb.String()
	}()
	var res string
	select {
	case res = <-got:
	case <-time.After(20 * time.Second):
		t.Fatal("нет ответа")
	}
	common.Close(cW)
	time.Sleep(1200 * time.Millisecond) // отметка в секунду
	mu.Lock()
	defer mu.Unlock()
	return res, found
}

func TestRaceDirectFast(t *testing.T) {
	d, v := &fakePath{tag: "direct", delay: 50 * time.Millisecond, reply: "D"}, &fakePath{tag: "vpn", delay: 100 * time.Millisecond, reply: "V"}
	res, found := runRace(t, d, v)
	if res != "D" || len(found) != 0 || !races.known("example.com") {
		t.Fatalf("ответ %q, найдено %v", res, found)
	}
	if string(v.got) != "hello" {
		t.Fatalf("VPN получил %q", v.got)
	}
}

func TestRaceDirectSilent(t *testing.T) {
	d, v := &fakePath{tag: "direct", delay: -1}, &fakePath{tag: "vpn", delay: 80 * time.Millisecond, reply: "V"}
	res, found := runRace(t, d, v)
	if res != "V" || len(found) != 1 {
		t.Fatalf("ответ %q, найдено %v", res, found)
	}
}

func TestRaceDirectEdge(t *testing.T) { // VPN первым, но прямой успел в фору
	d, v := &fakePath{tag: "direct", delay: 120 * time.Millisecond, reply: "D"}, &fakePath{tag: "vpn", delay: 30 * time.Millisecond, reply: "V"}
	res, found := runRace(t, d, v)
	if res != "D" || len(found) != 0 {
		t.Fatalf("ответ %q, найдено %v", res, found)
	}
}

func TestRaceDirectNotMuchSlower(t *testing.T) { // VPN 200 мс, прямой 450 мс: не сильно дольше — прямой
	d, v := &fakePath{tag: "direct", delay: 450 * time.Millisecond, reply: "D"}, &fakePath{tag: "vpn", delay: 200 * time.Millisecond, reply: "V"}
	res, found := runRace(t, d, v)
	if res != "D" || len(found) != 0 {
		t.Fatalf("ответ %q, найдено %v", res, found)
	}
}

func TestRaceDirectSlowButAlive(t *testing.T) { // VPN быстрее, но прямой ответил до секунды: прямой
	d, v := &fakePath{tag: "direct", delay: 800 * time.Millisecond, reply: "D"}, &fakePath{tag: "vpn", delay: 30 * time.Millisecond, reply: "V"}
	res, found := runRace(t, d, v)
	if res != "D" || len(found) != 0 || !races.known("example.com") {
		t.Fatalf("ответ %q, найдено %v", res, found)
	}
}

func TestRaceDirectStalled(t *testing.T) { // прямой ответил бы через 1,5 с: завис, VPN на отметке в секунду
	d, v := &fakePath{tag: "direct", delay: 1500 * time.Millisecond, reply: "D"}, &fakePath{tag: "vpn", delay: 30 * time.Millisecond, reply: "V"}
	res, found := runRace(t, d, v)
	if res != "V" || len(found) != 1 {
		t.Fatalf("ответ %q, найдено %v", res, found)
	}
}

func TestRaceStream(t *testing.T) { // после решения данные идут только победителю и не теряются
	d, v := &fakePath{tag: "direct", delay: 10 * time.Millisecond, reply: "D"}, &fakePath{tag: "vpn", delay: -1}
	races = &raceMemo{direct: map[string]time.Time{}}
	raceFound = func(string, string) {}
	cR, cW := pipe.New(pipe.WithoutSizeLimit())
	sR, sW := pipe.New(pipe.WithoutSizeLimit())
	go race(context.Background(), "b.example.com", d, v, &transport.Link{Reader: cR, Writer: sW}, &watchConn{})
	cW.WriteMultiBuffer(buf.MergeBytes(nil, []byte("hello")))
	mb, _ := sR.ReadMultiBuffer()
	if mb.String() != "D" {
		t.Fatalf("первый ответ %q", mb.String())
	}
	cW.WriteMultiBuffer(buf.MergeBytes(nil, []byte(" world")))
	common.Close(cW)
	for i := 0; i < 50; i++ {
		time.Sleep(20 * time.Millisecond)
		d.mu.Lock()
		s := string(d.got)
		d.mu.Unlock()
		if s == "hello world" {
			io.Discard.Write(nil)
			return
		}
	}
	t.Fatalf("прямой путь получил %q", d.got)
}

// floodPath: на первый запрос отвечает reply и дальше без остановки шлёт данные, считая, сколько приняла труба
type floodPath struct {
	tag   string
	reply string
	sent  atomic.Int64
}

func (f *floodPath) Start() error                         { return nil }
func (f *floodPath) Close() error                         { return nil }
func (f *floodPath) Tag() string                          { return f.tag }
func (f *floodPath) SenderSettings() *serial.TypedMessage { return nil }
func (f *floodPath) ProxySettings() *serial.TypedMessage  { return nil }
func (f *floodPath) Dispatch(ctx context.Context, l *transport.Link) {
	mb, err := l.Reader.ReadMultiBuffer()
	buf.ReleaseMulti(mb)
	if err != nil {
		return
	}
	go func() { // запрос дальше читаем и выбрасываем
		for {
			mb, err := l.Reader.ReadMultiBuffer()
			buf.ReleaseMulti(mb)
			if err != nil {
				return
			}
		}
	}()
	l.Writer.WriteMultiBuffer(buf.MergeBytes(nil, []byte(f.reply)))
	chunk := make([]byte, 64<<10)
	for f.sent.Load() < 64<<20 {
		if l.Writer.WriteMultiBuffer(buf.MergeBytes(nil, chunk)) != nil {
			return
		}
		f.sent.Add(int64(len(chunk)))
	}
}

// needPath: читает запрос без остановки и отвечает, только получив need байт; не читает после ответа, если stopAfter
type needPath struct {
	tag       string
	need      int64
	reply     string
	stopAfter bool
	release   chan struct{} // при stopAfter: после закрытия завершить соединение
	got       atomic.Int64
}

func (f *needPath) Start() error                         { return nil }
func (f *needPath) Close() error                         { return nil }
func (f *needPath) Tag() string                          { return f.tag }
func (f *needPath) SenderSettings() *serial.TypedMessage { return nil }
func (f *needPath) ProxySettings() *serial.TypedMessage  { return nil }
func (f *needPath) Dispatch(ctx context.Context, l *transport.Link) {
	replied := false
	for {
		mb, err := l.Reader.ReadMultiBuffer()
		f.got.Add(int64(mb.Len()))
		buf.ReleaseMulti(mb)
		if err != nil {
			common.Close(l.Writer)
			return
		}
		if !replied && f.got.Load() >= f.need {
			replied = true
			l.Writer.WriteMultiBuffer(buf.MergeBytes(nil, []byte(f.reply)))
			if f.stopAfter {
				select {
				case <-f.release:
				case <-ctx.Done():
				}
				common.Interrupt(l.Reader)
				common.Close(l.Writer)
				return
			}
		}
	}
}

// writeN: программа пишет total байт кусками, written — сколько у неё уже приняли
func writeN(w *pipe.Writer, total int, written *atomic.Int64) {
	chunk := make([]byte, 32<<10)
	for n := 0; n < total; n += len(chunk) {
		if w.WriteMultiBuffer(buf.MergeBytes(nil, chunk)) != nil {
			return
		}
		if written != nil {
			written.Add(int64(len(chunk)))
		}
	}
}

// программа не читает (видео на паузе), а сервер шлёт быстро: в памяти не больше предела труб
func TestRaceDownBounded(t *testing.T) {
	races = &raceMemo{direct: map[string]time.Time{}}
	raceFound = func(string, string) {}
	d, v := &floodPath{tag: "direct", reply: "D"}, &fakePath{tag: "vpn", delay: -1}
	cR, cW := pipe.New(pipe.WithoutSizeLimit())
	sR, sW := pipe.New(pipe.WithSizeLimit(64 << 10)) // труба к программе, как у диспетчера
	done := make(chan struct{})
	go func() {
		race(context.Background(), "c.example.com", d, v, &transport.Link{Reader: cR, Writer: sW}, &watchConn{})
		close(done)
	}()
	cW.WriteMultiBuffer(buf.MergeBytes(nil, []byte("hello")))
	mb, _ := sR.ReadMultiBuffer()
	if !strings.HasPrefix(mb.String(), "D") {
		t.Fatalf("первый ответ %q", mb.String())
	}
	buf.ReleaseMulti(mb)
	time.Sleep(700 * time.Millisecond)
	s1 := d.sent.Load()
	time.Sleep(300 * time.Millisecond)
	if s2 := d.sent.Load(); s2 != s1 || s2 > 2*raceBufLimit+(1<<20) {
		t.Fatalf("сервер успел отдать %d → %d байт, программа не читала", s1, s2)
	}
	common.Interrupt(sR) // программа ушла: гонка должна завершиться и разбудить сервер
	common.Close(cW)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("гонка не завершилась после ухода программы")
	}
}

// программа шлёт быстро, а сервер после ответа не читает: из программы забрано не больше предела трубы
func TestRaceUpBounded(t *testing.T) {
	races = &raceMemo{direct: map[string]time.Time{}}
	raceFound = func(string, string) {}
	d := &needPath{tag: "direct", need: 1, reply: "D", stopAfter: true, release: make(chan struct{})}
	v := &fakePath{tag: "vpn", delay: -1}
	cR, cW := pipe.New(pipe.WithSizeLimit(64 << 10)) // труба от программы, как у диспетчера
	sR, sW := pipe.New(pipe.WithoutSizeLimit())
	done := make(chan struct{})
	go func() {
		race(context.Background(), "d.example.com", d, v, &transport.Link{Reader: cR, Writer: sW}, &watchConn{})
		close(done)
	}()
	cW.WriteMultiBuffer(buf.MergeBytes(nil, []byte("hello")))
	mb, _ := sR.ReadMultiBuffer()
	if mb.String() != "D" {
		t.Fatalf("первый ответ %q", mb.String())
	}
	var written atomic.Int64
	go writeN(cW, 16<<20, &written)
	time.Sleep(500 * time.Millisecond)
	w1 := written.Load()
	time.Sleep(300 * time.Millisecond)
	if w2 := written.Load(); w2 != w1 || w2 > raceBufLimit+(1<<20) {
		t.Fatalf("у программы приняли %d → %d байт при пределе %d", w1, w2, raceBufLimit)
	}
	close(d.release) // сервер закрыл соединение: гонка завершается, запись программы не висит
	common.Close(cW)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("гонка не завершилась после ухода программы")
	}
}

// до решения VPN не читает запрос, а прямому нужен весь запрос: переполненный VPN не должен задерживать прямой
func TestRaceLaggedLoser(t *testing.T) {
	races = &raceMemo{direct: map[string]time.Time{}}
	raceFound = func(string, string) {}
	const total = 3 << 20
	d, v := &needPath{tag: "direct", need: total, reply: "D"}, &fakePath{tag: "vpn", delay: -1}
	cR, cW := pipe.New(pipe.WithSizeLimit(64 << 10))
	sR, sW := pipe.New(pipe.WithoutSizeLimit())
	go race(context.Background(), "e.example.com", d, v, &transport.Link{Reader: cR, Writer: sW}, &watchConn{})
	go writeN(cW, total, nil)
	got := make(chan string, 1)
	go func() {
		mb, _ := sR.ReadMultiBuffer()
		got <- mb.String()
	}()
	select {
	case r := <-got:
		if r != "D" {
			t.Fatalf("ответ %q", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("прямой получил %d из %d байт: запрос застрял на VPN", d.got.Load(), total)
	}
	common.Close(cW)
	common.Interrupt(sR)
}

func TestRaceMemoPrune(t *testing.T) {
	m := &raceMemo{direct: map[string]time.Time{}}
	for i := 0; i < raceMemoMax; i++ {
		m.direct[fmt.Sprintf("old%d.com", i)] = time.Now().Add(-time.Minute)
	}
	m.setDirect("new.com")
	if len(m.direct) != 1 || !m.known("new.com") {
		t.Fatalf("после чистки %d записей", len(m.direct))
	}
}

// graceUpPath: отвечает сразу и закрывает ответ, а запрос дочитывает и запоминает, жив ли ещё контекст
type graceUpPath struct {
	fakePath
	ctxAlive atomic.Bool // контекст был жив, когда пришёл хвост запроса
	ended    chan struct{}
}

func (g *graceUpPath) Dispatch(ctx context.Context, l *transport.Link) {
	defer close(g.ended)
	first := true
	for {
		mb, err := l.Reader.ReadMultiBuffer()
		g.mu.Lock()
		for _, b := range mb {
			g.got = append(g.got, b.Bytes()...)
		}
		tail := strings.Contains(string(g.got), "more")
		g.mu.Unlock()
		buf.ReleaseMulti(mb)
		if tail {
			g.ctxAlive.Store(ctx.Err() == nil)
		}
		if err != nil {
			<-ctx.Done() // после отсрочки контекст должен погаснуть
			return
		}
		if first {
			first = false
			l.Writer.WriteMultiBuffer(buf.MergeBytes(nil, []byte(g.reply)))
			common.Close(l.Writer)
		}
	}
}

// ответ кончился раньше запроса: хвост запроса доходит при живом контексте, потом контекст гаснет
func TestRaceUpGraceKeepsCtx(t *testing.T) {
	races = &raceMemo{direct: map[string]time.Time{}}
	raceFound = func(d, r string) {}
	direct := &graceUpPath{fakePath: fakePath{tag: "direct", reply: "ok"}, ended: make(chan struct{})}
	vpn := &fakePath{tag: "vpn", delay: -1}
	cR, cW := pipe.New(pipe.WithoutSizeLimit())
	sR, sW := pipe.New(pipe.WithoutSizeLimit())
	go race(context.Background(), "a.example.com", direct, vpn, &transport.Link{Reader: cR, Writer: sW}, &watchConn{})
	cW.WriteMultiBuffer(buf.MergeBytes(nil, []byte("hello")))
	for { // дочитать ответ до конца
		mb, err := sR.ReadMultiBuffer()
		buf.ReleaseMulti(mb)
		if err != nil {
			break
		}
	}
	time.Sleep(300 * time.Millisecond)
	cW.WriteMultiBuffer(buf.MergeBytes(nil, []byte("more")))
	common.Close(cW)
	select {
	case <-direct.ended:
	case <-time.After(raceUpGrace + 3*time.Second):
		t.Fatal("обработчик не завершился: контекст не погас")
	}
	direct.mu.Lock()
	got := string(direct.got)
	direct.mu.Unlock()
	if got != "hellomore" {
		t.Fatalf("запрос дошёл не целиком: %q", got)
	}
	if !direct.ctxAlive.Load() {
		t.Fatal("контекст погас до конца запроса")
	}
}

func runRaceVPNFirst(t *testing.T, direct, vpn *fakePath) (string, []string) {
	t.Helper()
	var mu sync.Mutex
	var found []string
	raceFound = func(d, r string) { mu.Lock(); found = append(found, d); mu.Unlock() }
	races = &raceMemo{direct: map[string]time.Time{}}
	cR, cW := pipe.New(pipe.WithoutSizeLimit())
	sR, sW := pipe.New(pipe.WithoutSizeLimit())
	go race(context.Background(), "n.example.com", direct, vpn, &transport.Link{Reader: cR, Writer: sW}, &watchConn{vpnFirst: true})
	cW.WriteMultiBuffer(buf.MergeBytes(nil, []byte("hello")))
	got := make(chan string, 1)
	go func() {
		mb, err := sR.ReadMultiBuffer()
		if err != nil {
			got <- "ERR"
			return
		}
		got <- mb.String()
	}()
	var res string
	select {
	case res = <-got:
	case <-time.After(20 * time.Second):
		t.Fatal("нет ответа")
	}
	common.Close(cW)
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	return res, found
}

// сайт проверяется: VPN главный, даже когда прямой ответил раньше. В «найдено» сайт гонка не отправляет
func TestRaceVPNFirst(t *testing.T) {
	d, v := &fakePath{tag: "direct", delay: 30 * time.Millisecond, reply: "D"}, &fakePath{tag: "px-0", delay: 150 * time.Millisecond, reply: "V"}
	res, found := runRaceVPNFirst(t, d, v)
	if res != "V" || len(found) != 0 || races.known("example.com") {
		t.Fatalf("ответ %q, найдено %v, прямой запомнен %v", res, found, races.known("example.com"))
	}
}

// VPN молчит дольше секунды: берётся прямой путь, сайт не ломается
func TestRaceVPNFirstFallback(t *testing.T) {
	d, v := &fakePath{tag: "direct", delay: 30 * time.Millisecond, reply: "D"}, &fakePath{tag: "px-0", delay: -1}
	t0 := time.Now()
	res, found := runRaceVPNFirst(t, d, v)
	if res != "D" || len(found) != 0 {
		t.Fatalf("ответ %q, найдено %v", res, found)
	}
	if e := time.Since(t0); e > 2500*time.Millisecond {
		t.Fatalf("долго: %v", e)
	}
}
