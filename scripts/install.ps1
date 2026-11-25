$ErrorActionPreference = 'Stop'

# Enforce TLS 1.2 for Windows PowerShell 5.1 compatibility.
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

$Repo = 'monicafaye/TypX'
$InstallDir = if ($env:TYPX_INSTALL_DIR) { $env:TYPX_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'TypX\bin' }
$Version = if ($env:TYPX_VERSION) { $env:TYPX_VERSION.TrimStart('v') } else { '' }
$SkipTinymist = $env:TYPX_SKIP_TINYMIST -eq '1'
$NoModifyPath = $env:TYPX_NO_MODIFY_PATH -eq '1'

function Get-TargetArch {
  $archRaw = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
  switch ($archRaw.ToUpperInvariant()) {
    'AMD64' { return 'amd64' }
    'ARM64' { return 'arm64' }
    default {
      throw "Unsupported Windows architecture: $archRaw (supported: AMD64, ARM64)."
    }
  }
}

function Get-LatestVersion {
  $request = [System.Net.HttpWebRequest]::Create("https://github.com/$Repo/releases/latest")
  $request.AllowAutoRedirect = $false
  $request.UserAgent = 'TypX-Installer'
  $response = $null
  try {
    $response = $request.GetResponse()
    $location = $response.Headers['Location']
  } catch [System.Net.WebException] {
    $errResp = $_.Exception.Response
    if ($errResp -and $errResp.Headers['Location']) {
      $location = $errResp.Headers['Location']
    } else {
      throw
    }
  } finally {
    if ($response) { $response.Close() }
  }

  if (-not $location) {
    throw 'Could not determine the latest TypX release tag.'
  }
  $tag = ($location.TrimEnd('/') -split '/')[-1]
  if (-not $tag -or $tag -eq 'latest' -or $tag -eq 'releases') {
    throw "Could not determine the latest TypX release tag from: $location"
  }
  return $tag.TrimStart('v')
}

function Add-ToUserPath([string]$Dir) {
  $normalizedTarget = $Dir.TrimEnd('\').ToLowerInvariant()
  $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
  $entries = @()
  if ($userPath) {
    $entries = $userPath -split ';' | Where-Object { $_ -and $_.Trim() -ne '' }
  }

  $exists = $false
  foreach ($entry in $entries) {
    if ($entry.Trim().TrimEnd('\').ToLowerInvariant() -eq $normalizedTarget) {
      $exists = $true
      break
    }
  }

  $changed = $false
  if (-not $exists) {
    $newUserPath = if ($entries.Count -gt 0) { ($entries -join ';') + ';' + $Dir } else { $Dir }
    [Environment]::SetEnvironmentVariable('Path', $newUserPath, 'User')
    $changed = $true
  }

  $sessionEntries = ($env:Path -split ';') | ForEach-Object { $_.Trim().TrimEnd('\').ToLowerInvariant() }
  if ($sessionEntries -notcontains $normalizedTarget) {
    $env:Path = "$Dir;$env:Path"
  }

  return $changed
}

function Find-ExistingTinymist {
  $cmd = Get-Command tinymist -ErrorAction SilentlyContinue
  if ($cmd -and $cmd.Source) {
    return $cmd.Source
  }

  $candidates = @(
    (Join-Path $env:USERPROFILE '.local\bin\tinymist.exe'),
    (Join-Path $env:USERPROFILE '.cargo\bin\tinymist.exe'),
    (Join-Path $env:LOCALAPPDATA 'Microsoft\WinGet\Links\tinymist.exe'),
    (Join-Path $env:USERPROFILE 'scoop\shims\tinymist.exe')
  )
  foreach ($candidate in $candidates) {
    if ($candidate -and (Test-Path -LiteralPath $candidate -PathType Leaf)) {
      return $candidate
    }
  }

  foreach ($extRoot in @(
    (Join-Path $env:USERPROFILE '.vscode\extensions'),
    (Join-Path $env:USERPROFILE '.vscode-insiders\extensions')
  )) {
    if (Test-Path -LiteralPath $extRoot -PathType Container) {
      $found = Get-ChildItem -Path $extRoot -Directory -Filter 'myriad-dreamin.tinymist-*' -ErrorAction SilentlyContinue |
        ForEach-Object {
          foreach ($sub in @('out', 'bin')) {
            $p = Join-Path $_.FullName (Join-Path $sub 'tinymist.exe')
            if (Test-Path -LiteralPath $p -PathType Leaf) { return $p }
          }
        } | Select-Object -First 1
      if ($found) { return $found }
    }
  }

  return $null
}

$Arch = Get-TargetArch
if (-not $Version) {
  $Version = Get-LatestVersion
}
$Tag = "v$Version"
$Archive = "typx-$Version-windows-$Arch.zip"
$BaseUrl = "https://github.com/$Repo/releases/download/$Tag"

$TmpDir = Join-Path ([IO.Path]::GetTempPath()) ('typx-install-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $TmpDir -Force | Out-Null

$PathUpdated = $false
try {
  $ArchivePath = Join-Path $TmpDir $Archive
  $SumsPath = Join-Path $TmpDir 'SHA256SUMS.txt'
  $ExtractDir = Join-Path $TmpDir 'extract'

  Write-Host "Downloading TypX $Version (windows/$Arch)..."
  Invoke-WebRequest -UseBasicParsing -Uri "$BaseUrl/$Archive" -OutFile $ArchivePath
  Invoke-WebRequest -UseBasicParsing -Uri "$BaseUrl/SHA256SUMS.txt" -OutFile $SumsPath

  $ExpectedSha = ''
  foreach ($line in (Get-Content -LiteralPath $SumsPath)) {
    if ($line -match '^\s*([0-9a-fA-F]{64})\s+\*?(.+?)\s*$') {
      if ($Matches[2] -eq $Archive) {
        $ExpectedSha = $Matches[1].ToLowerInvariant()
        break
      }
    }
  }
  if (-not $ExpectedSha) {
    throw "Checksum for $Archive was not found in SHA256SUMS.txt."
  }

  $ActualSha = (Get-FileHash -LiteralPath $ArchivePath -Algorithm SHA256).Hash.ToLowerInvariant()
  if ($ActualSha -ne $ExpectedSha) {
    throw "SHA-256 mismatch for $Archive (expected $ExpectedSha, got $ActualSha)."
  }
  Write-Host 'Verified SHA-256 checksum.'

  Expand-Archive -LiteralPath $ArchivePath -DestinationPath $ExtractDir -Force
  $BinSrc = Join-Path $ExtractDir 'typx.exe'
  if (-not (Test-Path -LiteralPath $BinSrc -PathType Leaf)) {
    throw "Expected binary not found in archive: $BinSrc"
  }

  New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
  $BinDst = Join-Path $InstallDir 'typx.exe'
  try {
    Copy-Item -LiteralPath $BinSrc -Destination $BinDst -Force
  } catch {
    throw "Could not write $BinDst. Close any running TypX windows and try again. ($($_.Exception.Message))"
  }
  Write-Host "Installed TypX to $BinDst"

  if (-not $NoModifyPath) {
    $PathUpdated = Add-ToUserPath -Dir $InstallDir
    if ($PathUpdated) {
      Write-Host "Added $InstallDir to your user PATH."
    }
  }

  if (-not $SkipTinymist) {
    $existingTinymist = Find-ExistingTinymist
    if ($existingTinymist) {
      Write-Host "Found existing tinymist at $existingTinymist."
    } else {
      Write-Host ''
      Write-Host 'Installing tinymist (required for live preview, diagnostics, and PDF export)...'
      $tinymistInstaller = Join-Path $TmpDir 'tinymist-installer.ps1'
      $tinymistInstalled = $false
      try {
        Invoke-WebRequest -UseBasicParsing -Uri 'https://github.com/Myriad-Dreamin/tinymist/releases/latest/download/tinymist-installer.ps1' -OutFile $tinymistInstaller
        $psExe = if ($PSVersionTable.PSEdition -eq 'Core') { 'pwsh' } else { 'powershell' }
        & $psExe -NoProfile -ExecutionPolicy Bypass -File $tinymistInstaller
        if ($LASTEXITCODE -eq 0) {
          $tinymistInstalled = $true
          $PathUpdated = $true
        }
      } catch {
        $tinymistInstalled = $false
      }

      if (-not $tinymistInstalled) {
        Write-Warning 'Could not install tinymist automatically.'
        Write-Host 'TypX is installed. Install tinymist manually by running:'
        Write-Host '  winget install Myriad-Dreamin.Tinymist'
      }
    }
  }
} finally {
  if (Test-Path -LiteralPath $TmpDir) {
    Remove-Item -LiteralPath $TmpDir -Recurse -Force -ErrorAction SilentlyContinue
  }
}

Write-Host ''
if ($PathUpdated) {
  Write-Host 'Close this terminal window and open a new one, then run:'
} else {
  Write-Host 'Verify your setup by running:'
}
Write-Host '  typx --check'
