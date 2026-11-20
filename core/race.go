package core

// Гонка путей: первое соединение с новым сайтом уходит сразу двумя путями, напрямую и через VPN.
// Прямой путь главный: если он ответил, работаем напрямую, как бы быстро ни ответил VPN. VPN — запасной путь
// наготове: его ответ берётся, только если прямой завис дольше секунды или оборвался.
// Лишнее — только начало рукопожатия по VPN и только пока сайт не запомнен.

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/xtls/xray-core/app/dispatcher"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/features/outbound"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/pipe"
)

const (
	raceBlockAfter = time.Second      // прямой молчит дольше — завис (провайдер): обычный сервер отвечает на рукопожатие за доли секунды
	raceGiveUp     = 15 * time.Second // ни один путь не ответил
	raceMemoTTL    = 30 * time.Minute // победа прямого пути: столько не гоняем сайт снова
	raceBufLimit   = 1 << 20          // байт в трубе пути: больше не копим, писатель ждёт читателя
	raceMemoMax    = 4096             // запомненных сайтов больше — убрать просроченные
	raceUpGrace    = 5 * time.Second  // ответ сервера кончился: столько ещё ждём конца запроса программы
)

type raceMemo struct {
	mu     sync.Mutex
	direct map[string]time.Time // корневой домен → до какого времени считать прямой путь рабочим
}

var races = &raceMemo{direct: map[string]time.Time{}}

// raceFound — куда сообщить о заблокированном сайте (в тестах подменяется). Победа VPN по времени —
// признак ненадёжный: в «найдено» сайт попадёт только после проверки двумя путями
var raceFound = func(domain, reason string) { app.report(domain, reason) }

func (m *raceMemo) known(root string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.direct[root]
	return ok && time.Now().Before(t)
}

// forget: прямой путь сайта больше не считаем рабочим — следующее соединение снова с гонкой
func (m *raceMemo) forget(root string) {
	m.mu.Lock()
	delete(m.direct, root)
	m.mu.Unlock()
}

func (m *raceMemo) setDirect(root string) {
	m.mu.Lock()
	now := time.Now()
	if len(m.direct) >= raceMemoMax { // просроченные не копим
		for k, t := range m.direct {
			if !now.Before(t) {
				delete(m.direct, k)
			}
		}
		if len(m.direct) >= raceMemoMax { // все свежие: начать заново, худшее — лишняя гонка
			m.direct = map[string]time.Time{}
		}
	}
	m.direct[root] = now.Add(raceMemoTTL)
	m.mu.Unlock()
}

func hookIntercept() {
	dispatcher.Intercept = intercept
	go stalls.loop()
}

// intercept: соединение, которое маршрут отправил напрямую. Новый сайт — гонка, известный — слежка за зависанием
func intercept(ctx context.Context, h outbound.Handler, target xnet.Destination, link *transport.Link) bool {
	if h.Tag() != "direct" || target.Address == nil || !target.Address.Family().IsDomain() || target.Port != 443 {
		return false
	}
	d := strings.ToLower(target.Address.Domain())
	if skipDomain(d) || app == nil || !app.smartOn() || app.listed(d) {
		return false
	}
	if target.Network == xnet.Network_UDP {
		smart.candidate(d) // проверка нового сайта начинается с первого же соединения, а не с записи в журнале
	}
	if target.Network == xnet.Network_UDP && (stalls.quicBlocked(rootOf(d)) || smart.checking(d)) {
		// HTTP/3 к сайту завис раньше (watch.go) или сайт сейчас проверяется (smart.go):
		// отказ сразу, браузер повторит по HTTPS
		common.Interrupt(link.Reader)
		common.Close(link.Writer)
		return true
	}
	ip := "" // адрес сервера: в TUN назначение — адрес, имя сайта узнано из рукопожатия
	if obs := session.OutboundsFromContext(ctx); len(obs) > 0 && obs[0].Target.Address != nil && obs[0].Target.Address.Family().IsIP() {
		ip = obs[0].Target.Address.String()
	}
	orig := link
	link, wc := stalls.wrap(d, target.Network.SystemString(), ip, link)
	// новый сайт, проверка ещё идёт: по настройке соединение ведёт VPN, прямой путь — запасной
	vpnFirst := target.Network == xnet.Network_TCP && app.vpnFirst() && smart.pending(d)
	if target.Network != xnet.Network_TCP || !vpnFirst && races.known(rootOf(d)) {
		h.Dispatch(ctx, link)
		return true
	}
	wc.vpnFirst, wc.orig = vpnFirst, orig
	vpn := app.vpnHandler()
	if vpn == nil {
		h.Dispatch(ctx, link)
		return true
	}
	race(ctx, d, h, vpn, link, wc)
	return true
}

// ctxFor: свой экземпляр сведений о выходе для каждого пути, чтобы обработчики не писали в один
func ctxFor(ctx context.Context) (context.Context, context.CancelFunc) {
	obs := session.OutboundsFromContext(ctx)
	if len(obs) > 0 {
		cp := append([]*session.Outbound{}, obs...)
		last := *obs[len(obs)-1]
		cp[len(cp)-1] = &last
		ctx = session.ContextWithOutbounds(ctx, cp)
	}
	return context.WithCancel(ctx)
}

type racePath struct {
	name   string
	up     *pipe.Writer // от программы к сайту
	down   *pipe.Reader // от сайта к программе
	cancel context.CancelFunc
	ended  chan struct{}   // обработчик пути вернулся
	first  buf.MultiBuffer // первый ответ сервера
	err    error
	at     time.Duration
	lagged bool // до решения не успевал читать запрос: часть данных ему не дошла, победить не может (под mu гонки)
}

// трубы ограничены: после решения медленная программа или медленный сервер тормозят другую сторону, а не копят память
func startPath(ctx context.Context, name string, h outbound.Handler) *racePath {
	uR, uW := pipe.New(pipe.WithSizeLimit(raceBufLimit))
	dR, dW := pipe.New(pipe.WithSizeLimit(raceBufLimit))
	c, cancel := ctxFor(ctx)
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		defer recoverLog("гонке путей (" + name + ")")
		h.Dispatch(c, &transport.Link{Reader: uR, Writer: dW})
	}()
	return &racePath{name: name, up: uW, down: dR, cancel: cancel, ended: ended}
}

func (p *racePath) stop() {
	p.cancel()
	common.Close(p.up)
	common.Interrupt(p.down) // первый ответ проигравшего не освобождаем: его поток мог ещё не дописать (уберёт сборщик мусора)
}

func copyMB(mb buf.MultiBuffer) buf.MultiBuffer {
	out := make(buf.MultiBuffer, 0, len(mb))
	for _, b := range mb {
		nb := buf.New()
		nb.Write(b.Bytes())
		out = append(out, nb)
	}
	return out
}

func race(ctx context.Context, domain string, direct, vpn outbound.Handler, link *transport.Link, wc *watchConn) {
	root := rootOf(domain)
	start := time.Now()
	paths := []*racePath{startPath(ctx, "direct", direct), startPath(ctx, "vpn", vpn)}

	// данные программы до решения уходят обоими путями, после — только победителю
	var mu sync.Mutex
	targets := paths
	var winner *racePath
	final := false // targets уже итоговые (под mu)
	decided := make(chan struct{})
	upDone := make(chan struct{}) // программа закончила слать запрос
	go func() {
		defer close(upDone)
		defer recoverLog("гонке путей (запрос)") // сбой здесь не должен ронять программу
		for {
			mb, err := link.Reader.ReadMultiBuffer()
			mu.Lock()
			ts := targets
			if len(ts) > 1 && !mb.IsEmpty() {
				// до решения путь с переполненной трубой не ждём: запись в неё встала бы и задержала второй путь.
				// Пока в трубе не больше предела, запись не блокируется (писатель здесь один)
				var ok []*racePath
				for _, p := range ts {
					if p.up.Len() > raceBufLimit {
						p.lagged = true
						continue
					}
					ok = append(ok, p)
				}
				if len(ok) != len(ts) {
					targets = ok
					ts = ok
				}
			}
			after := final // ошибка записи после решения — это путь победителя, а не остановленный проигравший
			mu.Unlock()
			if !mb.IsEmpty() {
				if len(ts) == 0 {
					buf.ReleaseMulti(mb)
				}
				for i, p := range ts {
					part := mb
					if i < len(ts)-1 {
						part = copyMB(mb)
					}
					// после решения путь победителя закрыт — дальше слать некуда, оборвать и запрос программы
					if werr := p.up.WriteMultiBuffer(part); werr != nil && after {
						common.Interrupt(link.Reader)
						return
					}
				}
			}
			if err != nil {
				<-decided
				mu.Lock()
				w := winner
				mu.Unlock()
				if w != nil {
					common.Close(w.up)
				}
				return
			}
		}
	}()

	got := make(chan *racePath, 2)
	for _, p := range paths {
		go func(p *racePath) {
			defer recoverLog("гонке путей (ответ)")
			p.first, p.err = p.down.ReadMultiBuffer()
			p.at = time.Since(start)
			got <- p
		}(p)
	}

	// прямой путь — всегда, если отвечает. VPN — только когда прямой завис (молчит дольше raceBlockAfter)
	// или оборвался: это борьба с провайдером, а не выбор, где быстрее.
	// vpnFirst (сайт новый, идёт проверка) — наоборот: VPN главный, прямой берётся, только если VPN
	// завис или оборвался. Обрыв посреди ответа у провайдера виден лишь позже первого ответа: соединение,
	// начатое напрямую, пришлось бы обрывать, и браузер приходил в себя 10-15 с
	vpnFirst := wc.vpnFirst
	var dRes, vRes *racePath
	timeout := time.NewTimer(raceGiveUp)
	defer timeout.Stop()
	block := time.NewTimer(raceBlockAfter)
	defer block.Stop()
	stalled := false // прямой молчит дольше raceBlockAfter
wait:
	for {
		directFailed := dRes != nil && dRes.err != nil
		directOK := dRes != nil && dRes.err == nil
		vpnOK := vRes != nil && vRes.err == nil
		vpnFailed := vRes != nil && vRes.err != nil
		if vpnOK && (vpnFirst || stalled || directFailed) {
			winner = paths[1]
			break
		}
		if vpnFirst && directOK && (stalled || vpnFailed) {
			winner = paths[0]
			break
		}
		if directFailed && vRes != nil { // оба пути оборвались
			break
		}
		select {
		case p := <-got:
			if p.name == "direct" {
				dRes = p
				if p.err == nil && !vpnFirst {
					winner = paths[0]
					break wait
				}
			} else {
				vRes = p
			}
		case <-block.C:
			stalled = true
		case <-timeout.C:
			break wait
		}
	}

	mu.Lock()
	if winner != nil && winner.lagged { // запрос дошёл не целиком: такое соединение не продолжить
		winner = nil
	}
	if winner != nil {
		targets = []*racePath{winner}
	} else {
		targets = nil
	}
	final = true
	mu.Unlock()
	close(decided)

	if winner == nil {
		for _, p := range paths {
			p.stop()
		}
		common.Interrupt(link.Reader)
		common.Close(link.Writer)
		return
	}
	loser := paths[0]
	if winner == loser {
		loser = paths[1]
	}
	loser.stop()
	if winner.name == "direct" {
		races.setDirect(root)
	} else if vpnFirst {
		wc.done.Store(true) // соединение идёт через VPN, слежка за прямым не нужна
		dispatcher.Switched(wc.orig, vpn.Tag(), "check-vpn")
	} else {
		wc.done.Store(true)
		dispatcher.Switched(wc.orig, vpn.Tag(), "race-vpn")
		why := "не отвечает напрямую"
		if dRes != nil && dRes.err != nil {
			why = "обрыв напрямую"
		}
		logLine(domain + ": " + why + ", через VPN ответ за " + vRes.at.Round(time.Millisecond).String())
		go raceFound(domain, why)
	}

	// первый ответ победителя — программе, дальше просто перекачка
	if err := link.Writer.WriteMultiBuffer(winner.first); err != nil {
		winner.first = nil
		winner.stop()
		common.Interrupt(link.Reader)
		return
	}
	winner.first = nil
	if err := buf.Copy(winner.down, link.Writer); err != nil {
		// программа ушла или путь оборвался: разбудить обе стороны, иначе обработчик пути
		// так и ждёт места в ограниченной трубе
		common.Interrupt(winner.down)
		common.Interrupt(winner.up)
		common.Interrupt(link.Reader)
		common.Interrupt(link.Writer)
		winner.cancel()
		return
	}
	common.Close(link.Writer)
	// ответ кончился, а программа ещё шлёт: обработчик пути скоро перестанет читать, и запись в ограниченную
	// трубу встала бы навсегда. Даём запросу дойти, потом закрываем. Контекст пути гасим только после:
	// отмена сразу оборвала бы отправку у обработчика, и отсрочка теряла бы смысл
	go func() {
		defer recoverLog("гонке путей (отсрочка)")
		defer winner.cancel()
		t := time.NewTimer(raceUpGrace)
		defer t.Stop()
		select {
		case <-upDone:
			// запрос целиком в трубе: ждём, пока обработчик допишет его серверу и вернётся
			select {
			case <-winner.ended:
			case <-t.C:
			}
		case <-t.C:
			common.Close(winner.up)
		}
	}()
}
