@echo off
cd /d C:\Users\adest\Desktop\git\Mimo\apiserver\new\sugarchain-node\backend
start /b btcd.exe --configfile=btcd-runtime.ini >> logs\node.stdout.log 2>> logs\node.stderr.log
exit 0
