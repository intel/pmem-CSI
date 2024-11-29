/*
Copyright 2019 Intel Corporation.

SPDX-License-Identifier: Apache-2.0
*/

package pod

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"strings"

	"k8s.io/kubernetes/test/e2e/framework"
	e2epod "k8s.io/kubernetes/test/e2e/framework/pod"

	. "github.com/onsi/ginkgo/v2"
)

// RunInPod optionally transfers some files into /tmp (source file
// relative to root dir, with same base file name, without path, with
// same x bit) and executes a shell command. Any error is treated as
// test failure.
//
// Relies on dd in the target container.
func RunInPod(f *framework.Framework, rootdir string, files []string, command string, namespace, pod, container string) (string, string) {
	parts := []string{"cd /tmp"}
	var input hiddenContent
	for _, file := range files {
		base := path.Base(file)
		full := path.Join(rootdir, file)
		data, err := os.ReadFile(full)
		framework.ExpectNoError(err, "read input file %q", full)
		input.Write(data)
		// Somehow count=1 bs=<total size> resulted in truncated data transfers.
		// This works, but has higher overhead.
		parts = append(parts, fmt.Sprintf("dd of=%s count=%d bs=1 status=none", base, len(data)))
		stat, err := os.Stat(full)
		framework.ExpectNoError(err)
		if stat.Mode().Perm()&0111 != 0 {
			parts = append(parts, fmt.Sprintf("chmod a+x %s", base))
		}
	}
	parts = append(parts, command)
	options := e2epod.ExecOptions{
		Command: []string{
			"/bin/sh",
			"-c",
			strings.Join(parts, " && "),
		},
		Namespace:     namespace,
		PodName:       pod,
		ContainerName: container,
		Stdin:         &input,
		CaptureStdout: true,
		CaptureStderr: true,
	}
	stdout, stderr, err := e2epod.ExecWithOptions(f, options)
	framework.ExpectNoError(err, "command failed in namespace %s, pod/container %s/%s:\nstderr:\n%s\nstdout:%s\n",
		namespace, pod, container, stderr, stdout)
	fmt.Fprintf(GinkgoWriter, "stderr:\n%s\nstdout:\n%s\n",
		stderr, stdout)

	return stdout, stderr
}

// hiddentContent is the same as bytes.Buffer, but doesn't print its entire content in String().
type hiddenContent struct {
	bytes.Buffer
}

func (h *hiddenContent) String() string {
	return fmt.Sprintf("<%d bytes>", h.Len())
}
