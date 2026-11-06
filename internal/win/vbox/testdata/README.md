Real `VBoxManage showvminfo --machinereadable` captures from VirtualBox 7.2.22 on
Windows 11 (2026-11-06), of a throwaway VM with an empty disk. Sanitized: user
name → `bro`, VM name → `fclaude`. `showvminfo-off-crlf.txt` keeps the native
CRLF line endings (`.gitattributes` marks it `-text`); the running capture has
two transient shares, one with a space and a non-ASCII character.
