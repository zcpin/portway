// 仅匹配显式绝对路径，保留其他安装、相似前缀以及无法确定含义的命令。
function IsAbsoluteStartupPath(const Path: String): Boolean;
begin
  Result := False;
  if Length(Path) < 3 then Exit;
  if (Path[1] = '\') and (Path[2] = '\') then begin
    Result := True;
    Exit;
  end;
  Result := (Path[2] = ':') and (Path[3] = '\') and
    (((Path[1] >= 'A') and (Path[1] <= 'Z')) or
     ((Path[1] >= 'a') and (Path[1] <= 'z')));
end;

function StartupCommandTargetsExecutable(CommandLine, InstalledExe: String): Boolean;
var
  Executable, Tail: String;
  Boundary, Cursor: Integer;
begin
  Result := False;
  CommandLine := Trim(CommandLine);
  if Length(CommandLine) = 0 then Exit;

  if CommandLine[1] = '"' then begin
    Boundary := Pos('"', Copy(CommandLine, 2, Length(CommandLine)));
    if Boundary = 0 then Exit;
    Executable := Copy(CommandLine, 2, Boundary - 1);
    Tail := Copy(CommandLine, Boundary + 2, Length(CommandLine));
    if Length(Tail) > 0 then
      if (Tail[1] <> ' ') and (Tail[1] <> #9) then Exit;
  end else begin
    Cursor := 1;
    while Cursor <= Length(CommandLine) do begin
      if (CommandLine[Cursor] = ' ') or (CommandLine[Cursor] = #9) then Break;
      Cursor := Cursor + 1;
    end;
    Executable := Copy(CommandLine, 1, Cursor - 1);
  end;

  StringChangeEx(Executable, '/', '\', True);
  StringChangeEx(InstalledExe, '/', '\', True);
  if not IsAbsoluteStartupPath(Executable) then Exit;
  if not IsAbsoluteStartupPath(InstalledExe) then Exit;
  try
    Result := CompareText(ExpandFileName(Executable), ExpandFileName(InstalledExe)) = 0;
  except
    Result := False;
  end;
end;

procedure RemoveInstalledAutostart(const InstalledExe: String);
var
  CommandLine: String;
begin
  if RegQueryStringValue(HKCU, 'Software\Microsoft\Windows\CurrentVersion\Run',
      'ssh-tunnel-daemon', CommandLine) then begin
    if StartupCommandTargetsExecutable(CommandLine, InstalledExe) then begin
      if RegDeleteValue(HKCU, 'Software\Microsoft\Windows\CurrentVersion\Run',
          'ssh-tunnel-daemon') then
        Log('Removed autostart entry for this installation')
      else
        Log('Unable to remove autostart entry for this installation');
    end;
  end;
end;
