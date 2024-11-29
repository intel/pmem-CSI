/*
Copyright 2018 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package e2e

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
)

// TODO: make framework/testfiles support absolute paths

// RootFileSource looks for files relative to a root directory.
type RootFileSource struct {
	Root string
}

// ReadTestFile looks for the file relative to the configured
// root directory.
func (r RootFileSource) ReadTestFile(filePath string) ([]byte, error) {
	var fullPath string
	if path.IsAbs(filePath) {
		fullPath = filePath
	} else {
		fullPath = filepath.Join(r.Root, filePath)
	}
	data, err := os.ReadFile(fullPath)
	if os.IsNotExist(err) {
		// Not an error (yet), some other provider may have the file.
		return nil, nil
	}
	return data, err
}

// DescribeFiles explains that it looks for files inside a certain
// root directory.
func (r RootFileSource) DescribeFiles() string {
	description := fmt.Sprintf("Test files are expected in %q", r.Root)
	if !path.IsAbs(r.Root) {
		// The default in test_context.go is the relative path
		// ../../, which doesn't really help locating the
		// actual location. Therefore we add also the absolute
		// path if necessary.
		abs, err := filepath.Abs(r.Root)
		if err == nil {
			description += fmt.Sprintf(" = %q", abs)
		}
	}
	description += "."
	return description
}
