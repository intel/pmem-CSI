/*
Copyright 2019 Intel Corporation

SPDX-License-Identifier: Apache-2.0
*/

// Package coverage adds one command line flag similar to
// -test.coverprofile. The difference is that the file is created
// during startup with ioutil.Tmpfile (i.e. * gets replaced with
// a unique string) and then passed to a restarted binary
// as -test.coverprofile.

package coverage

import (
	"flag"
	"os"
	"path/filepath"
	"syscall"

	"k8s.io/klog/v2"
)

var coverage = flag.String("coverprofile", "", "write a coverage profile to a unique file (* in name replaced with random string, otherwise appended)")

// Run re-execs the program with -test.coverprofile if -coverprofile
// was used, otherwise it executes the program's main function.
func Run(main func() int) {
	if *coverage != "" {
		abspath, err := filepath.Abs(*coverage)
		if err != nil {
			klog.Fatalf("cover profile %q: %s", *coverage, err)
		}
		f, err := os.CreateTemp(filepath.Dir(abspath), filepath.Base(abspath))
		if err != nil {
			klog.Fatalf("temporary cover profile %q: %s", abspath, err)
		}
		// Overwrite -coverprofile with empty string during next program run to
		// avoid endless recursion. Instead use -test.coverprofile with the new
		// name.
		args := os.Args
		args = append(args, "-coverprofile=")
		args = append(args, "-test.coverprofile="+f.Name())
		if err := syscall.Exec(args[0], args, os.Environ()); err != nil {
			klog.Fatalf("re-exec %v: %s", args, err)
		}
	}
	main()
}
