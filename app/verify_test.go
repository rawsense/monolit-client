package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestReasonReliable(t *testing.T) {
	for _, r := range []string{"подменён сертификат", "сброс соединения", "заглушка провайдера", "ответ 451, доступ ограничен"} {
		if !reliableReason(r) {
			t.Errorf("%q должен быть надёжным", r)
		}
	}
	for _, r := range []string{"замедление", "не отвечает напрямую", "обрыв напрямую", "адрес не находится", "нет ответа",
		"соединение не устанавливается", "обрыв при установке защищённого соединения", ""} {
		if reliableReason(r) {
			t.Errorf("%q должен проверяться двумя путями", r)
		}
	}
}

func TestRuleDomain(t *testing.T) {
	for in, want := range map[string]string{
		"www.linkedin.com":                            "linkedin.com",
		"news.bbc.co.uk":                              "bbc.co.uk",
		"cdn.steamstatic.com.":                        "steamstatic.com",
		"ec2-1-2-3-4.eu-west-1.compute.amazonaws.com": "ec2-1-2-3-4.eu-west-1.compute.amazonaws.com",
		"foo.amazonaws.com":                           "foo.amazonaws.com",
		"d111.cloudfront.net":                         "d111.cloudfront.net",
		"lh3.googleusercontent.com":                   "lh3.googleusercontent.com",
		"a1.akamaized.net":                            "a1.akamaized.net",
		"x.azureedge.net":                             "x.azureedge.net",
		"Static.Example.Azurewebsites.net":            "example.azurewebsites.net", // площадка из списка суффиксов: имя сайта на ней
	} {
		if got := ruleDomain(in); got != want {
			t.Errorf("ruleDomain(%q) = %q, ждали %q", in, got, want)
		}
	}
}

// fakeVerifier: пути проверки и список подменены
func fakeVerifier(direct, vpn func(string) string) (*verifier, *[]string, *[]string) {
	v := newVerifier()
	var mu sync.Mutex
	var added, dropped []string
	v.on = func() bool { return true }
	v.direct, v.vpn = direct, vpn
	v.add = func(h, r string) { mu.Lock(); added = append(added, h); mu.Unlock() }
	v.drop = func(d string) bool { mu.Lock(); dropped = append(dropped, d); mu.Unlock(); return true }
	return v, &added, &dropped
}

func fixed(r string) func(string) string { return func(string) string { return r } }

func TestVerifyDecide(t *testing.T) {
	cases := []struct {
		name        string
		direct, vpn string
		add         bool
	}{
		{"напрямую открылся", "", "", false},
		{"напрямую открылся, VPN нет", "", "нет ответа", false},
		{"напрямую нет, через VPN да", "нет ответа", "", true},
		{"оба не открылись", "нет ответа", "нет ответа", false},
		{"напрямую сбой DNS, через VPN да", dnsReason, "", false},
	}
	for _, c := range cases {
		v, added, _ := fakeVerifier(fixed(c.direct), fixed(c.vpn))
		v.decide("www.verify-test.example", "замедление")
		if got := len(*added) == 1; got != c.add {
			t.Errorf("%s: добавлено %v", c.name, *added)
		}
	}
}

func TestVerifyBothAtOnce(t *testing.T) { // пути проверяются одновременно, а не по очереди
	slow := func(string) string { time.Sleep(300 * time.Millisecond); return "" }
	v, _, _ := fakeVerifier(slow, slow)
	st := time.Now()
	v.dual("a.example")
	if took := time.Since(st); took > 550*time.Millisecond {
		t.Fatalf("проверка заняла %v — пути шли по очереди", took)
	}
}

func TestVerifyDedupe(t *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int32
	direct := func(string) string { calls.Add(1); <-release; return "нет ответа" }
	v, added, _ := fakeVerifier(direct, fixed(""))
	var wg sync.WaitGroup
	for _, h := range []string{"a.dedupe.example", "b.dedupe.example", "dedupe.example", "a.dedupe.example"} {
		wg.Add(1)
		go func(h string) { defer wg.Done(); v.suspect(h, "не отвечает напрямую") }(h)
	}
	wg.Wait()
	time.Sleep(100 * time.Millisecond)
	if !v.busy("dedupe.example") {
		t.Fatal("проверка не отмечена как идущая")
	}
	close(release)
	deadline := time.Now().Add(3 * time.Second)
	for v.busy("dedupe.example") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("один сайт проверялся %d раз", n)
	}
	if len(*added) != 1 {
		t.Fatalf("добавлено %v", *added)
	}
	v.suspect("c.dedupe.example", "замедление") // только что проверяли: не чаще verifyEvery
	time.Sleep(100 * time.Millisecond)
	if n := calls.Load(); n != 1 {
		t.Fatalf("повторная проверка раньше срока: %d", n)
	}
}

// сбой DNS: добавленное по нему убирается без проверки, а при перепроверке он не сбрасывает счёт удачных
func TestRecheckDNS(t *testing.T) {
	var mu sync.Mutex
	res := ""
	checked := map[string]int{}
	direct := func(h string) string {
		mu.Lock()
		defer mu.Unlock()
		checked[h]++
		return res
	}
	set := func(r string) { mu.Lock(); res = r; mu.Unlock() }
	v, _, dropped := fakeVerifier(direct, fixed(""))
	v.gap = 80 * time.Millisecond

	v.recheck([]Rule{{Domain: "riot.example", Reason: dnsReason}})
	if len(*dropped) != 1 || checked["riot.example"] != 0 {
		t.Fatalf("добавленный по сбою DNS: убрано %v, проверок %d", *dropped, checked["riot.example"])
	}

	*dropped = nil
	list := []Rule{{Domain: "slow.example", Reason: "замедление"}}
	v.recheck(list) // удачная
	time.Sleep(100 * time.Millisecond)
	set(dnsReason)
	v.recheck(list) // сбой DNS: не в счёт
	time.Sleep(100 * time.Millisecond)
	set("")
	v.recheck(list) // вторая удачная подряд
	if len(*dropped) != 1 {
		t.Fatalf("сбой DNS сбросил счёт: убрано %v", *dropped)
	}
}

func TestRecheckNeedsTwo(t *testing.T) {
	var mu sync.Mutex
	checked := map[string]int{}
	ok := true
	direct := func(h string) string {
		mu.Lock()
		defer mu.Unlock()
		checked[h]++
		if ok {
			return ""
		}
		return "нет ответа"
	}
	v, _, dropped := fakeVerifier(direct, fixed(""))
	v.gap = 80 * time.Millisecond
	list := []Rule{
		{Domain: "slow.example", Reason: "замедление"},
		{Domain: "cert.example", Reason: "подменён сертификат"}, // надёжная причина: не перепроверяем
	}
	v.recheck(list)
	if len(*dropped) != 0 {
		t.Fatalf("убран после одной удачной проверки: %v", *dropped)
	}
	v.recheck(list) // сразу: раньше gap не проверяем
	if checked["slow.example"] != 1 {
		t.Fatalf("проверок %d, ждали 1", checked["slow.example"])
	}
	time.Sleep(100 * time.Millisecond)
	v.recheck(list)
	if len(*dropped) != 1 || (*dropped)[0] != "slow.example" {
		t.Fatalf("после двух удачных не убран: %v", *dropped)
	}
	if checked["cert.example"] != 0 {
		t.Fatal("сайт с надёжной причиной перепроверялся")
	}

	// неудача между удачными — счёт заново
	*dropped = nil
	v.recheck(list)
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	ok = false
	mu.Unlock()
	v.recheck(list)
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	ok = true
	mu.Unlock()
	v.recheck(list)
	if len(*dropped) != 0 {
		t.Fatalf("убран, хотя между удачными была неудача: %v", *dropped)
	}
	time.Sleep(100 * time.Millisecond)
	v.recheck(list)
	if len(*dropped) != 1 {
		t.Fatalf("после двух удачных подряд не убран: %v", *dropped)
	}
}
