// Copyright 2020 The gVisor Authors.
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

package forwardcmd

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"os"
	"strings"

	"github.com/google/subcommands"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/cpuid"
	"gvisor.dev/gvisor/pkg/state/pretty"
	"gvisor.dev/gvisor/pkg/state/statefile"
	"gvisor.dev/gvisor/runsc/boot/sentryapi"
	"gvisor.dev/gvisor/runsc/cmd/util"
	"gvisor.dev/gvisor/runsc/config"
	"gvisor.dev/gvisor/runsc/flag"
)

// Statefile implements subcommands.Command for the "statefile" command.
type Statefile struct {
	list    bool
	get     string
	bitmask bool
	key     string
	output  string
	html    bool
}

// Name implements subcommands.Command.
func (*Statefile) Name() string {
	return "statefile"
}

// Synopsis implements subcommands.Command.
func (*Statefile) Synopsis() string {
	return "shows information about a statefile"
}

// Usage implements subcommands.Command.
func (*Statefile) Usage() string {
	return "statefile [flags] <statefile>\n"
}

// SetFlags implements subcommands.Command.
func (s *Statefile) SetFlags(f *flag.FlagSet) {
	f.BoolVar(&s.list, "list", false, "lists the metadata in the statefile.")
	f.StringVar(&s.get, "get", "", "extracts the given metadata key.")
	f.BoolVar(&s.bitmask, "bitmask", false, fmt.Sprintf("when used with -get %s, outputs CPU features as comma-separated hex uint32 block bitmasks.", sentryapi.CPUFeaturesKey))
	f.StringVar(&s.key, "key", "", "the integrity key for the file.")
	f.StringVar(&s.output, "output", "", "target to write the result.")
	f.BoolVar(&s.html, "html", false, "outputs in HTML format.")
}

// MetadataOnly reports whether the command only inspects statefile metadata
// without decoding the serialized object graph.
func (s *Statefile) MetadataOnly() bool {
	return s.list || s.get != "" || s.bitmask
}

// FetchSpec implements util.SubCommand.FetchSpec.
func (*Statefile) FetchSpec(_ *config.Config, _ *flag.FlagSet) (string, *specs.Spec, error) {
	// This command does not operate on a single container, so nothing to fetch.
	return "", nil, nil
}

// Execute implements subcommands.Command.Execute.
func (s *Statefile) Execute(_ context.Context, f *flag.FlagSet, args ...any) subcommands.ExitStatus {
	// Validate flags and positional arguments before opening or modifying files.
	if s.list && s.get != "" {
		return util.Errorf("error: can't specify -list and -get simultaneously.")
	}
	if s.bitmask && s.get != sentryapi.CPUFeaturesKey {
		return util.Errorf("error: -bitmask requires -get %s.", sentryapi.CPUFeaturesKey)
	}
	if f.NArg() != 1 {
		f.Usage()
		return subcommands.ExitUsageError
	}

	input, err := os.Open(f.Arg(0))
	if err != nil {
		return util.Errorf("error opening input: %v", err)
	}
	defer input.Close()
	var inStat unix.Stat_t
	if err := fstatEINTR(int(input.Fd()), &inStat); err != nil {
		return util.Errorf("error statting input: %v", err)
	}

	if s.output == "" {
		if err := s.writeTo(os.Stdout, input); err != nil {
			return util.Errorf("%v", err)
		}
		return subcommands.ExitSuccess
	}

	if err := writeOutputFile(s.output, &inStat, func(outFile *os.File) error {
		return s.writeTo(outFile, input)
	}); err != nil {
		return util.Errorf("%v", err)
	}
	return subcommands.ExitSuccess
}

const maxSymlinkHops = 40

func (s *Statefile) writeTo(output io.Writer, input *os.File) error {
	var key []byte
	if s.key != "" {
		key = []byte(s.key)
	}

	// Dump the full file?
	if !s.list && s.get == "" {
		rc, _, err := statefile.NewReader(input, key)
		if err != nil {
			return fmt.Errorf("error parsing statefile: %w", err)
		}
		defer rc.Close()
		if s.html {
			if _, err := fmt.Fprintf(output, "<html><body>\n"); err != nil {
				return fmt.Errorf("error writing output: %w", err)
			}
			if err := pretty.PrintHTML(output, rc); err != nil {
				return fmt.Errorf("error printing state: %w", err)
			}
			if err := rc.Verify(); err != nil {
				return fmt.Errorf("error verifying statefile: %w", err)
			}
			if _, err := fmt.Fprintf(output, "</body></html>\n"); err != nil {
				return fmt.Errorf("error writing output: %w", err)
			}
		} else {
			if err := pretty.PrintText(output, rc); err != nil {
				return fmt.Errorf("error printing state: %w", err)
			}
			if err := rc.Verify(); err != nil {
				return fmt.Errorf("error verifying statefile: %w", err)
			}
		}
		return nil
	}

	// Load just the metadata.
	metadata, err := statefile.Metadata(input, key)
	if err != nil {
		return fmt.Errorf("error reading metadata: %w", err)
	}

	// Is it a single key?
	if s.get != "" {
		val, ok := metadata[s.get]
		if !ok {
			return fmt.Errorf("metadata key %s: not found", s.get)
		}
		if s.bitmask {
			mask, err := cpuid.FeatureMaskStringFromNames(strings.Split(val, ","))
			if err != nil {
				return fmt.Errorf("error converting %s to bitmask: %w", s.get, err)
			}
			val = mask
		}
		if s.html {
			val = html.EscapeString(val)
			if _, err := fmt.Fprintf(output, "<html><body>\n"); err != nil {
				return fmt.Errorf("error writing output: %w", err)
			}
		}
		if _, err := fmt.Fprintf(output, "%s\n", val); err != nil {
			return fmt.Errorf("error writing output: %w", err)
		}
		if s.html {
			if _, err := fmt.Fprintf(output, "</body></html>\n"); err != nil {
				return fmt.Errorf("error writing output: %w", err)
			}
		}
		return nil
	}

	// List all keys.
	if s.html {
		if _, err := fmt.Fprintf(output, "<html><body>\n <ul>\n"); err != nil {
			return fmt.Errorf("error writing output: %w", err)
		}
	}
	for k := range metadata {
		var writeErr error
		if s.html {
			_, writeErr = fmt.Fprintf(output, "  <li>%s</li>\n", html.EscapeString(k))
		} else {
			_, writeErr = fmt.Fprintf(output, "%s\n", k)
		}
		if writeErr != nil {
			return fmt.Errorf("error writing output: %w", writeErr)
		}
	}
	if s.html {
		if _, err := fmt.Fprintf(output, " </ul>\n</body></html>\n"); err != nil {
			return fmt.Errorf("error writing output: %w", err)
		}
	}
	return nil
}

func fstatEINTR(fd int, st *unix.Stat_t) error {
	for {
		err := unix.Fstat(fd, st)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}
