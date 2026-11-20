package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
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

// applyUpdate скачивает из релиза файл программы для этой системы (в Windows — monolit.exe) и отдаёт оболочке
// на установку. Ссылка — только из релизов нашего репозитория; файл должен совпасть с SHA-256,
// если релиз его даёт (поле digest или файл <имя>.sha256)
func applyUpdate(ctx context.Context, cl *http.Client, r *Release) error {
	asset := plat.UpdateAsset()
	var url, digest, sumURL string
	var size int64
	for _, a := range r.Assets {
		switch {
		case strings.EqualFold(a.Name, asset):
			url, size, digest = a.URL, a.Size, a.Digest
		case strings.EqualFold(a.Name, asset+".sha256"):
			sumURL = a.URL
		}
	}
	if url == "" || asset == "" {
		return errors.New("в релизе нет " + asset)
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
	// файл создаём до скачивания: нет прав — сразу понятная ошибка, а не после загрузки
	out, err := plat.UpdateFile()
	if err != nil {
		return err
	}
	tmp := out.Name()
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
	if err := plat.UpdateInstall(tmp); err != nil {
		os.Remove(tmp)
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
