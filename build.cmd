set GOOS=windows
set GOARCH=amd64

@REM set PION_LOG_DEBUG=pc
@REM set dtls PIONS_LOG_INFO=all

go build -ldflags "-s -w" --buildmode=c-shared -o bin/AudioSender.dll
go build -ldflags "-s -w" -o bin/AudioSender.exe