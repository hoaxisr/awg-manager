Вот полная версия документа без хардкода, с однострочными командами для PowerShell, которая автоматически находит Git Bash и работает на Win11 с WSL.

---

# DEBUG: Сборка IPK (руководство по командам для слабых ИИ-агентов)

Ниже ровно те сценарии, по которым уже были успешно собраны пакеты:
- `awg-manager_2.6.3_mipsel-3.4-kn.ipk` (MIPS)
- `awg-manager_2.6.3_aarch64-3.10-kn.ipk` (Filogic 820 / ARM64)

## 1. Где запускать

Откройте PowerShell в **корне репозитория** (там, где лежат `scripts`, `VERSION`, `go.mod`).

## 2. Быстрая проверка перед сборкой

```powershell
go version
Get-ChildItem scripts
```

Ожидается:
- `go version go1.23.12 windows/amd64` (или другой `go1.23.x`)
- в `scripts` есть `build-ipk.sh`, `build-backend.sh`, `build-frontend.sh`

## 3. Команда сборки IPK для MIPS (однострочная, без хардкода)

```powershell
$b="$(Split-Path -Parent (Split-Path -Parent (Get-Command git).Source))\bin\bash.exe";$w=(Get-Location).Path;$u="/$($w[0].ToString().ToLowerInvariant())"+$w.Substring(2).Replace('\','/');&$b -lc "cd '$u' && ./scripts/build-ipk.sh mipsel-3.4"
```

## 4. Что должно получиться

В конце лога должна быть строка вида:

```text
IPK package created: dist/awg-manager_2.6.3_mipsel-3.4-kn.ipk
```

Проверка файла:

```powershell
Get-Item .\dist\awg-manager_2.6.3_mipsel-3.4-kn.ipk
```

## 5. Команда сборки IPK для Filogic 820 (ARM64)

Filogic 820 собираем как `aarch64-3.10`.

```powershell
$b="$(Split-Path -Parent (Split-Path -Parent (Get-Command git).Source))\bin\bash.exe";$w=(Get-Location).Path;$u="/$($w[0].ToString().ToLowerInvariant())"+$w.Substring(2).Replace('\','/');&$b -lc "cd '$u' && ./scripts/build-ipk.sh aarch64-3.10"
```

Ожидаемая строка в конце:

```text
IPK package created: dist/awg-manager_2.6.3_aarch64-3.10-kn.ipk
```

Проверка файла:

```powershell
Get-Item .\dist\awg-manager_2.6.3_aarch64-3.10-kn.ipk
```

## 6. Если сборка падает с Bash ошибкой на Windows

Ошибка:

```text
fatal error - couldn't create signal pipe, Win32 error 5
```

Что делать:
- перезапустить PowerShell/терминал с повышенными правами
- повторить нужную однострочную команду из п.3 или п.5

## 7. Если ругается на CRLF в shell-скриптах

Проверить `.gitattributes`:

```text
*.sh text eol=lf
```

И пересохранить `scripts/*.sh` в LF (не CRLF), затем снова выполнить сборку.

## 8. Замечания

- Предупреждения Svelte/a11y при `npm run build` допустимы, если итоговый `.ipk` создан.
- Для Keenetic MIPS целевой арх — `mipsel-3.4`.
- Для Filogic 820 целевой арх — `aarch64-3.10`.
- Версия пакета берётся из файла `VERSION`.
- **Как это работает:** Команда сама находит `git.exe` в системе, от него добирается до `bash.exe` из состава Git for Windows, конвертирует текущую папку в Unix‑путь и запускает сборочный скрипт. WSL‑bash не используется.

## 9. Установка IPK на роутер (если файл уже в `/opt/tmp`)

Пример для Filogic 820:
`/opt/tmp/awg-manager_2.6.3_aarch64-3.10-kn.ipk`

Команды на роутере:

```sh
# остановить сервис
/opt/etc/init.d/S99awg-manager stop

# установить/переустановить пакет
opkg install /opt/tmp/awg-manager_2.6.3_aarch64-3.10-kn.ipk --force-reinstall

# запустить сервис
/opt/etc/init.d/S99awg-manager start

# проверить статус
/opt/etc/init.d/S99awg-manager status
```