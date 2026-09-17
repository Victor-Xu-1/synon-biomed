//go:build linux && amd64

package kernel

import "golang.org/x/sys/unix"

// buildKernelSyscallFilter expresses the process policy through Linux's public
// seccomp ABI. Mounts, namespace isolation and network grants remain separate
// host controls; a syscall filter is not itself a complete sandbox.
func buildKernelSyscallFilter() []unix.SockFilter {
	load := uint16(unix.BPF_LD | unix.BPF_W | unix.BPF_ABS)
	equal := uint16(unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K)
	result := uint16(unix.BPF_RET | unix.BPF_K)
	denied := uint32(unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM))
	program := []unix.SockFilter{
		{Code: load, K: 4}, // seccomp_data.arch
		{Code: equal, K: unix.AUDIT_ARCH_X86_64, Jt: 1},
		{Code: result, K: unix.SECCOMP_RET_KILL_PROCESS},
		{Code: load, K: 0}, // seccomp_data.nr
		{Code: unix.BPF_JMP | unix.BPF_JGE | unix.BPF_K, K: 0x40000000, Jf: 2},
		{Code: equal, K: 0xffffffff, Jt: 1}, // preserve the invalid-syscall ENOSYS probe
		{Code: result, K: unix.SECCOMP_RET_KILL_PROCESS},
	}
	// Disallow process inspection, cross-process memory/fd access, kernel
	// keyrings and asynchronous syscall submission outside this filter.
	for _, number := range []uint32{
		unix.SYS_PTRACE, unix.SYS_REMAP_FILE_PAGES,
		unix.SYS_ADD_KEY, unix.SYS_REQUEST_KEY, unix.SYS_KEYCTL,
		unix.SYS_PROCESS_VM_READV, unix.SYS_PROCESS_VM_WRITEV,
		unix.SYS_IO_URING_SETUP, unix.SYS_IO_URING_ENTER, unix.SYS_IO_URING_REGISTER,
		unix.SYS_PIDFD_GETFD,
	} {
		program = append(program,
			unix.SockFilter{Code: equal, K: number, Jf: 1},
			unix.SockFilter{Code: result, K: denied})
	}
	program = append(program,
		unix.SockFilter{Code: equal, K: unix.SYS_SOCKET, Jt: 1},
		unix.SockFilter{Code: result, K: unix.SECCOMP_RET_ALLOW},
		unix.SockFilter{Code: load, K: 16}, // low word of seccomp_data.args[0]
		unix.SockFilter{Code: equal, K: unix.AF_UNIX, Jf: 1},
		unix.SockFilter{Code: result, K: denied},
		unix.SockFilter{Code: equal, K: unix.AF_NETLINK, Jf: 1},
		unix.SockFilter{Code: result, K: denied},
		unix.SockFilter{Code: result, K: unix.SECCOMP_RET_ALLOW})
	return program
}
