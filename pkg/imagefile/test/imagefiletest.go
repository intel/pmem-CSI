/*

Copyright (c) 2017-2019 Intel Corporation

SPDX-License-Identifier: Apache-2.0

*/

// Package test contains a black-box test for the imagefile package.
// It is a separate package to allow importing it into the E2E suite
// where we can use the setup files for the current cluster to
// actually use the image file inside a VM.
package test

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/onsi/ginkgo/v2"
	"github.com/stretchr/testify/assert"

	"github.com/intel/pmem-csi/pkg/imagefile"
)

// TInterface extends the GinkgoTInterface with support for nested tests.
type TInterface interface {
	ginkgo.GinkgoTInterface

	Outer(string, func(t TInterface))
	Inner(string, func(t TInterface))
}

// ImageFile can be embedded inside a Ginkgo suite or a normal Go test and
// verifies that image files really work, if possible also by mounting them
// in a VM.
func ImageFile(t TInterface) {
	tooSmallSize := imagefile.HeaderSize - 1
	toString := func(size imagefile.Bytes) string {
		return resource.NewQuantity(int64(size), resource.BinarySI).String()
	}

	// Try with ext4 and XFS.
	run := func(fs imagefile.FsType) {
		t.Outer(string(fs), func(t TInterface) {
			// Try with a variety of sizes because the image file is
			// sensitive to alignment problems.
			tests := []struct {
				size          string
				expectedError string
			}{
				{size: toString(tooSmallSize), expectedError: fmt.Sprintf("invalid image file size %d, must be larger than HeaderSize=%d", tooSmallSize, imagefile.HeaderSize)},
				{size: "512Mi"},
				{size: "511Mi"},
			}
			for _, tt := range tests {
				tt := tt
				t.Inner(tt.size, func(t TInterface) {
					quantity := resource.MustParse(tt.size)
					testImageFile(t, fs, imagefile.Bytes(quantity.Value()), tt.expectedError)
				})
			}
		})
	}
	run(imagefile.Ext4)
	run(imagefile.Xfs)
}

func testImageFile(t TInterface, fs imagefile.FsType, size imagefile.Bytes, expectedError string) {
	if _, err := exec.LookPath("parted"); err != nil {
		t.Skipf("parted not found: %v", err)
	}

	file, err := os.CreateTemp("", "image")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	defer rmTmpfile(file)

	err = imagefile.Create(file.Name(), size, fs)
	switch {
	case expectedError == "" && err != nil:
		logStderr(t, err)
		t.Fatalf("failed to create image file: %v", err)
	case expectedError != "" && err == nil:
		t.Fatalf("did not fail with %q", expectedError)
	case expectedError != "" && err != nil:
		assert.Equal(t, err.Error(), expectedError, "wrong error message")
		return
	}

	fi, err := file.Stat()
	if err != nil {
		t.Fatalf("failed to stat image file: %v", err)
	}
	assert.GreaterOrEqual(t, fi.Size(), int64(size), "nominal image size")

	if os.Getenv("REPO_ROOT") == "" || os.Getenv("CLUSTER") == "" {
		t.Log("for testing the image under QEMU, download files for a cluster and set REPO_ROOT and CLUSTER")
		return
	}
	if _, err := exec.LookPath("govm"); err != nil {
		t.Log("for  testing the image under QEMU, install govm")
		return
	}
	env := []string{
		"EXISTING_VM_FILE=" + file.Name(),
		os.ExpandEnv("PATH=${REPO_ROOT}/_work/bin:${PATH}"),
		os.ExpandEnv("RESOURCES_DIRECTORY=${REPO_ROOT}/_work/resources"),
		os.ExpandEnv("VM_IMAGE=${REPO_ROOT}/_work/${CLUSTER}/cloud-image.qcow2"),
	}
	t.Logf("running %s check-imagefile.sh", strings.Join(env, " "))
	cmd := exec.Cmd{
		Path: os.ExpandEnv("${REPO_ROOT}/test/check-imagefile.sh"),
		Args: []string{"check-imagefile.sh"},
		Env:  append(os.Environ(), env...),
		Dir:  os.Getenv("REPO_ROOT"),
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("failed to set up pipe: %v", err)
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		t.Fatalf("start check-imagefile.sh: %v", err)
	}
	scanner := bufio.NewScanner(stdout)
	success := ""
	for scanner.Scan() {
		line := scanner.Text()
		t.Logf("check-imagefile.sh: %s", line)
		if strings.HasPrefix(line, "SUCCESS: ") {
			success = line
		}
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("check-imagefile.sh failed: %v", err)
	}
	fsReadableForm := fs // %T output from stat
	if fs == imagefile.Ext4 {
		// We don't bother with checking what is actually mounted,
		// this is close enough.
		fsReadableForm = "ext2/ext3"
	}
	assert.Equal(t,
		fmt.Sprintf("SUCCESS: fstype=%s partition_size=%d block_size=%d",
			fsReadableForm,
			fi.Size()-int64(imagefile.HeaderSize),
			imagefile.BlockSize),
		success, "filesystem attributes")
}

func rmTmpfile(file *os.File) {
	file.Close()
	os.Remove(file.Name())
}

func logStderr(t TInterface, err error) {
	if err == nil {
		return
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		t.Logf("command failed, stderr:\n%s", string(exitError.Stderr))
	}
}
