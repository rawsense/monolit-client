# Monolit

VPN-клиент для Windows на ядре Xray. Через VPN идёт только заблокированное, остальное напрямую.

![Окно Monolit: кнопка подключения, список серверов с задержкой и выбор режима](.github/screenshot.png)

[Скачать последнюю версию](https://github.com/rawsense/monolit-client/releases/latest)

## Умный режим

Список заблокированных сайтов берётся из базы [runetfreedom](https://github.com/runetfreedom/russia-v2ray-rules-dat) и дополняется по ходу работы. К новому сайту клиент подключается одновременно напрямую и через VPN. Открылся напрямую — дальше ходит напрямую. Не открылся (заглушка провайдера, подменённый сертификат, сброс соединения, ответа нет дольше секунды) — уходит в VPN и попадает в список «Найдено само» с причиной. Список лежит в «Маршруты → Сайты», лишнее оттуда можно удалить.

Есть и обычный режим «Всё»: через VPN весь трафик, кроме российского.

## Возможности

- подписки monolit proxy, Remnawave и других панелей, отдельные ключи, конфиги Xray
- VLESS (Vision, Reality, XHTTP, WS, gRPC), Hysteria2, Trojan, VMess, Shadowsocks
- задержка до серверов
- свои правила для программ и сайтов, экран «Соединения» с тем, что куда сейчас идёт
- блокировка рекламы и трекеров (category-ads-all)
- совместимость с zapret и GoodbyeDPI
- автообновление

## Установка

Скачать `monolit.exe` из [релизов](https://github.com/rawsense/monolit-client/releases/latest) и запустить. Первый запуск попросит права администратора для сетевого адаптера, дальше программа запускается без вопросов.

Нужны Windows 10 или 11 и WebView2 (в Windows 11 уже есть).

Exe пока не подписан, поэтому SmartScreen может предупредить: «Подробнее» → «Выполнить в любом случае».

Удаление через «Приложения» в параметрах Windows.

## Android

Собирается из того же кода, в релизах пока нет.

## Ошибки и предложения

В [Issues](https://github.com/rawsense/monolit-client/issues). К ошибке лучше приложить журнал, он сохраняется кнопкой в настройках.

## Сборка

- `core/` — общая логика для Windows и Android: подписки, конфиг ядра, маршруты, умный режим, интерфейс (`core/web`)
- `windows/`, `android/` — то, что зависит от системы: окно, трей, сетевой адаптер, установка
- `third_party/` — Xray и gVisor с правками

Windows: нужны Go и [go-winres](https://github.com/tc-hib/go-winres), сборка `.\build.ps1`.

Android: нужны Go, gomobile, Android SDK с NDK и JDK 17, сборка `.\build-android.ps1`. Пути к SDK и JDK скрипт ищет сам, подробности в его начале.

Тесты: `go test -timeout 120s ./core ./windows`

## Лицензия

[GPL-3.0](LICENSE). Сторонние компоненты и их лицензии — в [THIRD_PARTY.md](THIRD_PARTY.md).

Флаги стран (`core/web/brand/flags.woff2`) — шрифт Twemoji Country Flags из [country-flag-emoji-polyfill](https://github.com/talkjs/country-flag-emoji-polyfill) (MIT), рисунки [Twemoji](https://github.com/twitter/twemoji) (CC-BY 4.0). Сервер AmneziaWG в ядре — [amneziawg-go](https://github.com/amnezia-vpn/amneziawg-go) (MIT), копия в `third_party/amneziawg-go`.
