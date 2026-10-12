// Copyright 2026 The gVisor Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"fmt"
	"slices"

	elasticarch "github.com/elastic/go-seccomp-bpf/arch"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/abi/linux"
	"gvisor.dev/gvisor/pkg/bpf"
	"gvisor.dev/gvisor/pkg/seccomp"
)

// evalCase is one seccomp_data input fed to each filter in the interpreter.
type evalCase struct {
	name string
	data linux.SeccompData
}

func x86_64(nr uintptr, args ...uint64) linux.SeccompData {
	d := linux.SeccompData{Nr: int32(nr), Arch: linux.AUDIT_ARCH_X86_64}
	copy(d.Args[:], args)
	return d
}

var evalCases = []evalCase{
	{"getpid", x86_64(unix.SYS_GETPID)},
	{"read", x86_64(unix.SYS_READ)},
	{"write", x86_64(unix.SYS_WRITE)},
	{"futex", x86_64(unix.SYS_FUTEX)},
	{"personality(0)", x86_64(unix.SYS_PERSONALITY, 0)},
	{"personality(1) [deny]", x86_64(unix.SYS_PERSONALITY, 1)},
	{"socket(AF_UNIX)", x86_64(unix.SYS_SOCKET, unix.AF_UNIX)},
	{"socket(AF_VSOCK) [deny]", x86_64(unix.SYS_SOCKET, unix.AF_VSOCK)},
	{"clone(SIGCHLD)", x86_64(unix.SYS_CLONE, uint64(unix.SIGCHLD))},
	{"clone(CLONE_NEWUSER) [deny]", x86_64(unix.SYS_CLONE, unix.CLONE_NEWUSER)},
	{"clone3 [ENOSYS]", x86_64(unix.SYS_CLONE3)},
	{"acct [deny]", x86_64(unix.SYS_ACCT)},
	{"nr 1000 [unknown]", x86_64(1000)},
	{"i386 getpid", linux.SeccompData{Nr: 20, Arch: uint32(elasticarch.I386.ID)}},
	{"x32 getpid", linux.SeccompData{Nr: unix.SYS_GETPID | 0x40000000, Arch: linux.AUDIT_ARCH_X86_64}},
}

// eval runs insns on d in gVisor's BPF interpreter.
func eval(insns []unix.SockFilter, d linux.SeccompData) (ret uint32, executed int) {
	in := make([]bpf.Instruction, len(insns))
	for i, x := range insns {
		in[i] = bpf.Instruction{OpCode: x.Code, JumpIfTrue: x.Jt, JumpIfFalse: x.Jf, K: x.K}
	}
	p, err := bpf.Compile(in, false /* optimize */)
	if err != nil {
		panic(err)
	}
	buf := make([]byte, d.SizeBytes())
	m, err := bpf.InstrumentedExec[bpf.NativeEndian](p, seccomp.DataAsBPFInput(&d, buf))
	if err != nil {
		panic(err)
	}
	for _, c := range m.Coverage {
		if c {
			executed++
		}
	}
	return m.ReturnValue, executed
}

func actionString(ret uint32) string {
	data := ret & unix.SECCOMP_RET_DATA
	switch ret & unix.SECCOMP_RET_ACTION_FULL {
	case unix.SECCOMP_RET_ALLOW:
		return "ALLOW"
	case unix.SECCOMP_RET_ERRNO:
		return fmt.Sprintf("ERRNO(%s)", unix.ErrnoName(unix.Errno(data)))
	case unix.SECCOMP_RET_KILL_THREAD:
		return "KILL_THREAD"
	case unix.SECCOMP_RET_KILL_PROCESS:
		return "KILL_PROCESS"
	case unix.SECCOMP_RET_TRAP:
		return "TRAP"
	case unix.SECCOMP_RET_TRACE:
		return "TRACE"
	case unix.SECCOMP_RET_LOG:
		return "LOG"
	}
	return fmt.Sprintf("%#x", ret)
}

// reportSemantics prints the action and executed instruction count of each
// filter for evalCases and for every unconditionally allowed syscall.
func reportSemantics(s *specs.LinuxSeccomp) {
	type prog struct {
		name  string
		insns []unix.SockFilter
	}
	var progs []prog
	for _, v := range variants {
		if v.program == nil {
			continue
		}
		insns, err := v.program(s)
		if err != nil {
			panic(fmt.Sprintf("%s: %v", v.name, err))
		}
		progs = append(progs, prog{v.name, insns})
	}

	fmt.Printf("## Filter decisions and BPF instructions executed\n\n")
	fmt.Printf("Each cell: action / instructions executed, from gVisor's instrumented BPF interpreter.\n\n| input |")
	for _, p := range progs {
		fmt.Printf(" %s |", p.name)
	}
	fmt.Printf("\n|---|%s\n", repeat("---|", len(progs)))
	for _, c := range evalCases {
		fmt.Printf("| %s |", c.name)
		for _, p := range progs {
			ret, n := eval(p.insns, c.data)
			fmt.Printf(" %s / %d |", actionString(ret), n)
		}
		fmt.Println()
	}

	// Syscalls allowed without argument checks. Kernels >= 5.11 skip the
	// filter for these (seccomp action cache); older kernels run it.
	var nrs []uintptr
	for _, r := range s.Syscalls {
		if r.Action != specs.ActAllow || len(r.Args) != 0 {
			continue
		}
		for _, name := range r.Names {
			if nr, ok := elasticarch.X86_64.SyscallNames[name]; ok {
				nrs = append(nrs, uintptr(nr))
			}
		}
	}
	fmt.Printf("\nInstructions executed over the %d unconditionally allowed x86_64 syscalls (the cost without the kernel action cache):\n\n", len(nrs))
	fmt.Printf("| variant | mean | median | max | not allowed |\n|---|---|---|---|---|\n")
	notAllowed := map[string][]string{}
	for _, p := range progs {
		var ns []int
		sum, denied := 0, 0
		for _, nr := range nrs {
			ret, n := eval(p.insns, x86_64(nr))
			if ret != unix.SECCOMP_RET_ALLOW {
				denied++
				notAllowed[p.name] = append(notAllowed[p.name], fmt.Sprintf("%s:%s", elasticarch.X86_64.SyscallNumbers[int(nr)], actionString(ret)))
			}
			ns = append(ns, n)
			sum += n
		}
		slices.Sort(ns)
		fmt.Printf("| %s | %.1f | %d | %d | %d |\n", p.name, float64(sum)/float64(len(ns)), ns[len(ns)/2], ns[len(ns)-1], denied)
	}
	fmt.Println()
	for _, p := range progs {
		if len(notAllowed[p.name]) > 0 {
			fmt.Printf("- %s does not allow: %v\n", p.name, notAllowed[p.name])
		}
	}

	fmt.Println()
}
