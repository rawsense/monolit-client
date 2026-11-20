package core

import "testing"

// копия подписки на диске с отступами и свежая без них — один и тот же конфиг, ядро перезапускать не надо
func TestSameJSON(t *testing.T) {
	disk := []byte("{\n  \"protocol\": \"vless\",\n  \"settings\": {\"port\": 443}\n}")
	fresh := []byte(`{"protocol":"vless","settings":{"port":443}}`)
	if !sameJSON(disk, fresh) {
		t.Fatal("одинаковые конфиги с разными отступами названы разными")
	}
	if sameJSON(fresh, []byte(`{"protocol":"vless","settings":{"port":8443}}`)) {
		t.Fatal("разные конфиги названы одинаковыми")
	}
}
