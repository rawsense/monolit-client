# Чужие компоненты

Monolit распространяется по лицензии GPL-3.0 (файл `LICENSE`). Внутри него работают чужие компоненты, у каждого своя лицензия.

| Компонент | Зачем | Лицензия |
|---|---|---|
| [Xray-core](https://github.com/XTLS/Xray-core) | ядро: протоколы, маршруты, TUN | MPL-2.0 |
| [gVisor](https://github.com/google/gvisor) | сетевой стек для TUN | Apache-2.0 |
| [Wintun](https://www.wintun.net/) | драйвер виртуального сетевого адаптера, файл `wintun.dll` | лицензия на готовые сборки Wintun |
| [uTLS](https://github.com/refraction-networking/utls) | отпечаток браузера в TLS | BSD-3-Clause |
| [go-webview2](https://github.com/jchv/go-webview2) | окно программы | MIT |
| [systray](https://github.com/fyne-io/systray) | значок в трее | Apache-2.0 |
| golang.org/x/net, golang.org/x/sys | сеть и системные вызовы | BSD-3-Clause |
| [Lucide](https://lucide.dev/) | иконки интерфейса | ISC |
| [Twemoji Country Flags](https://github.com/talkjs/country-flag-emoji-polyfill) | флаги стран в названиях серверов | код MIT, рисунки Twemoji CC-BY 4.0 |
| [Manrope](https://github.com/sharanda/manrope), [Geologica](https://github.com/googlefonts/geologica) | шрифты | SIL OFL 1.1 |

## Изменённые копии

Xray-core и gVisor лежат в `third_party/` с правками, исходный код правок опубликован здесь же:

- `third_party/xray-core` — TUN закрывается при остановке ядра и запускается повторно в том же процессе; учёт соединений по программам и сайтам для экрана «Соединения»; прерывание зависших соединений.
- `third_party/gvisor` — проверка времени в TCP по RFC 7323 и проверка контрольных сумм: без них поддельные пакеты программ обхода блокировок ломали настоящие соединения.

`wintun.dll` не изменён и подписан WireGuard LLC.
