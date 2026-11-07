$ErrorActionPreference = 'Stop'
$root = Split-Path $PSScriptRoot -Parent
$testRoot = Join-Path $root ('dist/windows-install-test-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $testRoot | Out-Null
$apps = @(
    @{ Component = 'client'; Binary = 'clientdashboard.exe' },
    @{ Component = 'server'; Binary = 'serverdashboard.exe' }
)
foreach ($app in $apps) {
    $config = Get-Content "$root/$($app.Component)/$($app.Component)dashboard/src-tauri/tauri.conf.json" -Raw | ConvertFrom-Json
    $app.Name = $config.productName
    $app.Version = $config.version
}
if (Get-Process clientdashboard,serverdashboard,tunnelx-client,tunnelx-server -ErrorAction SilentlyContinue) {
    throw 'Close existing TunnelX processes before running the desktop installation test.'
}
Add-Type @'
using System;
using System.Runtime.InteropServices;
public class TunnelXTestWindow {
    [DllImport("user32.dll")] public static extern bool PostMessage(IntPtr h, uint m, IntPtr w, IntPtr l);
    [DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr h);
}
'@
foreach ($app in $apps) {
    $component = $app.Component
    $display = (Get-Culture).TextInfo.ToTitleCase($component)
    $install = Join-Path $testRoot $component
    $registry = @("Software\Microsoft\Windows\CurrentVersion\Uninstall\$($app.Name)", "Software\xcloudy\$($app.Name)")
    $backups = @{}
    $process = $null
    $children = @()
    $state = Join-Path $env:APPDATA "cn.xcloudy.tunnelx.$component"
    $stateBackup = Join-Path $testRoot "$component-state-backup"
    $windowPreferences = Join-Path $state 'window-preferences.json'
    $hadWindowPreferences = Test-Path -LiteralPath $windowPreferences
    if (Test-Path $state) { Copy-Item -LiteralPath $state -Destination $stateBackup -Recurse }
    foreach ($key in $registry) {
        if (Test-Path "HKCU:/$key") {
            $file = Join-Path $testRoot ([guid]::NewGuid().ToString('N') + '.reg')
            & reg.exe export "HKCU\$key" $file /y | Out-Null
            if ($LASTEXITCODE -ne 0) { throw 'Registry backup failed' }
            $backups[$key] = $file
        }
    }
    try {
        $installer = Join-Path $root "dist/windows-x64/TunnelX-$display-$($app.Version)-windows-x64-setup.exe"
        $result = Start-Process -FilePath $installer -ArgumentList @('/S', '/UPDATE', '/NS', "/D=$install") -Wait -PassThru -WindowStyle Hidden
        if ($result.ExitCode -ne 0) { throw "Installer failed: $($result.ExitCode)" }
        foreach ($file in @($app.Binary, "tunnelx-$component.exe", "$component.yaml", 'uninstall.exe')) {
            if (-not (Test-Path (Join-Path $install $file))) { throw "Missing installed file: $file" }
        }
        $registeredVersion = (Get-ItemProperty "HKCU:/Software/Microsoft/Windows/CurrentVersion/Uninstall/$($app.Name)").DisplayVersion
        if ($registeredVersion -ne $app.Version) { throw "Wrong registered version: $registeredVersion" }
        $binaryVersion = (Get-Item (Join-Path $install $app.Binary)).VersionInfo.ProductVersion
        if ($binaryVersion -notmatch ('^' + [regex]::Escape($app.Version) + '($|[.+-])')) {
            throw "Wrong desktop binary version: $binaryVersion"
        }
        Write-Output "PASS $component installed application version: $($app.Version)"
        # This lifecycle test exercises the saved tray choice; the default Ask
        # prompt is covered separately by test-window-preferences.cjs.
        New-Item -ItemType Directory -Force $state | Out-Null
        [IO.File]::WriteAllText($windowPreferences, '{"close_behavior":"tray"}', [Text.UTF8Encoding]::new($false))
        $process = Start-Process -FilePath (Join-Path $install $app.Binary) -PassThru -WindowStyle Hidden
        $ready = $false
        for ($attempt = 0; $attempt -lt 60; $attempt++) {
            Start-Sleep -Milliseconds 500
            $process.Refresh()
            if ($process.HasExited) { throw "Desktop app exited with code $($process.ExitCode)" }
            $children = @(Get-CimInstance Win32_Process -Filter "ParentProcessId=$($process.Id)" | Where-Object { $_.Name -eq "tunnelx-$component.exe" })
            if ($children.Count -eq 1) {
                $listeners = @(Get-NetTCPConnection -State Listen -OwningProcess $children[0].ProcessId -ErrorAction SilentlyContinue)
                foreach ($listener in $listeners) {
                    try {
                        $response = Invoke-RestMethod "http://127.0.0.1:$($listener.LocalPort)/api/setup-status" -TimeoutSec 2 -NoProxy
                        if ($null -ne $response.configured -and $process.MainWindowHandle -ne 0) { $ready = $true; break }
                    } catch {}
                }
            }
            if ($ready) { break }
        }
        if (-not $ready) { throw 'Desktop window and bundled backend did not become ready' }
        Write-Output "PASS $component NSIS installation, resources, desktop window, bundled backend API"
        $mainWindow = $process.MainWindowHandle
        [TunnelXTestWindow]::PostMessage($mainWindow, 0x10, [IntPtr]::Zero, [IntPtr]::Zero) | Out-Null
        for ($attempt = 0; $attempt -lt 20; $attempt++) {
            Start-Sleep -Milliseconds 100
            $process.Refresh()
            if ($process.HasExited) { throw 'Desktop app exited instead of hiding to the tray' }
            if (-not [TunnelXTestWindow]::IsWindowVisible($mainWindow)) { break }
        }
        if ([TunnelXTestWindow]::IsWindowVisible($mainWindow)) { throw 'Desktop window stayed visible after close' }
        Start-Sleep -Seconds 2
        $process.Refresh()
        if ($process.HasExited) { throw 'Desktop app exited after hiding' }
        if (-not (Get-Process -Id $children[0].ProcessId -ErrorAction SilentlyContinue)) { throw 'Backend stopped after window close' }
        $response = Invoke-RestMethod "http://127.0.0.1:$($listener.LocalPort)/api/setup-status" -TimeoutSec 2 -NoProxy
        if ($null -eq $response.configured) { throw 'Backend API unavailable after window close' }
        Write-Output "PASS $component window close: hidden, desktop and backend remain running, API responds"
    } finally {
        if ($process -and -not $process.HasExited) { Stop-Process -Id $process.Id -Force }
        foreach ($child in $children) { Stop-Process -Id $child.ProcessId -Force -ErrorAction SilentlyContinue }
        if (Test-Path "$install/uninstall.exe") {
            $uninstall = Start-Process "$install/uninstall.exe" -ArgumentList @('/S', '/UPDATE', "_?=$install") -Wait -PassThru -WindowStyle Hidden
            if ($uninstall.ExitCode -ne 0) { throw "Uninstaller failed: $($uninstall.ExitCode)" }
            if (Test-Path (Join-Path $install $app.Binary)) { throw 'Uninstaller left the desktop executable installed' }
            Write-Output "PASS $component silent uninstall"
        }
        foreach ($key in $registry) {
            if (Test-Path "HKCU:/$key") { Remove-Item -LiteralPath "HKCU:/$key" -Recurse -Force }
            if ($backups.ContainsKey($key)) { & reg.exe import $backups[$key] | Out-Null }
        }
        if (Test-Path $stateBackup) { Copy-Item "$stateBackup/*" $state -Recurse -Force }
        if (-not $hadWindowPreferences -and (Test-Path -LiteralPath $windowPreferences)) {
            Remove-Item -LiteralPath $windowPreferences -Force
        }
    }
}
$resolvedTestRoot = [IO.Path]::GetFullPath($testRoot)
if (-not $resolvedTestRoot.StartsWith([IO.Path]::GetFullPath((Join-Path $root 'dist')) + [IO.Path]::DirectorySeparatorChar)) {
    throw 'Refusing to clean a test directory outside the workspace dist directory'
}
Remove-Item -LiteralPath $resolvedTestRoot -Recurse -Force
Write-Output 'ALL WINDOWS DESKTOP INSTALLATION TESTS PASSED'
