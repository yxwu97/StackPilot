@echo off
setlocal

set "STACKPILOT_ROOT=%~dp0"
set "STACKPILOT_EXE=%STACKPILOT_ROOT%dist\stackpilot.exe"

if not exist "%STACKPILOT_EXE%" (
    echo StackPilot executable was not found:
    echo   %STACKPILOT_EXE%
    echo Run npm run build from the repository first.
    pause
    exit /b 1
)

set "STACKPILOT_LAUNCH_ARGS="
if /i "%~1"=="--no-open" set "STACKPILOT_LAUNCH_ARGS=-NoOpen"

powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "%STACKPILOT_ROOT%scripts\start-stackpilot.ps1" %STACKPILOT_LAUNCH_ARGS%
if errorlevel 1 goto :failed
exit /b 0

:failed
echo.
echo StackPilot could not be started. Review the error above.
pause
exit /b 1
