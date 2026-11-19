package dispatcher

// monolit: учёт открытых соединений, путь которых выбрал маршрутизатор. Правила маршрутов меняются на лету
// (смена режима), а открытое соединение остаётся на старом пути, пока программа сама не переподключится:
// игра, открытая в режиме «Всё», так и шла бы через VPN. Reroute спрашивает маршрутизатор заново
// и обрывает те соединения, чей путь поменялся: программа переподключается уже правильным путём.

import (
	"sync"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/features/routing"
	"github.com/xtls/xray-core/transport"
)

type tracked struct {
	router routing.Router
	rc     routing.Context
	tag    string
	def    string // выход по умолчанию: им идёт соединение, когда ни одно правило не подошло
	link   *transport.Link
	seen   *transport.Link // тот же link, каким его видит Intercept (обёрнут журналом соединений)
	alt    string          // выход, которым соединение пошло на деле (Switched); пусто — как выбрал маршрутизатор
}

var (
	trackMu  sync.Mutex
	trackSet = map[*tracked]struct{}{}
)

func track(r routing.Router, rc routing.Context, tag, def string, link *transport.Link) func() {
	_, done := trackT(r, rc, tag, def, link)
	return done
}

func trackT(r routing.Router, rc routing.Context, tag, def string, link *transport.Link) (*tracked, func()) {
	t := &tracked{router: r, rc: rc, tag: tag, def: def, link: link}
	trackMu.Lock()
	trackSet[t] = struct{}{}
	trackMu.Unlock()
	return t, func() {
		trackMu.Lock()
		delete(trackSet, t)
		trackMu.Unlock()
	}
}

func (t *tracked) sees(link *transport.Link) {
	trackMu.Lock()
	t.seen = link
	trackMu.Unlock()
}

// Switched (monolit): соединение на деле пошло выходом outTag, а не тем, что выбрал маршрутизатор
// (гонка путей отдала его VPN). Журнал соединений показывает настоящий путь и причину rule, а Reroute
// не обрывает его, пока новый маршрут ведёт на любой из двух выходов. link — тот, что получил Intercept
func Switched(link *transport.Link, outTag, rule string) {
	if link == nil {
		return
	}
	trackMu.Lock()
	for t := range trackSet {
		if t.link == link || t.seen == link {
			t.alt = outTag
		}
	}
	trackMu.Unlock()
	if r, ok := connByLink.Load(link); ok {
		r.(*connRec).sw.Store(&[2]string{outTag, rule})
	}
}

// Reroute обрывает соединения, которым маршрутизатор теперь выбирает другой выход.
// same решает, считать ли два выхода одним путём (например, два прямых выхода). Возвращает, сколько оборвано
func Reroute(same func(old, new string) bool) int {
	trackMu.Lock()
	list := make([]*tracked, 0, len(trackSet))
	for t := range trackSet {
		list = append(list, t)
	}
	trackMu.Unlock()
	n := 0
	for _, t := range list {
		// правило не подошло — соединение пойдёт выходом по умолчанию. Раньше такие пропускались, и в режиме
		// «Всё» сайт, шедший напрямую по правилу умного режима, оставался на прямом пути
		next := t.def
		if route, err := t.router.PickRoute(t.rc); err == nil {
			next = route.GetOutboundTag()
		}
		trackMu.Lock()
		alt := t.alt
		trackMu.Unlock()
		if next == "" || same(t.tag, next) || alt != "" && same(alt, next) {
			continue
		}
		trackMu.Lock()
		_, alive := trackSet[t]
		delete(trackSet, t)
		trackMu.Unlock()
		if !alive {
			continue
		}
		common.Interrupt(t.link.Reader)
		common.Interrupt(t.link.Writer)
		n++
	}
	return n
}
