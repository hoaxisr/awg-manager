@echo off
setlocal enabledelayedexpansion
chcp 65001 >nul

:: === BUILD CONFIGURATION ===
:: Set BUILD_MODE to: "both", "mips", "arm", or "aarch64"
:: - "both": Build both MIPS and ARM64 (default)
:: - "mips": Build only MIPS
:: - "arm" or "aarch64": Build only ARM64
set "BUILD_MODE=arm"

:: Set UPLOAD_MODE to: "on" or "off"
:: - "on": Upload and install the built IPK on rax1 (default if ARM64 built)
:: - "off": Skip upload and installation
set "UPLOAD_MODE=on"
:: ===========================

set "LOGFILE=build.log"

:: Parse architecture argument (overrides BUILD_MODE if provided)
set "ARCH=%1"
if "%ARCH%"=="" set "ARCH=%BUILD_MODE%"

set "BUILD_MIPS=0"
set "BUILD_ARM=0"
if /i "%ARCH%"=="both" (
    set "BUILD_MIPS=1"
    set "BUILD_ARM=1"
    set "ARCH_DISPLAY=both"
) else if /i "%ARCH%"=="mips" (
    set "BUILD_MIPS=1"
    set "ARCH_DISPLAY=mips"
) else if /i "%ARCH%"=="arm" (
    set "BUILD_ARM=1"
    set "ARCH_DISPLAY=arm"
) else if /i "%ARCH%"=="aarch64" (
    set "BUILD_ARM=1"
    set "ARCH_DISPLAY=aarch64"
) else (
    echo ERROR: Invalid BUILD_MODE or argument '%ARCH%'. Use 'mips', 'arm'/'aarch64', or 'both'. >> %LOGFILE%
    echo Usage: %0 [mips^|arm^|both] or edit BUILD_MODE in script. >> %LOGFILE%
    pause
    exit /b 1
)

echo ======================================== >> %LOGFILE%
echo ======================================== 
echo  AWG Manager - IPK Build 0.9 >> %LOGFILE%
echo  AWG Manager - IPK Build 0.9
echo  (c) 2026, AWG Manager Team >> %LOGFILE%
echo  (c) 2026, AWG Manager Team
echo  Building for: %ARCH_DISPLAY% >> %LOGFILE%
echo  Building for: %ARCH_DISPLAY%
if "%1"=="" (
    echo  (using BUILD_MODE from script) >> %LOGFILE%
    echo  (using BUILD_MODE from script)
) else (
    echo  (using command line argument) >> %LOGFILE%
    echo  (using command line argument)
)
echo ======================================== >> %LOGFILE%
echo ========================================
echo. >> %LOGFILE%
echo.
echo Starting build at %DATE% %TIME% > %LOGFILE%

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
set "STEP=1"
if %BUILD_MIPS%==1 (
    echo [!STEP!/2] Building mipsel-3.4... >> %LOGFILE%
    echo [!STEP!/2] Building mipsel-3.4...
    "!BASH!" -lc "cd '!UNIX_PROJECT!' && ./scripts/build-ipk.sh mipsel-3.4 2>&1 | tee -a '!LOGFILE!'"
    if !errorlevel! neq 0 (
        echo ERROR: MIPS build failed! >> %LOGFILE%
        echo ERROR: MIPS build failed!
        pause
        exit /b !errorlevel!
    )
    echo. >> %LOGFILE%
    echo.
    set /a STEP+=1
)

if %BUILD_ARM%==1 (
    echo [!STEP!/2] Building aarch64-3.10... >> %LOGFILE%
    echo [!STEP!/2] Building aarch64-3.10...
    "!BASH!" -lc "cd '!UNIX_PROJECT!' && ./scripts/build-ipk.sh aarch64-3.10 2>&1 | tee -a '!LOGFILE!'"
    if !errorlevel! neq 0 (
        echo ERROR: ARM64 build failed! >> %LOGFILE%
        echo ERROR: ARM64 build failed!
        pause
        exit /b !errorlevel!
    )
    echo. >> %LOGFILE%
    echo.
)

if %BUILD_MIPS%==0 if %BUILD_ARM%==0 (
    echo ERROR: No architectures selected. >> %LOGFILE%
    echo ERROR: No architectures selected.
    pause
    exit /b 1
)

echo ======================================== >> %LOGFILE%
echo ========================================
if %BUILD_MIPS%==1 if %BUILD_ARM%==1 (
    echo  Done! Both IPKs created in dist\ >> %LOGFILE%
    echo  Done! Both IPKs created in dist\
) else (
    echo  Done! IPK created in dist\ >> %LOGFILE%
    echo  Done! IPK created in dist\
)
echo ======================================== >> %LOGFILE%
echo ========================================
dir "!PROJECT!\dist\*.ipk" >> %LOGFILE%
dir "!PROJECT!\dist\*.ipk"

if %BUILD_ARM%==0 goto :skip_upload
echo. >> %LOGFILE%
echo.
echo ======================================== >> %LOGFILE%
echo ========================================
echo  Uploading and installing on rax1 >> %LOGFILE%
echo  Uploading and installing on rax1
echo ======================================== >> %LOGFILE%
echo ========================================

:: Find the latest aarch64 IPK
echo Finding latest aarch64 IPK in dist\ >> %LOGFILE%
echo Finding latest aarch64 IPK in dist\
set "LATEST_IPK="
for /f "delims=" %%f in ('dir /b /o-d "!PROJECT!\dist\awg-manager*aarch64*.ipk" 2^>nul') do (
    set "LATEST_IPK=%%f"
    goto :found_ipk
)
:found_ipk
if not defined LATEST_IPK (
    echo ERROR: No aarch64 IPK found in dist\ >> %LOGFILE%
    echo ERROR: No aarch64 IPK found in dist\
    pause
    exit /b 1
)

echo Found latest IPK: %LATEST_IPK% >> %LOGFILE%
echo Found latest IPK: %LATEST_IPK%

:: Upload to rax1
echo Uploading %LATEST_IPK% to rax1:/opt/tmp/... >> %LOGFILE%
echo Uploading %LATEST_IPK% to rax1:/opt/tmp/...
"!BASH!" -c "cat '!UNIX_PROJECT!/dist/%LATEST_IPK%' | ssh rax1 'cat > /opt/tmp/%LATEST_IPK%' 2>&1 | tee -a '!LOGFILE!'"
if !errorlevel! neq 0 (
    echo ERROR: Upload failed for %LATEST_IPK% >> %LOGFILE%
    echo ERROR: Upload failed for %LATEST_IPK%
    pause
    exit /b 1
)
echo Upload successful >> %LOGFILE%
echo Upload successful
echo Verifying uploaded file size... >> %LOGFILE%
"!BASH!" -c "ssh rax1 'ls -l /opt/tmp/%LATEST_IPK%' 2>&1 | tee -a '!LOGFILE!'"

:: Install on rax1
echo Installing %LATEST_IPK% on rax1... >> %LOGFILE%
echo Installing %LATEST_IPK% on rax1...
"!BASH!" -c "ssh rax1 'opkg install /opt/tmp/%LATEST_IPK%' 2>&1 | tee -a '!LOGFILE!'"
if !errorlevel! neq 0 (
    echo ERROR: Install failed for %LATEST_IPK% >> %LOGFILE%
    echo ERROR: Install failed for %LATEST_IPK%
    pause
    exit /b 1
)
echo Install successful >> %LOGFILE%
echo Install successful

echo. >> %LOGFILE%
echo.
echo IPK uploaded and installed successfully on rax1! >> %LOGFILE%
echo IPK uploaded and installed successfully on rax1!

:skip_upload
echo Build completed at %DATE% %TIME% >> %LOGFILE%
pause
endlocal
exit /b 0