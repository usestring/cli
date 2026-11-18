# Installs the string CLI on Windows, then runs `string init` to give the coding
# agents on this machine the string-cli skill.
#
#   irm https://raw.githubusercontent.com/usestring/cli/main/install.ps1 | iex
#
# Runs in Windows PowerShell 5.1 and PowerShell 7, on x64 and Arm. irm | iex
# takes no parameters, so each has an environment variable as well:
#
#   -Version     STRING_VERSION      a release to install instead of the latest,
#                                    as 0.3.0 or v0.3.0, which pins this copy
#   -InstallDir  STRING_INSTALL_DIR  where string.exe goes
#                                    (default: %USERPROFILE%\.local\bin)
#   -NoInit      STRING_NO_INIT=1    skips string init
#   -Agent       STRING_INIT_AGENT   string init --agent, e.g. claude-code
#
# Releases come from https://cli.usestring.ai, or from the GitHub release when
# that host fails in any way: without a word when it only lacks the release,
# and with the reason otherwise. STRING_DOWNLOAD_URL replaces the host, with no
# fallback, for tests and rehearsals.
#
# irm | iex runs this in the session that ran it. The parameters' variables
# have names of their own, and the script block below copies them and removes
# them from that session; a copy run by its path keeps them in a scope of its
# own, which ends with it. Everything else stays inside the script block, so
# only PATH changes in the session.

param(
    [Alias('Version')][string]$StringInstallVersion = $env:STRING_VERSION,
    [Alias('InstallDir')][string]$StringInstallDir = $env:STRING_INSTALL_DIR,
    [Alias('NoInit')][switch]$StringInstallNoInit,
    [Alias('Agent')][string]$StringInstallAgent = $env:STRING_INIT_AGENT
)

& {
    $Version = $StringInstallVersion
    $InstallDir = $StringInstallDir
    $NoInit = $StringInstallNoInit
    $Agent = $StringInstallAgent
    Remove-Variable StringInstallVersion, StringInstallDir, StringInstallNoInit, StringInstallAgent -Scope 1 -ErrorAction Ignore
    Set-StrictMode -Version 3
    $ErrorActionPreference = 'Stop'
    # Windows PowerShell's progress bar slows a download many times over.
    $ProgressPreference = 'SilentlyContinue'

    $github = 'https://github.com/usestring/cli/releases'

    function Fail([string]$Message) {
        throw "install.ps1: $Message"
    }

    # Get-ReleaseVersion returns $Text without a leading v when it names a
    # release, such as 0.3.0 or 0.3.0-rc.1, and $null otherwise. A version goes
    # into URLs.
    function Get-ReleaseVersion([string]$Text) {
        $v = $Text -creplace '^v', ''
        if ($v -cmatch '\A(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?\z') {
            return $v
        }
        return $null
    }

    # New-MissingError is the error for what a host lacks: a file it answers
    # 404 for, or a stable release when its /stable names a pre-release.
    function New-MissingError([string]$Message) {
        $exception = New-Object Exception $Message
        $exception.Data['missing'] = $true
        return $exception
    }

    # Get-HttpStatus returns the status of the response a failed download got,
    # or 0 if it got none. Windows PowerShell throws a WebException and
    # PowerShell 7 an HttpResponseException, and each holds the response.
    function Get-HttpStatus([Exception]$Exception) {
        $response = $Exception.PSObject.Properties['Response']
        if (-not $response -or -not $response.Value) {
            return 0
        }
        return [int]$response.Value.StatusCode
    }

    function Save-Url([string]$Url, [string]$Path, [int]$Seconds) {
        # -TimeoutSec covers only the wait for the response's headers. From
        # PowerShell 7.4, -OperationTimeoutSeconds also ends a download that
        # stalls after them.
        $stallTimeout = (Get-Command Invoke-WebRequest).Parameters.ContainsKey('OperationTimeoutSeconds')
        try {
            if ($stallTimeout) {
                Invoke-WebRequest -Uri $Url -OutFile $Path -UseBasicParsing -TimeoutSec $Seconds -OperationTimeoutSeconds 60
            } else {
                Invoke-WebRequest -Uri $Url -OutFile $Path -UseBasicParsing -TimeoutSec $Seconds
            }
        } catch {
            $problem = "could not download $Url ($($_.Exception.Message))"
            if ((Get-HttpStatus $_.Exception) -eq 404) {
                throw (New-MissingError $problem)
            }
            throw $problem
        }
    }

    # Get-Release downloads the zip and checksums.txt from $Base, checks the one
    # against the other and returns the path of the string.exe it unpacks.
    function Get-Release([string]$Base) {
        $archive = Join-Path $tmp $zip
        $sums = Join-Path $tmp 'checksums.txt'
        Save-Url "$Base/$zip" $archive 300
        Save-Url "$Base/checksums.txt" $sums 30
        $expected = $null
        foreach ($line in [IO.File]::ReadAllLines($sums)) {
            $fields = -split $line
            if ($fields.Count -eq 2 -and $fields[1] -ceq $zip) {
                $expected = $fields[0]
            }
        }
        # .NET rather than Get-FileHash, which Windows PowerShell loads from a
        # module, and cannot under PowerShell 7: there it finds PowerShell 7's
        # module directories first.
        $sha256 = [Security.Cryptography.SHA256]::Create()
        $stream = [IO.File]::OpenRead($archive)
        try {
            $actual = [BitConverter]::ToString($sha256.ComputeHash($stream)).Replace('-', '')
        } finally {
            $stream.Dispose()
            $sha256.Dispose()
        }
        if (-not $expected -or $expected -ne $actual) {
            throw "$Base/$zip does not match its checksums.txt"
        }
        if (-not ('System.IO.Compression.ZipFile' -as [type])) {
            Add-Type -AssemblyName System.IO.Compression.FileSystem
        }
        $exe = Join-Path $tmp 'string.exe'
        $reader = [IO.Compression.ZipFile]::OpenRead($archive)
        try {
            $entry = $reader.GetEntry('string.exe')
            if (-not $entry) {
                throw "$Base/$zip holds no string.exe"
            }
            [IO.Compression.ZipFileExtensions]::ExtractToFile($entry, $exe, $true)
        } finally {
            $reader.Dispose()
        }
        return $exe
    }

    # Move-WhenFree retries a rename that a scanner briefly holding the file
    # refuses, which shows as a sharing violation or as access denied.
    function Move-WhenFree([string]$From, [string]$To) {
        for ($attempt = 1; ; $attempt++) {
            try {
                [IO.File]::Move($From, $To)
                return
            } catch {
                if ($attempt -ge 10) {
                    throw
                }
                Start-Sleep -Milliseconds 200
            }
        }
    }

    # Send-EnvironmentChange tells Explorer, and the terminals it starts from
    # then on, that PATH changed.
    function Send-EnvironmentChange {
        try {
            if (-not ('StringInstall.Native' -as [type])) {
                Add-Type -Namespace StringInstall -Name Native -MemberDefinition @'
[DllImport("user32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
public static extern IntPtr SendMessageTimeout(IntPtr hWnd, uint msg, UIntPtr wParam, string lParam, uint flags, uint timeout, out UIntPtr result);
'@
            }
            $result = [UIntPtr]::Zero
            [void][StringInstall.Native]::SendMessageTimeout([IntPtr]0xffff, 0x1a, [UIntPtr]::Zero, 'Environment', 2, 5000, [ref]$result)
        } catch {
            # New terminals then see the change after the next sign-in.
        }
    }

    if ($PSVersionTable.PSEdition -eq 'Core' -and -not $IsWindows) {
        Fail 'this is for Windows; on macOS and Linux, run install.sh instead'
    }
    # Windows PowerShell can default to TLS versions that both hosts refuse.
    if ($PSVersionTable.PSEdition -eq 'Desktop') {
        [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
    }

    # The machine's own CPU. PowerShell running as x86 or x64 under emulation on
    # an Arm machine finds the CPU it emulates in PROCESSOR_ARCHITECTURE, but
    # the system's value in the registry, whose SYSTEM hive every process sees
    # the same, names the machine's.
    $native = $null
    try {
        $system = [Microsoft.Win32.Registry]::LocalMachine.OpenSubKey('SYSTEM\CurrentControlSet\Control\Session Manager\Environment')
        if ($system) {
            try {
                $native = $system.GetValue('PROCESSOR_ARCHITECTURE')
            } finally {
                $system.Close()
            }
        }
    } catch {
        # The environment says the same, except under x64 emulation.
    }
    if (-not $native) {
        $native = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
    }
    switch ($native) {
        'AMD64' { $arch = 'amd64' }
        'ARM64' { $arch = 'arm64' }
        default { Fail "unsupported CPU $native" }
    }

    $want = $null
    $pinned = $false
    if ($Version) {
        $want = Get-ReleaseVersion $Version
        if (-not $want) {
            Fail "the version is $Version, which names no release; use one such as 0.3.0"
        }
        $pinned = $true
    }
    if (-not $InstallDir) {
        $InstallDir = Join-Path $env:USERPROFILE '.local\bin'
    }
    # .NET resolves a relative path against the process's directory, which is
    # not PowerShell's.
    $InstallDir = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($InstallDir).TrimEnd('\')
    $skipInit = $NoInit.IsPresent -or (@('', '0', 'false', 'off', 'no') -notcontains "$env:STRING_NO_INIT".ToLowerInvariant())
    $zip = "string_windows_$arch.zip"

    $tmp = Join-Path ([IO.Path]::GetTempPath()) ('string-install-' + [Guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $tmp | Out-Null
    try {
        $server = 'https://cli.usestring.ai'
        $fallback = $true
        if ($env:STRING_DOWNLOAD_URL) {
            $server = $env:STRING_DOWNLOAD_URL.TrimEnd('/')
            $fallback = $false
        }
        try {
            if (-not $want) {
                $stable = Join-Path $tmp 'stable'
                Save-Url "$server/stable" $stable 30
                $want = Get-ReleaseVersion (@([IO.File]::ReadAllLines($stable)) + '')[0]
                if (-not $want) {
                    throw "$server/stable does not name a release"
                }
                # While a release is rehearsed, the default host's /stable can
                # name a pre-release, which only -Version or
                # STRING_DOWNLOAD_URL installs.
                if ($fallback -and $want.Contains('-')) {
                    $named = $want
                    $want = $null
                    throw (New-MissingError "$server/stable names $named, a pre-release")
                }
            }
            $exe = Get-Release "$server/v$want"
        } catch {
            if (-not $fallback) {
                Fail $_.Exception.Message
            }
            # Before a release is promoted, and while one is rehearsed, the
            # host lacks what is asked for and GitHub has it, which is not
            # worth a line. Any other failure is, and an install that fails on
            # GitHub too names both reasons.
            $first = $_.Exception.Message
            $missing = $_.Exception.Data['missing']
            if (-not $missing) {
                Write-Host "install.ps1: $first; trying the GitHub release instead"
            }
            try {
                if ($want) {
                    $exe = Get-Release "$github/download/v$want"
                } else {
                    $exe = Get-Release "$github/latest/download"
                }
            } catch {
                if ($missing) {
                    Fail "$first; then $($_.Exception.Message)"
                }
                Fail $_.Exception.Message
            }
        }

        # Copies that earlier installs and updates moved aside while they ran.
        New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
        Get-ChildItem -LiteralPath $InstallDir -Filter 'string.exe.*.old' -Force -ErrorAction SilentlyContinue |
            Remove-Item -Force -ErrorAction SilentlyContinue

        $target = Join-Path $InstallDir 'string.exe'
        $part = Join-Path $InstallDir ('.string-install-' + [Guid]::NewGuid().ToString('N') + '.exe')
        try {
            Copy-Item -LiteralPath $exe -Destination $part
            $reported = $null
            try {
                $reported = "$(& $part --version 2>$null)".Trim()
            } catch {
                # Checked below.
            }
            if ($reported -notlike 'string *') {
                Fail "the string.exe built for windows/$arch does not run here"
            }
            if ($want -and $reported -cne "string $want") {
                Fail "the archive for $want holds $reported"
            }
            # Windows cannot replace an executable while it runs, but can rename
            # it, so the one there now moves aside under a name of its own.
            $aside = $null
            if (Test-Path -LiteralPath $target) {
                $aside = '{0}.{1}.old' -f $target, [Guid]::NewGuid().ToString('N').Substring(0, 12)
                Move-WhenFree $target $aside
            }
            try {
                Move-WhenFree $part $target
            } catch {
                if ($aside) {
                    Move-WhenFree $aside $target
                }
                Fail "could not move string.exe into $InstallDir ($($_.Exception.Message))"
            }
            if ($aside) {
                Remove-Item -LiteralPath $aside -Force -ErrorAction SilentlyContinue
            }
        } finally {
            Remove-Item -LiteralPath $part -Force -ErrorAction SilentlyContinue
        }
    } finally {
        Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
    }
    Write-Host "Installed $reported to $target"

    # string update keeps a copy registered this way up to date. A release from
    # before that command fails here, which leaves the copy as it is.
    $register = @('update', '--register')
    if ($pinned) {
        $register += '--pin'
    }
    & {
        $ErrorActionPreference = 'Continue'
        & $target $register *> $null
    }

    # In front of the user's other PATH entries for new terminals, and for this
    # session. The value is read and written raw, so that an entry such as
    # %USERPROFILE%\bin stays as it is.
    $key = [Microsoft.Win32.Registry]::CurrentUser.CreateSubKey('Environment')
    try {
        $raw = [string]$key.GetValue('Path', '', [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
        $entries = @($raw -split ';' | Where-Object { $_ })
        $expanded = @($entries | ForEach-Object { [Environment]::ExpandEnvironmentVariables($_).TrimEnd('\') })
        if ($expanded -notcontains $InstallDir) {
            $kind = [Microsoft.Win32.RegistryValueKind]::ExpandString
            if ($raw) {
                $kind = $key.GetValueKind('Path')
            }
            $key.SetValue('Path', ((@($InstallDir) + $entries) -join ';'), $kind)
            Send-EnvironmentChange
            Write-Host "Added $InstallDir to your PATH for new terminals."
        }
    } finally {
        $key.Close()
    }
    if (@($env:Path -split ';' | ForEach-Object { $_.TrimEnd('\') }) -notcontains $InstallDir) {
        $env:Path = "$InstallDir;$env:Path"
    }

    if ($skipInit) {
        return
    }
    Write-Host ''
    $initArgs = @('init')
    if ($Agent) {
        $initArgs += '--agent', $Agent
    }
    # When the caller redirects the error stream, Windows PowerShell makes each
    # line a native command writes to stderr an error, which Stop would turn
    # into the end of this script. init writes there while it signs in, so its
    # exit code alone says whether it worked.
    & {
        $ErrorActionPreference = 'Continue'
        & $target $initArgs
    }
    if ($LASTEXITCODE -ne 0) {
        Fail "string is installed, but string init exited with $LASTEXITCODE"
    }
}
