# TunnelMesh 一键安装共享模块（Windows / PowerShell 5.1+）。
# 三个角色入口 install-{server,agent,client}.ps1 只声明参数与角色，
# 下载、校验、交互、渲染、服务注册全部集中在这里，语义与 .sh 侧一一对应。
# 本文件不得包含任何真实 token、密码、DSN 或私钥。
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version 2.0

$script:TmRepository = 'nnworld/TunnelMesh'
$script:TmDefaultBaseUrl = 'https://github.com/nnworld/TunnelMesh/releases'
$script:TmLatestApiUrl = 'https://api.github.com/repos/nnworld/TunnelMesh/releases/latest'
$script:TmVersionPattern = '^v[0-9]+\.[0-9]+\.[0-9]+$'
$script:TmWinSwVersion = 'v2.12.0'
# 退出码与 .sh 侧完全一致，排障文档只需要一份。
$script:TmExit = @{ Ok = 0; Usage = 2; Preflight = 3; Download = 4; Checksum = 5; Config = 6; Service = 7; Uninstall = 8 }
$script:TmSecrets = @{}
$script:TmAnswers = @{}
$script:TmTunnels = @()
$script:TmYes = $false
$script:TmTokenFile = ''

function Write-TmInfo { param([string]$Message) Write-Host "==> $Message" }
function Write-TmWarn { param([string]$Message) Write-Warning $Message }
function Stop-Tm {
  param([int]$Code, [string]$Message)
  Write-Error "ERROR: $Message"
  exit $Code
}

function Get-TmMasked {
  param([string]$Value)
  if ([string]::IsNullOrEmpty($Value)) { return '(empty)' }
  if ($Value.Length -le 4) { return '****' }
  return $Value.Substring(0, 4) + '****'
}

function Test-TmAdmin {
  $id = [Security.Principal.WindowsIdentity]::GetCurrent()
  $principal = New-Object Security.Principal.WindowsPrincipal($id)
  return $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

# --- 平台与版本 ---
function Get-TmPlatform {
  $arch = "$env:PROCESSOR_ARCHITECTURE".ToUpperInvariant()
  switch ($arch) {
    'AMD64' { $goarch = 'amd64' }
    'ARM64' { $goarch = 'arm64' }
    default { Stop-Tm $script:TmExit.Usage "unsupported architecture: $env:PROCESSOR_ARCHITECTURE" }
  }
  return @{ GoOs = 'windows'; GoArch = $goarch; ArchiveExt = 'zip' }
}

function Get-TmArchiveName {
  param([string]$Version, [hashtable]$Platform)
  return "tunnelmesh-$Version-$($Platform.GoOs)-$($Platform.GoArch).$($Platform.ArchiveExt)"
}

function Resolve-TmVersion {
  param([string]$Version = 'latest', [string]$BaseUrl = $script:TmDefaultBaseUrl)
  if ($Version -and $Version -ne 'latest') {
    if ($Version -notmatch $script:TmVersionPattern) {
      Stop-Tm $script:TmExit.Usage "invalid version: expected vMAJOR.MINOR.PATCH, got '$Version'"
    }
    return $Version
  }
  $headers = @{ 'User-Agent' = 'tunnelmesh-installer' }
  if ($env:TUNNELMESH_GITHUB_TOKEN) { $headers['Authorization'] = "Bearer $($env:TUNNELMESH_GITHUB_TOKEN)" }
  $tag = ''
  try {
    # 优先走 API：响应体里直接有 tag_name。
    $resp = Invoke-RestMethod -Uri $script:TmLatestApiUrl -Headers $headers -TimeoutSec 30
    if ($resp.tag_name) { $tag = [string]$resp.tag_name }
  } catch { $tag = '' }
  if (-not $tag) {
    try {
      # API 配额耗尽时回退：releases/latest 的 302 Location 里带 tag，不消耗配额。
      $req = [System.Net.HttpWebRequest]::Create("$BaseUrl/latest")
      $req.AllowAutoRedirect = $false
      $req.Timeout = 30000
      $response = $req.GetResponse()
      $location = [string]$response.Headers['Location']
      $response.Close()
      if ($location -match '/tag/(v[0-9][^\s/]*)') { $tag = $Matches[1] }
    } catch { $tag = '' }
  }
  if (-not $tag) { Stop-Tm $script:TmExit.Download 'cannot resolve latest version; pass -Version vX.Y.Z explicitly' }
  Write-TmInfo "resolved latest version: $tag"
  return $tag
}

# --- 下载与校验 ---
function Invoke-TmDownload {
  param([Parameter(Mandatory = $true)][string]$Url, [Parameter(Mandatory = $true)][string]$OutFile)
  $dir = Split-Path -Parent $OutFile
  if ($dir -and -not (Test-Path -LiteralPath $dir)) { New-Item -ItemType Directory -Force -Path $dir | Out-Null }
  try { Invoke-WebRequest -Uri $Url -OutFile $OutFile -UseBasicParsing -TimeoutSec 300 }
  catch { Stop-Tm $script:TmExit.Download "download failed: $Url ($($_.Exception.Message))" }
}

function Get-TmFileSha256 {
  param([Parameter(Mandatory = $true)][string]$File)
  return (Get-FileHash -LiteralPath $File -Algorithm SHA256).Hash.ToLowerInvariant()
}

# Test-TmChecksum 读取发布归档同目录的 SHA256SUMS（格式：sha256 + 两个空格 + 文件名）。
function Test-TmChecksum {
  param([Parameter(Mandatory = $true)][string]$File, [Parameter(Mandatory = $true)][string]$SumsFile,
    [Parameter(Mandatory = $true)][string]$ArchiveName)
  if (-not (Test-Path -LiteralPath $SumsFile)) { Stop-Tm $script:TmExit.Checksum "SHA256SUMS not found: $SumsFile" }
  $expected = Get-TmExpectedSha256 -SumsFile $SumsFile -ArchiveName $ArchiveName
  if (-not $expected) { Stop-Tm $script:TmExit.Checksum "checksum entry not found for $ArchiveName" }
  $actual = Get-TmFileSha256 -File $File
  if ($actual -ne $expected) {
    Stop-Tm $script:TmExit.Checksum "checksum mismatch for $ArchiveName (expected $expected, actual $actual)"
  }
}

function Get-TmExpectedSha256 {
  param([string]$SumsFile, [string]$ArchiveName)
  foreach ($line in (Get-Content -LiteralPath $SumsFile)) {
    $fields = @($line -split '\s+')
    if ($fields.Count -ge 2 -and $fields[1] -eq $ArchiveName) { return $fields[0].ToLowerInvariant() }
  }
  return $null
}

# Test-TmChecksumQuiet 用于缓存命中判定：Stop-Tm 会直接 exit，无法用 try/catch 兜住，
# 因此缓存路径必须用这个返回布尔值的版本，损坏时删掉缓存重新下载。
function Test-TmChecksumQuiet {
  param([string]$File, [string]$SumsFile, [string]$ArchiveName)
  try {
    if (-not (Test-Path -LiteralPath $SumsFile) -or -not (Test-Path -LiteralPath $File)) { return $false }
    $expected = Get-TmExpectedSha256 -SumsFile $SumsFile -ArchiveName $ArchiveName
    if (-not $expected) { return $false }
    return ((Get-TmFileSha256 -File $File) -eq $expected)
  } catch { return $false }
}

function Get-TmCacheDir {
  $root = $env:LOCALAPPDATA
  if (-not $root) { $root = $env:USERPROFILE }
  return (Join-Path $root 'TunnelMesh\releases')
}

function Get-TmCachePath { param([string]$Name) return (Join-Path (Get-TmCacheDir) $Name) }

# --- 交互原语 ---
function Read-TmAnswer {
  param([string]$Key, [string]$Prompt, [string]$Default = '')
  if ($script:TmAnswers.ContainsKey($Key) -and $script:TmAnswers[$Key]) { return $script:TmAnswers[$Key] }
  if ($script:TmYes) { $script:TmAnswers[$Key] = $Default; return $Default }
  $suffix = if ($Default) { " [$Default]" } else { '' }
  $reply = Read-Host "==> $Prompt$suffix"
  if ([string]::IsNullOrWhiteSpace($reply)) { $reply = $Default }
  $script:TmAnswers[$Key] = $reply
  return $reply
}

function Read-TmBool {
  param([string]$Key, [string]$Prompt, [string]$Default = 'no')
  $current = Read-TmAnswer -Key $Key -Prompt "$Prompt (y/n)" -Default $Default
  switch ("$current".ToLowerInvariant()) {
    'y' { return 'yes' }
    'yes' { return 'yes' }
    'true' { return 'yes' }
    '1' { return 'yes' }
    default { return 'no' }
  }
}

function Read-TmChoice {
  param([string]$Key, [string]$Prompt, [string]$Default, [string[]]$Choices)
  for ($attempt = 1; $attempt -le 3; $attempt++) {
    $reply = Read-TmAnswer -Key $Key -Prompt "$Prompt ($($Choices -join ', '))" -Default $Default
    if ($Choices -contains $reply) { return $reply }
    Write-TmWarn "不在可选值内：$($Choices -join ', ')"
    $script:TmAnswers[$Key] = ''
  }
  Stop-Tm $script:TmExit.Usage 'invalid choice after 3 attempts'
}

# Read-TmSecret 用 -AsSecureString 关闭回显；-Yes 下不读，缺值直接失败而不是静默装出错误配置。
function Read-TmSecret {
  param([string]$Prompt)
  if ($script:TmYes) {
    Stop-Tm $script:TmExit.Preflight "missing required secret for '$Prompt': set the environment variable, pass -TokenFile/-SecretEnvFile, or drop -Yes"
  }
  $secure = Read-Host "==> $Prompt（输入不回显）" -AsSecureString
  $ptr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secure)
  try { return [Runtime.InteropServices.Marshal]::PtrToStringBSTR($ptr) }
  finally { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($ptr) }
}

function Read-TmSecretEnvFile {
  param([string]$Path)
  if (-not (Test-Path -LiteralPath $Path)) { Stop-Tm $script:TmExit.Usage "secret env file not found: $Path" }
  foreach ($line in (Get-Content -LiteralPath $Path)) {
    $trimmed = $line.Trim()
    if (-not $trimmed -or $trimmed.StartsWith('#')) { continue }
    $idx = $trimmed.IndexOf('=')
    if ($idx -lt 1) { continue }
    $key = $trimmed.Substring(0, $idx).Trim()
    $value = $trimmed.Substring($idx + 1).Trim()
    foreach ($quote in @('"', "'")) {
      if ($value.Length -ge 2 -and $value.StartsWith($quote) -and $value.EndsWith($quote)) {
        $value = $value.Substring(1, $value.Length - 2); break
      }
    }
    $script:TmSecrets[$key] = $value
  }
}

# 取值优先级与 .sh 侧一致：-SecretEnvFile > -TokenFile（仅 token） > 环境变量 > 交互。
function Get-TmSecretValue {
  param([string]$Key, [string]$Prompt, [string]$EnvName)
  $value = ''
  if ($script:TmSecrets.ContainsKey($EnvName)) { $value = [string]$script:TmSecrets[$EnvName] }
  if (-not $value -and $script:TmTokenFile -and $Key -like '*token*') {
    if (-not (Test-Path -LiteralPath $script:TmTokenFile)) { Stop-Tm $script:TmExit.Usage "token file not found: $($script:TmTokenFile)" }
    $value = [string](Get-Content -LiteralPath $script:TmTokenFile -TotalCount 1)
  }
  if (-not $value) {
    $item = Get-Item -Path "Env:$EnvName" -ErrorAction SilentlyContinue
    if ($item) { $value = [string]$item.Value }
  }
  if (-not $value) { $value = Read-TmSecret -Prompt $Prompt }
  if (-not $value) { Stop-Tm $script:TmExit.Preflight "missing required secret for '$Key'" }
  $script:TmAnswers[$Key] = $value
  $script:TmSecrets[$EnvName] = $value
  return $value
}

# --- 渲染 ---
function Convert-TmXml {
  param([string]$Value)
  if ($null -eq $Value) { return '' }
  return $Value.Replace('&', '&amp;').Replace('<', '&lt;').Replace('>', '&gt;').Replace('"', '&quot;')
}

function Convert-TmYamlQuote {
  param([string]$Value)
  if ($null -eq $Value) { return "''" }
  return "'" + $Value.Replace("'", "''") + "'"
}

# Write-TmEnvBlock 把敏感值渲染成 WinSW 的 <env> 元素：WinSW 没有 EnvironmentFile 机制，
# 服务进程的环境变量只能写进服务 XML，因此渲染后必须用 Protect-TmSecretFile 收紧 ACL。
function Write-TmEnvBlock {
  $sb = New-Object System.Text.StringBuilder
  foreach ($key in ($script:TmSecrets.Keys | Sort-Object)) {
    [void]$sb.AppendLine(('  <env name="{0}" value="{1}" />' -f (Convert-TmXml $key), (Convert-TmXml $script:TmSecrets[$key])))
  }
  return $sb.ToString()
}

function Write-TmYamlLine {
  param([System.Text.StringBuilder]$Sb, [string]$Indent, [string]$Key, [string]$Value)
  if ([string]::IsNullOrEmpty($Value)) { return }
  [void]$Sb.AppendLine("$Indent$Key`: $(Convert-TmYamlQuote $Value)")
}

function Write-TmYamlBool {
  param([System.Text.StringBuilder]$Sb, [string]$Indent, [string]$Key, [string]$Value)
  if ($Value -eq 'yes') { [void]$Sb.AppendLine("$Indent$Key`: true") }
  elseif ($Value -eq 'no') { [void]$Sb.AppendLine("$Indent$Key`: false") }
}

# Write-TmYaml 渲染角色配置；键名以 internal/config/config.go 的 yaml tag 为唯一权威来源，
# 与 .sh 侧的 tm_render_<role>_yaml 保持一致。敏感值一律不进 YAML。
function Write-TmYaml {
  param([string]$Role)
  $sb = New-Object System.Text.StringBuilder
  $a = $script:TmAnswers
  function Get-A { param([string]$Key) if ($a.ContainsKey($Key)) { return [string]$a[$Key] } else { return '' } }
  switch ($Role) {
    'server' {
      [void]$sb.AppendLine('# 由 TunnelMesh 一键安装脚本生成。敏感值渲染进服务 XML 的 <env> 块，不在本文件。')
      [void]$sb.AppendLine("mode: $(Convert-TmYamlQuote (Get-A 'mode'))")
      [void]$sb.AppendLine('')
      [void]$sb.AppendLine('storage:')
      Write-TmYamlLine $sb '  ' 'driver' (Get-A 'storage_driver')
      Write-TmYamlBool $sb '  ' 'auto_init' (Get-A 'auto_init')
      if ((Get-A 'storage_driver') -eq 'sqlite') {
        [void]$sb.AppendLine('  sqlite:')
        Write-TmYamlLine $sb '    ' 'path' (Get-A 'sqlite_path')
      } else {
        [void]$sb.AppendLine('  mysql:')
        Write-TmYamlBool $sb '    ' 'tls' (Get-A 'mysql_tls')
        [void]$sb.AppendLine('    # dsn 由 TUNNELMESH_STORAGE_MYSQL_DSN 注入，不要写进本文件。')
      }
      [void]$sb.AppendLine('')
      [void]$sb.AppendLine('registry:')
      Write-TmYamlLine $sb '  ' 'type' (Get-A 'registry_type')
      [void]$sb.AppendLine('')
      [void]$sb.AppendLine('server:')
      Write-TmYamlLine $sb '  ' 'http_addr' (Get-A 'http_addr')
      Write-TmYamlLine $sb '  ' 'dynamic_suffix' (Get-A 'dynamic_suffix')
      [void]$sb.AppendLine('  tcp_bridge:')
      [void]$sb.AppendLine('    enabled: true')
      [void]$sb.AppendLine('  webssh:')
      Write-TmYamlBool $sb '    ' 'enabled' (Get-A 'webssh_enabled')
      if ((Get-A 'proxy_entry_enabled') -eq 'yes') {
        [void]$sb.AppendLine('  proxy_entry:')
        [void]$sb.AppendLine('    enabled: true')
        Write-TmYamlLine $sb '    ' 'listen' (Get-A 'proxy_entry_listen')
        Write-TmYamlLine $sb '    ' 'domain_suffix' (Get-A 'proxy_entry_domain_suffix')
      }
      if ((Get-A 'relay_enabled') -eq 'yes') {
        [void]$sb.AppendLine('  relay:')
        [void]$sb.AppendLine('    enabled: true')
        Write-TmYamlLine $sb '    ' 'listen' (Get-A 'relay_listen')
        Write-TmYamlLine $sb '    ' 'endpoint' (Get-A 'relay_endpoint')
        Write-TmYamlLine $sb '    ' 'ca' (Get-A 'relay_ca')
        Write-TmYamlLine $sb '    ' 'cert' (Get-A 'relay_cert')
        Write-TmYamlLine $sb '    ' 'key' (Get-A 'relay_key')
        Write-TmYamlLine $sb '    ' 'server_name' (Get-A 'relay_server_name')
        [void]$sb.AppendLine('    # node_token 由 TUNNELMESH_SERVER_RELAY_NODE_TOKEN 注入。')
      }
      [void]$sb.AppendLine('')
      [void]$sb.AppendLine('tls:')
      Write-TmYamlBool $sb '  ' 'enabled' (Get-A 'tls_enabled')
      [void]$sb.AppendLine('')
      [void]$sb.AppendLine('downloads:')
      Write-TmYamlLine $sb '  ' 'github_repository' $script:TmRepository
    }
    'agent' {
      [void]$sb.AppendLine('# 由 TunnelMesh 一键安装脚本生成。agent.token 的 YAML 键不可序列化，')
      [void]$sb.AppendLine('# 只能通过 TUNNELMESH_AGENT_TOKEN 注入（渲染进服务 XML 的 <env> 块）。')
      [void]$sb.AppendLine('mode: local')
      [void]$sb.AppendLine('')
      [void]$sb.AppendLine('agent:')
      Write-TmYamlLine $sb '  ' 'server_url' (Get-A 'server_url')
      Write-TmYamlLine $sb '  ' 'id' (Get-A 'agent_id')
      Write-TmYamlLine $sb '  ' 'instance_id' (Get-A 'instance_id')
      if ((Get-A 'conn_min') -or (Get-A 'conn_max')) {
        [void]$sb.AppendLine('  connections:')
        Write-TmYamlLine $sb '    ' 'min' (Get-A 'conn_min')
        Write-TmYamlLine $sb '    ' 'max' (Get-A 'conn_max')
      }
      if (Get-A 'metadata_name') {
        [void]$sb.AppendLine('  metadata:')
        [void]$sb.AppendLine("    - name: $(Convert-TmYamlQuote (Get-A 'metadata_name'))")
        [void]$sb.AppendLine("      source: $(Convert-TmYamlQuote (Get-A 'metadata_source'))")
        Write-TmYamlLine $sb '      ' 'path' (Get-A 'metadata_path')
        Write-TmYamlLine $sb '      ' 'key' (Get-A 'metadata_key')
      }
    }
    'client' {
      [void]$sb.AppendLine('# 由 TunnelMesh 一键安装脚本生成。client.token 只能通过 TUNNELMESH_CLIENT_TOKEN 注入。')
      [void]$sb.AppendLine('mode: local')
      [void]$sb.AppendLine('')
      [void]$sb.AppendLine('client:')
      Write-TmYamlLine $sb '  ' 'server_url' (Get-A 'server_url')
      Write-TmYamlLine $sb '  ' 'instance_id_path' (Get-A 'instance_id_path')
      if ($script:TmTunnels.Count -gt 0) {
        [void]$sb.AppendLine('  tunnels:')
        foreach ($t in $script:TmTunnels) {
          [void]$sb.AppendLine("    - name: $(Convert-TmYamlQuote $t.Name)")
          [void]$sb.AppendLine("      protocol: $(Convert-TmYamlQuote $t.Protocol)")
          [void]$sb.AppendLine("      listen: $(Convert-TmYamlQuote $t.Listen)")
          [void]$sb.AppendLine("      agent_id: $(Convert-TmYamlQuote $t.AgentId)")
          if ($t.TargetHost) {
            [void]$sb.AppendLine("      target_host: $(Convert-TmYamlQuote $t.TargetHost)")
            [void]$sb.AppendLine("      target_port: $($t.TargetPort)")
          }
          if ($t.AuthMode) {
            [void]$sb.AppendLine("      auth_mode: $(Convert-TmYamlQuote $t.AuthMode)")
            if ($t.AllowRemote -eq 'yes') { [void]$sb.AppendLine('      allow_remote: true') }
            else { [void]$sb.AppendLine('      allow_remote: false') }
          }
        }
      }
    }
    default { Stop-Tm $script:TmExit.Usage "unknown role: $Role" }
  }
  return $sb.ToString()
}

# Convert-TmTunnelSpec：与 .sh 侧 tm_parse_tunnel_spec 同构。
# listen 自身含冒号，因此按位组装而不是按冒号直读六个字段。
function Convert-TmTunnelSpec {
  param([string]$Spec)
  $parts = @($Spec -split ':')
  if ($parts.Count -lt 5) {
    Stop-Tm $script:TmExit.Usage "tunnel spec needs name:protocol:listen_host:listen_port:agent_id[:target_host:target_port]: '$Spec'"
  }
  $protocol = $parts[1]
  if (@('tcp', 'udp', 'http', 'socks5') -notcontains $protocol) {
    Stop-Tm $script:TmExit.Usage "unknown protocol: '$protocol' (tcp|udp|http|socks5)"
  }
  $listen = "$($parts[2]):$($parts[3])"
  if ($parts[3] -notmatch '^[0-9]+$' -or [int]$parts[3] -lt 1 -or [int]$parts[3] -gt 65535) {
    Stop-Tm $script:TmExit.Usage "listen port out of range 1-65535: '$($parts[3])'"
  }
  $targetHost = if ($parts.Count -gt 5) { $parts[5] } else { '' }
  $targetPort = if ($parts.Count -gt 6) { $parts[6] } else { '' }
  if ($protocol -eq 'socks5') {
    $targetHost = ''; $targetPort = ''
  } elseif (-not $targetHost -or -not $targetPort) {
    Stop-Tm $script:TmExit.Usage "protocol $protocol requires target_host and target_port"
  } elseif ($targetPort -notmatch '^[0-9]+$' -or [int]$targetPort -lt 1 -or [int]$targetPort -gt 65535) {
    Stop-Tm $script:TmExit.Usage "target port out of range 1-65535: '$targetPort'"
  }
  return [pscustomobject]@{
    Name = $parts[0]; Protocol = $protocol; Listen = $listen; AgentId = $parts[4]
    TargetHost = $targetHost; TargetPort = $targetPort; AuthMode = ''; AllowRemote = 'no'
  }
}

# --- WinSW：只信任 winsw-checksums.txt 中登记过校验和的版本 ---
function Get-TmWinSW {
  param([string]$WinSWPath, [string]$Version = $script:TmWinSwVersion, [string]$Arch = 'amd64')
  if ($WinSWPath) {
    if (-not (Test-Path -LiteralPath $WinSWPath)) { Stop-Tm $script:TmExit.Usage "WinSW not found: $WinSWPath" }
    return (Resolve-Path -LiteralPath $WinSWPath).Path
  }
  $table = Join-Path $PSScriptRoot 'winsw-checksums.txt'
  $row = $null
  if (Test-Path -LiteralPath $table) {
    $row = Get-Content -LiteralPath $table |
      Where-Object { $_ -notmatch '^\s*(#|$)' } |
      ForEach-Object { , @($_ -split '\s+') } |
      Where-Object { $_[0] -eq $Version -and $_[1] -eq $Arch } |
      Select-Object -First 1
  }
  if (-not $row) {
    Stop-Tm $script:TmExit.Download @"
WinSW $Version ($Arch) 未登记在 winsw-checksums.txt 中，拒绝自动下载。
请手工下载并用 -WinSW 指定路径：
  https://github.com/winsw/winsw/releases/download/$Version/WinSW-x64.exe
或按 winsw-checksums.txt 头部的补录流程登记校验和后重试。
"@
  }
  $dest = Get-TmCachePath "WinSW-$Version-$Arch.exe"
  if (-not (Test-Path -LiteralPath $dest)) {
    Invoke-TmDownload -Url "https://github.com/winsw/winsw/releases/download/$Version/$($row[3])" -OutFile $dest
  }
  $actual = Get-TmFileSha256 -File $dest
  if ($actual -ne $row[2].ToLowerInvariant()) {
    Remove-Item -Force -LiteralPath $dest
    Stop-Tm $script:TmExit.Checksum "WinSW checksum mismatch (expected $($row[2]), actual $actual)"
  }
  return $dest
}

# --- 服务生命周期 ---
# 命名沿用既有 deploy\install\windows-install.ps1 的约定：WinSW 包装器叫
# tunnelmesh-<role>-service.exe/.xml，业务二进制叫 tunnelmesh-<role>.exe，两者不能同名。
function Get-TmServiceId { param([string]$Role) return "tunnelmesh-$Role" }
function Get-TmWrapperPath { param([string]$InstallDir, [string]$Role) return (Join-Path $InstallDir "tunnelmesh-$Role-service.exe") }
function Get-TmServiceXmlPath { param([string]$InstallDir, [string]$Role) return (Join-Path $InstallDir "tunnelmesh-$Role-service.xml") }

# Install-TmService 渲染服务 XML 并注册。$EnvBlock 是 Write-TmEnvBlock 的输出，
# 含敏感值，因此渲染完立刻收紧 ACL（只留 Administrators 与 SYSTEM）。
# $Template 由调用方给出（一键安装用刚解压的 Release 归档里的那份）；
# 为空时才回退到共享模块旁边的仓库相对路径，只在仓库内直接跑脚本时成立。
function Install-TmService {
  param([string]$Role, [string]$InstallDir, [string]$Binary, [string]$Config, [string]$EnvBlock, [string]$WinSWExe, [string]$Template)
  $template = $Template
  if (-not $template) { $template = Join-Path $PSScriptRoot '..\..\windows\tunnelmesh-service.xml' }
  if (-not (Test-Path -LiteralPath $template)) {
    Stop-Tm $script:TmExit.Service "release archive is missing ..\windows\tunnelmesh-service.xml (looked in $template)"
  }
  $roleTitle = $Role.Substring(0, 1).ToUpperInvariant() + $Role.Substring(1)
  $rendered = (Get-Content -Raw -LiteralPath $template)
  $rendered = $rendered.Replace('__ROLE_TITLE__', $roleTitle)
  $rendered = $rendered.Replace('__ROLE__', $Role)
  $rendered = $rendered.Replace('__BINARY__', (Split-Path -Leaf $Binary))
  $rendered = $rendered.Replace('__CONFIG__', (Convert-TmXml $Config))
  $rendered = $rendered.Replace('__INSTALL_DIR__', $InstallDir)
  # __ENV_BLOCK__ 用 String.Replace 字面替换：-replace 是正则，XML 里的 & 与路径里的 \ 会被吃掉。
  $rendered = $rendered.Replace('__ENV_BLOCK__', $EnvBlock.TrimEnd())
  $xml = Get-TmServiceXmlPath -InstallDir $InstallDir -Role $Role
  Write-TmFile -Path $xml -Content $rendered -Backup $true
  if ($EnvBlock) { Protect-TmSecretFile -Path $xml }
  $wrapper = Get-TmWrapperPath -InstallDir $InstallDir -Role $Role
  Copy-Item -Force -LiteralPath $WinSWExe -Destination $wrapper
  & $wrapper stop 2>$null | Out-Null
  & $wrapper uninstall 2>$null | Out-Null
  & $wrapper install
  if ($LASTEXITCODE -ne 0) { Stop-Tm $script:TmExit.Service "WinSW install failed for $(Get-TmServiceId -Role $Role)" }
  Write-TmInfo "registered service $(Get-TmServiceId -Role $Role)"
}

function Start-TmService {
  param([string]$InstallDir, [string]$Role)
  $wrapper = Get-TmWrapperPath -InstallDir $InstallDir -Role $Role
  & $wrapper start
  if ($LASTEXITCODE -ne 0) { Stop-Tm $script:TmExit.Service "WinSW start failed for $(Get-TmServiceId -Role $Role)" }
}

function Uninstall-TmRole {
  param([string]$Role, [string]$InstallDir, [string]$ConfigDir)
  $wrapper = Get-TmWrapperPath -InstallDir $InstallDir -Role $Role
  if (Test-Path -LiteralPath $wrapper) {
    & $wrapper stop 2>$null | Out-Null
    & $wrapper uninstall 2>$null | Out-Null
  }
  Remove-Item -Force -ErrorAction SilentlyContinue -LiteralPath $wrapper
  Remove-Item -Force -ErrorAction SilentlyContinue -LiteralPath (Get-TmServiceXmlPath -InstallDir $InstallDir -Role $Role)
  Remove-Item -Force -ErrorAction SilentlyContinue -LiteralPath (Join-Path $InstallDir "tunnelmesh-$Role.exe")
  Get-ChildItem -Path $InstallDir -Filter "tunnelmesh-$Role.exe.bak-*" -ErrorAction SilentlyContinue | Remove-Item -Force
  # 卸载只删二进制与服务定义；配置、密钥与日志保留，方便重装后继续用。
  Write-TmInfo "已卸载 tunnelmesh-$Role 的二进制与服务定义；配置与数据保留："
  Write-Host "  $(Join-Path $ConfigDir "$Role.yaml")"
  Write-Host "  $(Join-Path $InstallDir 'logs')"
}

# --- 落盘：先备份再覆盖，最多保留最近 3 份（与 .sh 侧一致）---
function Write-TmFile {
  param([string]$Path, [string]$Content, [bool]$Backup = $false)
  $dir = Split-Path -Parent $Path
  if ($dir -and -not (Test-Path -LiteralPath $dir)) { New-Item -ItemType Directory -Force -Path $dir | Out-Null }
  if ($Backup -and (Test-Path -LiteralPath $Path)) {
    $stamp = (Get-Date).ToUniversalTime().ToString('yyyyMMddTHHmmssZ')
    Copy-Item -LiteralPath $Path -Destination "$Path.bak-$stamp" -Force
    $old = @(Get-ChildItem -Path "$Path.bak-*" -ErrorAction SilentlyContinue | Sort-Object LastWriteTime -Descending)
    if ($old.Count -gt 3) { $old | Select-Object -Skip 3 | Remove-Item -Force }
  }
  [System.IO.File]::WriteAllText($Path, $Content, (New-Object System.Text.UTF8Encoding($false)))
}

function Protect-TmSecretFile {
  param([string]$Path)
  # 服务 XML 里含 <env> 形式的敏感值：去掉继承，只留 Administrators 与 SYSTEM。
  $acl = New-Object System.Security.AccessControl.FileSecurity
  $acl.SetAccessRuleProtection($true, $false)
  foreach ($who in @('BUILTIN\Administrators', 'NT AUTHORITY\SYSTEM')) {
    $acl.AddAccessRule((New-Object System.Security.AccessControl.FileSystemAccessRule($who, 'FullControl', 'Allow')))
  }
  Set-Acl -LiteralPath $Path -AclObject $acl
}

# --- 校验与密钥 ---
function Test-TmWsUrl {
  param([string]$Url, [string]$Suffix)
  if ($Url -notmatch '^wss?://\S+$') { Stop-Tm $script:TmExit.Usage "server URL must start with ws:// or wss://: '$Url'" }
  if (-not $Url.EndsWith($Suffix)) { Stop-Tm $script:TmExit.Usage "server URL must end with $Suffix`: '$Url'" }
}

# 名称规则与 internal/metadata 的 allowlist 一致：命中敏感词时服务端会清空值并标记 redacted，
# 因此在安装阶段就拒绝，避免用户装完才发现 metadata 一直是空的。
function Test-TmMetadataName {
  param([string]$Name)
  if ($Name -notmatch '^[A-Za-z0-9._-]+$') {
    Stop-Tm $script:TmExit.Usage "metadata 名称只允许字母、数字、点、下划线和短横线：'$Name'"
  }
  if ($Name -match '(?i)passw|passphrase|token|secret|private.?key|api.?key|credential|authorization|cookie|dsn') {
    Stop-Tm $script:TmExit.Usage "metadata 名称命中敏感词，服务端会拒收并标记 redacted：'$Name'"
  }
}

function New-TmSecretKey {
  $bytes = New-Object 'byte[]' 32
  $rng = [Security.Cryptography.RandomNumberGenerator]::Create()
  try { $rng.GetBytes($bytes) } finally { $rng.Dispose() }
  return [Convert]::ToBase64String($bytes)
}

# Test-TmListen 校验本地监听地址；非 loopback 必须显式同意并配认证（config 侧同样强制）。
# 返回 @{ AuthMode = ...; AllowRemote = 'yes'|'no' }，由调用方写进对应的那条转发。
function Test-TmListen {
  param([string]$Listen)
  if ($Listen -notmatch '^[^:]+:[0-9]+$') { Stop-Tm $script:TmExit.Usage "listen 必须是 host:port，收到 '$Listen'" }
  $port = [int]($Listen -split ':')[-1]
  if ($port -lt 1 -or $port -gt 65535) { Stop-Tm $script:TmExit.Usage "listen port out of range 1-65535: '$Listen'" }
  $listenHost = $Listen.Substring(0, $Listen.LastIndexOf(':'))
  if ($listenHost -like '127.*' -or $listenHost -eq '::1' -or $listenHost -eq 'localhost') {
    return @{ AuthMode = ''; AllowRemote = 'no' }
  }
  Write-TmWarn "监听地址 $Listen 不是 loopback：必须同时开启 allow_remote 并配置认证"
  # 每条转发都要重新问：Read-Tm* 对已有答案会直接返回，不清空就会沿用上一条的选择。
  $script:TmAnswers['allow_remote'] = ''
  $script:TmAnswers['auth_mode'] = ''
  $allow = if ($env:TUNNELMESH_ALLOW_REMOTE -eq '1') { 'yes' }
  else { Read-TmBool -Key 'allow_remote' -Prompt '允许非 loopback 监听' -Default 'no' }
  if ($allow -ne 'yes') { Stop-Tm $script:TmExit.Usage '已取消：非 loopback 监听需要显式同意' }
  $authMode = Read-TmChoice -Key 'auth_mode' -Prompt '认证方式（socks5 只支持 none|password）' -Default 'password' -Choices @('password', 'none')
  if ($authMode -eq 'none') { Stop-Tm $script:TmExit.Usage '非 loopback 监听不允许 auth_mode=none' }
  return @{ AuthMode = $authMode; AllowRemote = 'yes' }
}

function Get-TmBound {
  param([hashtable]$Bound, [string]$Key, $Default = $null)
  if ($Bound -and $Bound.ContainsKey($Key)) { return $Bound[$Key] }
  return $Default
}

# --- 角色问答 ---
function Invoke-TmRolePrompts {
  param([string]$Role, [string]$StateDir)
  switch ($Role) {
    'server' {
      $null = Read-TmChoice -Key 'mode' -Prompt '运行模式' -Default 'local' -Choices @('local', 'cluster')
      $null = Read-TmAnswer -Key 'http_addr' -Prompt 'HTTP 监听地址' -Default '127.0.0.1:8080'
      $null = Read-TmAnswer -Key 'dynamic_suffix' -Prompt '动态托管域名后缀' -Default 'apps.example.com'
      if ($script:TmAnswers['mode'] -eq 'cluster') {
        $script:TmAnswers['storage_driver'] = 'mysql'
        $null = Get-TmSecretValue -Key 'mysql_dsn' -Prompt 'MySQL DSN（含账号密码）' -EnvName 'TUNNELMESH_STORAGE_MYSQL_DSN'
        $null = Read-TmBool -Key 'mysql_tls' -Prompt 'MySQL 启用 TLS' -Default 'no'
        $null = Read-TmChoice -Key 'registry_type' -Prompt '注册发现' -Default 'database' -Choices @('database', 'etcd')
        $null = Read-TmBool -Key 'relay_enabled' -Prompt '启用 Server 节点间 relay' -Default 'no'
        if ($script:TmAnswers['relay_enabled'] -eq 'yes') {
          $null = Read-TmAnswer -Key 'relay_listen' -Prompt 'relay 监听地址' -Default '0.0.0.0:9443'
          $null = Read-TmAnswer -Key 'relay_endpoint' -Prompt 'relay 对外 endpoint（留空自动推导）' -Default ''
          $null = Read-TmAnswer -Key 'relay_ca' -Prompt 'relay CA 证书路径（留空为明文 relay）' -Default ''
          $null = Read-TmAnswer -Key 'relay_cert' -Prompt 'relay 证书路径' -Default ''
          $null = Read-TmAnswer -Key 'relay_key' -Prompt 'relay 私钥路径' -Default ''
          $null = Read-TmAnswer -Key 'relay_server_name' -Prompt 'relay TLS ServerName' -Default ''
          $null = Get-TmSecretValue -Key 'relay_node_token' -Prompt 'server-node relay token' -EnvName 'TUNNELMESH_SERVER_RELAY_NODE_TOKEN'
        }
      } else {
        $script:TmAnswers['storage_driver'] = 'sqlite'
        $null = Read-TmAnswer -Key 'sqlite_path' -Prompt 'SQLite 数据库路径' -Default (Join-Path $StateDir 'tunnelmesh.db')
        $script:TmAnswers['registry_type'] = 'database'
        $script:TmAnswers['relay_enabled'] = 'no'
      }
      $null = Read-TmBool -Key 'auto_init' -Prompt '自动建表/执行增量迁移' -Default 'yes'
      $null = Read-TmBool -Key 'webssh_enabled' -Prompt '启用浏览器 WebSSH/SFTP' -Default 'yes'
      $null = Read-TmBool -Key 'proxy_entry_enabled' -Prompt '启用 tp-* HTTP 代理入口' -Default 'no'
      if ($script:TmAnswers['proxy_entry_enabled'] -eq 'yes') {
        $null = Read-TmAnswer -Key 'proxy_entry_listen' -Prompt '代理入口内部监听地址' -Default '127.0.0.1:8089'
        $null = Read-TmAnswer -Key 'proxy_entry_domain_suffix' -Prompt '代理入口域名后缀' -Default ''
      }
      $null = Read-TmBool -Key 'tls_enabled' -Prompt '由本进程终止 TLS（否则交给 Nginx）' -Default 'no'
      if ((Read-TmBool -Key 'gen_identity_key' -Prompt '自动生成身份主密钥（SSO/MFA 与 token reveal 必需）' -Default 'yes') -eq 'yes') {
        $script:TmSecrets['TUNNELMESH_TOKEN_ENCRYPTION_KEY'] = New-TmSecretKey
        if ($script:TmAnswers['mode'] -eq 'cluster') {
          $script:TmSecrets['TUNNELMESH_TRACE_SIGNING_KEY'] = New-TmSecretKey
          $script:TmSecrets['TUNNELMESH_TOKEN_ENCRYPTION_KEY_ID'] = 'default'
        }
      }
      $null = Read-TmBool -Key 'bootstrap_admin' -Prompt '安装后立即创建首个管理员' -Default 'yes'
    }
    'agent' {
      $null = Read-TmAnswer -Key 'server_url' -Prompt 'Server WebSocket URL' -Default ''
      if (-not $script:TmAnswers['server_url']) {
        Stop-Tm $script:TmExit.Preflight '缺少 Server URL：交互输入，或 -ServerUrl'
      }
      Test-TmWsUrl -Url $script:TmAnswers['server_url'] -Suffix '/ws/agent'
      if (-not $script:TmAnswers.ContainsKey('agent_id') -or -not $script:TmAnswers['agent_id']) {
        $script:TmAnswers['agent_id'] = ($env:COMPUTERNAME -replace '[^A-Za-z0-9._-]', '-').ToLowerInvariant()
      }
      $null = Read-TmAnswer -Key 'agent_id' -Prompt 'Agent ID' -Default $script:TmAnswers['agent_id']
      $null = Get-TmSecretValue -Key 'agent_token' -Prompt 'Agent service token' -EnvName 'TUNNELMESH_AGENT_TOKEN'
      $null = Read-TmAnswer -Key 'conn_min' -Prompt '连接池最小连接数' -Default '1'
      $null = Read-TmAnswer -Key 'conn_max' -Prompt '连接池最大连接数' -Default '1'
      $null = Read-TmAnswer -Key 'instance_id' -Prompt 'instance_id（留空自动生成并持久化）' -Default ''
      if ((Read-TmBool -Key 'report_metadata' -Prompt '上报宿主机 metadata（来自 allowlist 的文件/环境变量）' -Default 'no') -eq 'yes') {
        $null = Read-TmAnswer -Key 'metadata_name' -Prompt 'metadata 名称' -Default 'device_id'
        Test-TmMetadataName -Name $script:TmAnswers['metadata_name']
        $null = Read-TmChoice -Key 'metadata_source' -Prompt 'metadata 来源' -Default 'env' -Choices @('env', 'file')
        $null = Read-TmAnswer -Key 'metadata_path' -Prompt '文件路径（source=file 时）' -Default ''
        $null = Read-TmAnswer -Key 'metadata_key' -Prompt '环境变量名（source=env 时）' -Default ''
      }
    }
    'client' {
      $null = Read-TmAnswer -Key 'server_url' -Prompt 'Server WebSocket URL' -Default ''
      if (-not $script:TmAnswers['server_url']) { Stop-Tm $script:TmExit.Preflight '缺少 Server URL：交互输入，或 -ServerUrl' }
      Test-TmWsUrl -Url $script:TmAnswers['server_url'] -Suffix '/ws/client'
      $null = Get-TmSecretValue -Key 'client_token' -Prompt 'Client service token' -EnvName 'TUNNELMESH_CLIENT_TOKEN'
      $null = Read-TmAnswer -Key 'instance_id_path' -Prompt 'instance_id 持久化路径' -Default (Join-Path $env:ProgramData 'TunnelMesh\client-instance-id')
      if ($script:TmTunnels.Count -gt 0) { return }
      if ((Read-TmBool -Key 'add_tunnel' -Prompt '现在添加本地转发（也可安装后用 -Tunnel 或直接编辑配置）' -Default 'no') -ne 'yes') { return }
      $index = 1
      while ($true) {
        # 每条转发用带序号的独立 key：Read-TmAnswer 对已有答案会直接返回，
        # 复用同一个 key 会让第二条之后全部拿到旧答案，循环永远问不出新内容。
        $nameKey = 't_name_' + $index
        $protoKey = 't_protocol_' + $index
        $listenKey = 't_listen_' + $index
        $agentKey = 't_agent_' + $index
        $null = Read-TmAnswer -Key $nameKey -Prompt '转发名称' -Default ('tunnel-' + $index)
        $null = Read-TmChoice -Key $protoKey -Prompt '协议' -Default 'tcp' -Choices @('tcp', 'udp', 'http', 'socks5')
        $null = Read-TmAnswer -Key $listenKey -Prompt '本地监听地址（host:port）' -Default ('127.0.0.1:' + (15432 + $index - 1))
        $null = Read-TmAnswer -Key $agentKey -Prompt '目标 Agent ID' -Default ''
        $tName = [string]$script:TmAnswers[$nameKey]
        $tProtocol = [string]$script:TmAnswers[$protoKey]
        $tListen = [string]$script:TmAnswers[$listenKey]
        $tAgent = [string]$script:TmAnswers[$agentKey]
        if (-not $tAgent) { Stop-Tm $script:TmExit.Usage '目标 Agent ID 不能为空' }
        $targetHost = ''
        $targetPort = ''
        if ($tProtocol -ne 'socks5') {
          $null = Read-TmAnswer -Key ('t_host_' + $index) -Prompt '目标 host' -Default ''
          $null = Read-TmAnswer -Key ('t_port_' + $index) -Prompt '目标 port' -Default ''
          $targetHost = [string]$script:TmAnswers['t_host_' + $index]
          $targetPort = [string]$script:TmAnswers['t_port_' + $index]
          if (-not $targetHost -or -not $targetPort) {
            Stop-Tm $script:TmExit.Usage 'tcp/udp/http 转发必须给出目标 host 与 port（socks5 才可以留空）'
          }
        }
        $auth = Test-TmListen -Listen $tListen
        # listen 自身含冒号，因此拆成 host 与 port 两段再交给 Convert-TmTunnelSpec 按位组装。
        $listenHost = $tListen.Substring(0, $tListen.LastIndexOf(':'))
        $listenPort = $tListen.Substring($tListen.LastIndexOf(':') + 1)
        $spec = ($tName, $tProtocol, $listenHost, $listenPort, $tAgent, $targetHost, $targetPort) -join ':'
        $tunnel = Convert-TmTunnelSpec -Spec $spec
        $tunnel.AuthMode = $auth.AuthMode
        $tunnel.AllowRemote = $auth.AllowRemote
        $script:TmTunnels += , $tunnel
        $index++
        if ((Read-TmBool -Key ('add_more_' + $index) -Prompt '继续添加下一条转发' -Default 'no') -ne 'yes') { break }
      }
    }
    default { Stop-Tm $script:TmExit.Usage "unknown role: $Role" }
  }
}

# Invoke-TmMain 与 .sh 侧 tm_main 的 8 个阶段一一对应：
# preflight → resolve&fetch → placement → install binary → configure → validate → register&start → verify&report。
function Invoke-TmMain {
  param([Parameter(Mandatory = $true)][string]$Role, [hashtable]$Bound = @{})

  $version = [string](Get-TmBound $Bound 'Version' 'latest')
  $script:TmYes = [bool](Get-TmBound $Bound 'Yes' $false)
  $uninstall = [bool](Get-TmBound $Bound 'Uninstall' $false)
  $noService = [bool](Get-TmBound $Bound 'NoService' $false)
  $noStart = [bool](Get-TmBound $Bound 'NoStart' $false)
  $noEnable = [bool](Get-TmBound $Bound 'NoEnable' $false)
  $noCache = [bool](Get-TmBound $Bound 'NoCache' $false)
  $keepConfig = [bool](Get-TmBound $Bound 'KeepConfig' $false)
  $reconfigure = [bool](Get-TmBound $Bound 'Reconfigure' $false)
  $baseUrl = [string](Get-TmBound $Bound 'BaseUrl' $script:TmDefaultBaseUrl)
  $archive = [string](Get-TmBound $Bound 'Archive' '')
  $script:TmTokenFile = [string](Get-TmBound $Bound 'TokenFile' '')
  $secretEnvFile = [string](Get-TmBound $Bound 'SecretEnvFile' '')
  $installDir = [string](Get-TmBound $Bound 'InstallDir' (Join-Path $env:ProgramFiles 'TunnelMesh'))
  $configDir = [string](Get-TmBound $Bound 'ConfigDir' (Join-Path $env:ProgramData 'TunnelMesh'))
  $stateDir = [string](Get-TmBound $Bound 'StateDir' (Join-Path $env:ProgramData 'TunnelMesh'))
  $serverUrl = [string](Get-TmBound $Bound 'ServerUrl' '')
  $agentId = [string](Get-TmBound $Bound 'AgentId' '')
  $tunnelSpecs = [string[]](Get-TmBound $Bound 'Tunnel' @())
  $winswPath = [string](Get-TmBound $Bound 'WinSW' '')
  # -RawBaseUrl 在 Windows 侧没有用途（共享模块用 . $PSScriptRoot 本地加载），
  # 保留参数只为与 .sh 侧参数面一致，避免用户照抄文档时报「参数不存在」。
  $null = Get-TmBound $Bound 'RawBaseUrl' ''

  if (-not (Test-TmAdmin)) {
    Stop-Tm $script:TmExit.Preflight 'Windows 服务是机器级的，请以管理员身份运行 PowerShell'
  }
  if ($secretEnvFile) { Read-TmSecretEnvFile -Path $secretEnvFile }
  if ($serverUrl) { $script:TmAnswers['server_url'] = $serverUrl }
  if ($agentId) { $script:TmAnswers['agent_id'] = $agentId }
  foreach ($spec in $tunnelSpecs) { $script:TmTunnels += , (Convert-TmTunnelSpec -Spec $spec) }

  # 卸载分支放在问答之前：卸载只需要角色与路径，问 Server URL / token 既多余，
  # 又会在 -Yes 下因为拿不到必填项而以退出码 3 失败，导致「装得上、卸不掉」。
  if ($uninstall) {
    Uninstall-TmRole -Role $Role -InstallDir $installDir -ConfigDir $configDir
    exit $script:TmExit.Ok
  }

  Invoke-TmRolePrompts -Role $Role -StateDir $stateDir

  $platform = Get-TmPlatform
  $work = Join-Path $env:TEMP ('tunnelmesh-install-' + [Guid]::NewGuid().ToString('N'))
  New-Item -ItemType Directory -Force -Path $work | Out-Null
  try {
    if ($archive) {
      if (-not (Test-Path -LiteralPath $archive)) { Stop-Tm $script:TmExit.Usage "archive not found: $archive" }
      if ($version -eq 'latest') {
        $leaf = Split-Path -Leaf $archive
        if ($leaf -match '^tunnelmesh-(v[0-9]+\.[0-9]+\.[0-9]+)-') { $version = $Matches[1] }
        else { Stop-Tm $script:TmExit.Usage "cannot infer version from archive name: $leaf (pass -Version)" }
      }
      $sums = Join-Path (Split-Path -Parent (Resolve-Path -LiteralPath $archive).Path) 'SHA256SUMS'
      if (Test-Path -LiteralPath $sums) {
        Test-TmChecksum -File $archive -SumsFile $sums -ArchiveName (Split-Path -Leaf $archive)
      } else {
        Write-TmWarn '本地归档同目录没有 SHA256SUMS，跳过校验（-Archive 模式）'
      }
      $archivePath = $archive
    } else {
      $version = Resolve-TmVersion -Version $version -BaseUrl $baseUrl
      $name = Get-TmArchiveName -Version $version -Platform $platform
      $cache = Get-TmCachePath $name
      $sumsCache = Join-Path $work 'SHA256SUMS'
      Invoke-TmDownload -Url "$baseUrl/download/$version/SHA256SUMS" -OutFile $sumsCache
      if ((-not $noCache) -and (Test-TmChecksumQuiet -File $cache -SumsFile $sumsCache -ArchiveName $name)) {
        Write-TmInfo "using cached archive $cache"
        $archivePath = $cache
      } else {
        $dest = Join-Path $work $name
        Write-TmInfo "downloading $name"
        Invoke-TmDownload -Url "$baseUrl/download/$version/$name" -OutFile $dest
        Test-TmChecksum -File $dest -SumsFile $sumsCache -ArchiveName $name
        if (-not $noCache) {
          New-Item -ItemType Directory -Force -Path (Get-TmCacheDir) | Out-Null
          Copy-Item -Force -LiteralPath $dest -Destination $cache
        }
        $archivePath = $dest
      }
    }

    $extract = Join-Path $work 'extract'
    New-Item -ItemType Directory -Force -Path $extract | Out-Null
    Expand-Archive -LiteralPath $archivePath -DestinationPath $extract -Force
    New-Item -ItemType Directory -Force -Path $installDir, $configDir, $stateDir, (Join-Path $installDir 'logs') | Out-Null

    $srcExe = Join-Path $extract "tunnelmesh-$Role.exe"
    if (-not (Test-Path -LiteralPath $srcExe)) { Stop-Tm $script:TmExit.Download "release archive is missing tunnelmesh-$Role.exe" }
    $destExe = Join-Path $installDir "tunnelmesh-$Role.exe"
    $installedVersion = ''
    if (Test-Path -LiteralPath $destExe) {
      try { $installedVersion = [string](& $destExe --version 2>$null | Select-Object -First 1) } catch { $installedVersion = '' }
      Write-TmInfo "已安装 $installedVersion，目标 $version：按升级流程处理（保留配置）"
      $wrapper = Get-TmWrapperPath -InstallDir $installDir -Role $Role
      if (Test-Path -LiteralPath $wrapper) { & $wrapper stop 2>$null | Out-Null }
      $stamp = (Get-Date).ToUniversalTime().ToString('yyyyMMddTHHmmssZ')
      Copy-Item -Force -LiteralPath $destExe -Destination "$destExe.bak-$stamp"
      $old = @(Get-ChildItem -Path "$destExe.bak-*" -ErrorAction SilentlyContinue | Sort-Object LastWriteTime -Descending)
      if ($old.Count -gt 1) { $old | Select-Object -Skip 1 | Remove-Item -Force }
    }
    Copy-Item -Force -LiteralPath $srcExe -Destination $destExe
    Write-TmInfo "installed $destExe"

    $configPath = Join-Path $configDir "$Role.yaml"
    if ($keepConfig -and (Test-Path -LiteralPath $configPath)) {
      Write-TmInfo "-KeepConfig：保留现有 $configPath"
    } elseif (-not $installedVersion -or $reconfigure) {
      Write-TmFile -Path $configPath -Content (Write-TmYaml -Role $Role) -Backup $true
    } else {
      Write-TmInfo '升级：保留现有配置（需要重新生成请加 -Reconfigure）'
    }

    & $destExe --config $configPath check-config
    if ($LASTEXITCODE -ne 0) { Stop-Tm $script:TmExit.Config "check-config failed；请检查 $configPath" }

    if ($noService) {
      Write-TmWarn '-NoService：只安装了二进制与配置，未注册服务'
    } else {
      # 上游没有 arm64 资产，Windows on ARM 统一用 x64 版 WinSW（仿真层可运行）。
      $winswArch = if ($platform.GoArch -eq 'arm64') { 'amd64' } else { $platform.GoArch }
      $winsw = Get-TmWinSW -WinSWPath $winswPath -Arch $winswArch
      # 服务模板取刚解压的 Release 归档里的那份，保证「模板与二进制同版本」；
      # 用户单独下载入口脚本时 $PSScriptRoot 下并没有 ..\windows\，不能依赖它。
      $serviceTemplate = Join-Path $extract 'deploy\windows\tunnelmesh-service.xml'
      Install-TmService -Role $Role -InstallDir $installDir -Binary $destExe -Config $configPath `
        -EnvBlock (Write-TmEnvBlock) -WinSWExe $winsw -Template $serviceTemplate
      if ($noEnable) { Write-TmInfo '-NoEnable：未调整启动类型；Windows 服务默认自动启动，可用 sc config 手工修改' }
      if ($noStart) { Write-TmInfo '-NoStart：已注册但未启动' } else { Start-TmService -InstallDir $installDir -Role $Role }
    }

    Write-Host ''
    Write-Host "==> TunnelMesh $Role $version 安装完成"
    Write-Host "  二进制:   $destExe"
    Write-Host "  配置:     $configPath"
    if ($script:TmSecrets.Count -gt 0) {
      $masked = ($script:TmSecrets.Keys | Sort-Object | ForEach-Object { $_ + '=' + (Get-TmMasked $script:TmSecrets[$_]) }) -join ' '
      Write-Host "  敏感值:   $(Get-TmServiceXmlPath -InstallDir $installDir -Role $Role)（$masked）"
    }
    Write-Host "  服务:     winsw ($(Get-TmServiceId -Role $Role))"
    Write-Host "  查看日志: Get-Content '$(Join-Path $installDir 'logs')\*.out.log' -Tail 50 -Wait"
    Write-Host '  卸载:     重新运行本脚本并加 -Uninstall'
    Invoke-TmRoleSummary -Role $Role -InstallDir $installDir -ConfigDir $configDir
  } finally {
    Remove-Item -Recurse -Force -ErrorAction SilentlyContinue -LiteralPath $work
  }
}

function Invoke-TmRoleSummary {
  param([string]$Role, [string]$InstallDir, [string]$ConfigDir)
  switch ($Role) {
    'server' {
      Write-Host "  管理后台:  http://$($script:TmAnswers['http_addr'])/"
      if ($script:TmAnswers['bootstrap_admin'] -eq 'yes') {
        Write-TmInfo '创建首个管理员（凭据只输出到本终端，请立即修改）'
        & (Join-Path $InstallDir 'tunnelmesh-server.exe') --config (Join-Path $ConfigDir 'server.yaml') admin bootstrap
        if ($LASTEXITCODE -ne 0) {
          Write-TmWarn 'admin bootstrap 失败，可稍后手工执行：tunnelmesh-server.exe --config <path> admin regenerate-credentials --confirm'
        }
      }
      Write-Host "  健康检查:  $(Join-Path $InstallDir 'tunnelmesh-server.exe') doctor"
    }
    'agent' {
      Write-Host "  Agent ID:  $($script:TmAnswers['agent_id'])"
      Write-Host "  下一步:    在管理后台为该 Agent 绑定 service token"
    }
    'client' {
      Write-Host "  本地转发: $($script:TmTunnels.Count) 条"
      Write-Host "  查看状态: $(Join-Path $InstallDir 'tunnelmesh-client.exe') status"
    }
  }
}
