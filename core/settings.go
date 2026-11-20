package core

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// Rule — сайт в одном из списков. Reason и When заполняются, когда клиент добавил сам
type Rule struct {
	Domain string `json:"domain"`
	Reason string `json:"reason,omitempty"`
	App    string `json:"app,omitempty"`  // у программы: название из описания файла («VALORANT»)
	Path   string `json:"path,omitempty"` // у программы: путь к файлу — для значка в списке
	When   int64  `json:"when,omitempty"`
}

// SubRef — одна добавленная подписка. Данные (серверы, трафик) лежат в кэше subs/<id>.json
type SubRef struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

type Settings struct {
	SubURL      string            `json:"subUrl,omitempty"` // старое поле: одна подписка, переносится в Subs
	Subs        []SubRef          `json:"subs"`
	Active      string            `json:"active"`           // id выбранной подписки. Серверы берутся только из неё
	Conn        string            `json:"conn"`             // название выбранного конфига; "" или нет такого — первый в подписке
	ConnBy      map[string]string `json:"connBy,omitempty"` // id подписки → что в ней выбирали: при возврате к подписке выбор возвращается
	Mode        string            `json:"mode"`             // smart | ai | all
	Connect     string            `json:"connect"`          // auto | tun | proxy
	Port        int               `json:"port"`             // порт прокси на 127.0.0.1
	AutoStart   bool              `json:"autoStart"`        // запуск при входе в Windows
	AutoOn      bool              `json:"autoOn"`           // включать VPN при запуске
	DNS         string            `json:"dns"`              // DoH для заблокированного
	Fingerprint string            `json:"fingerprint"`      // "" — как в подписке
	BlockAds    bool              `json:"blockAds"`
	ShowAI      bool              `json:"showAI"`        // режим «Только ИИ» на главном экране
	VpnFirst    bool              `json:"vpnFirst"`      // новый сайт идёт через VPN, пока его проверяет умный режим (race.go)
	SubRoutes   bool              `json:"subRoutes"`     // маршруты из конфига подписки: напрямую и блокировка (subroutes.go)
	UpdateHours int               `json:"updateHours"`   // обновление подписки
	Always      []Rule            `json:"always"`        // всегда через VPN
	Direct      []Rule            `json:"direct"`        // всегда напрямую
	Found       []Rule            `json:"found"`         // найдено автоматически
	Apps        []Rule            `json:"apps"`          // программы всегда через VPN (Domain = имя exe)
	AppsDirect  []Rule            `json:"appsDirect"`    // программы всегда напрямую
	Svc         map[string]string `json:"svc,omitempty"` // итог проверки сервисов напрямую (svc.go): direct | vpn
	TempIP      []string          `json:"-"`             // адреса серверов зависшего HTTP/3: их UDP 443 на 2 минуты через VPN (holdQUIC)
}

func defaults() Settings {
	return Settings{
		Mode: "smart", Connect: "auto", Port: 10808, AutoStart: true, AutoOn: true,
		DNS: "https://1.1.1.1/dns-query", BlockAds: true, UpdateHours: 12, VpnFirst: true,
	}
}

var (
	setMu   sync.Mutex
	setPath = filepath.Join(appDir(), "settings.json")
)

// dataRoot — папка данных, которую задала оболочка (SetDataDir). Пусто — папка настроек пользователя
var dataRoot string

// SetDataDir: где хранить настройки, подписки и журнал. Нужна системам, где у программы своя закрытая
// папка (Android). Вызывать до New
func SetDataDir(d string) {
	os.MkdirAll(d, 0o755)
	dataRoot = d
	setPath, subsDir, hwidFile = filepath.Join(d, "settings.json"), filepath.Join(d, "subs"), filepath.Join(d, "hwid")
}

func appDir() string {
	if dataRoot != "" {
		return dataRoot
	}
	base, err := os.UserConfigDir() // %APPDATA%
	if err != nil {
		base = os.TempDir()
	}
	d := filepath.Join(base, "monolit")
	os.MkdirAll(d, 0o755)
	return d
}

func loadSettings() Settings {
	s := defaults()
	if b, err := os.ReadFile(setPath); err == nil {
		if err := json.Unmarshal(b, &s); err != nil {
			// испорченный файл не затираем молча: откладываем рядом, чтобы настройки можно было вернуть руками
			s = defaults()
			os.Rename(setPath, setPath+".bad")
			logLine("настройки не прочитались, файл отложен в settings.json.bad: " + err.Error())
		}
	}
	if s.Mode == "ai" { // выбран до появления переключателя: карточку не прячем, иначе режим не видно
		s.ShowAI = true
	}
	if s.Port == 0 {
		s.Port = 10808
	}
	return s
}

func saveSettings(s Settings) error {
	setMu.Lock()
	defer setMu.Unlock()
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := setPath + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync() // на диск до переименования: при отключении питания не останется пустого файла
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, setPath)
}

// storeSettings сохраняет настройки, ошибку — в журнал
func storeSettings(s Settings) {
	if err := saveSettings(s); err != nil {
		logLine("настройки не сохранились: " + err.Error())
	}
}

// clone — копия со своими списками: снимок настроек читается без a.mu, а списки правятся на месте
func (s Settings) clone() Settings {
	s.Subs = slices.Clone(s.Subs)
	s.Always = slices.Clone(s.Always)
	s.Direct = slices.Clone(s.Direct)
	s.Found = slices.Clone(s.Found)
	s.Apps = slices.Clone(s.Apps)
	s.AppsDirect = slices.Clone(s.AppsDirect)
	s.Svc = maps.Clone(s.Svc)
	s.ConnBy = maps.Clone(s.ConnBy)
	s.TempIP = slices.Clone(s.TempIP)
	return s
}

// pickConn запоминает выбранное подключение и для текущей подписки
func (s *Settings) pickConn(name string) {
	s.Conn = name
	if s.Active == "" {
		return
	}
	if s.ConnBy == nil {
		s.ConnBy = map[string]string{}
	}
	if name == "" {
		delete(s.ConnBy, s.Active)
	} else {
		s.ConnBy[s.Active] = name
	}
}

// useSub переходит на подписку и возвращает подключение, которое в ней выбирали в прошлый раз.
// Если его там больше нет, pickConns возьмёт первое
func (s *Settings) useSub(id string) {
	if s.Active != "" && s.Conn != "" { // выбор сделан до появления ConnBy — не теряем его
		s.pickConn(s.Conn)
	}
	s.Active, s.Conn = id, s.ConnBy[id]
}

// dropSub забывает выбор в удалённой подписке
func (s *Settings) dropSub(id string) {
	delete(s.ConnBy, id)
	if s.Active == id {
		s.Active, s.Conn = "", ""
	}
}

func now() int64 { return time.Now().Unix() }
