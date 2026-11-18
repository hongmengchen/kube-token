# kube-token 安装脚本（Windows PowerShell）
#
#   irm https://raw.githubusercontent.com/hongmengchen/kube-token/main/install.ps1 | iex
#
# 参数：
#   -Version <tag>   指定版本，如 v0.6.1（默认 latest）
#   -BinDir <path>   安装目录（默认 %LOCALAPPDATA%\kube-token）
#
# 环境变量：
#   NO_ANIMATION=1   不播放安装动画（输出被重定向时会自动关闭）
#   NO_PROMPT=1      不询问是否安装 agent skill

param(
    [string]$Version = "latest",
    [string]$BinDir = ""
)

$ErrorActionPreference = "Stop"
$Repo = "hongmengchen/kube-token"
$Exe  = "kube-token.exe"

function Fail($msg) {
    Write-Host "错误: $msg" -ForegroundColor Red
    exit 1
}

# 控制台按 UTF-8 输出：否则 [Console]::Write 会把中文和 █░⠋ 写成 ???
try {
    [Console]::OutputEncoding = [System.Text.Encoding]::UTF8
    $OutputEncoding = [System.Text.Encoding]::UTF8
} catch { }

switch ($env:PROCESSOR_ARCHITECTURE) {
    "AMD64" { $Arch = "amd64" }
    "ARM64" { $Arch = "arm64" }
    default { Fail "不支持的 CPU 架构: $($env:PROCESSOR_ARCHITECTURE)" }
}
$Asset = "kube-token_windows_${Arch}.exe"

if ($Version -eq "latest") {
    $Base = "https://github.com/$Repo/releases/latest/download"
} else {
    $Base = "https://github.com/$Repo/releases/download/$Version"
}
$Url = "$Base/$Asset"

if (-not $BinDir) {
    if ($env:INSTALL_DIR) { $BinDir = $env:INSTALL_DIR }
    else { $BinDir = Join-Path $env:LOCALAPPDATA "kube-token" }
}

Write-Host "下载: $Url" -ForegroundColor DarkGray
New-Item -ItemType Directory -Force -Path $BinDir | Out-Null
$Target = Join-Path $BinDir $Exe
$Tmp = Join-Path $env:TEMP ("kube-token-" + [guid]::NewGuid().ToString("N") + ".exe")

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

    [Console]::ForegroundColor = [ConsoleColor]::DarkCyan
    [Console]::Write("`r  " + $Frames[$Index % 10])
    [Console]::ResetColor()
    [Console]::Write(" 正在下载 $Tag  ")
    if ($bar) {
        [Console]::ForegroundColor = [ConsoleColor]::DarkCyan
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
        $fs = [System.IO.File]::Create($Tmp)
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

        $size = (Get-Item $Tmp).Length
        [Console]::Write("`r  ")
        [Console]::ForegroundColor = [ConsoleColor]::Green
        [Console]::Write("✓")
        [Console]::ResetColor()
        # 行尾补空格盖掉上一帧进度条残留（不一定有 VT 支持，不能只靠 ESC[K）
        [Console]::WriteLine(" 已下载 $(Format-Size $size)  $Tag" + (" " * 40))
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
        Remove-Item -Force $Tmp -ErrorAction SilentlyContinue
        Fail "下载失败: $($_.Exception.Message)`n可手动到 https://github.com/$Repo/releases 下载"
    } finally {
        [Console]::CursorVisible = $true
    }
} else {
    try {
        Invoke-WebRequest -Uri $Url -OutFile $Tmp -UseBasicParsing
    } catch {
        Remove-Item -Force $Tmp -ErrorAction SilentlyContinue
        Fail "下载失败: $_`n可手动到 https://github.com/$Repo/releases 下载"
    }
}

$item = Get-Item $Tmp
if ($item.Length -lt 100000) {
    Remove-Item -Force $Tmp -ErrorAction SilentlyContinue
    Fail "下载的文件只有 $($item.Length) 字节，可能不是二进制"
}

# ---- 校验 SHA-256（release 带 checksums.txt 就强制校验）----
$sums = "$Tmp.sha256"
$verifyErr = ""
try {
    Invoke-WebRequest -Uri "$Base/checksums.txt" -OutFile $sums -UseBasicParsing
    $line = Select-String -Path $sums -Pattern ("\s+" + [regex]::Escape($Asset) + "$") | Select-Object -First 1
    if (-not $line) {
        $verifyErr = "checksums.txt 里没有 $Asset"
    } else {
        $want = ($line.Line -split "\s+")[0]
        $got = (Get-FileHash -Algorithm SHA256 -Path $Tmp).Hash
        if ($got -ne $want.ToUpper()) {
            $verifyErr = "校验失败：下载到的文件与 checksums.txt 不一致"
        }
    }
} catch { }
Remove-Item -Force $sums -ErrorAction SilentlyContinue

if ($verifyErr) {
    Remove-Item -Force $Tmp -ErrorAction SilentlyContinue
    Fail $verifyErr
}
if ($Animate) {
    [Console]::Write("  ")
    [Console]::ForegroundColor = [ConsoleColor]::Green
    [Console]::Write("✓")
    [Console]::ResetColor()
    [Console]::WriteLine(" SHA-256 校验通过" + (" " * 20))
}

try {
    Move-Item -Force -Path $Tmp -Destination $Target
} catch {
    Remove-Item -Force $Tmp -ErrorAction SilentlyContinue
    Fail "写入 $Target 失败（程序是否正在运行？）`n$($_.Exception.Message)"
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
    [Console]::WriteLine(" 已安装: $Target" + (" " * 20))
} else {
    Write-Host "已安装: $Target" -ForegroundColor Green
}
& $Target --version

# ---- agent skill（可选，只在真终端里问） ----
if ($Animate -and -not $env:NO_PROMPT) {
    $answer = Read-Host "`n把 agent skill 装到 $HOME\.claude\skills（让 agent 会用本工具）？[Y/n]"
    if ($answer -match '^\s*[nN]') {
        Write-Host "已跳过。以后随时可以运行: kube-token skill" -ForegroundColor DarkGray
    } else {
        & $Target skill
    }
}

if (Get-Command kube-token -ErrorAction SilentlyContinue) {
    Write-Host "当前终端已可用，直接运行: kube-token" -ForegroundColor Green
} else {
    Write-Host "新开一个终端后运行: kube-token" -ForegroundColor Yellow
}
