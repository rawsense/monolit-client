package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Conn — одно подключение из подписки (сервер + протокол)
type Conn struct {
	Tag      string          `json:"tag"`              // px-0, px-1…
	Name     string          `json:"name"`             // «north-star Vision»
	Server   string          `json:"server"`           // адрес сервера
	Proto    string          `json:"proto"`            // Vision | XHTTP | Hysteria2 | …
	Outbound json.RawMessage `json:"outbound"`         // готовый outbound Xray из подписки
	Routes   []SubRoute      `json:"routes,omitempty"` // маршруты из того же конфига (subroutes.go)
}

type Sub struct {
	Title    string `json:"title"`
	Used     int64  `json:"used"`
	Total    int64  `json:"total"`
	Expire   int64  `json:"expire"`
	Updated  int64  `json:"updated"`
	Announce string `json:"announce,omitempty"`
	Conns    []Conn `json:"conns"`
}

// subsDir: кэш подписок, по файлу на подписку. В тестовом запуске — своя папка
var subsDir = filepath.Join(appDir(), "subs")

func subFile(id string) string { return filepath.Join(subsDir, id+".json") }

func loadSub(id string) Sub {
	// под замком файла: чтение и откладывание в .bad не пересекутся с записью или удалением
	m := subLock(id)
	m.Lock()
	defer m.Unlock()
	var s Sub
	b, err := os.ReadFile(subFile(id))
	if err != nil {
		return s
	}
	if err := json.Unmarshal(b, &s); err != nil {
		// испорченный кэш не теряем молча: откладываем рядом, подписка скачается заново
		os.Rename(subFile(id), subFile(id)+".bad")
		logLine("подписка " + id + ": кэш не читается (" + err.Error() + "), сохранён как " + id + ".json.bad")
		return Sub{}
	}
	// кэш прошлой версии: подпись протокола и одинаковые имена приводим к нынешним правилам, не дожидаясь обновления подписки
	for i := range s.Conns {
		var ob map[string]any
		if json.Unmarshal(s.Conns[i].Outbound, &ob) == nil {
			if p := protoLabel(ob); p != "" {
				s.Conns[i].Proto = p
			}
		}
	}
	s.Conns = uniqueNames(s.Conns)
	return s
}

var (
	subLocksMu sync.Mutex
	subLocks   = map[string]*sync.Mutex{} // по замку на подписку: две записи одного файла не перемешаются
)

func subLock(id string) *sync.Mutex {
	subLocksMu.Lock()
	defer subLocksMu.Unlock()
	m := subLocks[id]
	if m == nil {
		m = &sync.Mutex{}
		subLocks[id] = m
	}
	return m
}

// saveSub пишет кэш целиком или никак: во временный файл рядом, на диск, потом подмена
func saveSub(id string, s Sub) error {
	m := subLock(id)
	m.Lock()
	defer m.Unlock()
	err := writeFileAtomic(subFile(id), s)
	if err != nil {
		logLine("подписка " + id + ": кэш не сохранился: " + err.Error())
	}
	return err
}

func writeFileAtomic(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// newSubID: s1, s2… первый свободный
func newSubID(refs []SubRef) string {
	for i := 1; ; i++ {
		id := fmt.Sprintf("s%d", i)
		free := true
		for _, r := range refs {
			if r.ID == id {
				free = false
			}
		}
		if free {
			return id
		}
	}
}

// serverName: «north-star Vision» → «north-star»
func (c Conn) ServerName() string {
	if n := strings.TrimSuffix(c.Name, " "+c.Proto); n != "" && n != c.Name {
		return n
	}
	if c.Name == c.Proto {
		return c.Server
	}
	return c.Name
}

// placeholder: «сервер» с адресом самого компьютера. Так панели подписок передают сообщение человеку,
// когда серверы отдавать нельзя: в списке появляются строки вроде «Подписке нужен HWID»
func (c Conn) placeholder() bool {
	h := strings.ToLower(strings.Trim(c.Server, "[]"))
	if h == "" || h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && (ip.IsLoopback() || ip.IsUnspecified())
}

// splitPlaceholders: настоящие подключения и тексты заглушек (без значков в начале)
func splitPlaceholders(conns []Conn) (real []Conn, notes []string) {
	for _, c := range conns {
		if !c.placeholder() {
			real = append(real, c)
			continue
		}
		if t := strings.TrimLeftFunc(c.Name, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }); t != "" {
			notes = append(notes, t)
		}
	}
	if len(notes) == 0 {
		notes = []string{"в подписке нет серверов"}
	}
	return real, notes
}

// jsonURL: у подписки monolit полный конфиг лежит по /s/<токен>/json
func jsonURL(u string) string {
	u = strings.TrimSpace(u)
	if regexp.MustCompile(`/s/[A-Za-z0-9_-]{10,40}$`).MatchString(u) {
		return u + "/json"
	}
	return u
}

func b64header(v string) string {
	if strings.HasPrefix(v, "base64:") {
		if b, err := base64.StdEncoding.DecodeString(v[7:]); err == nil {
			return string(b)
		}
	}
	return v
}

// isLink: вставлена ссылка на сервер (vless:// и т. п.), а не адрес подписки
func isLink(u string) bool {
	u = strings.ToLower(strings.TrimSpace(u))
	return !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://")
}

const subMaxSize = 4 << 20 // тело подписки больше — ошибка

// checkSubRedirect: не больше 10 переадресаций, только http(s) и без перехода с https на http
func checkSubRedirect(r *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("подписка: слишком много переадресаций")
	}
	switch r.URL.Scheme {
	case "http", "https":
	default:
		return fmt.Errorf("подписка: переадресация на %s:// не допускается", r.URL.Scheme)
	}
	if len(via) > 0 && via[len(via)-1].URL.Scheme == "https" && r.URL.Scheme == "http" {
		return errors.New("подписка: переадресация с https на http не допускается")
	}
	return nil
}

// fetchSub скачивает подписку. client — напрямую или через уже работающий VPN.
// Вставленная ссылка на сервер разбирается на месте: это подписка из одного подключения без обновлений
func fetchSub(ctx context.Context, client *http.Client, url string) (Sub, error) {
	var s Sub
	if isLink(url) {
		conns, err := parseSubBody([]byte(url))
		if err != nil {
			return s, err
		}
		s.Conns, s.Title, s.Updated = conns, conns[0].ServerName(), time.Now().Unix()
		return s, nil
	}
	first := jsonURL(url)
	s, err := fetchSubURL(ctx, client, first)
	// Сервер ответил, но не подпиской (502, пусто, не разобралось): пробуем тот же адрес с /json.
	// Так панели вроде Remnawave отдают готовый конфиг Xray любому приложению, даже когда по основному
	// адресу отвечают только знакомым. Не ответил вовсе (нет связи) — второй запрос ничего не даст
	var netErr *neturl.Error
	alt := withJSONPath(first)
	if err == nil || alt == "" || errors.As(err, &netErr) || ctx.Err() != nil {
		return s, err
	}
	if s2, err2 := fetchSubURL(ctx, client, alt); err2 == nil {
		return s2, nil
	}
	return s, err
}

// withJSONPath: адрес подписки с /json в конце пути; "" — он уже такой или адрес не разобрался
func withJSONPath(raw string) string {
	u, err := neturl.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	path := strings.TrimRight(u.Path, "/")
	if strings.HasSuffix(path, "/json") {
		return ""
	}
	u.Path = path + "/json"
	return u.String()
}

// fetchSubURL: один запрос подписки по готовому адресу
func fetchSubURL(ctx context.Context, client *http.Client, url string) (Sub, error) {
	var s Sub
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return s, err
	}
	subHeaders(req)
	cl := *client // свои правила переадресации, не трогая общий клиент
	cl.CheckRedirect = checkSubRedirect
	res, err := cl.Do(req)
	if err != nil {
		return s, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return s, fmt.Errorf("подписка ответила %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, subMaxSize+1))
	if err != nil {
		return s, err
	}
	if len(body) > subMaxSize { // обрезанная подписка хуже ошибки: пропали бы подключения
		return s, errors.New("подписка слишком большая")
	}
	s.Title = b64header(res.Header.Get("profile-title"))
	s.Announce = b64header(res.Header.Get("announce"))
	for _, kv := range strings.Split(res.Header.Get("subscription-userinfo"), ";") {
		k, v, _ := strings.Cut(strings.TrimSpace(kv), "=")
		n, _ := strconv.ParseInt(v, 10, 64)
		switch k {
		case "upload", "download":
			s.Used += n
		case "total":
			s.Total = n
		case "expire":
			s.Expire = n
		}
	}
	conns, err := parseSubBody(body)
	if err != nil {
		if s.Announce != "" {
			return s, errors.New(s.Announce)
		}
		return s, err
	}
	// Заглушки панели («Подписке нужен HWID», «Срок истёк») — не серверы: подключаться к ним нельзя.
	// Одни заглушки — это сообщение провайдера, показываем его ошибкой; вперемешку с серверами — убираем
	real, notes := splitPlaceholders(conns)
	if len(real) == 0 {
		if s.Announce != "" {
			return s, errors.New(s.Announce)
		}
		return s, errors.New(strings.Join(notes, ". "))
	}
	s.Conns = real
	if s.Title == "" {
		s.Title = res.Request.URL.Hostname()
	}
	s.Updated = time.Now().Unix()
	return s, nil
}
