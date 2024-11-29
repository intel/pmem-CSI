/*

Copyright (c) 2017-2019 Intel Corporation

SPDX-License-Identifier: Apache-2.0

*/

package imagefile

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNsdax(t *testing.T) {
	// Expected output comes from:
	// - curl -O https://github.com/kata-containers/osbuilder/raw/726f798ff795ef4a8300201cab8d83e83c1496a5/image-builder/nsdax.gpl.c
	// - gcc -o nsdax.gpl nsdax.gpl.c
	// - truncate -s 0 /tmp/image
	// - ./fsdax.gpl /tmp/image <offset> <alignment>
	// - tail -c +$((0x00001001)) /tmp/image | od -t x1 -a
	//
	// 0x00001001 = SZ_4K + 1
	tests := []struct {
		dataOffset uint
		alignment  uint
		odDump     string
	}{
		{1024, 2048,
			`0000000  4e  56  44  49  4d  4d  5f  50  46  4e  5f  49  4e  46  4f  00
          N   V   D   I   M   M   _   P   F   N   _   I   N   F   O nul
0000020  00  00  00  00  00  00  00  00  00  00  00  00  00  00  00  00
        nul nul nul nul nul nul nul nul nul nul nul nul nul nul nul nul
*
0000060  00  00  00  00  00  00  02  00  00  04  00  00  00  00  00  00
        nul nul nul nul nul nul stx nul nul eot nul nul nul nul nul nul
0000100  00  00  00  00  00  00  00  00  01  00  00  00  00  00  00  00
        nul nul nul nul nul nul nul nul soh nul nul nul nul nul nul nul
0000120  00  00  00  00  00  08  00  00  00  00  00  00  00  00  00  00
        nul nul nul nul nul  bs nul nul nul nul nul nul nul nul nul nul
0000140  00  00  00  00  00  00  00  00  00  00  00  00  00  00  00  00
        nul nul nul nul nul nul nul nul nul nul nul nul nul nul nul nul
*
0007760  00  00  00  00  00  00  00  00  30  44  54  e3  2b  23  ea  6c
        nul nul nul nul nul nul nul nul   0   D   T   c   +   #   j   l
0010000
`},
		{1, 2, `0000000  4e  56  44  49  4d  4d  5f  50  46  4e  5f  49  4e  46  4f  00
          N   V   D   I   M   M   _   P   F   N   _   I   N   F   O nul
0000020  00  00  00  00  00  00  00  00  00  00  00  00  00  00  00  00
        nul nul nul nul nul nul nul nul nul nul nul nul nul nul nul nul
*
0000060  00  00  00  00  00  00  02  00  01  00  00  00  00  00  00  00
        nul nul nul nul nul nul stx nul soh nul nul nul nul nul nul nul
0000100  00  00  00  00  00  00  00  00  01  00  00  00  00  00  00  00
        nul nul nul nul nul nul nul nul soh nul nul nul nul nul nul nul
0000120  00  00  00  00  02  00  00  00  00  00  00  00  00  00  00  00
        nul nul nul nul stx nul nul nul nul nul nul nul nul nul nul nul
0000140  00  00  00  00  00  00  00  00  00  00  00  00  00  00  00  00
        nul nul nul nul nul nul nul nul nul nul nul nul nul nul nul nul
*
0007760  00  00  00  00  00  00  00  00  33  38  54  e3  f3  0e  bb  6c
        nul nul nul nul nul nul nul nul   3   8   T   c   s  so   ;   l
0010000
`},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(fmt.Sprintf("offset %d, alignment %d", tt.dataOffset, tt.alignment),
			func(t *testing.T) {
				t.Parallel()
				data := nsdax(tt.dataOffset, tt.alignment)
				cmd := exec.Command("od", "-t", "x1", "-a")
				cmd.Stdin = bytes.NewBuffer(data)
				out, err := cmd.Output()
				if err != nil {
					t.Fatalf("od failed: %v", err)
				}
				assert.Equal(t, tt.odDump, string(out))
			})
	}
}

func rmTmpfile(file *os.File) {
	file.Close()
	os.Remove(file.Name())
}

func TestExtents(t *testing.T) {
	file, err := os.CreateTemp("", "extents")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	defer rmTmpfile(file)

	// Create a sparse file with one byte at 1MB, 2 bytes at 2MB, 4 bytes at 4MB, etc.
	// The file then should have one extent at each of these offsets.
	const numExtents = initialExtentSize
	offset := int64(0)
	for count := 1; count <= numExtents; count++ {
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			t.Fatalf("seek to %d: %v", offset, err)
		}
		if _, err := file.Write(make([]byte, count)); err != nil {
			t.Fatalf("write %d bytes at %d: %v", count, offset, err)
		}
		if offset == 0 {
			offset = 1024 * 1024
		} else {
			offset = offset * 2
		}
	}

	verify := func(filename string) {
		extents, err := getAllExtents(filename)
		if err != nil {
			var errno syscall.Errno
			if errors.As(err, &errno) && errno == syscall.ENOTSUP {
				t.Skipf("getting extents not supported for %q", filename)
			}
			t.Fatalf("could not get extents: %v", err)
		}
		assert.Equal(t, numExtents, len(extents), "number of extents")
		offset = 0
		for count := 1; count <= numExtents && count <= len(extents); count++ {
			extent := extents[count-1]
			assert.Equal(t, extent.Logical, uint64(offset), "offset of extent %d", count)
			if offset == 0 {
				offset = 1024 * 1024
			} else {
				offset = offset * 2
			}
		}
	}

	verify(file.Name())

	// Now create a sparse copy. This should be almost
	// instantaneous, despite the nominally large file.
	copy, err := os.CreateTemp(".", "copy")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	defer rmTmpfile(copy)
	start := time.Now()
	if err := dd(file.Name(), copy.Name(), true /* sparse */, 0); err != nil {
		t.Fatalf("failed to copy: %v", err)
	}
	delta := time.Since(start)
	assert.Less(t, delta.Seconds(), 10.0, "time for copying file")
	verify(copy.Name())
}
