# AWG Manager

> Веб-интерфейс для управления AmneziaWG VPN-туннелями на роутерах Keenetic.

> **Disclaimer:** AWG Manager — независимый open-source проект, не аффилированный с [Amnezia.org](https://amnezia.org) и не являющийся его официальным продуктом.

---

## Возможности

- Управление туннелями AmneziaWG через браузер
- Добавление, удаление и мониторинг peer-ов
- Тест скорости с отображением в реальном времени
- График трафика с периодами 1ч / 3ч / 24ч
- DNS-маршрутизация через туннели с поддержкой системных WireGuard-интерфейсов NDMS
- Просмотр статуса соединения в реальном времени
- Совместимость с Keenetic c использованием Entware (OPKG)

---

## Требования

- Роутер Keenetic с поддержкой Entware, установленный компонент Wireguard

---

## Установка (стабильная версия)

```sh
opkg update && opkg upgrade
opkg install curl
curl -sL https://raw.githubusercontent.com/hoaxisr/awg-manager/main/scripts/install.sh | sh
```

После установки веб-интерфейс доступен по адресу роутера на назначенном порту.

---

## Удаление

```sh
opkg remove awg-manager
rm -rf /opt/etc/awg-manager
```

---

## О проекте

AWG Manager создан как независимый инструмент для управления туннелями AmneziaWG непосредственно на роутере, без CLI.

Проект **не аффилирован с Amnezia.org**, не разрабатывается и не поддерживается командой Amnezia. AmneziaWG используется как транспортный протокол.

---

## Сообщество

Telegram: [@awgmanager](https://t.me/awgmanager)

---

## Поддержать проект

Если проект оказался полезным, можно поддержать разработку донатом:

**USDT (TRC20):** `TEpJh2p9j3fp6MigyqGvq1gC5D3CsxBeJw`

---

## Полезное

Установить и управлять AmneziaWG сервером - https://github.com/bivlked/amneziawg-installer
Другой вариант управления AmneziaWG сервером - https://github.com/pumbaX/awg-multi-script
