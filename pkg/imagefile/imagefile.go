/*

Copyright (c) 2017-2019 Intel Corporation

SPDX-License-Identifier: Apache-2.0

This file contains code originally published by Intel under GPL:
- https://github.com/torvalds/linux/blob/72c0870e3a05d9cd5466d08c3d2a3069ed0a2f9f/drivers/nvdimm/claim.c#L228-L249
  from https://github.com/torvalds/linux/commit/e1455744b27c9e6115c3508a7b2902157c2c4347
- https://github.com/torvalds/linux/blob/72c0870e3a05d9cd5466d08c3d2a3069ed0a2f9f/drivers/nvdimm/core.c#L179-L193
  from https://github.com/torvalds/linux/commit/eaf961536e1622ad21247ac8d44acd48ba65566e
- https://github.com/torvalds/linux/blob/a3619190d62ed9d66416891be2416f6bea2b3ca4/drivers/nvdimm/pfn.h#L12-L34
  from multiple commits by djb, see https://github.com/torvalds/linux/blame/a3619190d62ed9d66416891be2416f6bea2b3ca4/drivers/nvdimm/pfn.h#L12-L34
- https://github.com/kata-containers/osbuilder/blob/dbbf16082da3de37d89af0783e023269210b2c91/image-builder/nsdax.gpl.c#L1-L171
  from https://github.com/kata-containers/osbuilder/commit/726f798ff795ef4a8300201cab8d83e83c1496a5#diff-1d1124b18f3d6153eb2a9bba67c6314d

That code gets re-published here under Apache-2.0.

Furthermore, this file is based on the following code published by Intel under Apache-2.0:
- https://github.com/kata-containers/osbuilder/blob/d1751a35e1bd1613e66df87221faed195225718e/image-builder/image_builder.sh
*/

/*
Package imagefile contains code to create a file with the following content:

		.-----------.----------.---------------.
		| 0 - 512 B | 4 - 8 Kb |  2M - ...     |
		|-----------+----------+---------------+
		|   MBR #1  |   DAX    |  FS           |
		'-----------'----------'---------------'
		      |         |      ^
		      |         '-data-'
		      |                |
		      '--fs-partition--'

	                    ^          ^
	          daxHeaderOffset      |
	                           HeaderSize

MBR: Master boot record.
DAX: Metadata required by the NVDIMM driver to enable DAX in the guest (struct nd_pfn_sb).
FS: partition that contains a filesystem.

The MBR is useful for working with the image file:
  - the `file` utility uses it to determine what the file contains
  - when binding the entire file to /dev/loop0, /dev/loop0p1 will be
    the file system (beware that partprobe /dev/loop0 might be needed);
    alternatively one could bind the file system directly by specifying an offset

When such a file is created on a dax-capable filesystem, then it can
be used as backing store for a [QEMU nvdimm
device](https://github.com/qemu/qemu/blob/master/docs/nvdimm.txt) such
that the guest kernel provides a /dev/pmem0 (the FS partition above)
which can be mounted with -odax. For full dax semantic, the QEMU
device configuration must use the 'pmem' and 'share' flags.
*/
package imagefile

// Here the original C code gets included verbatim and compiled with cgo.

// #include <stdlib.h>
// #include <string.h>
// #include <endian.h>
//
// #define __KERNEL__
// #include <linux/types.h>
// #include <linux/byteorder/little_endian.h>
//
// /*
//   Next types, definitions and functions were copied from kernel 4.19.24 source
//   code, specifically from nvdimm driver
// */
//
// #define PFN_SIG_LEN 16
// #define PFN_SIG "NVDIMM_PFN_INFO\0"
// #define SZ_4K 0x00001000
//
// typedef __u16 u16;
// typedef __u8 u8;
// typedef __u64 u64;
// typedef __u32 u32;
// typedef int bool;
//
// enum nd_pfn_mode {
// 	PFN_MODE_NONE,
// 	PFN_MODE_RAM,
// 	PFN_MODE_PMEM,
// };
//
// struct nd_pfn_sb {
// 	u8 signature[PFN_SIG_LEN];
// 	u8 uuid[16];
// 	u8 parent_uuid[16];
// 	__le32 flags;
// 	__le16 version_major;
// 	__le16 version_minor;
// 	__le64 dataoff; /* relative to namespace_base + start_pad */
// 	__le64 npfns;
// 	__le32 mode;
// 	/* minor-version-1 additions for section alignment */
// 	__le32 start_pad;
// 	__le32 end_trunc;
// 	/* minor-version-2 record the base alignment of the mapping */
// 	__le32 align;
// 	/* minor-version-3 guarantee the padding and flags are zero */
// 	u8 padding[4000];
// 	__le64 checksum;
// };
//
// struct nd_gen_sb {
// 	char reserved[SZ_4K - 8];
// 	__le64 checksum;
// };
//
// u64 nd_fletcher64(void *addr, size_t len, bool le)
// {
// 	u32 *buf = addr;
// 	u32 lo32 = 0;
// 	u64 hi32 = 0;
// 	int i;
//
// 	for (i = 0; i < len / sizeof(u32); i++) {
// 		lo32 += le ? le32toh((__le32) buf[i]) : buf[i];
// 		hi32 += lo32;
// 	}
//
// 	return hi32 << 32 | lo32;
// }
//
// /*
//  * nd_sb_checksum: compute checksum for a generic info block
//  *
//  * Returns a fletcher64 checksum of everything in the given info block
//  * except the last field (since that's where the checksum lives).
//  */
// u64 nd_sb_checksum(struct nd_gen_sb *nd_gen_sb)
// {
// 	u64 sum;
// 	__le64 sum_save;
//
// 	sum_save = nd_gen_sb->checksum;
// 	nd_gen_sb->checksum = 0;
// 	sum = nd_fletcher64(nd_gen_sb, sizeof(*nd_gen_sb), 1);
// 	nd_gen_sb->checksum = sum_save;
// 	return sum;
// }
//
// void nsdax(void *p, unsigned int data_offset, unsigned int alignment)
// {
//      struct nd_pfn_sb *sb = p;
// 	memset(sb, 0, sizeof(*sb));
//
// 	strcpy((char*)sb->signature, PFN_SIG);
// 	sb->mode = PFN_MODE_RAM;
// 	sb->align = htole32(alignment);
// 	sb->dataoff = htole64((unsigned long)data_offset);
// 	sb->version_minor = 2;
//
// 	// checksum must be calculated at the end
// 	sb->checksum = nd_sb_checksum((struct nd_gen_sb*)sb);
// }
import "C"

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/intel/pmem-csi/third-party/go-fibmap"
)

type FsType string

const (
	Ext4 FsType = "ext4"
	Xfs  FsType = "xfs"
)

type Bytes int64

const (
	MiB = Bytes(1024 * 1024)
	KiB = Bytes(1024)

	// Alignment of partitions relative to the start of the block device, i.e. the MBR.
	// For DAX huge pages this has to be 2MB, see https://nvdimm.wiki.kernel.org/2mib_fs_dax
	DaxAlignment = 2 * MiB

	// DAX header offset in the file. This is where the Linux kernel expects it
	// (https://github.com/torvalds/linux/blob/2187f215ebaac73ddbd814696d7c7fa34f0c3de0/drivers/nvdimm/pfn_devs.c#L438-L596).
	daxHeaderOffset = 4 * KiB

	// Start of the file system.
	// Chosen so that we have enough space before it for MBR #1 and the DAX metadata.
	HeaderSize = DaxAlignment

	// Block size used for the filesystem. ext4 only supports dax with 4KiB blocks.
	BlockSize = 4 * KiB

	// /dev/pmem0 will have some fake disk geometry attached to it.
	// We have to make the final device size a multiple of the fake
	// head * track... even if the actual filesystem is smaller.
	DiskAlignment = DaxAlignment * 512
)

// Create writes a complete image file of a certain total size.
// The size must be a multiple of DaxAlignment. The resulting
// partition then is size - HeaderSize large.
//
// Depending on the filesystem, additional constraints apply
// for the size of that partition. A multiple of BlockSize
// should be safe.
//
// The resulting file will have "size" bytes in use. If bytes is zero,
// then the image file will be made as large as possible.
//
// The result will be sparse, i.e. empty parts are not actually
// written yet, but they will be allocated, so there is no risk
// later on that attempting to write fails due to lack of space.
func Create(filename string, size Bytes, fs FsType) error {
	if size != 0 && size <= HeaderSize {
		return fmt.Errorf("invalid image file size %d, must be larger than HeaderSize=%d", size, HeaderSize)
	}

	dir, err := os.Open(filepath.Dir(filename))
	if err != nil {
		return err
	}
	defer dir.Close()

	file, err := os.Create(filename)
	if err != nil {
		return err
	}

	// Delete file on failure.
	success := false
	defer func() {
		if !success {
			os.Remove(filename)
		}
	}()
	defer file.Close()

	// Enlarge the file and ensure that we really have enough space for it.
	size, err = allocateFile(file, size)
	if err != nil {
		return err
	}
	fsSize := size - HeaderSize

	// We write MBRs and rootfs into temporary files, then copy into the
	// final image file at the end.
	tmp, err := os.MkdirTemp("", "pmem-csi-imagefile")
	if err != nil {
		return fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(tmp)
	mbr1 := filepath.Join(tmp, "mbr1")
	fsimage := filepath.Join(tmp, "fsimage")

	// This is for the full image file.
	if err := writeMBR(mbr1, fs, HeaderSize, size); err != nil {
		return err
	}

	// Create a file of the desired size, then let mkfs write into it.
	fsFile, err := os.Create(fsimage)
	if err != nil {
		return err
	}
	defer fsFile.Close()
	if err := fsFile.Truncate(int64(fsSize)); err != nil {
		return err
	}
	args := []string{fmt.Sprintf("mkfs.%s", fs)}
	// Required for dax semantic.
	switch fs {
	case Ext4:
		args = append(args, "-b", fmt.Sprintf("%d", BlockSize))
	case Xfs:
		args = append(args,
			"-b", fmt.Sprintf("size=%d", BlockSize),
			"-m", "reflink=0",
		)
	}
	args = append(args, fsimage)
	cmd := exec.Command(args[0], args[1:]...)
	if _, err := cmd.Output(); err != nil {
		return fmt.Errorf("mkfs.%s for fs of size %d: %w", fs, fsSize, err)
	}

	// Now copy to the actual file.
	if err := dd(mbr1, filename, true, 0); err != nil {
		return err
	}
	if _, err := file.Seek(int64(daxHeaderOffset), io.SeekStart); err != nil {
		return err
	}
	if _, err := file.Write(nsdax(uint(HeaderSize), uint(DaxAlignment))); err != nil {
		return err
	}
	if err := dd(fsimage, filename, true, HeaderSize); err != nil {
		return err
	}

	// Some (but not all) kernels seem to expect the entire file to align at
	// a 2GiB boundary. Therefore we round up and add some un-allocated padding
	// at the end.
	newSize := (size + DaxAlignment - 1) / DaxAlignment * DaxAlignment
	if err := file.Truncate(int64(newSize)); err != nil {
		return fmt.Errorf("resize %q to %d: %w", filename, newSize, err)
	}

	// Safely close the file:
	// - sync content
	// - close it
	// - sync directory
	if err := file.Sync(); err != nil {
		return fmt.Errorf("syncing %q: %w", filename, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("closing %q: %w", filename, err)
	}
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("syncing parent directory of %q: %w", filename, err)
	}

	success = true
	return nil
}

func allocateFile(file *os.File, size Bytes) (Bytes, error) {
	if size != 0 {
		if err := file.Truncate(int64(size)); err != nil {
			return 0, fmt.Errorf("resize %q to %d: %w", file.Name(), size, err)
		}
		if err := unix.Fallocate(int(file.Fd()), 0, 0, int64(size)); err != nil {
			return 0, fmt.Errorf("fallocate %q size %d: %w", file.Name(), size, err)
		}
		return size, nil
	}

	// Determine how much space is left on the volume of the image file.
	var stat unix.Statfs_t
	if err := unix.Fstatfs(int(file.Fd()), &stat); err != nil {
		return 0, fmt.Errorf("unexpected error while checking the volume statistics for %q: %v", file.Name(), err)
	}

	// We have to try how large the file can become.
	blockOverhead := uint64(1)
	for blockOverhead < stat.Bfree {
		size := Bytes(int64(stat.Bfree-blockOverhead) * stat.Bsize)
		size = (size + DaxAlignment - 1) / DaxAlignment * DaxAlignment
		if size == 0 {
			break
		}
		size, err := allocateFile(file, Bytes(size))
		if err == nil {
			return size, nil
		}
		var errno syscall.Errno
		if !errors.As(err, &errno) || errno != syscall.ENOSPC {
			return 0, fmt.Errorf("allocate %d bytes for file %q: %v", size, file.Name(), err)
		}

		// Double the overhead while it is still small, later just increment with a fixed amount.
		// This way we don't make the overhead at most 64 * block size larger than it really
		// has to be while not trying too often (which we would do when incrementing the overhead
		// by just one from the start).
		if blockOverhead < 64 {
			blockOverhead *= 2
		} else {
			blockOverhead += 64
		}
	}
	return 0, fmt.Errorf("volume of %d blocks (block size %d bytes) too small for image file", stat.Bfree, stat.Bsize)
}

// nsdax prepares 4KiB of DAX metadata.
func nsdax(dataOffset uint, alignment uint) []byte {
	p := C.malloc(C.sizeof_struct_nd_pfn_sb)
	defer C.free(p)

	// Fill allocated memory...
	C.nsdax(p, C.uint(dataOffset), C.uint(alignment))

	// ... and return a copy in a normal slice.
	return C.GoBytes(p, C.sizeof_struct_nd_pfn_sb)
}

// writeMBR writes a master boot record at the start of the given image file.
func writeMBR(to string, fs FsType, partitionStart Bytes, partitionEnd Bytes) error {
	// Doesn't have to be a block device, but must exist and be large enough.
	file, err := os.Create(to)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Truncate(int64(partitionEnd)); err != nil {
		return fmt.Errorf("resize %q to %d: %w", to, partitionEnd, err)
	}

	// From https://github.com/kata-containers/osbuilder/blob/d1751a35e1bd1613e66df87221faed195225718e/image-builder/image_builder.sh#L346-L348
	// with some changes:
	// - no alignment
	// - subtract one from the end because it looks like start and end of the partition
	//   are both inclusive; at least for end == size of file we get an error
	//   (Error: The location .... is outside of the device ...).
	cmd := exec.Command("parted", "--script", "--align", "none", to, "--",
		"mklabel", "msdos",
		"mkpart", "primary", string(fs),
		fmt.Sprintf("%dB", partitionStart),
		fmt.Sprintf("%dB", partitionEnd-1),
	)
	if _, err := cmd.Output(); err != nil {
		return fmt.Errorf("write MBR with parted to %q: %w", to, err)
	}
	return nil
}

// dd copies one complete file into another at a certain target offset.
// Whether it actually writes the data even when it's just zeros is
// configurable.
func dd(from, to string, sparse bool, seek Bytes) error {
	fi, err := os.Stat(from)
	if err != nil {
		return err
	}

	var extents []fibmap.Extent
	if sparse {
		e, err := getAllExtents(from)
		if err != nil {
			return err
		}
		extents = e
	} else {
		// Copy the entire file.
		extents = append(extents, fibmap.Extent{Length: uint64(fi.Size())})
	}

	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer out.Close()

	for _, extent := range extents {
		// The extent might stretch beyond the end of the file.
		length := int64(extent.Length)
		remaining := fi.Size() - int64(extent.Logical)
		if length > remaining {
			length = remaining
		}
		if err := copyRange(in, out, int64(extent.Logical), int64(seek)+int64(extent.Logical), length); err != nil {
			return err
		}
	}

	return nil
}

const ioChunkSize = 256 * 1024 * 1024

func copyRange(from, to *os.File, skip, seek, size int64) error {
	buffer := make([]byte, ioChunkSize)

	if _, err := from.Seek(skip, io.SeekStart); err != nil {
		return err
	}
	if _, err := to.Seek(seek, io.SeekStart); err != nil {
		return err
	}
	remaining := size
	for remaining > 0 {
		current := remaining
		if current > int64(len(buffer)) {
			current = int64(len(buffer))
		}

		// We shouldn't run into io.EOF here because we don't
		// attempt to read past the end of the file, so any error
		// is a reason to fail.
		read, err := from.Read(buffer[0:current])
		if err != nil {
			return err
		}
		if int64(read) < current {
			return fmt.Errorf("%q: unexpected short read, got %d instead of %d bytes", from.Name(), read, current)
		}
		// In contrast to reads, a short write is guaranteed to return an error.
		if _, err := to.Write(buffer[0:current]); err != nil {
			return err
		}
		remaining = remaining - current
	}
	return nil
}

const initialExtentSize = 16

func getAllExtents(from string) ([]fibmap.Extent, error) {
	file, err := os.Open(from)
	if err != nil {
		return nil, err
	}
	fibmapFile := fibmap.NewFibmapFile(file)

	// Try with FIBMAP first.
	for size := uint32(initialExtentSize); ; size = size * 2 {
		extents, err := fibmapFile.Fiemap(size)
		// Got all extents?
		if err == 0 &&
			(len(extents) == 0 || (extents[len(extents)-1].Flags&fibmap.FIEMAP_EXTENT_LAST) != 0) {
			return extents, nil
		}
		if err == syscall.ENOTSUP {
			break
		}
		if err != 0 {
			return nil, &os.PathError{Op: "fibmap", Path: from,
				Err: &os.SyscallError{Syscall: "ioctl", Err: err}}
		}
	}

	// Not supported by tmpfs, which supports SEEK_DATA and SEEK_HOLE. Fall back to that.
	// TODO: error reporting in SeekDataHole()
	offsets := fibmapFile.SeekDataHole()
	var extents []fibmap.Extent
	for i := 0; i+1 < len(offsets); i += 2 {
		extents = append(extents, fibmap.Extent{Logical: uint64(offsets[i]), Length: uint64(offsets[i+1])})
	}
	return extents, nil
}
