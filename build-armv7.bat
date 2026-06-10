@echo off
cd /d %~dp0
go clean
set GOOS=linux
set GOARCH=arm
set GOARM=7
go build -o homed-mcp ./cmd/server/
echo Stopping existing process...
ssh root@192.168.0.14 "killall homed-mcp 2>/dev/null || true"
ssh root@192.168.0.14 "cd /opt/homed-mcp && rm homed-mcp && rm app.log"
echo Deploying to 192.168.0.14...
scp homed-mcp root@192.168.0.14:/opt/homed-mcp/
ssh root@192.168.0.14 "cd /opt/homed-mcp && chmod +x homed-mcp "

