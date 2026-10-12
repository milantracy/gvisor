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
	"unsafe"

	elastic "github.com/elastic/go-seccomp-bpf"
	elasticarch "github.com/elastic/go-seccomp-bpf/arch"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	xbpf "golang.org/x/net/bpf"
	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/bpf"
	ociseccomp "gvisor.dev/gvisor/runsc/specutils/seccomp"
)

func unsafePointer(p *unix.SockFprog) unsafe.Pointer { return unsafe.Pointer(p) }

// gvisorProgram uses the OCI converter runsc ships, unchanged.
func gvisorProgram(s *specs.LinuxSeccomp) ([]unix.SockFilter, error) {
	p, err := ociseccomp.BuildProgram(s)
	if err != nil {
		return nil, err
	}
	// bpf.Program has one unexported field: its instructions.
	insns := *(*[]bpf.Instruction)(unsafe.Pointer(&p))
	out := make([]unix.SockFilter, len(insns))
	for i, in := range insns {
		out[i] = unix.SockFilter{Code: in.OpCode, Jt: in.JumpIfTrue, Jf: in.JumpIfFalse, K: in.K}
	}
	return out, nil
}

// elasticProgram converts the OCI profile to an elastic Policy.
func elasticProgram(s *specs.LinuxSeccomp) ([]unix.SockFilter, error) {
	info, err := elasticarch.GetInfo("")
	if err != nil {
		return nil, err
	}
	def, err := elasticAction(s.DefaultAction, s.DefaultErrnoRet)
	if err != nil {
		return nil, err
	}
	// Policy.Validate rejects action data, so the errno is patched in below.
	p := elastic.Policy{DefaultAction: def &^ unix.SECCOMP_RET_DATA}
	for _, r := range s.Syscalls {
		act, err := elasticAction(r.Action, r.ErrnoRet)
		if err != nil {
			return nil, err
		}
		conds, err := elasticConditions(r.Args)
		if err != nil {
			return nil, err
		}
		g := elastic.SyscallGroup{Action: act}
		for _, name := range r.Names {
			// Skip names unknown on this arch, as runc does.
			if _, ok := info.SyscallNames[name]; !ok {
				continue
			}
			if len(conds) == 0 {
				g.Names = append(g.Names, name)
			} else {
				g.NamesWithCondtions = append(g.NamesWithCondtions, elastic.NameWithConditions{Name: name, Conditions: conds})
			}
		}
		p.Syscalls = append(p.Syscalls, g)
	}
	insns, err := p.Assemble()
	if err != nil {
		return nil, err
	}
	if def != p.DefaultAction {
		for i, in := range insns {
			if r, ok := in.(xbpf.RetConstant); ok && r.Val == uint32(p.DefaultAction) {
				insns[i] = xbpf.RetConstant{Val: uint32(def)}
			}
		}
	}
	raw, err := xbpf.Assemble(insns)
	if err != nil {
		return nil, err
	}
	out := make([]unix.SockFilter, len(raw))
	for i, in := range raw {
		out[i] = unix.SockFilter{Code: in.Op, Jt: in.Jt, Jf: in.Jf, K: in.K}
	}
	return out, nil
}

func elasticAction(a specs.LinuxSeccompAction, errnoRet *uint) (elastic.Action, error) {
	switch a {
	case specs.ActAllow:
		return elastic.ActionAllow, nil
	case specs.ActErrno:
		errno := uint(unix.EPERM)
		if errnoRet != nil {
			errno = *errnoRet
		}
		return elastic.ActionErrno | elastic.Action(errno), nil
	case specs.ActKill, specs.ActKillThread:
		return elastic.ActionKillThread, nil
	case specs.ActKillProcess:
		return elastic.ActionKillProcess, nil
	case specs.ActTrap:
		return elastic.ActionTrap, nil
	case specs.ActLog:
		return elastic.ActionLog, nil
	}
	return 0, fmt.Errorf("elastic: unsupported action %s", a)
}

// elasticConditions ANDs the args; containerd never repeats an index.
func elasticConditions(args []specs.LinuxSeccompArg) (elastic.ArgumentConditions, error) {
	var conds elastic.ArgumentConditions
	seen := map[uint]bool{}
	for _, a := range args {
		if seen[a.Index] {
			return nil, fmt.Errorf("elastic: repeated arg index %d", a.Index)
		}
		seen[a.Index] = true
		c := elastic.Condition{Argument: uint32(a.Index), Value: a.Value}
		switch a.Op {
		case specs.OpEqualTo:
			c.Operation = elastic.Equal
		case specs.OpNotEqual:
			c.Operation = elastic.NotEqual
		case specs.OpGreaterThan:
			c.Operation = elastic.GreaterThan
		case specs.OpGreaterEqual:
			c.Operation = elastic.GreaterOrEqual
		case specs.OpLessThan:
			c.Operation = elastic.LessThan
		case specs.OpLessEqual:
			c.Operation = elastic.LessOrEqual
		case specs.OpMaskedEqual:
			// Only (arg & mask) == 0 maps to elastic.
			if a.ValueTwo != 0 {
				return nil, fmt.Errorf("elastic: unsupported MASKED_EQ value %#x", a.ValueTwo)
			}
			c.Operation = elastic.BitsNotSet
		default:
			return nil, fmt.Errorf("elastic: unsupported op %s", a.Op)
		}
		conds = append(conds, c)
	}
	return conds, nil
}
