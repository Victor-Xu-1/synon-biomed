//go:build linux && amd64

package kernel

import (
	"testing"

	"golang.org/x/sys/unix"
)

// Interpret the small emitted instruction vocabulary against synthetic syscall
// metadata. Real child-process namespace and socket tests exercise installation.
func evaluateKernelFilter(t *testing.T, arch, number, argument uint32) uint32 {
	t.Helper()
	var accumulator uint32
	for pc := 0; pc < len(synonKernelFilter); pc++ {
		instruction := synonKernelFilter[pc]
		switch instruction.Code {
		case unix.BPF_LD | unix.BPF_W | unix.BPF_ABS:
			switch instruction.K {
			case 0:
				accumulator = number
			case 4:
				accumulator = arch
			case 16:
				accumulator = argument
			default:
				t.Fatalf("invalid seccomp metadata offset %d", instruction.K)
			}
		case unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K:
			if accumulator == instruction.K {
				pc += int(instruction.Jt)
			} else {
				pc += int(instruction.Jf)
			}
		case unix.BPF_JMP | unix.BPF_JGE | unix.BPF_K:
			if accumulator >= instruction.K {
				pc += int(instruction.Jt)
			} else {
				pc += int(instruction.Jf)
			}
		case unix.BPF_RET | unix.BPF_K:
			return instruction.K
		default:
			t.Fatalf("unexpected filter instruction %d", instruction.Code)
		}
	}
	t.Fatal("filter did not reach a terminal policy decision")
	return 0
}

func TestKernelSyscallFilterHasExplicitSecurityBoundaries(t *testing.T) {
	for _, number := range []uint32{unix.SYS_PTRACE, unix.SYS_REMAP_FILE_PAGES,
		unix.SYS_ADD_KEY, unix.SYS_REQUEST_KEY, unix.SYS_KEYCTL,
		unix.SYS_PROCESS_VM_READV, unix.SYS_PROCESS_VM_WRITEV,
		unix.SYS_IO_URING_SETUP, unix.SYS_IO_URING_ENTER, unix.SYS_IO_URING_REGISTER, unix.SYS_PIDFD_GETFD} {
		if got := evaluateKernelFilter(t, unix.AUDIT_ARCH_X86_64, number, 0); got != unix.SECCOMP_RET_ERRNO|uint32(unix.EPERM) {
			t.Errorf("syscall %d escaped denied policy: %x", number, got)
		}
	}
	for _, family := range []uint32{unix.AF_UNIX, unix.AF_NETLINK} {
		if got := evaluateKernelFilter(t, unix.AUDIT_ARCH_X86_64, unix.SYS_SOCKET, family); got != unix.SECCOMP_RET_ERRNO|uint32(unix.EPERM) {
			t.Errorf("socket family %d escaped denied policy: %x", family, got)
		}
	}
	for _, family := range []uint32{unix.AF_INET, unix.AF_INET6} {
		if got := evaluateKernelFilter(t, unix.AUDIT_ARCH_X86_64, unix.SYS_SOCKET, family); got != unix.SECCOMP_RET_ALLOW {
			t.Errorf("network namespace transport family %d disabled: %x", family, got)
		}
	}
	for _, number := range []uint32{unix.SYS_READ, unix.SYS_WRITE, unix.SYS_CLONE, unix.SYS_EXECVE, 0xffffffff} {
		if got := evaluateKernelFilter(t, unix.AUDIT_ARCH_X86_64, number, 0); got != unix.SECCOMP_RET_ALLOW {
			t.Errorf("ordinary execution syscall %d disabled: %x", number, got)
		}
	}
	if evaluateKernelFilter(t, unix.AUDIT_ARCH_I386, unix.SYS_READ, 0) != unix.SECCOMP_RET_KILL_PROCESS ||
		evaluateKernelFilter(t, unix.AUDIT_ARCH_X86_64, 0x40000000, 0) != unix.SECCOMP_RET_KILL_PROCESS {
		t.Fatal("unsupported syscall ABI was admitted")
	}
}
