package main

import (
	"fmt"
	"os"
	"testing"
)

// go test -run TestIconPreview с MONOLIT_ICON_PREVIEW=1 — пробные PNG знака в design/ для просмотра
func TestIconPreview(t *testing.T) {
	if os.Getenv("MONOLIT_ICON_PREVIEW") == "" {
		t.Skip("только для просмотра")
	}
	for _, s := range []int{16, 32, 48, 256} {
		if err := os.WriteFile(fmt.Sprintf("design/icon-%d.png", s), markPNG(s, colOn), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile("design/icon-off-32.png", markPNG(32, colOff), 0o644); err != nil {
		t.Fatal(err)
	}
}
