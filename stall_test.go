package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// замедление провайдера: первые 24 КБ приходят, потом тишина — проверка должна это заметить.
// Обычная большая и короткая страницы — открылись
func TestReadBodyStall(t *testing.T) {
	stop := make(chan struct{})
	defer close(stop)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/stall":
			w.Write([]byte(strings.Repeat("a", 24<<10)))
			w.(http.Flusher).Flush()
			select {
			case <-stop:
			case <-r.Context().Done():
			}
		case "/big":
			w.Write([]byte(strings.Repeat("b", 200<<10)))
		default:
			w.Write([]byte("короткая страница"))
		}
	}))
	defer srv.Close()

	get := func(path string) string {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		return readBody(res.Body, probeRead, 300*time.Millisecond)
	}
	if r := get("/stall"); !strings.HasPrefix(r, "загрузка встала после 24 КБ") {
		t.Fatalf("замедление не замечено: %q", r)
	}
	if r := get("/big"); r != "" {
		t.Fatalf("большая страница: %q", r)
	}
	if r := get("/small"); r != "" {
		t.Fatalf("короткая страница: %q", r)
	}
}
