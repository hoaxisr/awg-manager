@echo off
setlocal EnableExtensions EnableDelayedExpansion

rem Persistent Docker test runner for backend Go tests.
rem Goals:
rem 1) Avoid expensive container cold-start on every run.
rem 2) Keep build/module caches between runs.
rem 3) Avoid false-green test cache via -count=1.
rem
rem Representative usage examples:
rem   scripts\dev\dev-backend-tests.bat start
rem   scripts\dev\dev-backend-tests.bat status
rem   scripts\dev\dev-backend-tests.bat run ./internal/managed ./internal/api
rem   scripts\dev\dev-backend-tests.bat run ./internal/managed -run TestService_Create_CapturesPrivateKey
rem   scripts\dev\dev-backend-tests.bat run ./internal/sys/httpdownload -run TestReader_EmitsAfterByteThreshold
rem   scripts\dev\dev-backend-tests.bat full
rem   scripts\dev\dev-backend-tests.bat stop
rem
rem Notes:
rem   - "ok ... 0.00Xs" is Go's internal test time.
rem   - "[run]/[full] elapsed: ..." is end-to-end wall-clock time from this bat.

set "SCRIPT_DIR=%~dp0"
for %%I in ("%SCRIPT_DIR%..\..") do set "REPO_ROOT=%%~fI"
set "REPO_ROOT=%REPO_ROOT:\=/%"

set "CONTAINER_NAME=awgm-go-test-runner"
set "IMAGE=golang:1.26.4-bookworm"
set "CACHE_ROOT=%REPO_ROOT%/.cache/docker-go-test"
set "GOCACHE_DIR=%CACHE_ROOT%/go-build"
set "GOMODCACHE_DIR=%CACHE_ROOT%/go-mod"

if "%~1"=="" goto :help

if /I "%~1"=="start" goto :start
if /I "%~1"=="stop" goto :stop
if /I "%~1"=="status" goto :status
if /I "%~1"=="shell" goto :shell
if /I "%~1"=="run" goto :run
if /I "%~1"=="full" goto :full
if /I "%~1"=="coverage" goto :coverage

echo Unknown command: %~1
echo.
goto :help

:start
call :timer_start
call :ensure_cache_dirs
if errorlevel 1 exit /b 1
call :remove_existing_container
echo Starting persistent runner "%CONTAINER_NAME%"...
docker run -d --name %CONTAINER_NAME% ^
  -v "%REPO_ROOT%:/src" ^
  -v "%GOCACHE_DIR%:/go-build-cache" ^
  -v "%GOMODCACHE_DIR%:/go/pkg/mod" ^
  -w /src ^
  -e GOCACHE=/go-build-cache ^
  -e GOMODCACHE=/go/pkg/mod ^
  %IMAGE% sleep infinity
if errorlevel 1 exit /b 1
echo Runner started.
call :timer_stop "start"
goto :eof

:stop
call :timer_start
echo Stopping runner "%CONTAINER_NAME%"...
docker rm -f %CONTAINER_NAME% >nul 2>&1
echo Done.
call :timer_stop "stop"
goto :eof

:status
docker ps -a --filter "name=%CONTAINER_NAME%"
goto :eof

:shell
call :timer_start
call :ensure_runner
if errorlevel 1 exit /b 1
docker exec -it %CONTAINER_NAME% bash
call :timer_stop "shell"
exit /b %ERRORLEVEL%

:run
call :timer_start
shift
if "%~1"=="" (
  echo Usage: %~nx0 run ^<go test packages/args^>
  echo Example: %~nx0 run ./internal/managed ./internal/api
  exit /b 1
)
set "GO_TEST_ARGS="
:collect_args
if "%~1"=="" goto :run_exec
if defined GO_TEST_ARGS (
  set "GO_TEST_ARGS=!GO_TEST_ARGS! %~1"
) else (
  set "GO_TEST_ARGS=%~1"
)
shift
goto :collect_args

:run_exec
call :timer_mark __RUN_TOTAL_START
call :timer_mark __RUN_PREP_START
call :ensure_runner
if errorlevel 1 exit /b 1
call :timer_report "prepare" __RUN_PREP_START
echo Running targeted tests with -count=1: %GO_TEST_ARGS%
call :timer_mark __RUN_EXEC_START
docker exec %CONTAINER_NAME% bash -c "start=$(date +%%s%%3N); go test -count=1 %GO_TEST_ARGS%; code=$?; end=$(date +%%s%%3N); echo [inner-go-test] elapsed: $((end-start)) ms; exit $code"
set "__RUN_RC=%ERRORLEVEL%"
call :timer_report "docker-exec-total" __RUN_EXEC_START
call :timer_report "run" __RUN_TOTAL_START
exit /b %__RUN_RC%

:full
call :timer_mark __FULL_TOTAL_START
call :timer_mark __FULL_PREP_START
call :ensure_runner
if errorlevel 1 exit /b 1
call :timer_report "prepare" __FULL_PREP_START
echo Running full backend suite with -count=1...
call :timer_mark __FULL_EXEC_START
docker exec %CONTAINER_NAME% bash -c "start=$(date +%%s%%3N); go test -count=1 ./...; code=$?; end=$(date +%%s%%3N); echo [inner-go-test] elapsed: $((end-start)) ms; exit $code"
set "__FULL_RC=%ERRORLEVEL%"
call :timer_report "docker-exec-total" __FULL_EXEC_START
call :timer_report "full" __FULL_TOTAL_START
exit /b %__FULL_RC%

:coverage
call :timer_mark __COV_TOTAL_START
call :timer_mark __COV_PREP_START
call :ensure_runner
if errorlevel 1 exit /b 1
call :timer_report "prepare" __COV_PREP_START
echo Running backend coverage with -count=1...
call :timer_mark __COV_EXEC_START
docker exec %CONTAINER_NAME% bash -c "start=$(date +%%s%%3N); go test -count=1 -covermode=atomic -coverpkg=./internal/...,./cmd/... -coverprofile=coverage.out ./internal/... ./cmd/...; code=$?; if [ $code -eq 0 ]; then go tool cover -func=coverage.out | tee coverage.txt; go tool cover -html=coverage.out -o coverage.html; fi; end=$(date +%%s%%3N); echo [inner-go-coverage] elapsed: $((end-start)) ms; exit $code"
set "__COV_RC=%ERRORLEVEL%"
call :timer_report "docker-exec-total" __COV_EXEC_START
call :timer_report "coverage" __COV_TOTAL_START
exit /b %__COV_RC%

:ensure_runner
docker inspect -f "{{.State.Running}}" %CONTAINER_NAME% 2>nul | findstr /i "true" >nul
if errorlevel 1 (
  echo Runner not started. Starting now...
  call :start
  if errorlevel 1 exit /b 1
)
exit /b 0

:remove_existing_container
docker rm -f %CONTAINER_NAME% >nul 2>&1
exit /b 0

:ensure_cache_dirs
if not exist "%CACHE_ROOT:\=\\%" mkdir "%CACHE_ROOT:\=\\%" >nul 2>&1
if not exist "%GOCACHE_DIR:\=\\%" mkdir "%GOCACHE_DIR:\=\\%" >nul 2>&1
if not exist "%GOMODCACHE_DIR:\=\\%" mkdir "%GOMODCACHE_DIR:\=\\%" >nul 2>&1
exit /b 0

:help
echo Usage: %~nx0 ^<command^>
echo.
echo Commands:
echo   start   - start persistent docker runner with mounted Go caches
echo   stop    - stop/remove persistent runner
echo   status  - show runner container state
echo   shell   - open bash shell inside runner
echo   run     - run targeted tests: go test -count=1 ^<args^>
echo   full     - run full suite: go test -count=1 ./...
echo   coverage - run backend coverage and write coverage.out/txt/html
echo.
echo Examples:
echo   %~nx0 start
echo   %~nx0 run ./internal/managed ./internal/api
echo   %~nx0 run ./internal/managed -run TestService_Create_CapturesPrivateKey
echo   %~nx0 full
echo   %~nx0 coverage
echo   %~nx0 stop
exit /b 0

:timer_start
call :time_to_cs "%TIME%" __TIMER_START_CS
exit /b 0

:timer_stop
set "__TIMER_LABEL=%~1"
call :time_to_cs "%TIME%" __TIMER_END_CS
set /a "__TIMER_ELAPSED_CS=%__TIMER_END_CS%-%__TIMER_START_CS%"
if %__TIMER_ELAPSED_CS% LSS 0 set /a "__TIMER_ELAPSED_CS+=24*60*60*100"
set /a "__TIMER_SEC=__TIMER_ELAPSED_CS%/100"
set /a "__TIMER_REM_CS=__TIMER_ELAPSED_CS%%100"
set /a "__TIMER_ELAPSED_MS=%__TIMER_ELAPSED_CS%*10"
if %__TIMER_REM_CS% LSS 10 (
  echo [%__TIMER_LABEL%] elapsed: %__TIMER_SEC%.0%__TIMER_REM_CS%s ^(%__TIMER_ELAPSED_MS% ms^)
) else (
  echo [%__TIMER_LABEL%] elapsed: %__TIMER_SEC%.%__TIMER_REM_CS%s ^(%__TIMER_ELAPSED_MS% ms^)
)
exit /b 0

:time_to_cs
set "__TT=%~1"
set "__HH=%__TT:~0,2%"
set "__MM=%__TT:~3,2%"
set "__SS=%__TT:~6,2%"
set "__CS=%__TT:~9,2%"
if "%__HH:~0,1%"==" " set "__HH=0%__HH:~1,1%"
set /a "__OUT=(1%__HH%-100)*360000 + (1%__MM%-100)*6000 + (1%__SS%-100)*100 + (1%__CS%-100)"
set "%~2=%__OUT%"
exit /b 0

:timer_mark
call :time_to_cs "%TIME%" __NOW_CS
set "%~1=%__NOW_CS%"
exit /b 0

:timer_report
set "__TIMER_LABEL=%~1"
set "__TIMER_START_CS_VAR=%~2"
call :time_to_cs "%TIME%" __TIMER_END_CS
call set "__TIMER_START_CS=%%%__TIMER_START_CS_VAR%%%"
set /a "__TIMER_ELAPSED_CS=%__TIMER_END_CS%-%__TIMER_START_CS%"
if %__TIMER_ELAPSED_CS% LSS 0 set /a "__TIMER_ELAPSED_CS+=24*60*60*100"
set /a "__TIMER_SEC=__TIMER_ELAPSED_CS/100"
set /a "__TIMER_REM_CS=__TIMER_ELAPSED_CS%%100"
set /a "__TIMER_ELAPSED_MS=%__TIMER_ELAPSED_CS%*10"
if %__TIMER_REM_CS% LSS 10 (
  echo [%__TIMER_LABEL%] elapsed: %__TIMER_SEC%.0%__TIMER_REM_CS%s ^(%__TIMER_ELAPSED_MS% ms^)
) else (
  echo [%__TIMER_LABEL%] elapsed: %__TIMER_SEC%.%__TIMER_REM_CS%s ^(%__TIMER_ELAPSED_MS% ms^)
)
exit /b 0
