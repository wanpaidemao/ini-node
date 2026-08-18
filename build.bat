@echo off
REM Unified build: build backend (btcd/walletapi) and control center,
REM copy backend artifacts into frontend\bin so everything runs from one dir.
setlocal
set ROOT=%~dp0

echo [1/4] Building backend: btcd.exe ...
cd /d "%ROOT%backend"
go build -o ..\frontend\bin\btcd.exe . || goto :err

echo [2/4] Building backend: walletapi.exe ...
go build -o ..\frontend\bin\walletapi.exe .\cmd\walletapi\ || goto :err

echo [3/4] Building control center: ini-node.exe (wails3) ...
cd /d "%ROOT%frontend"
wails3 build || goto :err

echo [4/4] Verifying artifacts in frontend\bin ...
if exist "%ROOT%frontend\bin\btcd.exe" (
  echo   OK: frontend\bin\btcd.exe
) else (
  echo   MISSING: frontend\bin\btcd.exe
)
if exist "%ROOT%frontend\bin\walletapi.exe" (
  echo   OK: frontend\bin\walletapi.exe
) else (
  echo   MISSING: frontend\bin\walletapi.exe
)
if exist "%ROOT%frontend\bin\ini-node.exe" (
  echo   OK: frontend\bin\ini-node.exe
) else (
  echo   MISSING: frontend\bin\ini-node.exe
)
echo Build complete - backend artifacts copied into frontend\bin.
exit /b 0

:err
echo Build FAILED - see errors above.
exit /b 1
