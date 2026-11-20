package core

import (
	"embed"
	"io/fs"
	"log"
	"net"
	"net/http"
)

// интерфейс один на все системы: тот же HTML показывает окно на компьютере и на телефоне

//go:embed all:web
var webFS embed.FS

// ServeUI раздаёт интерфейс с этого же устройства и возвращает его адрес
func ServeUI() string {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	go http.Serve(ln, http.FileServer(http.FS(sub)))
	return "http://" + ln.Addr().String() + "/"
}
