set GOOS=windows
set GOARCH=amd64
go build -ldflags "-s -w" -o bin/RTSPSender.exe

set GOOS=darwin
set GOARCH=amd64
go build -ldflags "-s -w" -o bin/RTSPSender
