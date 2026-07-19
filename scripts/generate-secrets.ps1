param(
    [string]$OutputPath = ".env"
)

$ErrorActionPreference = "Stop"

# 已有环境文件可能包含正在使用的加密密钥，禁止直接覆盖。
if (Test-Path -LiteralPath $OutputPath) {
    throw "目标文件已存在，未覆盖：$OutputPath"
}

$encryptionBytes = [byte[]]::new(32)
$tokenBytes = [byte[]]::new(24)
$randomGenerator = [System.Security.Cryptography.RandomNumberGenerator]::Create()
try {
    $randomGenerator.GetBytes($encryptionBytes)
    $randomGenerator.GetBytes($tokenBytes)
}
finally {
    $randomGenerator.Dispose()
}
$encryptionKey = [Convert]::ToBase64String($encryptionBytes)
$setupToken = (($tokenBytes | ForEach-Object { $_.ToString("x2") }) -join "")

$content = @(
    "APP_ENCRYPTION_KEY=$encryptionKey"
    "SETUP_TOKEN=$setupToken"
    "PUBLIC_BASE_URL=https://monitor.example.com"
    "APP_PORT=8080"
    "ALLOW_PRIVATE_TARGETS=false"
    "TZ=Asia/Shanghai"
    "DOMAIN=monitor.example.com"
    "ACME_EMAIL=admin@example.com"
) -join [Environment]::NewLine

$resolvedOutputPath = [System.IO.Path]::GetFullPath($OutputPath)
[System.IO.File]::WriteAllText($resolvedOutputPath, $content, [System.Text.UTF8Encoding]::new($false))
Write-Host "已生成 $OutputPath，请修改域名和邮箱后再启动。"
