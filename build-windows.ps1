param(
    [ValidateSet('x64', 'arm64')][string]$Arch = 'x64',
    [switch]$SkipInstall,
    [string]$OutputDirectory = ''
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$root = $PSScriptRoot
$target = if ($Arch -eq 'x64') { 'x86_64-pc-windows-msvc' } else { 'aarch64-pc-windows-msvc' }
$goArch = if ($Arch -eq 'x64') { 'amd64' } else { 'arm64' }
$output = if ($OutputDirectory) { [IO.Path]::GetFullPath((Join-Path $root $OutputDirectory)) } else { Join-Path $root "dist/windows-$Arch" }
$distRoot = [IO.Path]::GetFullPath((Join-Path $root 'dist')) + [IO.Path]::DirectorySeparatorChar
if (-not $output.StartsWith($distRoot, [StringComparison]::OrdinalIgnoreCase)) { throw 'OutputDirectory must be inside the workspace dist directory' }
New-Item -ItemType Directory -Force $output | Out-Null
$originalPath = $env:Path
if (Test-Path "$env:USERPROFILE/.cargo/bin") { $env:Path = "$env:USERPROFILE/.cargo/bin;$env:Path" }

function Invoke-Checked {
    param([string]$Program, [string[]]$Arguments)
    & $Program @Arguments
    if ($LASTEXITCODE -ne 0) { throw "$Program failed with exit code $LASTEXITCODE" }
}

$previous = @{}
foreach ($name in @('GOOS', 'GOARCH', 'CGO_ENABLED', 'CARGO_TARGET_DIR')) {
    $previous[$name] = [Environment]::GetEnvironmentVariable($name)
}
try {
    $env:GOOS = 'windows'
    $env:GOARCH = $goArch
    $env:CGO_ENABLED = '0'
    $env:CARGO_TARGET_DIR = Join-Path $root 'bin/tauri-target'
    Invoke-Checked rustup @('target', 'add', $target)
    foreach ($component in @('client', 'server')) {
        $display = (Get-Culture).TextInfo.ToTitleCase($component)
        $dashboard = Join-Path $root "$component/${component}dashboard"
        $tauriConfig = Get-Content "$dashboard/src-tauri/tauri.conf.json" -Raw | ConvertFrom-Json
        $version = $tauriConfig.version
        Push-Location $dashboard
        try {
            if (-not $SkipInstall) { Invoke-Checked npm @('ci', '--no-audit', '--no-fund') }
            Invoke-Checked npm @('run', 'build')
            $sidecars = Join-Path $dashboard 'src-tauri/binaries'
            New-Item -ItemType Directory -Force $sidecars | Out-Null
            $sidecar = Join-Path $sidecars "tunnelx-$component-$target.exe"
            Invoke-Checked go @('build', '-trimpath', '-ldflags=-s -w', '-o', $sidecar, "$root/$component")
            Invoke-Checked npm @('run', 'tauri', '--', 'build', '--target', $target, '--bundles', 'nsis', '--ci')
            $bundle = Join-Path $env:CARGO_TARGET_DIR "$target/release/bundle/nsis"
            $installer = Join-Path $bundle "$($tauriConfig.productName)_${version}_${Arch}-setup.exe"
            if (-not (Test-Path -LiteralPath $installer)) { throw "Missing $component NSIS installer: $installer" }
            Copy-Item -LiteralPath $installer -Destination "$output/TunnelX-$display-$version-windows-$Arch-setup.exe" -Force
            $portable = Join-Path $output "TunnelX-$display-$version-windows-$Arch-cli"
            New-Item -ItemType Directory -Force $portable | Out-Null
            Copy-Item $sidecar "$portable/tunnelx-$component.exe" -Force
            Copy-Item "$root/$component/$component.yaml" $portable -Force
            Copy-Item "$root/LICENSE" $portable -Force
            Set-Content "$portable/Run-$display.cmd" "@echo off`r`ncd /d `"%~dp0`"`r`ntunnelx-$component.exe $component.yaml`r`npause" -Encoding ascii
            Compress-Archive "$portable/*" "$portable.zip" -Force
        } finally { Pop-Location }
    }
    $sums = Get-ChildItem $output -File | Where-Object { $_.Extension -in '.exe', '.zip' } | Sort-Object Name | ForEach-Object {
        "$( (Get-FileHash $_.FullName -Algorithm SHA256).Hash.ToLower() )  $($_.Name)"
    }
    Set-Content "$output/SHA256SUMS" $sums -Encoding ascii
    Write-Host "Windows packages: $output"
} finally {
    foreach ($name in $previous.Keys) { [Environment]::SetEnvironmentVariable($name, $previous[$name]) }
    $env:Path = $originalPath
}
