@echo off
setlocal

set BINARY=http-proxy.exe

if not exist %BINARY% (
    echo %BINARY% not found, building...
    call build.bat
    if errorlevel 1 exit /b %errorlevel%
)

echo Starting http-proxy ...
echo Management UI: http://127.0.0.1:9090
echo.
%BINARY%
