# Сборка build\monolit.exe: иконка и манифест (чёткость на HiDPI), затем exe без консоли
$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot
$ver = (Select-String -Path core\app.go -Pattern 'const version = "(.+)"').Matches[0].Groups[1].Value
$env:MONOLIT_ICON = '1'; go test -timeout 60s -run TestWriteIcon ./windows | Out-Null; Remove-Item Env:MONOLIT_ICON
& "$env:USERPROFILE\go\bin\go-winres.exe" simply --icon windows\assets\monolit.ico --manifest gui --arch amd64 --out windows\rsrc `
  --product-name Monolit --file-description "Monolit" --product-version $ver --file-version $ver --copyright "rawsense"
go build -trimpath -ldflags "-H windowsgui -s -w" -o build\monolit.exe ./windows
if ($LASTEXITCODE -ne 0) { exit 1 }
"build\monolit.exe $ver, $([math]::Round((Get-Item build\monolit.exe).Length / 1MB, 1)) МБ"
