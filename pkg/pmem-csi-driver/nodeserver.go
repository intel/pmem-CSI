/*
Copyright 2017 The Kubernetes Authors.

SPDX-License-Identifier: Apache-2.0
*/

package pmemcsidriver

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
	"k8s.io/utils/keymutex"
	"k8s.io/utils/mount"

	pmemerr "github.com/intel/pmem-csi/pkg/errors"
	pmemexec "github.com/intel/pmem-csi/pkg/exec"
	grpcserver "github.com/intel/pmem-csi/pkg/grpc-server"
	"github.com/intel/pmem-csi/pkg/imagefile"
	pmemlog "github.com/intel/pmem-csi/pkg/logger"
	"github.com/intel/pmem-csi/pkg/pmem-csi-driver/parameters"
	pmdmanager "github.com/intel/pmem-csi/pkg/pmem-device-manager"
	"github.com/intel/pmem-csi/pkg/volumepathhandler"
	"github.com/intel/pmem-csi/pkg/xfs"
)

const (
	// ProvisionerIdentity is supposed to pass to NodePublish
	// only in case of Persistent volumes that were provisioned by
	// the driver
	volumeProvisionerIdentity = "storage.kubernetes.io/csiProvisionerIdentity"
	defaultFilesystem         = "ext4"

	// kataContainersImageFilename is the image file that Kata Containers
	// needs to make available inside the VM.
	kataContainersImageFilename = "kata-containers-pmem-csi-vm.img"

	// "-o dax" is said to be deprecated (https://www.kernel.org/doc/Documentation/filesystems/dax.txt)
	// but in practice works across a wider range of kernel versions whereas
	// "-o dax=always", the recommended alternative, fails on old kernels.
	// Given that "-o dax" is part of the kernel API, it's unlikely that
	// support for it really gets removed, therefore we continue to use it.
	daxMountFlag = "dax"
)

type nodeServer struct {
	nodeCaps []*csi.NodeServiceCapability
	cs       *nodeControllerServer
	// Driver deployed to provision only ephemeral volumes(only for Kubernetes v1.15)
	mounter mount.Interface

	// A directory for additional mount points.
	mountDirectory string
}

var _ csi.NodeServer = &nodeServer{}
var _ grpcserver.Service = &nodeServer{}
var volumeMutex = keymutex.NewHashed(-1)

func NewNodeServer(cs *nodeControllerServer, mountDirectory string) *nodeServer {
	return &nodeServer{
		nodeCaps: []*csi.NodeServiceCapability{
			{
				Type: &csi.NodeServiceCapability_Rpc{
					Rpc: &csi.NodeServiceCapability_RPC{
						Type: csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME,
					},
				},
			},
		},
		cs:             cs,
		mounter:        mount.New(""),
		mountDirectory: mountDirectory,
	}
}

func (ns *nodeServer) RegisterService(rpcServer *grpc.Server) {
	csi.RegisterNodeServer(rpcServer, ns)
}

func (ns *nodeServer) NodeGetInfo(ctx context.Context, req *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	return &csi.NodeGetInfoResponse{
		NodeId: ns.cs.nodeID,
		AccessibleTopology: &csi.Topology{
			Segments: map[string]string{
				DriverTopologyKey: ns.cs.nodeID,
			},
		},
	}, nil
}

func (ns *nodeServer) NodeGetCapabilities(ctx context.Context, req *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	return &csi.NodeGetCapabilitiesResponse{
		Capabilities: ns.nodeCaps,
	}, nil
}

func (ns *nodeServer) NodeGetVolumeStats(ctx context.Context, req *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (ns *nodeServer) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	logger := klog.FromContext(ctx).WithValues("volume-id", volumeID)
	ctx = klog.NewContext(ctx, logger)

	// Check arguments
	if req.GetVolumeCapability() == nil {
		return nil, status.Error(codes.InvalidArgument, "Volume capability missing in request")
	}
	if volumeID == "" {
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
	}
	if len(req.GetTargetPath()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Target path missing in request")
	}

	// Serialize by VolumeId
	volumeMutex.LockKey(volumeID)
	defer func() {
		_ = volumeMutex.UnlockKey(volumeID)
	}()

	var ephemeral bool
	var device *pmdmanager.PmemDeviceInfo
	var err error

	srcPath := req.GetStagingTargetPath()
	targetPath := req.GetTargetPath()
	mountFlags := req.GetVolumeCapability().GetMount().GetMountFlags()
	readOnly := req.GetReadonly()
	fsType := req.GetVolumeCapability().GetMount().GetFsType()
	volumeContext := req.GetVolumeContext()
	// volumeContext contains the original volume name for persistent volumes.
	logger.V(3).Info("Publishing volume",
		"target-path", targetPath,
		"source-path", srcPath,
		"read-only", readOnly,
		"mount-flags", mountFlags,
		"fs-type", fsType,
		"volume-context", volumeContext,
	)

	// Kubernetes v1.16+ would request ephemeral volumes via VolumeContext
	val, ok := req.GetVolumeContext()[parameters.Ephemeral]
	if ok {
		b, err := strconv.ParseBool(val)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		ephemeral = b
	} else {
		// If met all below conditions we still treat as a ephemeral volume request:
		// 1) No Volume found with given volume id
		// 2) No provisioner info found in VolumeContext "storage.kubernetes.io/csiProvisionerIdentity"
		// 3) No StagingPath in the request
		if device, err = ns.cs.dm.GetDevice(ctx, volumeID); err != nil && !errors.Is(err, pmemerr.DeviceNotFound) {
			return nil, status.Errorf(codes.Internal, "failed to get device details for volume id '%s': %v", volumeID, err)
		}
		_, ok := req.GetVolumeContext()[volumeProvisionerIdentity]
		ephemeral = device == nil && !ok && len(srcPath) == 0
	}

	var volumeParameters parameters.Volume
	if ephemeral {
		v, err := parameters.Parse(parameters.EphemeralVolumeOrigin, req.GetVolumeContext())
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "ephemeral inline volume parameters: "+err.Error())
		}
		volumeParameters = v

		device, err := ns.createEphemeralDevice(ctx, req, volumeParameters)
		if err != nil {
			// createEphemeralDevice() returns status.Error, so safe to return
			return nil, err
		}
		srcPath = device.Path
		if v.GetUsage() == parameters.UsageAppDirect {
			mountFlags = append(mountFlags, daxMountFlag)
		}
	} else {
		// Validate parameters.
		v, err := parameters.Parse(parameters.PersistentVolumeOrigin, req.GetVolumeContext())
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "persistent volume context: "+err.Error())
		}
		volumeParameters = v

		dm, err := ns.getDeviceManagerForVolume(ctx, volumeID)
		if err != nil {
			return nil, err
		}

		if device, err = dm.GetDevice(ctx, volumeID); err != nil {
			if errors.Is(err, pmemerr.DeviceNotFound) {
				return nil, status.Errorf(codes.NotFound, "no device found with volume id %q: %v", volumeID, err)
			}
			return nil, status.Errorf(codes.Internal, "failed to get device details for volume id %q: %v", volumeID, err)
		}
		mountFlags = append(mountFlags, "bind")
	}

	if readOnly {
		mountFlags = append(mountFlags, "ro")
	}

	rawBlock := false
	switch req.VolumeCapability.GetAccessType().(type) {
	case *csi.VolumeCapability_Block:
		rawBlock = true
		// For block volumes, source path is the actual Device path
		srcPath = device.Path
	case *csi.VolumeCapability_Mount:
		if !ephemeral && len(srcPath) == 0 {
			return nil, status.Error(codes.FailedPrecondition, "Staging target path missing in request")
		}

		notMnt, err := mount.IsNotMountPoint(ns.mounter, targetPath)
		if err != nil && !os.IsNotExist(err) {
			return nil, status.Error(codes.Internal, "validate target path: "+err.Error())
		}
		if !notMnt {
			// Check if mount is compatible. Return OK if these match:
			// 1) Requested target path MUST match the published path of that volume ID
			// 2) VolumeCapability MUST match
			//    VolumeCapability/Mountflags must match used flags.
			//    VolumeCapability/fsType (if present in request) must match used fsType.
			// 3) Readonly MUST match
			// If there is mismatch of any of above, we return ALREADY_EXISTS error.
			mpList, err := ns.mounter.List()
			if err != nil {
				return nil, status.Errorf(codes.Internal, "Failed to fetch existing mount details while checking %q: %v", targetPath, err)
			}
			for i := len(mpList) - 1; i >= 0; i-- {
				if mpList[i].Path == targetPath {
					logger.V(5).Info("Found mounted filesystem",
						"mount-options", mpList[i].Opts,
						"fs-type", mpList[i].Type,
					)
					if (fsType == "" || mpList[i].Type == fsType) && findMountFlags(mountFlags, mpList[i].Opts) {
						logger.V(3).Info("Parameters match existing filesystem, done")
						return &csi.NodePublishVolumeResponse{}, nil
					}
					break
				}
			}
			logger.V(3).Info("Parameters do not match existing filesystem, bailing out")
			return nil, status.Error(codes.AlreadyExists, "Volume published but is incompatible")
		}
	}

	if rawBlock && volumeParameters.GetKataContainers() {
		// We cannot pass block devices with DAX semantic into QEMU.
		// TODO: add validation of CreateVolumeRequest.VolumeCapabilities and already detect the problem there.
		return nil, status.Error(codes.InvalidArgument, "raw block volumes are incompatible with Kata Containers")
	}

	// We always (bind) mount. This is not strictly necessary for
	// Kata Containers and persistent volumes because we could use
	// the mounted filesystem at the staging path, but done anyway
	// for two reason:
	// - avoids special cases
	// - we don't have the staging path in NodeUnpublishVolume
	//   and can't undo some of the operations there if they refer to
	//   the staging path
	hostMount := targetPath
	if volumeParameters.GetKataContainers() {
		// Create a mount point inside our state directory. Mount directory gets created here,
		// the mount point itself in ns.mount().
		if err := os.MkdirAll(ns.mountDirectory, os.FileMode(0755)); err != nil && !os.IsExist(err) {
			return nil, status.Error(codes.Internal, "create parent directory for mounts: "+err.Error())
		}
		hostMount = filepath.Join(ns.mountDirectory, req.GetVolumeId())
	}
	if err := ns.mount(ctx, srcPath, hostMount, mountFlags, rawBlock); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	if ephemeral && fsType == "xfs" {
		if err := xfs.ConfigureFS(hostMount); err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
	}

	if !volumeParameters.GetKataContainers() {
		// A normal volume, return early.
		return &csi.NodePublishVolumeResponse{}, nil
	}

	// When we get here, the volume is for Kata Containers and the
	// following has already happened:
	// - persistent filesystem volumes or ephemeral volume: formatted and mounted at hostMount
	// - persistent raw block volumes: we bailed out with an error
	//
	// To support Kata Containers for ephemeral and persistent filesystem volumes,
	// we now need to:
	// - create the image file inside the mounted volume
	// - bind-mount the partition inside that file to a loop device
	// - mount the loop device instead of the original volume
	//
	// All of that has to be idempotent (because we might get
	// killed while working on this) *and* we have to undo it when
	// returning a failure (because then NodePublishVolume might
	// not be called again - see in particular the final errors in
	// https://github.com/kubernetes/kubernetes/blob/ca532c6fb2c08f859eca13e0557f3b2aec9a18e0/pkg/volume/csi/csi_client.go#L627-L649).

	// There's some overhead for the imagefile inside the host filesystem, but that should be small
	// relative to the size of the volumes, so we simply create an image file that is as large as
	// the mounted filesystem allows. Create() is not idempotent, so we have to check for the
	// file before overwriting something that was already created earlier.
	imageFile := filepath.Join(hostMount, kataContainersImageFilename)
	if _, err := os.Stat(imageFile); err != nil {
		if !os.IsNotExist(err) {
			return nil, status.Error(codes.Internal, "unexpected error while checking for image file: "+err.Error())
		}
		var imageFsType imagefile.FsType
		switch fsType {
		case "xfs":
			imageFsType = imagefile.Xfs
		case "":
			fallthrough
		case "ext4":
			imageFsType = imagefile.Ext4
		default:
			return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("fsType %q not supported for Kata Containers", fsType))
		}
		if err := imagefile.Create(imageFile, 0 /* no fixed size */, imageFsType); err != nil {
			return nil, status.Error(codes.Internal, "create Kata Container image file: "+err.Error())
		}
	}

	// If this offset ever changes, then we have to make future versions of PMEM-CSI more
	// flexible and dynamically determine the offset. For now we assume that the
	// file was created by the current version and thus use the fixed offset.
	offset := int64(imagefile.HeaderSize)
	handler := volumepathhandler.VolumePathHandler{}
	loopDev, err := handler.AttachFileDeviceWithOffset(ctx, imageFile, offset)
	if err != nil {
		return nil, status.Error(codes.Internal, "create loop device: "+err.Error())
	}

	// TODO: Try to mount with dax first, fall back to mount without it if not supported.
	if err := ns.mount(ctx, loopDev, targetPath, []string{}, false); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &csi.NodePublishVolumeResponse{}, nil
}

func (ns *nodeServer) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	targetPath := req.GetTargetPath()
	logger := klog.FromContext(ctx).WithValues("volume-id", volumeID, "target-path", targetPath)
	ctx = klog.NewContext(ctx, logger)

	// Check arguments
	if volumeID == "" {
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
	}
	if targetPath == "" {
		return nil, status.Error(codes.InvalidArgument, "Target path missing in request")
	}

	// Serialize by VolumeId
	volumeMutex.LockKey(volumeID)
	defer func() {
		_ = volumeMutex.UnlockKey(volumeID)
	}()

	var vol *nodeVolume
	if vol = ns.cs.getVolumeByID(volumeID); vol == nil {
		// For ephemeral volumes we use volumeID as volume name.
		vol = ns.cs.getVolumeByName(volumeID)
	}

	// The CSI spec 1.2 requires that the SP returns NOT_FOUND
	// when the volume is not known.  This is problematic for
	// idempotent calls, in particular for ephemeral volumes,
	// because the first call will remove the volume and then the
	// second would fail. Even for persistent volumes this may be
	// problematic and therefore it was already changed for
	// ControllerUnpublishVolume
	// (https://github.com/container-storage-interface/spec/pull/375).
	//
	// For NodeUnpublishVolume, a bug is currently pending in
	// https://github.com/kubernetes/kubernetes/issues/90752.

	// Check if the target path is really a mount point. If it's not a mount point *and* we don't
	// have such a volume, then we are done.
	notMnt, err := ns.mounter.IsLikelyNotMountPoint(targetPath)
	if (notMnt || err != nil && !os.IsNotExist(err)) && vol == nil {
		logger.V(3).Info("Target path is not a mount point, no such volume -> done")
		return &csi.NodeUnpublishVolumeResponse{}, nil
	}

	// If we don't have volume information, we can't proceed. But
	// what we return depends on the circumstances.
	if vol == nil {
		if err == nil && !notMnt {
			// It is a mount point and we don't know the volume. Don't
			// do anything because the call is invalid. We return
			// NOT_FOUND as required by the spec.
			return nil, status.Errorf(codes.NotFound, "no volume found with volume id %q", volumeID)
		}
		// No volume, no mount point. Looks like an
		// idempotent call for an operation that was
		// completed earlier, so don't return an
		// error.
		return &csi.NodeUnpublishVolumeResponse{}, nil
	}

	p, err := parameters.Parse(parameters.NodeVolumeOrigin, vol.Params)
	if err != nil {
		// This should never happen because PMEM-CSI itself created these parameters.
		// But if it happens, better fail and force an admin to recover instead of
		// potentially destroying data.
		return nil, status.Errorf(codes.Internal, "previously stored volume parameters for volume with ID %q: %v", volumeID, err)
	}

	// Unmounting the image if still mounted. It might have been unmounted before if
	// a previous NodeUnpublishVolume call was interrupted.
	if err == nil && !notMnt {
		logger.V(3).Info("Unmounting at target path")
		if err := ns.mounter.Unmount(targetPath); err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		logger.V(5).Info("Unmounted")
	}

	if p.GetKataContainers() {
		if err := ns.nodeUnpublishKataContainerImage(ctx, req, p); err != nil {
			return nil, err
		}
	}

	err = os.Remove(targetPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, status.Error(codes.Internal, "unexpected error while removing target path: "+err.Error())
	}
	logger.V(5).Info("Target path removed with harmless error or no error", "error", err)

	if p.GetPersistency() == parameters.PersistencyEphemeral {
		if _, err := ns.cs.DeleteVolume(ctx, &csi.DeleteVolumeRequest{VolumeId: vol.ID}); err != nil {
			return nil, status.Error(codes.Internal, fmt.Sprintf("Failed to delete ephemeral volume %s: %s", volumeID, err.Error()))
		}
	}

	return &csi.NodeUnpublishVolumeResponse{}, nil
}

func (ns *nodeServer) nodeUnpublishKataContainerImage(ctx context.Context, req *csi.NodeUnpublishVolumeRequest, p parameters.Volume) error {
	// Reconstruct where the volume was mounted before creating the image file.
	hostMount := filepath.Join(ns.mountDirectory, req.GetVolumeId())

	// We know the parent and the well-known image name, so we have the full path now
	// and can detach the loop device from it.
	imageFile := filepath.Join(hostMount, kataContainersImageFilename)

	logger := klog.FromContext(ctx).WithName("nodeUnpublishKataContainerImage").WithValues("image-file", imageFile)
	ctx = klog.NewContext(ctx, logger)

	// This will warn if the loop device is not found, but won't treat that as an error.
	// This is the behavior that we want.
	logger.V(3).Info("Removing Kata Containers image file loop device")
	handler := volumepathhandler.VolumePathHandler{}
	if err := handler.DetachFileDevice(ctx, imageFile); err != nil {
		return status.Error(codes.Internal, fmt.Sprintf("remove loop device for Kata Container image file %q: %v", imageFile, err))
	}

	// We do *not* remove the image file. It may be needed again
	// when mounting a persistent volume a second time. If not,
	// it'll get deleted together with the device. But before
	// the device can be deleted, we need to unmount it.
	notMnt, err := ns.mounter.IsLikelyNotMountPoint(hostMount)
	if notMnt || err != nil && !os.IsNotExist(err) {
		logger.V(3).Info("Kata Container image file not mounted", "mountpoint", hostMount)
	} else {
		logger.V(3).Info("Unmounting Kata Containers image file mount", "mountpoint", hostMount)
		if err := ns.mounter.Unmount(hostMount); err != nil {
			return status.Error(codes.Internal, fmt.Sprintf("unmount ephemeral Kata Container volume: %v", err))
		}
	}
	if err := os.Remove(hostMount); err != nil && !os.IsNotExist(err) {
		return status.Error(codes.Internal, "unexpected error while removing ephemeral volume mount point: "+err.Error())
	}

	return nil
}

func (ns *nodeServer) NodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	stagingtargetPath := req.GetStagingTargetPath()
	logger := klog.FromContext(ctx).WithValues("volume-id", volumeID, "staging-target-path", stagingtargetPath)
	ctx = klog.NewContext(ctx, logger)

	// Check arguments
	if volumeID == "" {
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
	}
	if stagingtargetPath == "" {
		return nil, status.Error(codes.InvalidArgument, "Target path missing in request")
	}
	if req.GetVolumeCapability() == nil {
		return nil, status.Error(codes.InvalidArgument, "Volume capability missing in request")
	}

	// We should do nothing for block device usage
	switch req.VolumeCapability.GetAccessType().(type) {
	case *csi.VolumeCapability_Block:
		return &csi.NodeStageVolumeResponse{}, nil
	}

	requestedFsType := req.GetVolumeCapability().GetMount().GetFsType()
	if requestedFsType == "" {
		// Default to ext4 filesystem
		requestedFsType = defaultFilesystem
	}

	v, err := parameters.Parse(parameters.PersistentVolumeOrigin, req.GetVolumeContext())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "persistent volume context: "+err.Error())
	}

	// Serialize by VolumeId
	volumeMutex.LockKey(req.GetVolumeId())
	defer func() {
		_ = volumeMutex.UnlockKey(req.GetVolumeId())
	}()

	mountOptions := req.GetVolumeCapability().GetMount().GetMountFlags()
	logger.V(3).Info("Staging volume",
		"fs-type", requestedFsType,
		"mount-options", mountOptions,
	)

	dm, err := ns.getDeviceManagerForVolume(ctx, volumeID)
	if err != nil {
		return nil, err
	}

	device, err := dm.GetDevice(ctx, volumeID)
	if err != nil {
		if errors.Is(err, pmemerr.DeviceNotFound) {
			return nil, status.Errorf(codes.NotFound, "no device found with volume id %q: %v", volumeID, err)
		}
		return nil, status.Errorf(codes.Internal, "failed to get device details for volume id %q: %v", volumeID, err)
	}

	// Check does devicepath already contain a filesystem?
	existingFsType, err := determineFilesystemType(ctx, device.Path)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// what to do if existing file system is detected;
	if existingFsType != "" {
		// Is existing filesystem type same as requested?
		if existingFsType == requestedFsType {
			logger.V(4).Info("Skipping mkfs as file system already exists on device", "device", device.Path)
		} else {
			return nil, status.Error(codes.AlreadyExists, "File system with different type exists")
		}
	} else {
		if err = ns.provisionDevice(ctx, device, requestedFsType); err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
	}

	if v.GetUsage() == parameters.UsageAppDirect {
		mountOptions = append(mountOptions, daxMountFlag)
	}

	if err = ns.mount(ctx, device.Path, stagingtargetPath, mountOptions, false /* raw block */); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	if requestedFsType == "xfs" {
		if err := xfs.ConfigureFS(stagingtargetPath); err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
	}

	return &csi.NodeStageVolumeResponse{}, nil
}

func (ns *nodeServer) NodeUnstageVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	stagingtargetPath := req.GetStagingTargetPath()
	logger := klog.FromContext(ctx).WithValues("volume-id", volumeID, "staging-target-path", stagingtargetPath)
	ctx = klog.NewContext(ctx, logger)

	// Check arguments
	if volumeID == "" {
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
	}
	if stagingtargetPath == "" {
		return nil, status.Error(codes.InvalidArgument, "Target path missing in request")
	}

	// Serialize by VolumeId
	volumeMutex.LockKey(volumeID)
	defer func() {
		_ = volumeMutex.UnlockKey(volumeID)
	}()

	logger.V(3).Info("Unstage volume")
	dm, err := ns.getDeviceManagerForVolume(ctx, volumeID)
	if err != nil {
		return nil, err
	}
	// by spec, we have to return OK if asked volume is not mounted on asked path,
	// so we look up the current device by volumeID and see is that device
	// mounted on staging target path
	if _, err := dm.GetDevice(ctx, volumeID); err != nil {
		if errors.Is(err, pmemerr.DeviceNotFound) {
			return nil, status.Errorf(codes.NotFound, "no device found with volume id '%s': %s", volumeID, err.Error())
		}
		return nil, status.Errorf(codes.Internal, "failed to get device details for volume id '%s': %s", volumeID, err.Error())
	}

	// Find out device name for mounted path
	mountedDev, _, err := mount.GetDeviceNameFromMount(ns.mounter, stagingtargetPath)
	if err != nil {
		return nil, err
	}
	if mountedDev == "" {
		logger.Info("No device name found for staging target path, skipping unmount")
		return &csi.NodeUnstageVolumeResponse{}, nil
	}
	logger.V(3).Info("Unmounting", "device", mountedDev)
	if err := ns.mounter.Unmount(stagingtargetPath); err != nil {
		return nil, err
	}

	return &csi.NodeUnstageVolumeResponse{}, nil
}

func (ns *nodeServer) NodeExpandVolume(context.Context, *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

// createEphemeralDevice creates new pmem device for given req.
// On failure it returns one of status errors.
func (ns *nodeServer) createEphemeralDevice(ctx context.Context, req *csi.NodePublishVolumeRequest, p parameters.Volume) (*pmdmanager.PmemDeviceInfo, error) {
	ctx, _ = pmemlog.WithName(ctx, "createEphemeralDevice")

	// If the caller has use the heuristic for detecting ephemeral volumes, the flag won't
	// be set. Fix that here.
	ephemeral := parameters.PersistencyEphemeral
	p.Persistency = &ephemeral

	// Create new device, using the same code that the normal CreateVolume also uses,
	// so internally this volume will be tracked like persistent volumes.
	volumeID, _, err := ns.cs.createVolumeInternal(ctx, p, req.GetVolumeId(),
		[]*csi.VolumeCapability{req.VolumeCapability},
		&csi.CapacityRange{RequiredBytes: p.GetSize()},
	)
	if err != nil {
		// This is already a status error.
		return nil, err
	}

	device, err := ns.cs.dm.GetDevice(ctx, volumeID)
	if err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("ephemeral inline volume: device not found after creating volume %q: %v", volumeID, err))
	}

	// Create filesystem
	if err := ns.provisionDevice(ctx, device, req.GetVolumeCapability().GetMount().GetFsType()); err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("ephemeral inline volume: failed to create filesystem: %v", err))
	}

	return device, nil
}

// provisionDevice initializes the device with requested filesystem.
// It can be called multiple times for the same device (idempotent).
func (ns *nodeServer) provisionDevice(ctx context.Context, device *pmdmanager.PmemDeviceInfo, fsType string) error {
	ctx, logger := pmemlog.WithName(ctx, "provisionDevice")

	if fsType == "" {
		// Empty FsType means "unspecified" and we pick default, currently hard-coded to ext4
		fsType = defaultFilesystem
	}

	// Check does devicepath already contain a filesystem?
	existingFsType, err := determineFilesystemType(ctx, device.Path)
	if err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	if existingFsType != "" {
		// Is existing filesystem type same as requested?
		if existingFsType == fsType {
			logger.V(4).Info("Skipping mkfs because file system already exists", "fs-type", existingFsType, "device", device.Path)
			return nil
		}
		return status.Error(codes.AlreadyExists, "File system with different type exists")
	}
	cmd := ""
	var args []string
	// hard-code block size to 4k to avoid smaller values and trouble to dax mount option
	switch fsType {
	case "ext4":
		cmd = "mkfs.ext4"
		args = []string{"-b", "4096", "-E", "stride=512,stripe_width=512", "-F", device.Path}
	case "xfs":
		cmd = "mkfs.xfs"
		// reflink=0: reflink and DAX are mutually exclusive
		// (http://man7.org/linux/man-pages/man8/mkfs.xfs.8.html).
		// su=2m,sw=1: use 2MB-aligned and -sized block allocations
		args = []string{"-b", "size=4096", "-m", "reflink=0", "-d", "su=2m,sw=1", "-f", device.Path}
	default:
		return fmt.Errorf("Unsupported filesystem '%s'. Supported filesystems types: 'xfs', 'ext4'", fsType)
	}

	output, err := pmemexec.RunCommand(ctx, cmd, args...)
	if err != nil {
		return fmt.Errorf("mkfs failed: output:[%s] err:[%v]", output, err)
	}

	return nil
}

// mount creates the target path (parent must exist) and mounts the source there. It is idempotent.
func (ns *nodeServer) mount(ctx context.Context, sourcePath, targetPath string, mountOptions []string, rawBlock bool) error {
	notMnt, err := ns.mounter.IsLikelyNotMountPoint(targetPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to determine if '%s' is a valid mount point: %s", targetPath, err.Error())
	}
	if !notMnt {
		return nil
	}

	// Create target path, using a file for raw block bind mounts
	// or a directory for filesystems. Might already exist from a
	// previous call or because Kubernetes erroneously created it
	// for us.
	if rawBlock {
		f, err := os.OpenFile(targetPath, os.O_CREATE, os.FileMode(0644))
		if err == nil {
			defer f.Close()
		} else if !os.IsExist(err) {
			return fmt.Errorf("create target device file: %w", err)
		}
	} else {
		if err := os.Mkdir(targetPath, os.FileMode(0755)); err != nil && !os.IsExist(err) {
			return fmt.Errorf("create target directory: %w", err)
		}
	}

	// We supposed to use "mount" package - ns.mounter.Mount()
	// but it seems not supporting -c "canonical" option, so do it with exec()
	// added -c makes canonical mount, resulting in mounted path matching what LV thinks is lvpath.
	args := []string{"-c"}
	if len(mountOptions) != 0 {
		args = append(args, "-o", strings.Join(mountOptions, ","))
	}
	args = append(args, sourcePath, targetPath)
	if _, err := pmemexec.RunCommand(ctx, "mount", args...); err != nil {
		return fmt.Errorf("mount filesystem failed: %s", err.Error())
	}

	return nil
}

// getDeviceManagerForVolume checks the stored volume parametes for the
// given id and returns the device manager which creates that volume.
// NOT_FOUND is returned when the volume does not exist.
func (ns *nodeServer) getDeviceManagerForVolume(ctx context.Context, id string) (pmdmanager.PmemDeviceManager, error) {

	vol := ns.cs.getVolumeByID(id)
	if vol == nil {
		return nil, status.Errorf(codes.NotFound, "unknown volume: "+id)
	}

	v, err := parameters.Parse(parameters.NodeVolumeOrigin, vol.Params)
	if err != nil {
		return nil, fmt.Errorf("failed to parse volume parameters for volume %q: %v", id, err)
	}

	dm := ns.cs.dm
	if v.GetDeviceMode() != dm.GetMode() {
		dm, err = pmdmanager.New(ctx, v.GetDeviceMode(), 0)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize device manager for volume %q, volume mode %q: %v", id, v.GetDeviceMode(), err)
		}
	}

	return dm, nil
}

// This is based on function used in LV-CSI driver
func determineFilesystemType(ctx context.Context, devicePath string) (string, error) {
	if devicePath == "" {
		return "", fmt.Errorf("null device path")
	}
	// Use `file -bsL` to determine whether any filesystem type is detected.
	// If a filesystem is detected (ie., the output is not "data", we use
	// `blkid` to determine what the filesystem is. We use `blkid` as `file`
	// has inconvenient output.
	// We do *not* use `lsblk` as that requires udev to be up-to-date which
	// is often not the case when a device is erased using `dd`.
	output, err := pmemexec.RunCommand(ctx, "file", "-bsL", devicePath)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(output) == "data" {
		// No filesystem detected.
		return "", nil
	}
	// Some filesystem was detected, use blkid to figure out what it is.
	output, err = pmemexec.RunCommand(ctx, "blkid", "-c", "/dev/null", "-o", "full", devicePath)
	if err != nil {
		return "", err
	}
	if len(output) == 0 {
		return "", fmt.Errorf("no device information for %s", devicePath)
	}

	// expected output format from blkid:
	// devicepath: UUID="<uuid>" TYPE="<filesystem type>"
	attrs := strings.Split(string(output), ":")
	if len(attrs) != 2 {
		return "", fmt.Errorf("Can not parse blkid output: %s", output)
	}
	for _, field := range strings.Fields(attrs[1]) {
		attr := strings.Split(field, "=")
		if len(attr) == 2 && attr[0] == "TYPE" {
			return strings.Trim(attr[1], "\""), nil
		}
	}
	return "", fmt.Errorf("no filesystem type detected for %s", devicePath)
}

// findMountFlags finds existence of all flags in findIn array
func findMountFlags(flags []string, findIn []string) bool {
	for _, f := range flags {
		// bind mounts are not visible in mount options
		// so ignore the flag
		if f == "bind" {
			continue
		}
		found := false
		for _, fIn := range findIn {
			if f == "dax=always" && fIn == "dax" ||
				f == "dax" && fIn == "dax=always" ||
				f == fIn {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	return true
}
