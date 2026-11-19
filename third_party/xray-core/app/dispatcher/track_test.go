package dispatcher

import (
	"errors"
	"testing"

	"github.com/xtls/xray-core/features/routing"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/pipe"
)

// маршрутизатор-заглушка: выход задаётся полем, как будто правила заменили
type fakeRouter struct {
	routing.Router
	out string
}

type fakeRoute struct {
	routing.Route
	out string
}

func (r fakeRoute) GetOutboundTag() string { return r.out }

func (r *fakeRouter) PickRoute(routing.Context) (routing.Route, error) {
	if r.out == "" {
		return nil, errors.New("no rule")
	}
	return fakeRoute{out: r.out}, nil
}

func newLink() (*transport.Link, *pipe.Reader) {
	ur, uw := pipe.New()
	dr, dw := pipe.New()
	_ = uw
	return &transport.Link{Reader: ur, Writer: dw}, dr
}

// игра открылась в «Всё» через VPN; после смены на «Умный» её путь прямой — соединение обрывается.
// Прямое соединение, которому положен другой прямой выход, не трогаем
func TestReroute(t *testing.T) {
	r := &fakeRouter{out: "px-0"}
	game, gameDown := newLink()
	site, siteDown := newLink()
	doneGame := track(r, nil, "px-0", "px-0", game)
	doneSite := track(r, nil, "direct", "px-0", site)
	defer doneGame()
	defer doneSite()

	same := func(a, b string) bool {
		g := func(s string) string {
			if s == "direct-ru" {
				return "direct"
			}
			return s
		}
		return g(a) == g(b)
	}
	r.out = "direct-ru"
	if n := Reroute(same); n != 1 {
		t.Fatalf("оборвано %d, ждали 1 (только игра)", n)
	}
	if _, err := gameDown.ReadMultiBuffer(); err == nil {
		t.Fatal("соединение игры не оборвано")
	}
	if err := site.Writer.WriteMultiBuffer(nil); err != nil {
		t.Fatal("прямое соединение оборвано зря")
	}
	_ = siteDown
	if n := Reroute(same); n != 0 {
		t.Fatalf("повторный пересчёт оборвал ещё %d", n)
	}
}

// сайт шёл напрямую по правилу умного режима; в «Всё» правила для него нет, путь — выход по умолчанию (VPN).
// Раньше такое соединение пропускалось и оставалось на прямом пути
func TestRerouteToDefault(t *testing.T) {
	r := &fakeRouter{out: "direct"}
	site, siteDown := newLink()
	done := track(r, nil, "direct", "px-0", site)
	defer done()
	r.out = ""
	if n := Reroute(func(a, b string) bool { return a == b }); n != 1 {
		t.Fatalf("оборвано %d, ждали 1", n)
	}
	if _, err := siteDown.ReadMultiBuffer(); err == nil {
		t.Fatal("соединение не оборвано")
	}
}

// гонка отдала соединение VPN, хотя маршрут был прямой (Switched). Сайт попал в «найдено» (маршрут стал VPN)
// или остался прямым — соединение не обрывается. Обрывается, только когда маршрут ушёл на третий выход
func TestRerouteSwitched(t *testing.T) {
	r := &fakeRouter{out: "direct"}
	site, _ := newLink()
	seen := &transport.Link{Reader: site.Reader, Writer: site.Writer} // как его видит Intercept
	tr, done := trackT(r, nil, "direct", "px-0", site)
	defer done()
	tr.sees(seen)
	Switched(seen, "px-0", "check-vpn")
	same := func(a, b string) bool { return a == b }
	for _, out := range []string{"px-0", "direct"} {
		r.out = out
		if n := Reroute(same); n != 0 {
			t.Fatalf("маршрут %s: оборвано %d, ждали 0", out, n)
		}
	}
	r.out = "block"
	if n := Reroute(same); n != 1 {
		t.Fatalf("маршрут block: оборвано %d, ждали 1", n)
	}
}
