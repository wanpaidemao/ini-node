@echo off
rem ============================================================
rem  Sugarchain node launcher (original dir / port / config)
rem  - CWD:    C:\Users\adest\Desktop\git\Mimo\apiserver\new\sugarchain-node\backend
rem  - Config:  btcd-runtime.ini (datadir=...AppData\Local\Btcd,
rem             rpclisten=127.0.0.1:8334, headerwindow=50000,
rem             rpcuser/rpcpass=sugar, addcheckpoint=43760164:5765dce5...)
rem  - RPC:     127.0.0.1:8334   (profile: 6000)
rem  - Logs:    logs\node.stdout.log / logs\node.stderr.log
rem ============================================================
cd /d C:\Users\adest\Desktop\git\Mimo\apiserver\new\sugarchain-node\backend

rem Launch ini in a new window, detached from this shell.
start "ini-node" ini.exe --configfile=btcd-runtime.ini

exit 0
