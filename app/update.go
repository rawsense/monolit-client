package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Обновления клиента — из релизов GitHub. Ядро Xray встроено в exe и обновляется вместе с ним
const (
	releasesRepo = "rawsense/monolit-client"
	releasesAPI  = "https://api.github.com/repos/" + releasesRepo + "/releases/latest"
	// exe скачиваем только из релизов этого же репозитория
	releasesDL = "https://github.com/" + releasesRepo + "/releases/download/"
)

// dlClient — клиент для больших файлов: тот же путь (напрямую или через VPN), но без общего тайм-аута.
// Зависания ловят тайм-ауты соединения, TLS и заголовков, а общий срок задаёт контекст (downloadTimeout).
// Клиент без общего срока отдаём как есть. Иначе — копия транспорта; done закрывает её простаивающие
// соединения, вызывать после скачивания
func dlClient(cl *http.Client) (c *http.Client, done func()) {
	if cl.Timeout == 0 {
		return cl, func() {}
	}
	var tr *http.Transport
	if t, ok := cl.Transport.(*http.Transport); ok && t != nil {
		tr = t.Clone()
	} else {
		tr = http.DefaultTransport.(*http.Transport).Clone()
	}
	if tr.DialContext == nil {
		tr.DialContext = (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	}
	tr.TLSHandshakeTimeout = 15 * time.Second
	tr.ResponseHeaderTimeout = 30 * time.Second
	return &http.Client{Transport: tr, CheckRedirect: cl.CheckRedirect, Jar: cl.Jar}, tr.CloseIdleConnections
}

func init() { go removeOldExe() }

// removeOldExe убирает monolit.exe.old после обновления. Старая копия ещё может работать
// несколько секунд (новая ждёт её выхода), поэтому пробуем несколько раз
func removeOldExe() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	for i := 0; i < 6; i++ {
		if err := os.Remove(exe + ".old"); err == nil || errors.Is(err, os.ErrNotExist) {
			return
		}
		time.Sleep(5 * time.Second)
	}
}

type Release struct {
	Tag    string `json:"tag_name"`
	Notes  string `json:"body"`
	Assets []struct {
		Name   string `json:"name"`
		URL    string `json:"browser_download_url"`
		Size   int64  `json:"size"`
		Digest string `json:"digest"` // "sha256:…", GitHub считает сам; у старых релизов может не быть
	} `json:"assets"`
}

// newer: v0.2.0 > 0.1.0
func newer(tag, cur string) bool {
	p := func(s string) [3]int {
		var v [3]int
		for i, x := range strings.SplitN(strings.TrimPrefix(s, "v"), ".", 3) {
			v[i], _ = strconv.Atoi(x)
		}
		return v
	}
	a, b := p(tag), p(cur)
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

func checkUpdate(ctx context.Context, cl *http.Client) (*Release, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", releasesAPI, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	res, err := cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == 404 {
		return nil, nil // релизов ещё нет
	}
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub ответил %d", res.StatusCode)
	}
	var r Release
	if err := json.NewDecoder(res.Body).Decode(&r); err != nil {
		return nil, err
	}
	if !newer(r.Tag, version) {
		return nil, nil
	}
	return &r, nil
}

// applyUpdate скачивает monolit.exe из релиза рядом с текущим (в Program Files у установленной копии) и подменяет его. Работает после перезапуска.
// Ссылка — только из релизов нашего репозитория; файл должен быть exe (PE) и совпасть с SHA-256,
// если релиз его даёт (поле digest или файл monolit.exe.sha256)
func applyUpdate(ctx context.Context, cl *http.Client, r *Release) error {
	var url, digest, sumURL string
	var size int64
	for _, a := range r.Assets {
		switch {
		case strings.EqualFold(a.Name, "monolit.exe"):
			url, size, digest = a.URL, a.Size, a.Digest
		case strings.EqualFold(a.Name, "monolit.exe.sha256"):
			sumURL = a.URL
		}
	}
	if url == "" {
		return errors.New("в релизе нет monolit.exe")
	}
	if !strings.HasPrefix(url, releasesDL) {
		return errors.New("ссылка на обновление ведёт не в релизы Monolit")
	}
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	cl, clDone := dlClient(cl)
	defer clDone()
	var want []byte
	if d, ok := strings.CutPrefix(digest, "sha256:"); ok {
		if want, _ = hex.DecodeString(d); len(want) != sha256.Size {
			return errors.New("в релизе неверная контрольная сумма")
		}
	} else if sumURL != "" && strings.HasPrefix(sumURL, releasesDL) {
		var err error
		if want, err = fetchSum(ctx, cl, sumURL); err != nil {
			return fmt.Errorf("контрольная сумма: %w", err)
		}
	}
	// подменяем запущенный exe: установленная копия лежит в Program Files (installDir), туда пишет только
	// процесс с правами. Копия из другого места (перенос не удался, сборка для проверки) обновляется на месте.
	// Файл создаём до скачивания: нет прав — сразу понятная ошибка, а не после загрузки
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(exe), "monolit.exe.download")
	out, err := os.Create(tmp)
	if err != nil {
		if errors.Is(err, os.ErrPermission) && !isAdmin() {
			return errors.New("нет прав на запись в " + filepath.Dir(exe) + ": откройте Monolit ярлыком из меню «Пуск» (с правами администратора) и повторите обновление")
		}
		return err
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	res, err := cl.Do(req)
	if err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		out.Close()
		os.Remove(tmp)
		return fmt.Errorf("GitHub ответил %d", res.StatusCode)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), res.Body)
	if err == nil {
		err = out.Sync()
	}
	out.Close()
	if err != nil || (size > 0 && n != size) {
		os.Remove(tmp)
		return errors.New("файл скачан не полностью")
	}
	if want != nil && !bytes.Equal(h.Sum(nil), want) {
		os.Remove(tmp)
		return errors.New("контрольная сумма обновления не совпала")
	}
	if !isPE(tmp) {
		os.Remove(tmp)
		return errors.New("скачанный файл — не программа Windows")
	}
	os.Remove(exe + ".old")
	if err := os.Rename(exe, exe+".old"); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, exe); err != nil {
		os.Rename(exe+".old", exe)
		return err
	}
	return nil
}

// fetchSum читает файл суммы: первые 64 шестнадцатеричных знака («<сумма>  monolit.exe» или просто сумма)
func fetchSum(ctx context.Context, cl *http.Client, url string) ([]byte, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	res, err := cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("ответ %d", res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4096))
	if err != nil {
		return nil, err
	}
	f := strings.Fields(string(raw))
	if len(f) == 0 {
		return nil, errors.New("пустой файл")
	}
	sum, err := hex.DecodeString(f[0])
	if err != nil || len(sum) != sha256.Size {
		return nil, errors.New("не SHA-256")
	}
	return sum, nil
}

// isPE: файл начинается с «MZ», а по смещению из e_lfanew (0x3C) лежит «PE\0\0»
func isPE(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var hdr [64]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil || hdr[0] != 'M' || hdr[1] != 'Z' {
		return false
	}
	off := int64(binary.LittleEndian.Uint32(hdr[0x3C:]))
	var sig [4]byte
	if _, err := f.ReadAt(sig[:], off); err != nil {
		return false
	}
	return sig == [4]byte{'P', 'E', 0, 0}
}
