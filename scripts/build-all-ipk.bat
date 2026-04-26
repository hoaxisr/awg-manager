@echo off
setlocal enabledelayedexpansion
chcp 65001 >nul

echo ========================================
echo  AWG Manager - IPK Build 0.9
echo  (c) 2026, AWG Manager Team
echo ========================================
echo.

:: Поиск Git Bash через git (без WSL, без реестра)
set "BASH="
for /f "delims=" %%i in ('where git 2^>nul') do (
    if not defined BASH (
        set "GITDIR=%%~dpi"
        if exist "!GITDIR!..\bin\bash.exe" (
            pushd "!GITDIR!..\bin"
            set "BASH=!CD!\bash.exe"
            popd
        ) else if exist "!GITDIR!..\..\bin\bash.exe" (
            pushd "!GITDIR!..\..\bin"
            set "BASH=!CD!\bash.exe"
            popd
        )
    )
)

if not defined BASH (
    echo ERROR: Git Bash не найден. Установите Git for Windows.
    pause
    exit /b 1
)

echo Using bash: !BASH!

:: Корень проекта – на уровень выше папки scripts, где лежит этот bat
set "PROJECT=%~dp0.."
pushd "%PROJECT%"
set "PROJECT=%CD%"
popd

:: Преобразование в Unix-путь для Git Bash
set "UNIX_PROJECT=%PROJECT:\=/%"
set "UNIX_PROJECT=/%UNIX_PROJECT::=%"
set "UNIX_PROJECT=%UNIX_PROJECT://=/%"

:: Сборка
echo [1/2] Building mipsel-3.4...
"!BASH!" -lc "cd '!UNIX_PROJECT!' && ./scripts/build-ipk.sh mipsel-3.4"
if !errorlevel! neq 0 (
    echo ERROR: MIPS build failed!
    pause
    exit /b !errorlevel!
)
echo.

echo [2/2] Building aarch64-3.10...
"!BASH!" -lc "cd '!UNIX_PROJECT!' && ./scripts/build-ipk.sh aarch64-3.10"
if !errorlevel! neq 0 (
    echo ERROR: ARM64 build failed!
    pause
    exit /b !errorlevel!
)
echo.

echo ========================================
echo  Done! Both IPKs created in dist\
echo ========================================
dir "!PROJECT!\dist\*.ipk"

pause
endlocal
exit /b 0