# Monolit

Monolit — VPN-клиент для Windows на ядре Xray, который пускает через VPN только те сайты, что без него не открываются. Всё остальное идёт напрямую, поэтому российские сайты и банки работают так же, как без VPN.

![Окно Monolit: кнопка подключения, список серверов с задержкой и выбор режима](.github/screenshot.png)

Скачать: [последний релиз](https://github.com/rawsense/monolit-client/releases/latest)

## Как работает умный режим

Сначала клиент пробует открыть сайт напрямую. Если не вышло, сайт попадает в список и дальше открывается через VPN. Список лежит в настройках: там видно, почему каждый сайт туда попал, и лишний можно удалить.

Кому так не нужно, есть режим «Всё»: через VPN идёт весь трафик, кроме российского.

## Возможности

- подписки monolit proxy, Remnawave и других панелей, а ещё списки ключей и готовые конфиги Xray
- VLESS (Vision, Reality, XHTTP, WS, gRPC), Hysteria2, Trojan, VMess, Shadowsocks
- задержка до каждого сервера прямо в списке, переключение одним нажатием
- своё правило для любой программы или сайта: через VPN или напрямую. Что куда идёт прямо сейчас, показывает экран «Соединения»
- блокировка рекламы
- работает одновременно с zapret
- обновляется сам из релизов этого репозитория

## Установка

Скачайте `monolit.exe` из [релизов](https://github.com/rawsense/monolit-client/releases/latest) и запустите. При первом запуске Windows один раз попросит права администратора: без них программа не создаст сетевой адаптер. Потом вставьте ссылку на подписку и нажмите кнопку включения.

Подойдёт Windows 10 или 11. Ещё нужен Microsoft Edge WebView2, но в Windows 11 он уже есть.

Программа пока без цифровой подписи, поэтому Windows может показать окно «Windows защитила ваш компьютер». Нажмите «Подробнее», потом «Выполнить в любом случае».

Удаляется через «Приложения» в параметрах Windows. Программа спросит, удалить ли заодно подписки и настройки.

## Android

Версия для Android собирается из этого же кода. В релизах её пока нет.

## Ошибки и идеи

Пишите в [Issues](https://github.com/rawsense/monolit-client/issues). Если что-то сломалось, приложите журнал (кнопка в настройках) и опишите, что делали: так причину найти проще.

## Сборка

Почти весь код лежит в `core/`, он общий для Windows и Android: подписки, конфиг ядра, маршруты, умный режим, интерфейс окна (`core/web`). В `windows/` и `android/` — только то, чем системы отличаются: окно, трей, сетевой адаптер, установка. В `third_party/` лежат Xray и gVisor с нашими правками.

Для Windows нужны Go и [go-winres](https://github.com/tc-hib/go-winres):

```powershell
.\build.ps1
```

Для Android — Go, gomobile, Android SDK с NDK и JDK 17. Где скрипт их ищет, написано в его начале:

```powershell
.\build-android.ps1
```

Тесты:

```bash
go test -timeout 120s ./core ./windows
```

## Лицензия

[GPL-3.0](LICENSE). Чужие компоненты и их лицензии перечислены в [THIRD_PARTY.md](THIRD_PARTY.md).

Флаги стран в `core/web/brand/flags.woff2` — шрифт Twemoji Country Flags из проекта [country-flag-emoji-polyfill](https://github.com/talkjs/country-flag-emoji-polyfill) (MIT), сами рисунки — из [Twemoji](https://github.com/twitter/twemoji) (CC-BY 4.0). Сервер AmneziaWG в ядре сделан на [amneziawg-go](https://github.com/amnezia-vpn/amneziawg-go) (MIT), копия лежит в `third_party/amneziawg-go`.
