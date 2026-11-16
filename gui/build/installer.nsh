; A per-user task starts the daemon outside the window's job object. An
; uninstall removes the tasks it may have left behind, for both channels.

!macro customUnInstall
  ExecWait '"$SYSDIR\schtasks.exe" /delete /tn "\Flotilla\daemon-stable" /f'
  ExecWait '"$SYSDIR\schtasks.exe" /delete /tn "\Flotilla\daemon-preview" /f'
!macroend
