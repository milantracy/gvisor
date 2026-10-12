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

// Binary seccomp-bench compares seccomp filters that gVisor pkg/seccomp,
// elastic/go-seccomp-bpf and libseccomp (as used by runc) generate from the
// containerd default OCI profile.
package main

import (
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"time"

	specs "github.com/opencontainers/runtime-spec/specs-go"
	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/log"
)

// profile.json is containerd's contrib/seccomp.DefaultProfile with the
// default containerd capability set.
//
//go:embed profile.json
var profileJSON []byte

func loadProfile() *specs.LinuxSeccomp {
	var s specs.LinuxSeccomp
	if err := json.Unmarshal(profileJSON, &s); err != nil {
		panic(err)
	}
	return &s
}

// variant is one way to turn an OCI profile into an installed filter.
type variant struct {
	name string
	// program returns the BPF program without installing it.
	program func(*specs.LinuxSeccomp) ([]unix.SockFilter, error)
	// install builds the filter and installs it on the calling thread.
	install func(*specs.LinuxSeccomp) error
}

var variants = []variant{
	{name: "none", install: func(*specs.LinuxSeccomp) error { return nil }},
	{name: "gvisor", program: gvisorProgram, install: installWith(gvisorProgram)},
	{name: "elastic", program: elasticProgram, install: installWith(elasticProgram)},
}

func findVariant(name string) (variant, bool) {
	for _, v := range variants {
		if v.name == name {
			return v, true
		}
	}
	return variant{}, false
}

func installWith(program func(*specs.LinuxSeccomp) ([]unix.SockFilter, error)) func(*specs.LinuxSeccomp) error {
	return func(s *specs.LinuxSeccomp) error {
		insns, err := program(s)
		if err != nil {
			return err
		}
		return loadFilter(insns)
	}
}

// loadFilter installs insns on the calling thread only, as runc does.
func loadFilter(insns []unix.SockFilter) error {
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	prog := unix.SockFprog{Len: uint16(len(insns)), Filter: &insns[0]}
	return unix.Prctl(unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafePointer(&prog)), 0, 0)
}

// target is a syscall timed in the child.
type target struct {
	name string
	nr   uintptr
	args [6]uintptr
}

var targets = []target{
	// Allowed without argument checks: served by the kernel action cache.
	{name: "getpid", nr: unix.SYS_GETPID},
	// Allowed after an argument check: runs the filter.
	{name: "personality(0)", nr: unix.SYS_PERSONALITY},
	// Not in the profile: runs the filter to the default action.
	{name: "acct(NULL)", nr: unix.SYS_ACCT},
}

type childResult struct {
	Variant string
	SetupNs int64
	// NsPerOp maps a target name to ns/op for each round.
	NsPerOp map[string][]float64
}

func init() {
	// Keep main on one thread: filters are installed per thread.
	runtime.LockOSThread()
}

func main() {
	if len(os.Args) > 2 && os.Args[1] == "child" {
		runChild(os.Args[2])
		return
	}
	reps := flag.Int("reps", 10, "child processes per variant")
	iters := flag.Int("iters", 2_000_000, "syscalls per round")
	rounds := flag.Int("rounds", 5, "rounds per child")
	builds := flag.Int("builds", 50, "filter builds for build-time measurement")
	flag.Parse()
	if os.Geteuid() == 0 {
		fmt.Fprintln(os.Stderr, "run as non-root: acct(NULL) must fail with EPERM")
		os.Exit(1)
	}
	log.SetTarget(log.GoogleEmitter{Writer: &log.Writer{Next: io.Discard}})

	s := loadProfile()
	reportEnv(s)
	reportSemantics(s)
	reportBuild(s, *builds)
	reportRuntime(*reps, *iters, *rounds)
}

func runChild(name string) {
	log.SetTarget(log.GoogleEmitter{Writer: &log.Writer{Next: io.Discard}})
	v, ok := findVariant(name)
	if !ok {
		panic("unknown variant " + name)
	}
	var iters, rounds int
	fmt.Sscan(os.Getenv("BENCH_ITERS"), &iters)
	fmt.Sscan(os.Getenv("BENCH_ROUNDS"), &rounds)
	s := loadProfile()

	start := time.Now()
	if err := v.install(s); err != nil {
		panic(err)
	}
	res := childResult{Variant: name, SetupNs: time.Since(start).Nanoseconds(), NsPerOp: map[string][]float64{}}

	for _, t := range targets {
		run := func(n int) {
			for i := 0; i < n; i++ {
				unix.RawSyscall6(t.nr, t.args[0], t.args[1], t.args[2], t.args[3], t.args[4], t.args[5])
			}
		}
		run(iters / 10)
		for r := 0; r < rounds; r++ {
			start := time.Now()
			run(iters)
			res.NsPerOp[t.name] = append(res.NsPerOp[t.name], float64(time.Since(start).Nanoseconds())/float64(iters))
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(res); err != nil {
		panic(err)
	}
}

func reportEnv(s *specs.LinuxSeccomp) {
	var uname unix.Utsname
	unix.Uname(&uname)
	fmt.Printf("## Environment\n\n")
	fmt.Printf("- kernel: %s\n- go: %s %s/%s, GOMAXPROCS=%d\n", unix.ByteSliceToString(uname.Release[:]), runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.GOMAXPROCS(0))
	fmt.Printf("- libseccomp: %s\n", libseccompVersion())
	n := 0
	for _, r := range s.Syscalls {
		n += len(r.Names)
	}
	fmt.Printf("- profile: %d rules, %d syscall names, arches %v, default %s\n\n", len(s.Syscalls), n, s.Architectures, s.DefaultAction)
}

// reportBuild times building each filter without installing it.
func reportBuild(s *specs.LinuxSeccomp, builds int) {
	fmt.Printf("## Filter build (no install), %d builds\n\n", builds)
	fmt.Printf("| variant | instructions | median build | min build |\n|---|---|---|---|\n")
	for _, v := range variants {
		if v.program == nil {
			continue
		}
		var ds []time.Duration
		var n int
		for i := 0; i < builds; i++ {
			start := time.Now()
			insns, err := v.program(s)
			if err != nil {
				panic(fmt.Sprintf("%s: %v", v.name, err))
			}
			ds = append(ds, time.Since(start))
			n = len(insns)
		}
		slices.Sort(ds)
		fmt.Printf("| %s | %d | %v | %v |\n", v.name, n, ds[len(ds)/2].Round(time.Microsecond), ds[0].Round(time.Microsecond))
	}
	fmt.Println()
}

// reportRuntime runs each variant in fresh child processes, interleaved.
func reportRuntime(reps, iters, rounds int) {
	self, err := os.Executable()
	if err != nil {
		panic(err)
	}
	all := map[string][]childResult{}
	for r := 0; r < reps; r++ {
		for _, v := range variants {
			cmd := exec.Command(self, "child", v.name)
			cmd.Env = append(os.Environ(), fmt.Sprintf("BENCH_ITERS=%d", iters), fmt.Sprintf("BENCH_ROUNDS=%d", rounds))
			cmd.Stderr = os.Stderr
			out, err := cmd.Output()
			if err != nil {
				panic(fmt.Sprintf("child %s: %v", v.name, err))
			}
			var res childResult
			if err := json.Unmarshal(out, &res); err != nil {
				panic(err)
			}
			all[v.name] = append(all[v.name], res)
		}
	}

	fmt.Printf("## Syscall cost with filter installed\n\n")
	fmt.Printf("%d processes per variant, %d rounds of %d calls each. Median (min) of per-round ns/op.\n\n", reps, rounds, iters)
	fmt.Printf("| variant | setup (build+install) |")
	for _, t := range targets {
		fmt.Printf(" %s |", t.name)
	}
	fmt.Printf("\n|---|---|%s\n", repeat("---|", len(targets)))
	for _, v := range variants {
		rs := all[v.name]
		var setup []float64
		for _, r := range rs {
			setup = append(setup, float64(r.SetupNs))
		}
		fmt.Printf("| %s | %v |", v.name, time.Duration(median(setup)).Round(time.Microsecond))
		for _, t := range targets {
			var xs []float64
			for _, r := range rs {
				xs = append(xs, r.NsPerOp[t.name]...)
			}
			fmt.Printf(" %.1f (%.1f) |", median(xs), slices.Min(xs))
		}
		fmt.Println()
	}
	fmt.Println()
}

func median(xs []float64) float64 {
	ys := slices.Clone(xs)
	slices.Sort(ys)
	return ys[len(ys)/2]
}

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}
