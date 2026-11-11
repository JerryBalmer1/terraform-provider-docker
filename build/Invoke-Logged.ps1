#Requires -Version 7.0
<#
.SYNOPSIS
  Run one long command in the background with a hard cap: <Name>.pid at start, <Name>.log (+ <Name>.err.log),
  <Name>.exit at the end. The only way an agent or a person starts a long run (CLAUDE.md, "Waiting on long runs").

.DESCRIPTION
  Files go to -LogDir (default test/harness/.logs/<pass>, git-ignored; never %TEMP%):
    <Name>.pid       the wrapper's PID, then the child's PID (written at start)
    <Name>.log       the child's stdout; <Name>.err.log its stderr
    <Name>.exit      the child's exit code, or "124 cap <n>m reached: killed" (written last, always)
  A waiter checks at most once a minute: <Name>.exit exists -> done; no exit file and neither PID alive -> the run
  crashed, read the last 30 lines of the log. At the cap the wrapper kills the child's process tree and, with
  -HarnessDown, runs test/harness/scripts/harness-down.ps1, then writes the exit file.

.EXAMPLE
  pwsh -NoProfile -File build/Invoke-Logged.ps1 -Name default -CapMinutes 15 -Command 'Invoke-Build'
  pwsh -NoProfile -File build/Invoke-Logged.ps1 -Name ExampleTests -CapMinutes 90 -HarnessDown -Command 'Invoke-Build ExampleTests'
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)][ValidatePattern('^[A-Za-z0-9_.-]+$')][string]$Name,
    [Parameter(Mandatory)][string]$Command,
    [Parameter(Mandatory)][ValidateRange(1, 600)][int]$CapMinutes,
    [string]$LogDir,
    [switch]$HarnessDown
)
$ErrorActionPreference = 'Stop'
$repo = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
if (-not $LogDir) { $LogDir = Join-Path $repo 'test/harness/.logs/p6b' }
$null = New-Item -ItemType Directory -Force $LogDir
$pidFile = Join-Path $LogDir "$Name.pid"
$log = Join-Path $LogDir "$Name.log"
$errLog = Join-Path $LogDir "$Name.err.log"
$exitFile = Join-Path $LogDir "$Name.exit"
$child = Join-Path $LogDir "$Name.cmd.ps1"
foreach ($f in $exitFile, $log, $errLog) { Remove-Item -LiteralPath $f -Force -ErrorAction Ignore }

# The child: the command in the repository root; exit code = a failed Invoke-Build (throw) -> 1, else $LASTEXITCODE.
Set-Content -LiteralPath $child -Encoding utf8 -Value @"
Set-Location -LiteralPath '$($repo.Replace("'", "''"))'
`$global:LASTEXITCODE = 0
try {
$Command
    `$code = if (`$LASTEXITCODE) { `$LASTEXITCODE } else { 0 }
} catch {
    Write-Host (`$_ | Out-String)
    `$code = 1
}
exit `$code
"@

$started = Get-Date
Set-Content -LiteralPath $pidFile -Value "wrapper $PID"
$p = Start-Process -FilePath (Get-Process -Id $PID).Path -ArgumentList @('-NoProfile', '-NonInteractive', '-File', $child) `
    -WorkingDirectory $repo -RedirectStandardOutput $log -RedirectStandardError $errLog -NoNewWindow -PassThru
Add-Content -LiteralPath $pidFile -Value "child $($p.Id)"
Write-Host "Invoke-Logged ${Name}: child $($p.Id), cap $CapMinutes min, log $log"

$exitText = $null
if (-not $p.WaitForExit($CapMinutes * 60 * 1000)) {
    Write-Host "Invoke-Logged ${Name}: cap of $CapMinutes min reached, killing process tree $($p.Id)"
    & taskkill.exe /T /F /PID $p.Id 2>&1 | Out-Host
    try { $p.Kill($true) } catch {}
    $null = $p.WaitForExit(60000)
    if ($HarnessDown) {
        Write-Host "Invoke-Logged ${Name}: harness-down"
        try { & (Join-Path $repo 'test/harness/scripts/harness-down.ps1') *>&1 | Out-Host } catch { Write-Warning "harness-down: $_" }
    }
    $exitText = "124 cap ${CapMinutes}m reached: killed"
} else {
    $p.WaitForExit()
    $exitText = [string]$p.ExitCode
}
$dur = (Get-Date) - $started
Add-Content -LiteralPath $log -Value ("### Invoke-Logged {0}: exit {1}, {2:hh\:mm\:ss}" -f $Name, $exitText, $dur)
Set-Content -LiteralPath $exitFile -Value ("{0}`nduration {1:hh\:mm\:ss}" -f $exitText, $dur)
Write-Host "Invoke-Logged ${Name}: exit $exitText after $('{0:hh\:mm\:ss}' -f $dur)"
exit $(if ($exitText -match '^\d+$') { [int]$exitText } else { 124 })
