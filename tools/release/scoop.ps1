# Installs string with Scoop, as people do, from the manifest at a commit of a
# copy of this repository, and checks that the copy it installs says the
# version.
#
#   scoop.ps1 REPOSITORY COMMIT VERSION
#
# REPOSITORY is a Git URL: Scoop refuses a plain path. The release workflow
# runs it on the commit promote made or found, taken from GitHub, and the
# packages workflow on a pull request's own checkout, so the release's script
# has run before any release depends on it.
param(
    [Parameter(Mandatory)] [string] $Repository,
    [Parameter(Mandatory)] [string] $Commit,
    [Parameter(Mandatory)] [string] $Version
)
$ErrorActionPreference = 'Stop'

# Scoop runs as scripts, which the installer checks are allowed.
Set-ExecutionPolicy -Scope Process -ExecutionPolicy RemoteSigned -Force
$env:SCOOP = Join-Path $env:RUNNER_TEMP 'scoop'
$installer = Join-Path $env:RUNNER_TEMP 'install-scoop.ps1'
Invoke-RestMethod https://get.scoop.sh -OutFile $installer
# The runner's account is an administrator, which Scoop's installer refuses
# unless told to go on.
& $installer -RunAsAdmin
scoop bucket add usestring $Repository
if ($LASTEXITCODE -ne 0) { throw "scoop bucket add exited with $LASTEXITCODE." }
git -C (Join-Path $env:SCOOP 'buckets\usestring') checkout --quiet $Commit
if ($LASTEXITCODE -ne 0) { throw "git checkout exited with $LASTEXITCODE." }
# Without updating the buckets first, so the commit above is what installs.
scoop install string --no-update-scoop
if ($LASTEXITCODE -ne 0) { throw "scoop install exited with $LASTEXITCODE." }
$got = & (Join-Path $env:SCOOP 'shims\string.exe') --version
if ($got -ne "string $Version") { throw "Scoop installed a copy that says $got." }
