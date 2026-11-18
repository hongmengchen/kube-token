# kube-token 安装脚本（Windows PowerShell）
#
#   irm https://raw.githubusercontent.com/hongmengchen/kube-token/main/install.ps1 | iex
#
# 参数：
#   -Version <tag>   指定版本，如 v0.1.0（默认 latest）
#   -BinDir <path>   安装目录（默认 %LOCALAPPDATA%\kube-token）

param(
    [string]$Version = "latest",
    [string]$BinDir = ""
)

$ErrorActionPreference = "Stop"
$Repo = "hongmengchen/kube-token"
$Exe  = "kube-token.exe"

$Arch = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
if ($Arch -eq "arm64") {
    Write-Host "暂未提供 Windows ARM64 构建，将安装 amd64 版本（Windows 会自动转译运行）" -ForegroundColor Yellow
}
$Asset = "kube-token_windows_amd64.exe"

if ($Version -eq "latest") {
    $Url = "https://github.com/$Repo/releases/latest/download/$Asset"
} else {
    $Url = "https://github.com/$Repo/releases/download/$Version/$Asset"
}

if (-not $BinDir) {
    if ($env:INSTALL_DIR) { $BinDir = $env:INSTALL_DIR }
    else { $BinDir = Join-Path $env:LOCALAPPDATA "kube-token" }
}

Write-Host "下载: $Url" -ForegroundColor DarkGray
New-Item -ItemType Directory -Force -Path $BinDir | Out-Null
$Target = Join-Path $BinDir $Exe

try {
    Invoke-WebRequest -Uri $Url -OutFile $Target -UseBasicParsing
} catch {
    Write-Error "下载失败: $_`n可手动到 https://github.com/$Repo/releases 下载"
    exit 1
}

$item = Get-Item $Target
if ($item.Length -lt 100000) {
    Write-Error "下载的文件只有 $($item.Length) 字节，可能不是二进制"
    exit 1
}

# 持久化：写进用户 PATH，供以后新开的终端使用
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if (-not $userPath) { $userPath = "" }
if ((($userPath -split ";") | Where-Object { $_ }) -notcontains $BinDir) {
    $newPath = if ($userPath) { "$userPath;$BinDir" } else { $BinDir }
    [Environment]::SetEnvironmentVariable("Path", $newPath, "User")
    Write-Host "$BinDir 已写入用户 PATH" -ForegroundColor DarkGray
}

# 当前进程：立刻生效，不用重开终端
if ((($env:Path -split ";") | Where-Object { $_ }) -notcontains $BinDir) {
    $env:Path = if ($env:Path) { "$env:Path;$BinDir" } else { $BinDir }
}

Write-Host "已安装: $Target" -ForegroundColor Green
& $Target --version

if (Get-Command kube-token -ErrorAction SilentlyContinue) {
    Write-Host "当前终端已可用，直接运行: kube-token" -ForegroundColor Green
} else {
    Write-Host "新开一个终端后运行: kube-token" -ForegroundColor Yellow
}
