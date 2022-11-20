set GOOS=windows
set GOARCH=amd64

go build -ldflags "-s -w" --buildmode=c-shared -o bin/RTSPSender.dll
go build -ldflags "-s -w" -o bin/RTSPSender.exe