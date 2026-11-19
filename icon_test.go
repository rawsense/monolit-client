package main

import (
	"os"
	"testing"
)

// go test -run TestWriteIcon — пишет assets/monolit.ico для ресурсов exe (вызывает build.ps1)
func TestWriteIcon(t *testing.T) {
	if os.Getenv("MONOLIT_ICON") == "" {
		t.Skip("только для сборки")
	}
	if err := os.WriteFile("assets/monolit.ico", icoBytes(colOn), 0o644); err != nil {
		t.Fatal(err)
	}
}
