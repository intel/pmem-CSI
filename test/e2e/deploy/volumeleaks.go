/*
Copyright 2020 Intel Corporation.

SPDX-License-Identifier: Apache-2.0
*/

package deploy

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	pmemcsidriver "github.com/intel/pmem-csi/pkg/pmem-csi-driver"

	. "github.com/onsi/gomega"
)

// Register list of volumes before test, using out-of-band host commands (i.e. not CSI API).
func GetHostVolumes(d *Deployment) map[string][]string {
	var cmd string
	var hdr string
	switch d.Mode {
	case pmemcsidriver.LVM:
		// lvs adds many space (0x20) chars at end, we could squeeze
		// repetitions using tr here, but TrimSpace() below strips those away
		cmd = "sudo lvs --foreign --noheadings"
		hdr = "LVM Volumes"
	case pmemcsidriver.Direct:
		// ndctl produces multiline block. We want one line per namespace.
		// Pick uuid, mode, size for comparison. Note that sorting changes the order so lines
		// are not grouped by volume, but keeping volume order would need more complex parsing
		// and this is not meant to be pretty-printed for human, just to detect the change.
		cmd = "sudo ndctl list |tr -d '\"' |egrep 'uuid|mode|^ *size' |sort |tr -d ' \n'"
		hdr = "Namespaces"
	}
	result := make(map[string][]string)
	// Instead of trying to find out number of hosts, we trust the set of
	// ssh.N helper scripts matches running hosts, which should be the case in
	// correctly running tester system. We run ssh.N commands until a ssh.N
	// script appears to be "no such file".
	for worker := 1; ; worker++ {
		sshcmd := fmt.Sprintf("%s/_work/%s/ssh.%d", os.Getenv("REPO_ROOT"), os.Getenv("CLUSTER"), worker)
		ssh := exec.Command(sshcmd, cmd)
		// Intentional Output instead of CombinedOutput to dismiss warnings from stderr.
		// lvs may emit lvmetad-related WARNING msg which can't be silenced using -q option.
		out, err := ssh.Output()
		if err != nil && os.IsNotExist(err) {
			break
		}
		buf := fmt.Sprintf("%s on Node %d", hdr, worker)
		result[buf] = strings.Split(strings.TrimSpace(string(out)), "\n")
	}
	return result
}

// CheckForLeftovers lists volumes again after test, diff means leftovers.
func CheckForLeftoverVolumes(d *Deployment, volBefore map[string][]string) {
	volNow := GetHostVolumes(d)
	Expect(volNow).To(Equal(volBefore), "same volumes before and after the test")
}
