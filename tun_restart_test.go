package main

import (
	"net"
	"os"
	"testing"
	"time"
)

// проверка: TUN можно поднять и закрыть несколько раз в одном процессе. Без маршрутов и адреса — сеть не меняется.
// Запуск только вручную с правами администратора: MONOLIT_TUN_TEST=1
func TestTunRestart(t *testing.T) {
	if os.Getenv("MONOLIT_TUN_TEST") != "1" {
		t.Skip("только вручную")
	}
	if err := ensureWintun(); err != nil {
		t.Fatal(err)
	}
	cfg := []byte(`{"log":{"loglevel":"warning"},"inbounds":[{"tag":"tun-in","protocol":"tun","settings":{"name":"` + tunName + `","MTU":1500}}],"outbounds":[{"tag":"direct","protocol":"freedom"}]}`)
	var c Core
	for i := 1; i <= 3; i++ {
		if err := c.Start(cfg, true); err != nil {
			t.Fatalf("запуск %d: %v", i, err)
		}
		time.Sleep(time.Second)
		_, err := net.InterfaceByName(tunName)
		t.Logf("запуск %d: адаптер есть: %v", i, err == nil)
		c.Stop()
		time.Sleep(time.Second)
		_, err = net.InterfaceByName(tunName)
		t.Logf("остановка %d: адаптер исчез: %v", i, err != nil)
	}
}
