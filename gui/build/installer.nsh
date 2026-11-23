; The daemon and its hosts outlive the app that spawned them: killing the
; window does not stop them, so an upgrade or uninstall that only closed the
; window left a daemon serving an install directory about to be replaced or
; removed - every later start failed with a path error. These macros stop the
; processes that belong to THIS install directory (both channels can be
; installed side by side; killing by image name would take the other channel's
; daemon down too).
!macro stopThisInstallDaemon
  nsExec::ExecToLog `powershell -NoProfile -ExecutionPolicy Bypass -Command "Get-Process 'flt-daemon','flt-session-host','flt-replay-agent' -ErrorAction SilentlyContinue | Where-Object { try { $$_.Path -like '$INSTDIR*' } catch { $$false } } | Stop-Process -Force"`
!macroend

!macro customInit
  !insertmacro stopThisInstallDaemon
!macroend

!macro customUnInit
  !insertmacro stopThisInstallDaemon
!macroend
