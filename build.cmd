set GOOS=windows
set GOARCH=amd64

::go build -ldflags "-s -w" --buildmode=c-shared -o bin/AudioSender.dll
go build -ldflags "-s -w" -o bin/AudioSender.exe