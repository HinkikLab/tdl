@echo off
setlocal

pushd "%~dp0" || exit /b 1

where go >nul 2>&1
if errorlevel 1 (
    echo [ERROR] Go is not installed or is not available in PATH.
    popd
    exit /b 1
)

set "GOOS=windows"
set "GOARCH=amd64"
set "GOAMD64=v1"
set "CGO_ENABLED=0"

echo Building stripped Windows amd64 executable...
go build -trimpath -ldflags="-s -w" -o "tdl.exe" .
set "BUILD_EXIT_CODE=%ERRORLEVEL%"
if not "%BUILD_EXIT_CODE%"=="0" (
    echo [ERROR] Build failed with exit code %BUILD_EXIT_CODE%.
    popd
    exit /b %BUILD_EXIT_CODE%
)

echo Built "%CD%\tdl.exe"
popd
endlocal
exit /b 0
