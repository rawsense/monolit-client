# amneziawg-go в monolit client

Исходник: https://github.com/amnezia-vpn/amneziawg-go, тег `v3.1.20261116`
(коммит 08fe3d33bbb116be09a6183d02609311b8b0c9e8). Лицензия — MIT, файл LICENSE.

Взяты только пакеты, которые нужны ядру Xray для подключения AmneziaWG:
`conn`, `device`, `tun`, `ipc`, `ratelimiter`, `replay`, `rwcancel`, `tai64n`.
Убраны: программа-демон (`main*.go`), `outline`, `tun/netstack`, тестовые помощники и тесты.
Код пакетов не менялся. `go.mod` сокращён до трёх зависимостей, которые эти пакеты используют.
