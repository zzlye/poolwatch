param(
    [string]$OutputPath = "web\public\downloads\poolwatch-browser-helper-v1.0.0.zip"
)

$ErrorActionPreference = "Stop"
$workspace = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot "..")).Path
$source = Join-Path $workspace "browser-extension"
$destination = Join-Path $workspace $OutputPath
$destinationDirectory = Split-Path -Parent $destination

# 安装包属于正式页面资源，始终从受版本控制的浏览器助手源码生成。
New-Item -ItemType Directory -Force -Path $destinationDirectory | Out-Null
Compress-Archive -Path (Join-Path $source "*") -DestinationPath $destination -CompressionLevel Optimal -Force
Write-Output $destination
