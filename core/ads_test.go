package core

import (
	"slices"
	"strings"
	"testing"
)

// блокировка рекламы: свои списки и кабинеты (analytics.google.com) — раньше неё; путь кабинетов как у правил ниже
func TestAdsAllow(t *testing.T) {
	idx := func(tags []string, tag string) int {
		return slices.IndexFunc(tags, func(s string) bool { return strings.HasPrefix(s, tag+">") })
	}
	tags := svcTestTags(t, Settings{Mode: "smart", BlockAds: true, Direct: []Rule{{Domain: "a.com"}}, Always: []Rule{{Domain: "b.com"}}})
	ads, allow, d, v := idx(tags, "ads"), idx(tags, "ads-allow"), idx(tags, "my-direct"), idx(tags, "my-vpn")
	if ads < 0 || allow < 0 || d < 0 || v < 0 || d > ads || v > ads || allow > ads {
		t.Fatalf("порядок правил: %v", tags)
	}
	if tags[allow] != "ads-allow>direct" {
		t.Fatalf("умный режим, google.com не в «найдено»: %s", tags[allow])
	}
	tags = svcTestTags(t, Settings{Mode: "smart", BlockAds: true, Found: []Rule{{Domain: "google.com"}}})
	if a := tags[idx(tags, "ads-allow")]; strings.HasSuffix(a, ">direct") {
		t.Fatalf("google.com в «найдено», а кабинет напрямую: %s", a)
	}
	tags = svcTestTags(t, Settings{Mode: "all", BlockAds: true})
	if a := tags[idx(tags, "ads-allow")]; strings.HasSuffix(a, ">direct") {
		t.Fatalf("всё через VPN, а кабинет напрямую: %s", a)
	}
	if tags = svcTestTags(t, Settings{Mode: "smart"}); idx(tags, "ads-allow") >= 0 || idx(tags, "ads") >= 0 {
		t.Fatalf("блокировка выключена, а правила есть: %v", tags)
	}
}

// программы напрямую: своё правило после своих списков сайтов, путь — напрямую даже в режиме «Всё»
func TestAppsDirect(t *testing.T) {
	tags := svcTestTags(t, Settings{Mode: "all", Always: []Rule{{Domain: "b.com"}}, Apps: []Rule{{Domain: "a.exe"}}, AppsDirect: []Rule{{Domain: "VALORANT.exe"}}})
	i := slices.Index(tags, "apps-direct>direct")
	v := slices.IndexFunc(tags, func(s string) bool { return strings.HasPrefix(s, "my-vpn>") })
	a := slices.IndexFunc(tags, func(s string) bool { return strings.HasPrefix(s, "apps>") })
	if i < 0 || v < 0 || a < 0 || v > i || v > a {
		t.Fatalf("правила программ: %v", tags)
	}
}
