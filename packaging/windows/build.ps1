# Builds the Windows installer: Itan.exe (the tray app) with ffmpeg.
#   pwsh packaging/windows/build.ps1 -Version 0.2.0 [-OutDir dist]
# Writes <OutDir>\Itan-<version>-windows-amd64-setup.exe. Needs Go and
# Inno Setup 6, which GitHub's Windows runners have.
#
# The installer isn't code-signed, so Windows SmartScreen warns before it
# runs (see the README).
param(
	[Parameter(Mandatory)] [string] $Version,
	[string] $OutDir = "dist"
)
$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue" # much faster downloads
$root = (Resolve-Path "$PSScriptRoot\..\..").Path
Set-Location $root
$short = ($Version -split '-')[0] # file versions are numbers only: 0.2.0-rc.1 is 0.2.0

$ff = @{}
foreach ($line in Get-Content packaging\ffmpeg.env) {
	if ($line -match '^([A-Z0-9_]+)=(.*)$') { $ff[$Matches[1]] = $Matches[2].Trim() }
}
$work = Join-Path ([IO.Path]::GetTempPath()) "itan-$([guid]::NewGuid())"
$stage = Join-Path $work "Itan"
New-Item -ItemType Directory -Force $stage | Out-Null

try {
	Write-Host "--- ffmpeg $($ff.FFMPEG_VERSION)"
	$cache = if ($env:FFMPEG_CACHE) { $env:FFMPEG_CACHE } else { Join-Path $root "dist\ffmpeg-cache" }
	$cache = Join-Path $cache "windows-$($ff.FFMPEG_VERSION)"
	if (-not (Test-Path "$cache\ffprobe.exe")) {
		New-Item -ItemType Directory -Force $cache | Out-Null
		$zip = Join-Path $work "ffmpeg.zip"
		Invoke-WebRequest -Uri $ff.FFMPEG_WINDOWS_URL -OutFile $zip
		$sum = (Get-FileHash -Algorithm SHA256 $zip).Hash.ToLower()
		if ($sum -ne $ff.FFMPEG_WINDOWS_SHA256) { throw "ffmpeg download: checksum $sum, want $($ff.FFMPEG_WINDOWS_SHA256)" }
		Expand-Archive $zip -DestinationPath "$work\ffmpeg"
		$dir = (Get-ChildItem "$work\ffmpeg" -Directory | Select-Object -First 1).FullName
		Copy-Item "$dir\bin\ffmpeg.exe", "$dir\bin\ffprobe.exe" $cache
		Copy-Item "$dir\README.txt" "$cache\ffmpeg-build-README.txt"
	}
	Copy-Item "$cache\*" $stage

	Write-Host "--- Itan.exe $Version"
	# Icon, version details and manifest (DPI awareness, modern controls).
	go run github.com/tc-hib/go-winres@v0.3.3 simply --arch amd64 --manifest gui `
		--icon packaging\windows\itan.ico --product-name Itan --file-description Itan `
		--product-version $short --file-version $short --original-filename Itan.exe `
		--copyright "Copyright (c) 2026 Opeoluwa Adeyeri" --out cmd\itan-desktop\rsrc
	if ($LASTEXITCODE) { throw "go-winres failed" }
	$env:GOOS = "windows"; $env:GOARCH = "amd64"; $env:CGO_ENABLED = "0"
	go build -trimpath -ldflags "-s -w -H=windowsgui -X main.version=$Version" -o "$stage\Itan.exe" ./cmd/itan-desktop
	if ($LASTEXITCODE) { throw "go build failed" }
	Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED

	Copy-Item LICENSE "$stage\LICENSE.txt"
	Copy-Item packaging\ffmpeg-NOTICE.txt, packaging\GPL-3.0.txt $stage
	go run ./packaging/notices -goos windows ./cmd/itan-desktop > "$stage\THIRD-PARTY.txt"
	if ($LASTEXITCODE) { throw "notices failed" }

	Write-Host "--- installer"
	New-Item -ItemType Directory -Force $OutDir | Out-Null
	$iscc = "${env:ProgramFiles(x86)}\Inno Setup 6\ISCC.exe"
	& $iscc /Q "/DVersion=$Version" "/DShortVersion=$short" "/DStage=$stage" "/DOutDir=$((Resolve-Path $OutDir).Path)" packaging\windows\itan.iss
	if ($LASTEXITCODE) { throw "Inno Setup failed" }
	Write-Host "wrote $OutDir\Itan-$Version-windows-amd64-setup.exe"
} finally {
	Remove-Item cmd\itan-desktop\rsrc_windows_*.syso -ErrorAction SilentlyContinue
	Remove-Item -Recurse -Force $work -ErrorAction SilentlyContinue
}
