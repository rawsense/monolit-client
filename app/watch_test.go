package main

import (
	"testing"
	"time"

	"github.com/xtls/xray-core/common/buf"
)

type nopRW struct{}

func (nopRW) ReadMultiBuffer() (buf.MultiBuffer, error) { return nil, nil }
func (nopRW) WriteMultiBuffer(mb buf.MultiBuffer) error { buf.ReleaseMulti(mb); return nil }

func mb(n int) buf.MultiBuffer {
	b := buf.New()
	b.Extend(int32(n))
	return buf.MultiBuffer{b}
}

func TestStallRule(t *testing.T) {
	newC := func() (*watchConn, *watchReader, *watchWriter) {
		c := &watchConn{start: time.Now()}
		return c, &watchReader{nopRW{}, c}, &watchWriter{nopRW{}, c}
	}
	later := time.Now().Add(3 * time.Second)

	c, r, w := newC() // запрос ушёл, ответа нет
	r.note(mb(600), nil)
	if !c.stuck(later) {
		t.Fatal("зависший запрос не замечен")
	}

	c, r, w = newC() // запрос и ответ
	r.note(mb(600), nil)
	w.WriteMultiBuffer(mb(5000))
	if c.stuck(later) {
		t.Fatal("ответ получен, а считается зависанием")
	}

	c, r, w = newC() // после ответа только служебный кадр (подтверждение окна)
	r.note(mb(600), nil)
	w.WriteMultiBuffer(mb(5000))
	r.note(mb(40), nil)
	if c.stuck(later) {
		t.Fatal("служебный кадр принят за запрос")
	}

	c, r, _ = newC() // соединение закрыто
	r.note(mb(600), nil)
	w.Close()
	c.done.Store(true)
	if c.stuck(later) {
		t.Fatal("закрытое соединение считается зависшим")
	}

	c, r, _ = newC() // долгоживущее соединение
	c.start = time.Now().Add(-2 * time.Minute)
	r.note(mb(600), nil)
	if c.stuck(later) {
		t.Fatal("долгое соединение считается зависшим")
	}

	c, r, _ = newC() // рукопожатие: ни байта в ответ за 1,1 с — уже зависание
	r.note(mb(600), nil)
	if !c.stuck(time.Now().Add(1100 * time.Millisecond)) {
		t.Fatal("зависшее рукопожатие не замечено за секунду")
	}
	if c.stuck(time.Now().Add(500 * time.Millisecond)) {
		t.Fatal("рукопожатие: сработало раньше секунды")
	}

	c, r, w = newC() // запрос после рукопожатия: за 1,1 с ещё не зависание, за 3 с — да
	r.note(mb(600), nil)
	w.WriteMultiBuffer(mb(5000))
	r.note(mb(300), nil)
	if c.stuck(time.Now().Add(1100 * time.Millisecond)) {
		t.Fatal("запрос: сработало раньше 2,5 с")
	}
	if !c.stuck(later) {
		t.Fatal("зависший запрос после рукопожатия не замечен")
	}
}
