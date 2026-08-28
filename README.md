# AWG Manager

> Веб-интерфейс для управления AmneziaWG VPN-туннелями на роутерах Keenetic.
В тестовом режиме добавлена поддержка Sing-box (vless tcp, hysteria, trojan, etc)

> **Disclaimer:** AWG Manager — независимый open-source проект, не аффилированный с [Amnezia.org](https://amnezia.org) и Sing-box [SagerNet](https://github.com/SagerNet/sing-box) и не являющийся их официальным продуктом.Програма находится в стадии вечной BETA версии.

![awgm-showcase](https://raw.githubusercontent.com/hoaxisr/awg-manager/develop/scripts/dev/awgm-showcase.webp)

---

## Возможности

- Управление туннелями AmneziaWG/Sing-box через браузер
- Добавление, удаление и мониторинг peer-ов
- Тест скорости с отображением в реальном времени
- График трафика с периодами 1ч / 3ч / 24ч
- Создание AWG серверов на роутере
- DNS-маршрутизация через туннели с поддержкой системных WireGuard-интерфейсов NDMS и системы правил Sing-box
- Просмотр статуса соединения в реальном времени
- Совместимость с Keenetic c использованием Entware (OPKG)

---

## Требования

- Роутер Keenetic с поддержкой Entware, установленный компонент Wireguard

---

## Установка (стабильная версия)

curl не требуется — установка идёт встроенным busybox wget (полезно на устройствах с малой внутренней памятью):

```sh
opkg update && opkg upgrade
wget -qO- http://repo.hoaxisr.ru/install.sh | sh
```

Вариант через GitHub (HTTPS): нужен curl, т.к. busybox wget прошивки может не поддерживать HTTPS:
```sh
opkg install curl
curl -sL https://raw.githubusercontent.com/hoaxisr/awg-manager/develop/scripts/install.sh | sh
```

После установки веб-интерфейс доступен по адресу роутера и обычно по 2222 порту.

---

## Удаление

```sh
opkg remove awg-manager
rm -rf /opt/etc/awg-manager
```

---

## О проекте

AWG Manager создан как независимый инструмент для управления туннелями AmneziaWG/Sing-box непосредственно на роутере, без CLI.

Проект **не аффилирован с Amnezia.org**, не разрабатывается и не поддерживается командой Amnezia. AmneziaWG используется как транспортный протокол.
Проект **не аффилирован с SagerNet**, не разрабатывается и не поддерживается командой SagerNet. Sing-box используется как транспортный протокол.

---

## Сообщество

Telegram: [@awgmanager](https://t.me/awgmanager)

---

## Поддержать проект

Если у вас есть лишние шекели, вам понравилось как вам не отвечают на вопросы и вы готовы самостоятельно решать проблемы, а еще вы оценили подход — «ни дня без нового бага», то:

Вы можете поделиться богатством и дать возможность родить новые проблемы там, где еще вчера все было хорошо:

**USDT / ETH:** `0x7eae43b82157f2e4ea233eddf5d9ce19a1064f04`

**USDT / Tron:** `TDisGwxj2AopFzT2VQ9JwY6QDyjChUP5EA`

**Boosty:** https://boosty.to/awgm_hoaxisr/donate

**ЮMoney:** https://yoomoney.ru/fundraise/1GF36UHR07L.260312

**Или любая сумма:** https://yoomoney.ru/to/4100119477098112/0

---

## Полезное

Установить и управлять AmneziaWG сервером - https://github.com/bivlked/amneziawg-installer

Другой вариант управления AmneziaWG сервером - https://github.com/pumbaX/awg-multi-script

Документация проекта - https://awgm.hoaxisr.ru/install/
