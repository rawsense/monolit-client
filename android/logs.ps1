# Забрать журналы Monolit с телефона по USB (отладка по USB включена): журнал программы, файл аварий
# и журнал Android за последние минуты. Всё кладётся в build\android\logs\<дата-время>
$ErrorActionPreference = 'Stop'
Set-Location (Split-Path $PSScriptRoot)
$sdk = $env:ANDROID_HOME
if (-not $sdk) { $sdk = "$env:LOCALAPPDATA\Android\Sdk" }
$adb = "$sdk\platform-tools\adb.exe"
$pkg = 'io.github.rawsense.monolit'
$dst = "build\android\logs\$(Get-Date -Format 'yyyy-MM-dd_HH-mm-ss')"
New-Item -ItemType Directory -Force $dst | Out-Null
$src = "/sdcard/Android/data/$pkg/files"
foreach ($f in 'monolit.log', 'monolit.log.1', 'crash.log', 'crash.log.1') {
  & $adb shell "test -f $src/$f" 2>$null
  if ($LASTEXITCODE -eq 0) { & $adb pull "$src/$f" "$dst\$f" | Out-Null }
}
# журнал Android: падения приложения, его строки и решения системы о сервисе VPN
& $adb logcat -d -v time -t 5000 | Select-String -Pattern 'monolit|GoLog|AndroidRuntime|FATAL|Vpn' | Set-Content -Encoding utf8 "$dst\logcat.txt"
& $adb shell "dumpsys package $pkg | grep -E 'versionName|versionCode|firstInstallTime|lastUpdateTime'" | Set-Content -Encoding utf8 "$dst\version.txt"
Get-ChildItem $dst | ForEach-Object { "{0}  {1:N0} байт" -f $_.Name, $_.Length }
"папка: $dst"
