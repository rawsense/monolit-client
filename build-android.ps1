# Сборка build\android\monolit.apk: библиотека общей логики и ядра (gomobile), затем приложение
# инструментами Android SDK, без Gradle. Нужны: Go, gomobile и gobind (go install golang.org/x/mobile/cmd/...),
# Android SDK с NDK, JDK 17. Пути берутся из JAVA_HOME и ANDROID_HOME, иначе из обычных мест.
# Подпись: ключ MONOLIT_KEYSTORE или ..\monolit-keys\monolit-release.jks, пароль — в файле .pass рядом с ключом.
# Ключа нет — отладочный build\android\debug.jks (такую сборку поверх выпускной не поставить)
$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot

$jdk = $env:JAVA_HOME
if (-not $jdk) { $jdk = (Get-ChildItem ..\android-tools -Directory -Filter 'jdk-*' | Sort-Object Name | Select-Object -Last 1).FullName }
$sdk = $env:ANDROID_HOME
if (-not $sdk) { $sdk = "$env:LOCALAPPDATA\Android\Sdk" }
$ndk = (Get-ChildItem "$sdk\ndk" -Directory | Sort-Object Name | Select-Object -Last 1).FullName
$bt = (Get-ChildItem "$sdk\build-tools" -Directory | Sort-Object Name | Select-Object -Last 1).FullName
$jar = (Get-ChildItem "$sdk\platforms" -Directory -Filter 'android-*' | Sort-Object { [double]($_.Name -replace 'android-', '') } | Select-Object -Last 1).FullName + '\android.jar'
$env:JAVA_HOME = $jdk; $env:ANDROID_HOME = $sdk; $env:ANDROID_NDK_HOME = $ndk
$env:PATH = "$jdk\bin;$env:USERPROFILE\go\bin;$env:PATH"

function Step($what) { if ($LASTEXITCODE -ne 0) { Write-Host "не вышло: $what"; exit 1 } }

$ver = (Select-String -Path core\app.go -Pattern 'const version = "(.+)"').Matches[0].Groups[1].Value
$p = $ver.Split('.'); $code = [int]$p[0] * 10000 + [int]$p[1] * 100 + [int]$p[2]
$out = 'build\android'
$abi = 'arm64-v8a'
New-Item -ItemType Directory -Force $out | Out-Null
foreach ($d in 'aar', 'classes', 'stage', 'gen') { if (Test-Path "$out\$d") { Remove-Item -Recurse -Force "$out\$d" } }

# 1. общая логика и ядро — библиотекой
gomobile bind -target=android/arm64 -androidapi 24 -trimpath -ldflags "-s -w" -o "$out\monolit.aar" ./android/mobile; Step 'gomobile bind'
Expand-Archive -Path (Copy-Item "$out\monolit.aar" "$out\monolit.zip" -PassThru) -DestinationPath "$out\aar" -Force
Remove-Item "$out\monolit.zip"

# 2. ресурсы и манифест
& "$bt\aapt2.exe" compile --dir android\app\res -o "$out\res.zip"; Step 'aapt2 compile'
& "$bt\aapt2.exe" link -o "$out\base.apk" -I $jar --manifest android\app\AndroidManifest.xml --java "$out\gen" `
  --min-sdk-version 24 --target-sdk-version 36 --version-code $code --version-name $ver "$out\res.zip"; Step 'aapt2 link'

# 3. код приложения
$src = @(Get-ChildItem android\app\java, "$out\gen" -Recurse -Filter *.java | ForEach-Object FullName)
New-Item -ItemType Directory -Force "$out\classes" | Out-Null
javac -nowarn -encoding UTF-8 -source 8 -target 8 -bootclasspath $jar -classpath "$out\aar\classes.jar;$bt\core-lambda-stubs.jar" -d "$out\classes" $src; Step 'javac'
jar --create --file "$out\app.jar" -C "$out\classes" .; Step 'jar'
New-Item -ItemType Directory -Force "$out\stage\lib\$abi" | Out-Null
& "$bt\d8.bat" --release --min-api 24 --lib $jar --output "$out\stage" "$out\app.jar" "$out\aar\classes.jar"; Step 'd8'
Copy-Item "$out\aar\jni\$abi\libgojni.so" "$out\stage\lib\$abi\"

# 4. сборка, выравнивание, подпись
Push-Location "$out\stage"
& "$bt\aapt.exe" add ..\base.apk classes.dex "lib/$abi/libgojni.so" | Out-Null; $ok = $LASTEXITCODE
Pop-Location
if ($ok -ne 0) { Write-Host 'не вышло: aapt add'; exit 1 }
& "$bt\zipalign.exe" -f -p 4 "$out\base.apk" "$out\aligned.apk"; Step 'zipalign'
$ks = $env:MONOLIT_KEYSTORE; $key = 'выпускной'
if (-not $ks) { $ks = '..\monolit-keys\monolit-release.jks' }
$passFile = [IO.Path]::ChangeExtension($ks, '.pass')
if (-not (Test-Path $ks) -or -not (Test-Path $passFile)) {
  $ks = "$out\debug.jks"; $passFile = "$out\debug.pass"; $key = 'отладочный'
  if (-not (Test-Path $ks)) {
    [IO.File]::WriteAllText("$PWD\$passFile", 'android')
    keytool -genkeypair -keystore $ks -storepass:file $passFile -alias monolit -keyalg RSA -keysize 2048 -validity 10000 -dname 'CN=Monolit debug' | Out-Null; Step 'keytool'
  }
}
& "$bt\apksigner.bat" sign --ks $ks --ks-pass "file:$passFile" --out "$out\monolit.apk" "$out\aligned.apk"; Step 'apksigner'
Remove-Item "$out\base.apk", "$out\aligned.apk", "$out\res.zip", "$out\app.jar" -ErrorAction SilentlyContinue
"$out\monolit.apk $ver, $([math]::Round((Get-Item "$out\monolit.apk").Length / 1MB, 1)) МБ, ключ $key"
