# make.ps1 - Windows entry point for the build targets when GNU make is not
# installed. It runs scripts/make.sh with Git for Windows' bash, exactly as the
# Makefile does on Linux and macOS.
#
#   .\make.ps1 check
param([string]$Target = 'check')

$ErrorActionPreference = 'Stop'

# Use Git for Windows' bash explicitly: C:\Windows\System32\bash.exe is the WSL
# launcher and would build inside Linux instead of natively.
$candidates = @()
$git = Get-Command git -ErrorAction SilentlyContinue
if ($git) {
    $gitRoot = Split-Path (Split-Path $git.Source -Parent) -Parent
    $candidates += (Join-Path $gitRoot 'bin\bash.exe')
}
$candidates += 'C:\Program Files\Git\bin\bash.exe'
$bash = $candidates | Where-Object { Test-Path $_ } | Select-Object -First 1
if (-not $bash) { throw 'Git for Windows bash not found; install Git for Windows.' }

# Windows PowerShell 5.1 turns any stderr line of a native command into an
# error record; with 'Stop' that aborts on "go: downloading ...". Only the
# exit code decides success.
$ErrorActionPreference = 'Continue'
& $bash (Join-Path $PSScriptRoot 'scripts/make.sh') $Target 2>&1 | ForEach-Object { "$_" }
exit $LASTEXITCODE
