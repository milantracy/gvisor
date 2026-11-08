// Copyright 2018 The gVisor Authors.
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

// Package sandboxsetup provides helpers for setting up gVisor sandboxes.
package sandboxsetup

import (
	"fmt"
	"os"

	"gvisor.dev/gvisor/runsc/flag"
)

// PrepareArgs returns the args that can be used to re-execute the current
// program. It manipulates the flags of the subcommand identified by
// subCmdName and fSet is the flag.FlagSet of this subcommand. It applies
// the flags specified by override map. In case of conflict, flag is
// overridden.
//
// Postcondition: PrepareArgs() takes ownership of override map.
func PrepareArgs(subCmdName string, fSet *flag.FlagSet, override map[string]string) []string {
	var args []string
	// Add all args up until (and including) the sub command.
	for _, arg := range os.Args {
		args = append(args, arg)
		if arg == subCmdName {
			break
		}
	}
	// Set sub command flags. Iterate through all the explicitly set flags.
	fSet.Visit(func(gf *flag.Flag) {
		// If a conflict is found with override, then prefer override
		// flag.
		if ov, ok := override[gf.Name]; ok {
			args = append(args, fmt.Sprintf("--%s=%s", gf.Name, ov))
			delete(override, gf.Name)
			return
		}
		// Otherwise pass through the original flag.
		args = append(args, fmt.Sprintf("--%s=%s", gf.Name, gf.Value))
	})
	// Apply remaining override flags (that didn't conflict above).
	for of, ov := range override {
		args = append(args, fmt.Sprintf("--%s=%s", of, ov))
	}
	// Add the non-flag arguments at the end.
	args = append(args, fSet.Args()...)
	return args
}
