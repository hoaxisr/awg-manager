@echo off
chcp 65001 >nul
echo ========================================
echo  AWG Manager - IPK Build
echo ========================================
echo.

:: ========================================================
::  Автоматический поиск bash.exe (Git Bash)
:: ========================================================
set "BASH="
set "SYSTEM_BASH=%SystemRoot%\System32\bash.exe"
set "SYSTEM_BASH_WOW=%SystemRoot%\SysWOW64\bash.exe"

:: 1. Основной способ: через git.exe
for /f "delims=" %%i in ('where git 2^>nul') do (
    if not defined BASH (
        for %%g in ("%%i") do (
            if exist "%%~dpg\..\bin\bash.exe" (
                set "BASH=%%~dpg\..\bin\bash.exe"
            ) else if exist "%%~dpg\..\..\bin\bash.exe" (
                set "BASH=%%~dpg\..\..\bin\bash.exe"
            )
        )
    )
)

:: 2. Поиск в реестре
if not defined BASH (
    for /f "tokens=2*" %%a in (
        'reg query "HKLM\SOFTWARE\GitForWindows" /v InstallPath 2^>nul ^| find "InstallPath"'
    ) do (
        if exist "%%b\bin\bash.exe" set "BASH=%%b\bin\bash.exe"
    )
)
if not defined BASH (
    for /f "tokens=2*" %%a in (
        'reg query "HKCU\SOFTWARE\GitForWindows" /v InstallPath 2^>nul ^| find "InstallPath"'
    ) do (
        if exist "%%b\bin\bash.exe" set "BASH=%%b\bin\bash.exe"
    )
)

:: 3. Запасной вариант: поиск bash, исключая WSL
if not defined BASH (
    for /f "delims=" %%i in ('where bash 2^>nul') do (
        if not defined BASH (
            if /i not "%%i"=="%SYSTEM_BASH%" if /i not "%%i"=="%SYSTEM_BASH_WOW%" (
                set "BASH=%%i"
            )
        )
    )
)

:: Если не найден – ошибка
if not defined BASH (
    echo ERROR: Git Bash не найден.
    echo Убедитесь, что Git for Windows установлен и добавлен в PATH.
    pause
    exit /b 1
)

:: Нормализация пути (убирает ".." и приводит к полному)
for %%F in ("%BASH%") do set "BASH=%%~fF"

echo Using bash: %BASH%
:: --------------------------------------------------------

:: Динамическое определение корня проекта
for %%I in ("%~dp0..") do set "PROJECT=%%~fI"

:: Преобразование в Unix-style path
set "UNIX_PROJECT=%PROJECT:\=/%"
set "UNIX_PROJECT=/%UNIX_PROJECT::=%"
set "UNIX_PROJECT=%UNIX_PROJECT://=/%"

echo [1/2] Building mipsel-3.4...
"%BASH%" -lc "cd '%UNIX_PROJECT%' && ./scripts/build-ipk.sh mipsel-3.4"
if %errorlevel% neq 0 (
    echo.
    echo ERROR: MIPS build failed!
    exit /b %errorlevel%
)
echo.

echo [2/2] Building aarch64-3.10...
"%BASH%" -lc "cd '%UNIX_PROJECT%' && ./scripts/build-ipk.sh aarch64-3.10"
if %errorlevel% neq 0 (
    echo.
    echo ERROR: ARM64 build failed!
    exit /b %errorlevel%
)
echo.

echo ========================================
echo  Done! Both IPKs created in dist/
echo ========================================
dir "%PROJECT%\dist\*.ipk"

pause