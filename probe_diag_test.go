package main

import (
	"net/url"
	"os"
	"testing"
)

func TestProbeDiag(t *testing.T) {
	if os.Getenv("MONOLIT_PROBE") == "" {
		t.Skip()
	}
	d := os.Getenv("MONOLIT_PROBE")
	t.Log("напрямую:", probe(d, nil))
	t.Log("через VPN:", probe(d, &url.URL{Scheme: "socks5", Host: "127.0.0.1:10815"}))
}
