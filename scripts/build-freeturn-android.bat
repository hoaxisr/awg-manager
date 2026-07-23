@echo off
setlocal
REM Build patched free-turn-proxy (awg.4) for Android jniLibs.
REM Usage:
REM   build-freeturn-android.bat
REM   build-freeturn-android.bat D:\turn-proxy-android

set "ROOT=%~dp0.."
set "FTP=%ROOT%\third_party\free-turn-proxy"
set "ANDROID=%~1"
if "%ANDROID%"=="" set "ANDROID=D:\turn-proxy-android"
set "OUT=%ANDROID%\app\src\main\jniLibs"
set "VER=1.8.0-awg.4"
set "LDFLAGS=-X main.version=%VER% -s -w -checklinkname=0"

if not exist "%FTP%\cmd\client" (
  echo ERROR: %FTP% not found
  exit /b 1
)
if not exist "%ANDROID%\app" (
  echo ERROR: Android repo not found: %ANDROID%
  exit /b 1
)

mkdir "%OUT%\arm64-v8a" 2>nul
mkdir "%OUT%\armeabi-v7a" 2>nul

pushd "%FTP%"

echo ==^> arm64-v8a
set GOOS=android& set GOARCH=arm64& set CGO_ENABLED=0
go build -ldflags "%LDFLAGS%" -o "%OUT%\arm64-v8a\libfreeturn-%VER%-android-arm64.so" ./cmd/client
if errorlevel 1 goto fail

echo ==^> armeabi-v7a
set GOOS=android& set GOARCH=arm& set GOARM=7& set CGO_ENABLED=0
go build -ldflags "%LDFLAGS%" -o "%OUT%\armeabi-v7a\libfreeturn-%VER%-android-armeabi-v7.so" ./cmd/client
if errorlevel 1 goto fail

popd
echo.
echo OK: %OUT%\...\libfreeturn-%VER%-android-*.so
echo CoreProcessController picks the lexicographically newest libfreeturn*.so name.
exit /b 0

:fail
popd
echo BUILD FAILED
exit /b 1
