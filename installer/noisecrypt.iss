; NoiseCrypt installer, for Inno Setup 6.
;
; Built by the release workflow, one per architecture:
;
;   iscc /DAppVersion=0.3.0 /DArch=amd64 /DBinary=..\dist\noisecrypt.exe installer\noisecrypt.iss
;
; # Why per-user, and why no UAC prompt
;
; Everything this program needs lives under the current user: the file associations go in
; HKCU\Software\Classes, and identities go in the user's own profile. Nothing requires
; elevation, so nothing asks for it. An installer that demands administrator for a tool
; that writes only to the user's own hive is asking for a privilege it will not use, and
; the habit of granting that is exactly the habit worth not encouraging in a program whose
; whole subject is caution.
;
; # Why the installer does not write the registry itself
;
; It could, and nearly every installer does. It would then hold a second copy of knowledge
; that already lives in the binary, and the two would drift the first time an entry
; changed. So it calls `noisecrypt shell register` after copying files and
; `shell unregister` before removing them. One description of what integration means, and
; someone who downloaded the bare executable gets the same feature from the same code.

#ifndef AppVersion
  #define AppVersion "dev"
#endif
#ifndef Arch
  #define Arch "amd64"
#endif
#ifndef Binary
  #define Binary "..\dist\noisecrypt.exe"
#endif

#define AppName "NoiseCrypt"
#define Publisher "BREIZHZION"
#define AppURL "https://github.com/bzhzion/noisecrypt"

[Setup]
; Fixed for the lifetime of the product. Changing it makes Windows treat a new version as
; a different application and leaves the old one installed alongside it.
AppId={{7D3F1A62-4C58-4E71-9B0D-2A6E5C8F41B3}
AppName={#AppName}
AppVersion={#AppVersion}
AppVerName={#AppName} {#AppVersion}
AppPublisher={#Publisher}
AppPublisherURL={#AppURL}
AppSupportURL={#AppURL}/issues
AppUpdatesURL={#AppURL}/releases
VersionInfoVersion=0.0.0.0
VersionInfoProductName={#AppName}
VersionInfoCompany={#Publisher}
VersionInfoDescription={#AppName} setup
VersionInfoCopyright=Copyright (c) 2026 {#Publisher}. Licensed under BZ-1.1.

; {autopf} resolves to {localappdata}\Programs when privileges are lowest, which is where
; a per-user program belongs.
DefaultDirName={autopf}\{#AppName}
DefaultGroupName={#AppName}
PrivilegesRequired=lowest
PrivilegesRequiredOverridesAllowed=dialog

; Declared so Explorer is told to refresh its icon and association caches. Without the
; first one a freshly registered .ncry can keep the generic icon until the shell restarts,
; which looks exactly like the registration having failed.
ChangesAssociations=yes
ChangesEnvironment=yes

LicenseFile=..\LICENSE.md
Compression=lzma2/max
SolidCompression=yes
WizardStyle=modern
DisableProgramGroupPage=yes
; One screen fewer for a tool with one file in it. The directory page stays, because
; someone who wants it elsewhere should be able to say so.
DisableWelcomePage=yes

SetupIconFile=..\assets\noisecrypt.ico
UninstallDisplayIcon={app}\noisecrypt.exe
UninstallDisplayName={#AppName}

OutputDir=dist
OutputBaseFilename={#AppName}-{#AppVersion}-{#Arch}-setup

#if Arch == "arm64"
ArchitecturesAllowed=arm64
ArchitecturesInstallIn64BitMode=arm64
#else
; x64compatible rather than x64: an ARM64 machine can run this build under emulation, and
; refusing to install there would be refusing something that works.
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
#endif

[Languages]
Name: "en"; MessagesFile: "compiler:Default.isl"

[Tasks]
Name: "shellintegration"; Description: "Right-click to encrypt, double-click to decrypt"; GroupDescription: "Integration:"
Name: "addtopath"; Description: "Add NoiseCrypt to my PATH, so I can run it from a terminal"; GroupDescription: "Integration:"

[Files]
Source: "{#Binary}"; DestDir: "{app}"; DestName: "noisecrypt.exe"; Flags: ignoreversion
Source: "..\README.md"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\LICENSE.md"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\CHANGELOG.md"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{group}\{#AppName}"; Filename: "{app}\noisecrypt.exe"; Comment: "Open the NoiseCrypt interface"
Name: "{group}\Uninstall {#AppName}"; Filename: "{uninstallexe}"

[Run]
; The binary registers its own shell integration. Synchronous and hidden: the wizard should
; not finish claiming the feature is there before it is.
Filename: "{app}\noisecrypt.exe"; Parameters: "shell register"; Tasks: shellintegration; Flags: runhidden waituntilterminated; StatusMsg: "Registering the right-click and double-click integration..."
Filename: "{app}\noisecrypt.exe"; Description: "Open the {#AppName} interface now"; Flags: nowait postinstall skipifsilent

[UninstallRun]
; Runs before the files are removed, which is the only order that works: unregistering
; needs the program that knows what to unregister.
Filename: "{app}\noisecrypt.exe"; Parameters: "shell unregister"; RunOnceId: "UnregisterShell"; Flags: runhidden waituntilterminated

[Code]

const
  EnvironmentKey = 'Environment';

{ PATH is edited in code rather than with a [Registry] entry, because the obvious entry is
  a trap. A registry entry that appends to Path needs uninsdeletevalue to clean up, and
  uninsdeletevalue deletes the whole value: uninstalling this program would wipe the
  user's entire PATH. So both directions are done by hand, and the removal takes out
  exactly this one directory. }

function PathDirectory(): String;
begin
  Result := ExpandConstant('{app}');
end;

function CurrentPath(var Value: String): Boolean;
begin
  Result := RegQueryStringValue(HKEY_CURRENT_USER, EnvironmentKey, 'Path', Value);
  if not Result then
    Value := '';
end;

{ Compared with semicolons on both sides, so a directory whose name is a prefix of another
  one is not mistaken for it. Without the padding, "...\NoiseCrypt" matches inside
  "...\NoiseCrypt2" and the entry is silently never added. }
function PathContains(const Haystack, Needle: String): Boolean;
begin
  Result := Pos(';' + Lowercase(Needle) + ';', ';' + Lowercase(Haystack) + ';') > 0;
end;

procedure AddToPath();
var
  Value, Directory: String;
begin
  Directory := PathDirectory();
  CurrentPath(Value);
  if PathContains(Value, Directory) then
    Exit;
  if (Value <> '') and (Copy(Value, Length(Value), 1) <> ';') then
    Value := Value + ';';
  RegWriteExpandStringValue(HKEY_CURRENT_USER, EnvironmentKey, 'Path', Value + Directory);
end;

procedure RemoveFromPath();
var
  Value, Directory: String;
  P: Integer;
begin
  Directory := PathDirectory();
  if not CurrentPath(Value) then
    Exit;
  P := Pos(';' + Lowercase(Directory) + ';', ';' + Lowercase(Value) + ';');
  if P = 0 then
    Exit;
  { P indexes the padded string, so it is one ahead of the real position. }
  Delete(Value, P, Length(Directory) + 1);
  { A leading or trailing semicolon left behind is harmless but untidy, and an empty
    element in PATH is read by some tools as the current directory. }
  if Copy(Value, 1, 1) = ';' then
    Delete(Value, 1, 1);
  if (Value <> '') and (Copy(Value, Length(Value), 1) = ';') then
    Delete(Value, Length(Value), 1);
  RegWriteExpandStringValue(HKEY_CURRENT_USER, EnvironmentKey, 'Path', Value);
end;

procedure CurStepChanged(CurStep: TSetupStep);
begin
  if (CurStep = ssPostInstall) and WizardIsTaskSelected('addtopath') then
    AddToPath();
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
begin
  if CurUninstallStep = usUninstall then
    RemoveFromPath();
end;

{ What uninstalling deliberately does not touch: the identities in the user's profile.
  Those are private keys. A program that deletes them on uninstall destroys the only copy
  of the means to open every container the user has ever been sent, and does it as a side
  effect of a routine action. They stay, and they are the user's to remove. }
