@REM https://habr.com/ru/post/249449/

@SET GOOS=windows
@SET GOARCH=amd64
go build -ldflags "-s -w" -o bin/RTSPSender.exe

@SET GOOS=darwin
@SET GOARCH=amd64
go build -ldflags "-s -w" -o bin/RTSPSender
