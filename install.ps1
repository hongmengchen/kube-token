# kube-token 安装脚本（Windows PowerShell）
#
#   irm https://raw.githubusercontent.com/hongmengchen/kube-token/main/install.ps1 | iex
#
# 参数：
#   -Version <tag>   指定版本，如 v0.1.0（默认 latest）
#   -BinDir <path>   安装目录（默认 %LOCALAPPDATA%\kube-token）
#
# 环境变量：
#   NO_ANIMATION=1   不播放安装动画（输出被重定向时会自动关闭）

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

# ---- 动画：和 `kube-token upgrade` 同一套观感（spinner + 进度条 + 大小/速度） ----
$Animate = (-not [Console]::IsOutputRedirected) -and (-not $env:NO_ANIMATION)
$Frames = @('⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏')
$Tag = $Version

function Format-Size([double]$Bytes) {
    if ($Bytes -ge 1MB) { return ("{0:N1} MB" -f ($Bytes / 1MB)) }
    if ($Bytes -ge 1KB) { return ("{0:N0} KB" -f ($Bytes / 1KB)) }
    return ("{0:N0} B" -f $Bytes)
}

function Write-Download-Progress([int]$Index, [long]$Done, [long]$Total, [double]$Speed) {
    $width = 22
    $bar = ""
    if ($Total -gt 0) {
        $filled = [Math]::Min($width, [int]($Done * $width / $Total))
        $bar = ("█" * $filled) + ("░" * ($width - $filled))
        $stats = "{0,5:N1}%  {1:N1}/{2:N1} MB" -f (100.0 * $Done / $Total), ($Done / 1MB), ($Total / 1MB)
    } else {
        $stats = "{0:N1} MB" -f ($Done / 1MB)
    }
    if ($Speed -gt 0) { $stats += ("  {0:N1} MB/s" -f ($Speed / 1MB)) }

    [Console]::ForegroundColor = [ConsoleColor]::Magenta
    [Console]::Write("`r  " + $Frames[$Index % 10])
    [Console]::ResetColor()
    [Console]::Write(" 正在下载 $Tag  ")
    if ($bar) {
        [Console]::ForegroundColor = [ConsoleColor]::Magenta
        [Console]::Write($bar)
        [Console]::ResetColor()
        [Console]::Write("  ")
    }
    [Console]::Write("$stats   ")
}

function Save-Download-Animated {
    Add-Type -AssemblyName System.Net.Http
    $client = New-Object System.Net.Http.HttpClient
    $client.Timeout = [TimeSpan]::FromMinutes(10)
    try {
        $resp = $client.GetAsync($Url, [System.Net.Http.HttpCompletionOption]::ResponseHeadersRead).GetAwaiter().GetResult()
        if (-not $resp.IsSuccessStatusCode) {
            throw "HTTP $([int]$resp.StatusCode) $($resp.ReasonPhrase)"
        }
        # latest 会 302 到 /download/<tag>/，顺手把版本号找出来
        try {
            $final = $resp.RequestMessage.RequestUri.AbsoluteUri
            if ($final -match '/download/(v[^/]+)/') { $script:Tag = $Matches[1] }
        } catch { }

        $total = 0
        if ($resp.Content.Headers.ContentLength) { $total = [long]$resp.Content.Headers.ContentLength }

        $stream = $resp.Content.ReadAsStreamAsync().GetAwaiter().GetResult()
        $fs = [System.IO.File]::Create($Target)
        try {
            $buffer = New-Object byte[] 65536
            $done = [long]0
            $watch = [System.Diagnostics.Stopwatch]::StartNew()
            $lastMs = 0.0
            $lastBytes = [long]0
            $speed = 0.0
            $i = 0
            while (($n = $stream.Read($buffer, 0, $buffer.Length)) -gt 0) {
                $fs.Write($buffer, 0, $n)
                $done += $n
                if (($watch.Elapsed.TotalMilliseconds - $lastMs) -ge 200) {
                    $dt = ($watch.Elapsed.TotalMilliseconds - $lastMs) / 1000.0
                    if ($dt -gt 0) { $speed = ($done - $lastBytes) / $dt }
                    $lastMs = $watch.Elapsed.TotalMilliseconds
                    $lastBytes = $done
                    Write-Download-Progress $i $done $total $speed
                    $i++
                }
            }
        } finally {
            $fs.Close()
            $stream.Close()
            $resp.Dispose()
        }

        $size = (Get-Item $Target).Length
        [Console]::Write("`r  ")
        [Console]::ForegroundColor = [ConsoleColor]::Green
        [Console]::Write("✓")
        [Console]::ResetColor()
        [Console]::WriteLine(" 已下载 $(Format-Size $size)  $Tag                ")
    } finally {
        $client.Dispose()
    }
}

if ($Animate) {
    try {
        [Console]::CursorVisible = $false
        Save-Download-Animated
    } catch {
        [Console]::CursorVisible = $true
        $msg = "下载失败: $($_.Exception.Message)`n可手动到 https://github.com/$Repo/releases 下载"
        Write-Error $msg
        exit 1
    } finally {
        [Console]::CursorVisible = $true
    }
} else {
    try {
        Invoke-WebRequest -Uri $Url -OutFile $Target -UseBasicParsing
    } catch {
        Write-Error "下载失败: $_`n可手动到 https://github.com/$Repo/releases 下载"
        exit 1
    }
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

if ($Animate) {
    [Console]::ForegroundColor = [ConsoleColor]::Green
    [Console]::Write("  ✓")
    [Console]::ResetColor()
    [Console]::WriteLine(" 已安装: $Target")
} else {
    Write-Host "已安装: $Target" -ForegroundColor Green
}
& $Target --version

if (Get-Command kube-token -ErrorAction SilentlyContinue) {
    Write-Host "当前终端已可用，直接运行: kube-token" -ForegroundColor Green
} else {
    Write-Host "新开一个终端后运行: kube-token" -ForegroundColor Yellow
}
