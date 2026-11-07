// Package procjob ties child processes' lifetime to ours.
//
// The VS Code extension ends the wrapper with TerminateProcess, which runs no
// cleanup: any child (ssh.exe, a native claude.exe in capture mode) would be
// orphaned. On Windows the child is put in a Job Object with
// KILL_ON_JOB_CLOSE; the job handle is held until we exit, at which point the
// kernel closes it and kills the job. On Linux (CI) the parent-death signal
// gives the same guarantee.
//
// Usage: Prepare(cmd) before cmd.Start(), Attach(cmd) right after.
package procjob
