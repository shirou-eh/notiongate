# notiongate — irm installer (Windows PowerShell)
# usage:
#   irm https://raw.githubusercontent.com/shirou-eh/notiongate/main/scripts/install.ps1 | iex
#   irm https://raw.githubusercontent.com/shirou-eh/notiongate/main/scripts/install.ps1 | iex; notiongate menu
# env:
#   $env:NOTIONGATE_VERSION = "latest" | "v0.1.0"
#   $env:NOTIONGATE_INSTALL_DIR = "C:\tools\notiongate"

param(
  [string]$Version = $env:NOTIONGATE_VERSION,
  [string]$InstallDir = $env:NOTIONGATE_INSTALL_DIR
)

$ErrorActionPreference = "Stop"
$Repo = "shirou-eh/notiongate"
$Binary = "notiongate.exe"
if (-not $Version -or $Version -eq "") { $Version = "latest" }

function Info($msg)  { Write-Host "[notiongate] $msg" -ForegroundColor Cyan }
function Warn($msg)  { Write-Host "[warn] $msg" -ForegroundColor Yellow }
function Err($msg)   { Write-Host "[error] $msg" -ForegroundColor Red; exit 1 }

# Гейтик — немой, просто существует.
Write-Host @"
  +--------------+
  |  ████████    |   notiongate
  | ███ ██ ███   |   Гейтик просто есть.
  | ██████████   |
  +--------------+
"@

# arch
$Arch = "amd64"
if ($env:PROCESSOR_ARCHITECTURE -like "*ARM*") { $Arch = "arm64" }
$Os = "windows"

if (-not $InstallDir -or $InstallDir -eq "") {
  # пробуем %LOCALAPPDATA%\notiongate, затем C:\tools
  $InstallDir = Join-Path $env:LOCALAPPDATA "notiongate"
  if (-not (Test-Path $InstallDir)) {
    try { New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null } catch {}
  }
}
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
Info "OS: $Os/$Arch  version: $Version  dir: $InstallDir"

# resolve latest
if ($Version -eq "latest") {
  try {
    $r = Invoke-WebRequest -Uri "https://github.com/$Repo/releases/latest" -MaximumRedirection 0 -ErrorAction SilentlyContinue
  } catch {
    $loc = $_.Exception.Response.Headers.Location
    if ($loc) {
      $tag = ($loc -split "/")[-1]
      if ($tag) { $Version = $tag; Info "latest -> $Version" }
    }
  }
}
$Version = $Version.TrimStart("v")
$Asset = "notiongate-$Os-$Arch.exe"
if ($Arch -eq "amd64") { $Asset = "notiongate-windows-amd64.exe" }

$Urls = @(
  "https://github.com/$Repo/releases/download/v$Version/$Asset",
  "https://github.com/$Repo/releases/download/v$Version/notiongate.exe",
  "https://github.com/$Repo/releases/download/v$Version/notiongate-$Os-$Arch"
)

$Tmp = Join-Path $env:TEMP "notiongate-install-$([Guid]::NewGuid().ToString().Substring(0,8))"
New-Item -ItemType Directory -Path $Tmp | Out-Null
$BinTmp = Join-Path $Tmp $Binary
$Downloaded = $null
foreach ($url in $Urls) {
  Info "try $url"
  try {
    # 1.5.1+ : curl alias → Invoke-WebRequest, используем напрямую
    Invoke-WebRequest -Uri $url -OutFile $BinTmp -UseBasicParsing -TimeoutSec 60
    if ((Test-Path $BinTmp) -and ((Get-Item $BinTmp).Length -gt 100kb)) { $Downloaded = $url; break }
  } catch { continue }
}

if (-not $Downloaded) {
  Warn "бинарник не найден в релизах, пробую go install..."
  if (-not (Get-Command go -ErrorAction SilentlyContinue)) { Err "не найден ни релиз ни go. Установи go или укажи -Version" }
  $env:GOBIN = $Tmp
  & go install "notiongate/cmd/notiongate@v$Version" 2>$null
  if (-not (Test-Path $BinTmp)) { & go install "./cmd/notiongate" 2>$null }
  if (-not (Test-Path $BinTmp)) { Err "go install провалился" }
  $Downloaded = "go install"
}

# если архив
try {
  $sig = Get-Content -Path $BinTmp -TotalCount 2 -Encoding Byte
  if ($sig[0] -eq 0x50 -and $sig[1] -eq 0x4B) {
    Info "распаковываю zip..."
    Expand-Archive -Path $BinTmp -DestinationPath $Tmp -Force
    $found = Get-ChildItem -Path $Tmp -Recurse -Filter "$Binary" | Select-Object -First 1
    if ($found) { $BinTmp = $found.FullName }
  } elseif ($sig[0] -eq 0x1F -and $sig[1] -eq 0x8B) {
    # tar.gz — распакуем через tar если есть
    tar -xzf $BinTmp -C $Tmp 2>$null
    $found = Get-ChildItem -Path $Tmp -Recurse -Filter "$Binary" | Select-Object -First 1
    if ($found) { $BinTmp = $found.FullName }
  }
} catch {}

$Dest = Join-Path $InstallDir $Binary
try {
  Move-Item -Force -Path $BinTmp -Destination $Dest
} catch {
  Err "не удалось переместить в $Dest : $_ (запусти PowerShell от администратора)"
}

# PATH
$UserPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($UserPath -notlike "*$InstallDir*") {
  Warn "$InstallDir не в PATH"
  try {
    [Environment]::SetEnvironmentVariable("Path", "$UserPath;$InstallDir", "User")
    $env:Path += ";$InstallDir"
    Info "добавил $InstallDir в User PATH (переоткрой терминал)"
  } catch { Warn "не удалось добавить в PATH — добавь вручную" }
}

Info "установлено: $Dest ($Downloaded)"
try { & $Dest version } catch {}

Write-Host @"

  Гейтик просто существует.

  Дальше:
    notiongate menu              — интерактивное меню
    notiongate setup             — мастер настройки
    notiongate login --serve     — добавить аккаунт и запустить
    notiongate autostart install — автозапуск

"@

# cleanup
Remove-Item -Recurse -Force $Tmp -ErrorAction SilentlyContinue
