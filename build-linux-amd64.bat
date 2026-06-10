@echo off
REM ============================================================================
REM Build script for homed-mcp (Linux x86_64 / Ubuntu)
REM
REM Cross-compiles the MCP server from Windows to a statically-linked
REM Linux amd64 binary. The output does NOT require Go or any external
REM runtime on the target machine.
REM
REM Usage:
REM     build-linux-amd64.bat
REM
REM Output:
REM     mcp-server\homed-mcp-linux-amd64
REM ============================================================================

setlocal

REM Switch to the script directory (mcp-server\).
cd /d "%~dp0"

go clean

REM Toolchain settings.
set CGO_ENABLED=0
set GOOS=linux
set GOARCH=amd64

REM Strip build paths and shrink the binary.
set GOFLAGS=-trimpath
set LDFLAGS=-s -w

set OUTPUT=homed-mcp-linux-amd64
set PKG=./cmd/server

echo === homed-mcp build ===
echo GOOS=%GOOS% GOARCH=%GOARCH% CGO_ENABLED=%CGO_ENABLED%
echo.

REM Refresh the module cache (idempotent, fast on subsequent runs).
go mod download
if errorlevel 1 (
    echo [ERROR] go mod download failed.
    exit /b 1
)

REM Build the binary.
go build %GOFLAGS% -ldflags="%LDFLAGS%" -o %OUTPUT% %PKG%
if errorlevel 1 (
    echo [ERROR] go build failed.
    exit /b 1
)

echo.
echo Build successful: %CD%\%OUTPUT%

REM Print the file size in a human-readable form.
for %%I in (%OUTPUT%) do echo Size: %%~zI bytes

endlocal