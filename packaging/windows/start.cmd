@echo off
rem One-click start for the Windows Sovereign bundle.
rem
rem The web console is embedded in the binary: no Node.js, no Go toolchain and
rem no external database are required. Keep this file next to sovereign-server.exe,
rem config.example.yaml and QUICKSTART.txt (i.e. do not move the .exe on its own).
setlocal EnableExtensions
chcp 65001 >nul
title Sovereign
cd /d "%~dp0"

if not exist "sovereign-server.exe" (
    echo [x] sovereign-server.exe is missing next to this script.
    echo     Unpack the whole archive and run start.cmd from inside it.
    pause
    exit /b 1
)

if not exist "config.yaml" (
    if not exist "config.example.yaml" (
        echo [x] config.yaml is missing and config.example.yaml cannot create one.
        echo     Re-download the archive; it must contain config.example.yaml.
        pause
        exit /b 1
    )
    echo [*] First run: creating config.yaml from config.example.yaml
    copy /y "config.example.yaml" "config.yaml" >nul
)

echo.
echo   Sovereign is starting.
echo   Console : https://127.0.0.1:8000/   (self-signed certificate: accept once)
echo   Login   : the first start prints the generated "admin" password below.
echo   Stop    : press Ctrl+C in this window.
echo.

rem Give the listener a moment, then open the console in the default browser.
if exist "%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe" (
    start "" /b "%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -WindowStyle Hidden -Command "Start-Sleep -Seconds 6; Start-Process 'https://127.0.0.1:8000/'"
)

"sovereign-server.exe" -config config.yaml
set "exitcode=%ERRORLEVEL%"

echo.
echo Sovereign exited with code %exitcode%.
pause
exit /b %exitcode%
