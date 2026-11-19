package main

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// Идентификатор установки (HWID). Панели вроде Remnawave считают по нему устройства подписки и без него
// вместо серверов отдают заглушки «Подписке нужен HWID». Он случайный и к железу не привязан:
// создаётся один раз и лежит рядом с настройками, переустановка Windows даёт новый
var (
	hwidFile = filepath.Join(appDir(), "hwid")
	hwidMu   sync.Mutex
	hwidRe   = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

func hwid() string {
	hwidMu.Lock()
	defer hwidMu.Unlock()
	if b, err := os.ReadFile(hwidFile); err == nil {
		if v := strings.TrimSpace(string(b)); hwidRe.MatchString(v) {
			return v
		}
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return ""
	}
	v := hex.EncodeToString(raw)
	if err := os.WriteFile(hwidFile, []byte(v), 0o600); err != nil {
		logLine("идентификатор установки не сохранился: " + err.Error())
	}
	return v
}

// subHeaders: чем клиент представляется серверу подписки. Имя компьютера и пользователя не передаём
func subHeaders(req *http.Request) {
	req.Header.Set("User-Agent", "monolit-client/"+version+" xray")
	if id := hwid(); id != "" {
		req.Header.Set("x-hwid", id)
		req.Header.Set("x-device-os", "Windows")
		req.Header.Set("x-ver-os", osVersion())
		req.Header.Set("x-device-model", "Desktop")
	}
}
