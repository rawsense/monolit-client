package core

// Сервисы, которые обходчики блокировок на компьютере (zapret, GoodbyeDPI) часто открывают напрямую.
// В умном режиме они не уходят в VPN по базе заблокированных: каждый проверяется напрямую сам по себе
// (бывает, что обходчик открывает Discord, но не YouTube, и наоборот). Открылся — сервис идёт напрямую,
// нет — через VPN. Проверка — при подключении, раз в 10 минут и сразу, как только прямое соединение
// сервиса зависло или оборвалось (гонка путей и слежка за зависанием сообщают об этом в report)

import (
	"strings"
	"sync"
	"time"
)

type service struct {
	Key, Name string
	Domains   []string // домены сервиса: для правила и чтобы узнать сервис по имени сайта
	Geo       []string // группы geosite в правило вместе с доменами
	Probe     []string // что открыть при проверке: напрямую должно открыться всё
}

var services = []service{
	{Key: "youtube", Name: "YouTube",
		Domains: []string{"youtube.com", "youtu.be", "yt.be", "googlevideo.com", "ytimg.com", "youtube-nocookie.com", "youtubei.googleapis.com", "yt3.ggpht.com", "yt4.ggpht.com"},
		Geo:     []string{"geosite:youtube"},
		// страница — замедление видно по загрузке (readBody), сервер видео — без него страница есть, а видео нет
		Probe: []string{"www.youtube.com", "redirector.googlevideo.com"}},
	{Key: "discord", Name: "Discord",
		Domains: []string{"discord.com", "discord.gg", "discordapp.com", "discordapp.net", "discord.media", "discordcdn.com", "discord.dev", "discord.new", "discord.gift", "discordstatus.com", "dis.gd"},
		Geo:     []string{"geosite:discord"},
		Probe:   []string{"discord.com", "gateway.discord.gg", "cdn.discordapp.com"}},
}

const (
	svcDirect = "direct"
	svcVPN    = "vpn"
	svcEvery  = 10 * time.Minute // плановая проверка
	svcRetry  = time.Minute      // повторная по сбою прямого соединения — не чаще
)

// svcOf: к какому сервису относится сайт, или nil
func svcOf(d string) *service {
	d = strings.TrimSuffix(strings.ToLower(d), ".")
	for i := range services {
		for _, s := range services[i].Domains {
			if matchDomain(d, s) {
				return &services[i]
			}
		}
	}
	return nil
}

// svcRules — правила умного режима: проверенные напрямую — напрямую, остальные (и ещё не проверенные) — через VPN.
// По правилу на итог, а не на сервис: ruleTag у ядра должен быть уникален, иначе маршруты не применяются
func svcRules(s Settings, via func(genRule) genRule) []any {
	by := map[string][]string{}
	for _, sv := range services {
		st := s.Svc[sv.Key]
		if st != svcDirect && st != svcVPN {
			st = "wait"
		}
		by[st] = append(by[st], append(append([]string{}, sv.Geo...), prefixed("domain:", sv.Domains)...)...)
	}
	var rules []any
	if d := by[svcDirect]; len(d) > 0 {
		rules = append(rules, genRule{"domain": d, "outboundTag": "direct", "ruleTag": "svc-direct"})
	}
	if d := by[svcVPN]; len(d) > 0 {
		rules = append(rules, via(genRule{"domain": d, "ruleTag": "svc-vpn"}))
	}
	if d := by["wait"]; len(d) > 0 {
		rules = append(rules, via(genRule{"domain": d, "ruleTag": "svc-wait"}))
	}
	return rules
}

func prefixed(p string, l []string) []string {
	r := make([]string, len(l))
	for i, s := range l {
		r[i] = p + s
	}
	return r
}

type svcChecker struct {
	mu     sync.Mutex
	busy   map[string]bool
	last   map[string]time.Time
	direct func(host string) string // подменяется в тестах; "" — открылся
}

var svcs = &svcChecker{busy: map[string]bool{}, last: map[string]time.Time{}, direct: func(h string) string { return probe(h, nil) }}

// svcCheckAll: проверить все сервисы, если с прошлой проверки прошло не меньше gap
func (a *App) svcCheckAll(gap time.Duration) {
	for i := range services {
		a.svcCheck(&services[i], gap)
	}
}

func (a *App) svcCheck(sv *service, gap time.Duration) {
	if !a.smartOn() {
		return
	}
	svcs.mu.Lock()
	if svcs.busy[sv.Key] || time.Since(svcs.last[sv.Key]) < gap {
		svcs.mu.Unlock()
		return
	}
	svcs.busy[sv.Key], svcs.last[sv.Key] = true, time.Now()
	svcs.mu.Unlock()
	safeGo("проверке "+sv.Name, func() {
		defer func() {
			svcs.mu.Lock()
			delete(svcs.busy, sv.Key)
			svcs.mu.Unlock()
		}()
		why, unsure := svcProbe(sv)
		if !a.smartOn() { // пока проверяли, отключились или сменили режим
			return
		}
		if unsure { // только сбой DNS: путь не меняем, проверим в следующий раз
			if firstNotice("сбой DNS " + sv.Key) {
				logLine(sv.Name + ": напрямую " + why + ", это сбой DNS, путь не меняю")
			}
			return
		}
		state := svcDirect
		if why != "" {
			state = svcVPN
		}
		a.svcSet(sv, state, why)
	})
}

// svcProbe: все адреса сервиса напрямую одновременно. "" — всё открылось, иначе первая причина.
// unsure — не открылось только из-за сбоя DNS: это не признак блокировки (dnsReason)
func svcProbe(sv *service) (why string, unsure bool) {
	res := make([]string, len(sv.Probe))
	var wg sync.WaitGroup
	for i, h := range sv.Probe {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer recoverLog("проверке " + h)
			res[i] = "сбой проверки"
			res[i] = svcs.direct(h)
		}()
	}
	wg.Wait()
	for i, r := range res {
		if r != "" && !dnsFail(r) {
			return sv.Probe[i] + ": " + r, false
		}
	}
	for i, r := range res {
		if r != "" {
			return sv.Probe[i] + ": " + r, true
		}
	}
	return "", false
}

func (a *App) svcState(sv *service) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.set.Svc[sv.Key]
}

// svcSet: запомнить итог проверки и, если путь сменился, пересобрать маршруты
func (a *App) svcSet(sv *service, state, why string) {
	a.mu.Lock()
	old := a.set.Svc[sv.Key]
	if old == state {
		a.mu.Unlock()
		return
	}
	m := make(map[string]string, len(services))
	for k, v := range a.set.Svc {
		m[k] = v
	}
	m[sv.Key] = state
	a.set.Svc = m // новая карта, а не правка на месте: снимки настроек смотрят в старую
	a.mu.Unlock()
	safeGo("apply", func() { a.apply(false) })
	if state == svcDirect {
		logLine(sv.Name + ": открывается напрямую, идёт без VPN")
		a.event("found", sv.Name+" напрямую", "открывается без VPN")
		return
	}
	logLine(sv.Name + ": напрямую не открылся (" + why + "), идёт через VPN")
	if old == svcDirect { // было напрямую и перестало: сказать; первая проверка с итогом «VPN» ничего не меняет
		a.event("found", sv.Name+" через VPN", "напрямую перестал открываться")
	}
}

// svcFailed: прямое соединение сервиса зависло или оборвалось — проверить сервис сейчас, не ждать плановой
func (a *App) svcFailed(sv *service, domain, reason string) {
	if a.svcState(sv) != svcDirect {
		return
	}
	logLine(domain + ": " + reason + ", проверяю " + sv.Name + " заново")
	a.svcCheck(sv, svcRetry)
}
