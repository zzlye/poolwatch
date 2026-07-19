param(
    [string]$BackupDirectory = "D:\tmp\号池监控备份"
)

$ErrorActionPreference = "Stop"

# PowerShell 脚本仅用于 Windows 开发环境，临时备份统一放在 D:\tmp。
$projectDirectory = Split-Path -Parent $PSScriptRoot
$composeFile = Join-Path $projectDirectory "docker-compose.yml"
$resolvedBackupDirectory = [System.IO.Path]::GetFullPath($BackupDirectory)
[System.IO.Directory]::CreateDirectory($resolvedBackupDirectory) | Out-Null
$timestamp = Get-Date -Format "yyyyMMdd-HHmmss"
$archiveName = "poolwatch-$timestamp.tar.gz"

$environmentFile = Join-Path $projectDirectory ".env"
$containerOutput = & docker compose --project-directory $projectDirectory --env-file $environmentFile -f $composeFile ps -q app
if ($LASTEXITCODE -ne 0) {
    throw "读取应用容器失败。"
}
$containerId = ($containerOutput | Out-String).Trim()
if (-not $containerId) {
    throw "未找到正在运行的 app 容器。"
}

$volumeOutput = & docker inspect --format '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Name}}{{end}}{{end}}' $containerId
if ($LASTEXITCODE -ne 0) {
    throw "读取数据卷信息失败。"
}
$volumeName = ($volumeOutput | Out-String).Trim()
if (-not $volumeName) {
    throw "未找到挂载到 /data 的数据卷。"
}

# 首次备份可能需要辅助镜像，先准备好再进入停机窗口。
& docker image inspect alpine:3.22 *> $null
if ($LASTEXITCODE -ne 0) {
    & docker pull alpine:3.22 | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw "准备备份辅助镜像失败。"
    }
}

try {
    & docker compose --project-directory $projectDirectory --env-file $environmentFile -f $composeFile stop app
    if ($LASTEXITCODE -ne 0) {
        throw "停止应用失败。"
    }

    & docker run --rm --read-only --volume "${volumeName}:/source:ro" --volume "${resolvedBackupDirectory}:/backup" alpine:3.22 tar -czf "/backup/$archiveName" -C /source .
    if ($LASTEXITCODE -ne 0) {
        throw "创建备份失败。"
    }

    $archivePath = Join-Path $resolvedBackupDirectory $archiveName
    $hash = (Get-FileHash -Algorithm SHA256 -LiteralPath $archivePath).Hash.ToLowerInvariant()
    [System.IO.File]::WriteAllText("$archivePath.sha256", "$hash  $archiveName`n", [System.Text.UTF8Encoding]::new($false))
    Write-Host "备份已生成：$archivePath"
}
finally {
    & docker compose --project-directory $projectDirectory --env-file $environmentFile -f $composeFile start app | Out-Null
}
