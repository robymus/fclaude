# Brief goal summary

I want to run Claude Code in a sandbox VM, in yolo mode, giving free access to install tools and operate, but in a safe way.

# Details

## Infrastructure

We should have an Ubuntu VM, with Claude in it and all necessary integrations. I'm happy to go with system containers or full VMs, lxd/incus/qemu/kvm, or other options, please give me recommendations based on all the requirements. Host OS is MX Linux - it's this machine, feel free to explore capabilities.

## Virtual disk

Let's have a loopback file at ~/.fclaude/fclaude-disk, with initial size of 32GB. Install script should create this. Also provide a mechanism to increase this via a script, that handles physical file increase and file system growing as well. Choose a filesystem that supports this easily.

## Workspace sharing

Claude in the container should have it's home directory /home/fclaude (run as user fclaude)

The important part is how I will share workspaces. Claude in the container will be free to manage the VM, will have sudo access, do anything.

I will have projects hosted on the host machine, and when I want to work on something, I want to mount this into the container, under /home/fclaude.

After the work session, I want to unmount it.

Please give recommendations how it can be done, it's okay to install things on the host machine (eg. nfs server, or something)

Default mounting path: $HOME/path1/path2 should be mounted as /home/fclaude/path1/path2

Let's have a config in ~/.fclaude/config, where we set can change this.

## Networking

Container should be able to access the internet and unprivileged ports on the host machine. No access to privileged port on host machine should be enforced by firewall rules on the host.

## Wrapper

I want to have a wrapper on the host machine, that:
- start the vm if not already running
- mounts the working directory in the container
  - this should be reference counted, in case multiple instances are running
- runs claude in the container in the mounted part of the current directory (this can be as simple as running it in ssh)
- unmounts the working directory when exited

By default, the directory to mount should be the current working directory. Extra arguments to skip mounting, or specify the directory to mount.

Wrapper shouldn't stop the VM, but provide an extra argument that can trigger VM shutdown, with confirmation prompt.

I will use this from the cli.

Additionally, I'm also using claude code from VS Code extension, so let's create a Claude Process Wrapper for that as well, or make sure that the fclaude wrapper is compatible. It's okay for this to be a separate one, or a thin wrapper around fclaude - in this case, call it fclaude-vscode

Wrapper can be a simple shell script, if suitable, or if more complexity is needed, I prefer to use Go.

## Install

Let's have an install and an uninstall script.
Install should create the virtual machine (with a new ubuntu, fclaude user, claude installed, ready to go), set up network, firewalling, install the wrapper scripts in, create a config template.

Uninstall script should undo everything. Also ask for a lot of confirmation and if everything had been backed up correctly.

Install should check if the ~/.fclaude/fclaude-disk already exist, give a warning if it is, and ask if this is a restore from backup operation, if yes, continue setting up other things (eg. vm, etc.), but don't touch the disk. If no, simply exit. User must explicitly delete it to create a new one, script should never delete.

Install Ansible on the VM as first step of setup, and use Ansible for subsequent configurations and installs.

Put all identifying details of the VM after installing in ~/.fclaude/vm, for example, id, networking, ip address.

Install scripts in ~/.local/bin

## Headless or not?

Claude in the VM sould be able to run Chrome (or other browser) with automation, so it can look at results and screenshot things. This script and installer don't have to install the browser, but the infrastructure take this into consideration.

## Maintenance shell access

Create a way to access the VM for maintenance.
It can be as simple as ssh into the VM with a maintenance user, which has sudo privileges.
Add an extra argument to the fclaude wrapper for the maintenance access.