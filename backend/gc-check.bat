@echo off
REM GC check: sample pprof heap stats every run, append to gc-monitor.log.
echo === %date% %time% === >> ..\dev_doc\gc-monitor.log
curl -s --max-time 10 "http://localhost:6000/debug/pprof/heap?debug=1" | findstr /C:"# Alloc" /C:"# NumGC" /C:"# GCCPUFraction" /C:"# NextGC" /C:"# PauseTotalNs" >> ..\dev_doc\gc-monitor.log
echo. >> ..\dev_doc\gc-monitor.log
