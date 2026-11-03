; Inno Setup script for the Windows installer. build.ps1 runs it with
; Version, ShortVersion, Stage (the files to install) and OutDir defined.
;
; It installs for the current user without asking for admin rights, into
; %LOCALAPPDATA%\Programs\Itan. Itan's data (%LOCALAPPDATA%\Itan) stays
; when it's uninstalled, so reinstalling keeps the library.

#ifndef Version
  #define Version "0.0.0-dev"
  #define ShortVersion "0.0.0"
  #define Stage "..\..\dist\windows\Itan"
  #define OutDir "..\..\dist"
#endif

[Setup]
AppId={{6F3C2E8A-4B71-4D2F-9A5E-1C8B7D0E3F42}
AppName=Itan
AppVersion={#Version}
AppVerName=Itan {#Version}
AppPublisher=Opeoluwa Adeyeri
AppPublisherURL=https://github.com/hardope/itan
AppSupportURL=https://github.com/hardope/itan/issues
AppUpdatesURL=https://github.com/hardope/itan/releases
AppCopyright=Copyright (c) 2026 Opeoluwa Adeyeri
VersionInfoVersion={#ShortVersion}
DefaultDirName={autopf}\Itan
DefaultGroupName=Itan
DisableProgramGroupPage=yes
PrivilegesRequired=lowest
PrivilegesRequiredOverridesAllowed=dialog
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
OutputDir={#OutDir}
OutputBaseFilename=Itan-{#Version}-windows-amd64-setup
SetupIconFile=itan.ico
UninstallDisplayIcon={app}\Itan.exe
UninstallDisplayName=Itan
WizardStyle=modern
Compression=lzma2/max
SolidCompression=yes
; Close a running Itan before replacing its files.
CloseApplications=force
RestartApplications=no

[Tasks]
Name: "startup"; Description: "Start Itan when I sign in, so other devices can watch"
Name: "desktopicon"; Description: "{cm:CreateDesktopIcon}"; GroupDescription: "{cm:AdditionalIcons}"; Flags: unchecked

[Files]
Source: "{#Stage}\*"; DestDir: "{app}"; Flags: ignoreversion recursesubdirs

[Icons]
Name: "{autoprograms}\Itan"; Filename: "{app}\Itan.exe"
Name: "{autodesktop}\Itan"; Filename: "{app}\Itan.exe"; Tasks: desktopicon

[Registry]
; The same value the app's "Open at login" switch sets.
Root: HKCU; Subkey: "Software\Microsoft\Windows\CurrentVersion\Run"; ValueType: string; ValueName: "Itan"; ValueData: """{app}\Itan.exe"" -background"; Tasks: startup; Flags: uninsdeletevalue

[Run]
; Installed for all users (as an administrator): let other devices on
; private networks connect without the firewall asking.
Filename: "{sys}\netsh.exe"; Parameters: "advfirewall firewall add rule name=""Itan"" dir=in action=allow program=""{app}\Itan.exe"" profile=private enable=yes"; Flags: runhidden; Check: IsAdminInstallMode
Filename: "{app}\Itan.exe"; Description: "Open Itan now"; Flags: nowait postinstall skipifsilent

[UninstallRun]
Filename: "{sys}\taskkill.exe"; Parameters: "/IM Itan.exe /F"; Flags: runhidden; RunOnceId: "StopItan"
Filename: "{sys}\netsh.exe"; Parameters: "advfirewall firewall delete rule name=""Itan"""; Flags: runhidden; Check: IsAdminInstallMode; RunOnceId: "RemoveFirewallRule"
