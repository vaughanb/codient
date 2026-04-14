$ErrorActionPreference = 'Stop'

$Repo = 'vaughanb/codient'
$InstallDir = if ($env:CODIENT_INSTALL_DIR) { $env:CODIENT_INSTALL_DIR } else {
    Join-Path $env:LOCALAPPDATA 'codient'
}

function Info($msg) { Write-Host $msg -ForegroundColor Cyan }
function Warn($msg) { Write-Host $msg -ForegroundColor Yellow }
function Fail($msg) { Write-Host "error: $msg" -ForegroundColor Red; exit 1 }

$Arch = switch ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture) {
    'X64'   { 'amd64' }
    'Arm64' { 'arm64' }
    default { Fail "unsupported architecture: $_" }
}

Info 'Detecting latest release...'
$Release = Invoke-RestMethod "https://api.github.com/repos/$Repo/releases/latest"
$Tag = $Release.tag_name
if (-not $Tag) { Fail 'could not determine latest release' }
$Version = $Tag.TrimStart('v')
Info "Latest version: $Version"

$Archive = "codient_${Version}_windows_${Arch}.zip"
$Url = "https://github.com/$Repo/releases/download/$Tag/$Archive"

$TmpDir = Join-Path ([System.IO.Path]::GetTempPath()) ([System.IO.Path]::GetRandomFileName())
New-Item -ItemType Directory -Path $TmpDir -Force | Out-Null

try {
    $ZipPath = Join-Path $TmpDir $Archive

    Info "Downloading $Archive..."
    Invoke-WebRequest -Uri $Url -OutFile $ZipPath -UseBasicParsing

    Info 'Extracting...'
    Expand-Archive -Path $ZipPath -DestinationPath $TmpDir -Force

    if (-not (Test-Path $InstallDir)) {
        New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
    }

    Copy-Item (Join-Path $TmpDir 'codient.exe') (Join-Path $InstallDir 'codient.exe') -Force

    Info "Installed codient $Version to $InstallDir\codient.exe"

    $UserPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if ($UserPath -split ';' -notcontains $InstallDir) {
        [Environment]::SetEnvironmentVariable('Path', "$InstallDir;$UserPath", 'User')
        $env:Path = "$InstallDir;$env:Path"
        Info "Added $InstallDir to your user PATH."
        Warn 'Restart your terminal for PATH changes to take effect.'
    }
}
finally {
    Remove-Item -Recurse -Force $TmpDir -ErrorAction SilentlyContinue
}
