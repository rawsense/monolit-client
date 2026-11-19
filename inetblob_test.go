package main

import (
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/windows/registry"
)

func blob(t *testing.T) []byte {
	k, err := registry.OpenKey(registry.CURRENT_USER, inetKey+`\Connections`, registry.QUERY_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	b, _, err := k.GetBinaryValue("DefaultConnectionSettings")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// MONOLIT_INET=1: меняет только список исключений при выключенном прокси и возвращает его
func TestInetBlob(t *testing.T) {
	if os.Getenv("MONOLIT_INET") == "" {
		t.Skip()
	}
	before := blob(t)
	ik, _ := registry.OpenKey(registry.CURRENT_USER, inetKey, registry.QUERY_VALUE|registry.SET_VALUE)
	defer ik.Close()
	ov, _, _ := ik.GetStringValue("ProxyOverride")
	defer ik.SetStringValue("ProxyOverride", ov)
	if before[8]&2 != 0 {
		t.Skip("прокси включён, не трогаю")
	}
	if err := setInetProxy(false, "127.0.0.1:10808", "monolit-test;<local>"); err != nil {
		t.Fatal(err)
	}
	after := blob(t)
	if !strings.Contains(string(after), "monolit-test") {
		t.Fatal("DefaultConnectionSettings не обновился")
	}
	t.Logf("flags до %d после %d", before[8], after[8])
	k, _ := registry.OpenKey(registry.CURRENT_USER, inetKey+`\Connections`, registry.SET_VALUE)
	k.SetBinaryValue("DefaultConnectionSettings", before)
	k.Close()
	refreshInet()
}
