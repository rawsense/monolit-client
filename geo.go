package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Базы сайтов и адресов от runetfreedom: в них есть списки заблокированного в России (ru-blocked)
const (
	geoRepo = "https://github.com/runetfreedom/russia-v2ray-rules-dat/"
	geoBase = geoRepo + "releases/latest/download/"
)

var geoFiles = []string{"geosite.dat", "geoip.dat"}

func geoDir() string {
	d := filepath.Join(appDir(), "geo")
	os.MkdirAll(d, 0o755)
	return d
}

func geoReady() bool {
	for _, f := range geoFiles {
		if st, err := os.Stat(filepath.Join(geoDir(), f)); err != nil || st.Size() < 1000 {
			return false
		}
	}
	return true
}

func geoAge() time.Duration {
	st, err := os.Stat(filepath.Join(geoDir(), "geosite.dat"))
	if err != nil {
		return 1 << 62
	}
	return time.Since(st.ModTime())
}

// geoSem: обновления баз идут по одному — у одновременных вызовов общий файл .part.
// Канал, а не мьютекс: ожидающий выходит по отмене контекста
var geoSem = make(chan struct{}, 1)

// geoTagBase: адрес скачивания из конкретного релиза. releases/latest отвечает переадресацией на
// …/releases/tag/<тег>; так сумма и база берутся из одного релиза, даже если между ними вышел новый.
// Не вышло — пустая строка, тогда качаем по latest
func geoTagBase(ctx context.Context, client *http.Client) string {
	nr := *client
	nr.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, _ := http.NewRequestWithContext(ctx, "GET", geoRepo+"releases/latest", nil)
	res, err := nr.Do(req)
	if err != nil {
		return ""
	}
	res.Body.Close()
	tag, ok := strings.CutPrefix(res.Header.Get("Location"), geoRepo+"releases/tag/")
	if !ok || !geoTagOK(tag) {
		return ""
	}
	return geoRepo + "releases/download/" + tag + "/"
}

// geoTagOK: тег из заголовка подставляем в адрес — только простые символы
func geoTagOK(tag string) bool {
	if tag == "" || len(tag) > 64 {
		return false
	}
	for _, c := range tag {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '.' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// errGeoSum — сумма не совпала: возможно, между файлами вышел новый релиз
var errGeoSum = errors.New("контрольная сумма не совпала")

// geoTagFile — тег релиза, из которого скачаны базы: тот же тег — качать нечего
func geoTagFile() string { return filepath.Join(geoDir(), "tag") }

// updateGeo скачивает обе базы одновременно; файл заменяется только целиком скачанным.
// Рядом с каждой базой runetfreedom кладёт .sha256sum: если он скачался — сверяем сумму, иначе только размер.
// changed=false — в релизе те же базы, что уже скачаны: ничего не качали, только отметили время проверки
func updateGeo(ctx context.Context, client *http.Client) (changed bool, err error) {
	select {
	case geoSem <- struct{}{}:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	defer func() { <-geoSem }()
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	client, clDone := dlClient(client)
	defer clDone()
	base := geoTagBase(ctx, client)
	if base != "" && geoReady() {
		if old, e := os.ReadFile(geoTagFile()); e == nil && string(old) == base {
			now := time.Now()
			for _, f := range geoFiles {
				os.Chtimes(filepath.Join(geoDir(), f), now, now) // «обновлены сегодня» и отсчёт до следующей проверки
			}
			return false, nil
		}
	}
	os.Remove(geoTagFile()) // пока качаем, тег не соответствует файлам
	if base == "" {
		base = geoBase
	}
	errs := make([]error, len(geoFiles))
	var wg sync.WaitGroup
	for i, f := range geoFiles {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer recoverLog("geo")
			b := base
			err := geoFetch(ctx, client, b, f)
			if errors.Is(err, errGeoSum) { // ещё раз: сумма и база из одного релиза
				if nb := geoTagBase(ctx, client); nb != "" {
					b = nb
				}
				err = geoFetch(ctx, client, b, f)
			}
			errs[i] = err
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return false, err
	}
	if base != geoBase {
		os.WriteFile(geoTagFile(), []byte(base), 0o644)
	}
	return true, nil
}

// geoFetch скачивает одну базу и её сумму из base
func geoFetch(ctx context.Context, client *http.Client, base, f string) error {
	want, _ := fetchSum(ctx, client, base+f+".sha256sum")
	req, _ := http.NewRequestWithContext(ctx, "GET", base+f, nil)
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	if res.StatusCode != 200 {
		res.Body.Close()
		return fmt.Errorf("%s: ответ %d", f, res.StatusCode)
	}
	tmp := filepath.Join(geoDir(), f+".part")
	out, err := os.Create(tmp)
	if err != nil {
		res.Body.Close()
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), res.Body)
	res.Body.Close()
	if err == nil {
		err = out.Sync()
	}
	out.Close()
	if err != nil || n < 1000 {
		os.Remove(tmp)
		return fmt.Errorf("%s: скачано не полностью", f)
	}
	if want != nil && !bytes.Equal(h.Sum(nil), want) {
		os.Remove(tmp)
		return fmt.Errorf("%s: %w", f, errGeoSum)
	}
	return os.Rename(tmp, filepath.Join(geoDir(), f))
}
