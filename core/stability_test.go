package core

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// журнал теста — во временную папку, не в настоящий monolit.log
func tempLog(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "monolit.log")
	logMu.Lock()
	oldPath, oldFile := logPath, logFile
	logPath, logFile, logRetry = func() string { return p }, nil, time.Time{}
	logMu.Unlock()
	t.Cleanup(func() {
		logMu.Lock()
		if logFile != nil {
			logFile.Close()
		}
		logPath, logFile = oldPath, oldFile
		logMu.Unlock()
	})
	return p
}

func tempSubs(t *testing.T) {
	t.Helper()
	old := subsDir
	subsDir = t.TempDir()
	t.Cleanup(func() { subsDir = old })
}

func TestSaveSubAtomic(t *testing.T) {
	tempLog(t)
	tempSubs(t)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := Sub{Title: fmt.Sprintf("t%d", i)}
			for j := 0; j < i*50; j++ {
				s.Conns = append(s.Conns, Conn{Tag: fmt.Sprintf("px-%d", j), Name: strings.Repeat("x", 40)})
			}
			if err := saveSub("s1", s); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	s := loadSub("s1")
	if !strings.HasPrefix(s.Title, "t") {
		t.Fatalf("кэш испорчен: %+v", s.Title)
	}
	left, _ := filepath.Glob(filepath.Join(subsDir, "*.tmp*"))
	if len(left) != 0 {
		t.Fatalf("остались временные файлы: %v", left)
	}
}

func TestLoadSubBad(t *testing.T) {
	lp := tempLog(t)
	tempSubs(t)
	os.WriteFile(subFile("s2"), []byte(`{"title":"обрыв`), 0o600)
	if s := loadSub("s2"); s.Title != "" {
		t.Fatalf("прочитан %q", s.Title)
	}
	if _, err := os.Stat(subFile("s2") + ".bad"); err != nil {
		t.Fatalf("испорченный кэш не отложен: %v", err)
	}
	if b, _ := os.ReadFile(lp); !strings.Contains(string(b), "s2.json.bad") {
		t.Fatalf("в журнале нет записи: %q", b)
	}
}

func TestFetchSubLimits(t *testing.T) {
	plain := strings.Join(sampleLinks, "\n")
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(plain + "\n"))
		w.Write([]byte(strings.Repeat("#", subMaxSize)))
	}))
	defer big.Close()
	if _, err := fetchSub(context.Background(), big.Client(), big.URL); err == nil || !strings.Contains(err.Error(), "слишком большая") {
		t.Fatalf("большая подписка: %v", err)
	}

	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(plain)) }))
	defer ok.Close()
	// с https на http — нельзя
	down := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, ok.URL, http.StatusFound)
	}))
	defer down.Close()
	if _, err := fetchSub(context.Background(), down.Client(), down.URL); err == nil {
		t.Fatal("переход с https на http принят")
	}
	// с http на http — можно
	same := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, ok.URL, http.StatusFound)
	}))
	defer same.Close()
	if s, err := fetchSub(context.Background(), same.Client(), same.URL); err != nil || len(s.Conns) != len(sampleLinks) {
		t.Fatalf("обычная переадресация: %v", err)
	}
	// бесконечная переадресация
	var loop *httptest.Server
	loop = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, loop.URL+"/again", http.StatusFound)
	}))
	defer loop.Close()
	if _, err := fetchSub(context.Background(), loop.Client(), loop.URL); err == nil {
		t.Fatal("бесконечная переадресация принята")
	}
	if checkSubRedirect(&http.Request{URL: mustURL("file:///c:/x")}, []*http.Request{{URL: mustURL("http://a")}}) == nil {
		t.Fatal("переход на file:// принят")
	}
}

func TestLogRotate(t *testing.T) {
	p := tempLog(t)
	os.WriteFile(p, make([]byte, logMaxSize+1), 0o644)
	logLine("первая строка")
	st, err := os.Stat(p + ".1")
	if err != nil || st.Size() <= logMaxSize {
		t.Fatalf("старый журнал не отложен: %v", err)
	}
	if st, _ := os.Stat(p); st == nil || st.Size() > 1000 {
		t.Fatal("новый журнал не начат")
	}
	// рост во время работы: проверка раз в logCheckEvery строк
	long := strings.Repeat("ж", 20<<10)
	for i := 0; i < 2*logCheckEvery; i++ {
		logLine(long)
	}
	if st, _ := os.Stat(p); st == nil || st.Size() > logMaxSize+int64(logCheckEvery)*int64(len(long)+64) {
		t.Fatalf("журнал вырос до %d", st.Size())
	}
}

func TestLogOpenBackoff(t *testing.T) {
	tempLog(t)
	logMu.Lock()
	logPath = func() string {
		return filepath.Join(os.TempDir(), "нет-такой-папки-monolit", "x", "monolit.log")
	}
	logMu.Unlock()
	logLine("a")
	logMu.Lock()
	retry, f := logRetry, logFile
	logMu.Unlock()
	if f != nil || time.Until(retry) < 20*time.Second {
		t.Fatalf("нет паузы перед повтором: %v", time.Until(retry))
	}
}

func TestSmartCheckedPrune(t *testing.T) {
	s := &smartDetector{checked: map[string]time.Time{}}
	for i := 0; i < smartCheckedMax; i++ {
		s.checked[fmt.Sprintf("d%d.com", i)] = time.Now().Add(-7 * time.Hour)
	}
	s.checked["fresh.com"] = time.Now()
	s.prune()
	if len(s.checked) != 1 {
		t.Fatalf("осталось %d", len(s.checked))
	}
}

func TestRootOf(t *testing.T) {
	for in, want := range map[string]string{
		"www.linkedin.com": "linkedin.com",
		"news.bbc.co.uk":   "bbc.co.uk",
		"a.b.example.com.": "example.com",
		"user.github.io":   "user.github.io",
		"example.com":      "example.com",
		"localhost":        "localhost",
		"1.2.3.4":          "1.2.3.4",
		"[2001:db8::1]":    "[2001:db8::1]",
		"WWW.Google.COM":   "google.com",
	} {
		if got := rootOf(in); got != want {
			t.Errorf("%s: %s, ждали %s", in, got, want)
		}
	}
}

func mustURL(s string) *url.URL { u, _ := url.Parse(s); return u }

// Замер сразу после запуска ядра или смены маршрутов ничего не говорит о сервере: ядро сообщает,
// сколько прошло с последней перемены, и неудачи в это окно не записываются
func TestCoreSettledFor(t *testing.T) {
	var c Core
	if c.SettledFor() < time.Minute {
		t.Error("ядро, которое не запускали, считается устоявшимся")
	}
	c.changed = time.Now()
	if d := c.SettledFor(); d > time.Second {
		t.Errorf("сразу после перемены прошло %s", d)
	}
	c.changed = time.Now().Add(-settleTime - time.Second)
	if c.SettledFor() < settleTime {
		t.Error("через settleTime ядро должно считаться устоявшимся")
	}
}
