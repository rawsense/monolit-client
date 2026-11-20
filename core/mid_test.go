package core

import (
	"testing"
	"time"
)

func tlsRec(typ byte, n int) []byte {
	b := make([]byte, 5+n)
	b[0], b[1], b[2], b[3], b[4] = typ, 3, 3, byte(n>>8), byte(n)
	return b
}

// записи TLS: целые — не посреди; оборвались в теле или в заголовке — посреди; не TLS — не разбираем
func TestTLSRecs(t *testing.T) {
	var r tlsRecs
	r.feed(append(tlsRec(22, 100), tlsRec(23, 16384)...))
	if r.mid.Load() {
		t.Fatal("целые записи посчитаны оборванными")
	}
	rec := tlsRec(23, 16384)
	r.feed(rec[:9000]) // провайдер пропустил часть записи
	if !r.mid.Load() {
		t.Fatal("обрыв в теле записи не замечен")
	}
	r.feed(rec[9000:])
	if r.mid.Load() {
		t.Fatal("запись дошла, а считается оборванной")
	}
	r.feed(rec[:3]) // кусок заголовка
	if !r.mid.Load() {
		t.Fatal("обрыв в заголовке не замечен")
	}
	r.feed(rec[3:])
	if r.mid.Load() {
		t.Fatal("после заголовка по частям — всё ещё посреди")
	}
	// по байту — тот же итог
	var s tlsRecs
	for _, b := range append(tlsRec(23, 50), tlsRec(23, 70)...) {
		s.feed([]byte{b})
	}
	if s.mid.Load() || s.broken {
		t.Fatal("по байту: неверно")
	}
	var h tlsRecs
	h.feed([]byte("HTTP/1.1 200 OK\r\n\r\n"))
	if !h.broken || h.mid.Load() {
		t.Fatal("не TLS: должен перестать разбирать")
	}
}

func TestCutMid(t *testing.T) {
	c := &watchConn{network: "tcp", start: time.Now()}
	rec := tlsRec(23, 16384)
	c.recs.feed(rec[:16000])
	c.lastDn.Store(time.Now().Add(-midWait - time.Second).UnixNano())
	if !c.cutMid(time.Now()) {
		t.Fatal("обрыв посреди ответа не замечен")
	}
	c.lastDn.Store(time.Now().UnixNano())
	if c.cutMid(time.Now()) {
		t.Fatal("данные только что шли — рано")
	}
	u := &watchConn{network: "udp", start: time.Now()}
	u.recs.feed(rec[:16000])
	u.lastDn.Store(time.Now().Add(-time.Minute).UnixNano())
	if u.cutMid(time.Now()) {
		t.Fatal("UDP не разбираем")
	}
	if !reliableReason(midReason) {
		t.Fatal("обрыв посреди ответа должен быть надёжным признаком")
	}
}

// HTTP/3 к сайту завис — напрямую не пускаем на время, другие сайты не трогаем
func TestNoQUIC(t *testing.T) {
	s := &stallWatch{conns: map[*watchConn]struct{}{}, testing: map[string]bool{}, noQUIC: map[string]time.Time{}}
	if s.quicBlocked("vinfo-api.com") {
		t.Fatal("заблокирован без причины")
	}
	s.quicOff("vinfo-api.com")
	if !s.quicBlocked("vinfo-api.com") || s.quicBlocked("example.com") {
		t.Fatal("неверно")
	}
	s.noQUIC["vinfo-api.com"] = time.Now().Add(-time.Second)
	if s.quicBlocked("vinfo-api.com") {
		t.Fatal("срок вышел, а всё ещё заблокирован")
	}
}

// UDP: повторы без ответа не сбрасывают отсчёт — зависание видно через stallWait от первой отправки;
// TCP: отсчёт от последней отправки, как раньше
func TestStuckUDP(t *testing.T) {
	mk := func(nw string) (*watchConn, *watchReader) {
		c := &watchConn{network: nw, start: time.Now().Add(-10 * time.Second)}
		c.down.Store(20 << 10) // ответ уже шёл: ждём stallWait
		return c, &watchReader{c: c}
	}
	send := func(r *watchReader, ago time.Duration) {
		r.note(nil, nil) // пустая пачка ничего не меняет
		n := int64(200)
		at := time.Now().Add(-ago).UnixNano()
		if r.c.pending.Add(n) == n {
			r.c.pendAt.Store(at)
		}
		r.c.lastUp.Store(at)
	}
	u, ur := mk("udp")
	send(ur, 3*time.Second)        // первая без ответа — 3 с назад
	send(ur, 500*time.Millisecond) // повтор недавно
	if !u.stuck(time.Now()) {
		t.Fatal("UDP: повтор сбросил отсчёт")
	}
	c, cr := mk("tcp")
	send(cr, 3*time.Second)
	send(cr, 500*time.Millisecond)
	if c.stuck(time.Now()) {
		t.Fatal("TCP: отсчёт должен идти от последней отправки")
	}
}
