@echo off
chcp 65001 >nul
echo ========================================
echo  AWG Manager - IPK Build
echo ========================================
echo.

set "BASH=C:\PROGRA~1\Git\bin\bash.exe"
set "PROJECT=C:\Users\iqubik\Documents\GitHub\awg\awg-manager"

echo [1/2] Building mipsel-3.4...
"%BASH%" -lc "cd /c/Users/iqubik/Documents/GitHub/awg/awg-manager && ./scripts/build-ipk.sh mipsel-3.4"
if %errorlevel% neq 0 (
    echo.
    echo ERROR: MIPS build failed!
    exit /b %errorlevel%
)
echo.

echo [2/2] Building aarch64-3.10...
"%BASH%" -lc "cd /c/Users/iqubik/Documents/GitHub/awg/awg-manager && ./scripts/build-ipk.sh aarch64-3.10"
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
