/*
Copyright 2017 The Kubernetes Authors.

SPDX-License-Identifier: Apache-2.0
*/

package pmemcsidriver

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"sync"

	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
	"k8s.io/klog/v2"

	"github.com/container-storage-interface/spec/lib/go/csi"

	pmemerr "github.com/intel/pmem-csi/pkg/errors"
	grpcserver "github.com/intel/pmem-csi/pkg/grpc-server"
	pmemlog "github.com/intel/pmem-csi/pkg/logger"
	"github.com/intel/pmem-csi/pkg/pmem-csi-driver/parameters"
	pmdmanager "github.com/intel/pmem-csi/pkg/pmem-device-manager"
	pmemstate "github.com/intel/pmem-csi/pkg/pmem-state"
	"k8s.io/utils/keymutex"
)

type nodeVolume struct {
	ID     string            `json:"id"`
	Size   int64             `json:"size"`
	Params map[string]string `json:"parameters"`
}

type nodeControllerServer struct {
	*DefaultControllerServer
	nodeID      string
	dm          pmdmanager.PmemDeviceManager
	sm          pmemstate.StateManager
	pmemVolumes map[string]*nodeVolume // map of reqID:nodeVolume
	mutex       sync.Mutex             // lock for pmemVolumes
}

var _ csi.ControllerServer = &nodeControllerServer{}
var _ grpcserver.Service = &nodeControllerServer{}

var nodeVolumeMutex = keymutex.NewHashed(-1)

func NewNodeControllerServer(ctx context.Context, nodeID string, dm pmdmanager.PmemDeviceManager, sm pmemstate.StateManager) *nodeControllerServer {
	ctx, logger := pmemlog.WithName(ctx, "NewNodeControllerServer")

	serverCaps := []csi.ControllerServiceCapability_RPC_Type{
		csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
		csi.ControllerServiceCapability_RPC_LIST_VOLUMES,
		csi.ControllerServiceCapability_RPC_GET_CAPACITY,
	}

	ncs := &nodeControllerServer{
		DefaultControllerServer: NewDefaultControllerServer(serverCaps),
		nodeID:                  nodeID,
		dm:                      dm,
		sm:                      sm,
		pmemVolumes:             map[string]*nodeVolume{},
	}

	// Restore provisioned volumes from state.
	if sm != nil {
		// Get actual devices at DeviceManager
		devices, err := dm.ListDevices(ctx)
		if err != nil {
			logger.Error(err, "Failed to get volumes")
		}
		cleanupList := []string{}
		ids, err := sm.GetAll()
		if err != nil {
			logger.Error(err, "Failed to load state")
		}

		for _, id := range ids {
			// retrieve volume info
			vol := &nodeVolume{}
			if err := sm.Get(id, vol); err != nil {
				logger.Error(err, "Failed to retrieve volume info from persistent state", "volume-id", id)
				continue
			}
			v, err := parameters.Parse(parameters.NodeVolumeOrigin, vol.Params)
			if err != nil {
				logger.Error(err, "Failed to parse volume parameters for volume", "volume-id", id)
				continue
			}

			found := false
			if v.GetDeviceMode() != dm.GetMode() {
				dm, err := pmdmanager.New(ctx, v.GetDeviceMode(), 0)
				if err != nil {
					logger.Error(err, "Failed to initialize device manager for state volume", "volume-id", id, "device-mode", v.GetDeviceMode())
					continue
				}

				if _, err := dm.GetDevice(ctx, id); err == nil {
					found = true
				} else if !errors.Is(err, pmemerr.DeviceNotFound) {
					logger.Error(err, "Failed to fetch device for state volume", "volume-id", id, "device-mode", v.GetDeviceMode())
					// Let's ignore this volume
					continue
				}
			} else {
				// See if the device data stored at StateManager is still valid
				for _, devInfo := range devices {
					if devInfo.VolumeId == id {
						found = true
						break
					}
				}
			}

			if found {
				ncs.pmemVolumes[id] = vol
			} else {
				// if not found in DeviceManager's list, add to cleanupList
				cleanupList = append(cleanupList, id)
			}
		}

		for _, id := range cleanupList {
			if err := sm.Delete(id); err != nil {
				logger.Error(err, "Failed to remove stale volume from state", "volume-id", id)
			}
		}
	}

	return ncs
}

func (cs *nodeControllerServer) RegisterService(rpcServer *grpc.Server) {
	csi.RegisterControllerServer(rpcServer, cs)
}

func (cs *nodeControllerServer) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	topology := []*csi.Topology{}

	var resp *csi.CreateVolumeResponse

	if err := cs.ValidateControllerServiceRequest(csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME); err != nil {
		return nil, err
	}

	if req.GetVolumeCapabilities() == nil {
		return nil, status.Error(codes.InvalidArgument, "Volume Capabilities missing in request")
	}

	if len(req.GetName()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Name missing in request")
	}

	p, err := parameters.Parse(parameters.CreateVolumeOrigin, req.GetParameters())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "persistent volume: "+err.Error())
	}

	nodeVolumeMutex.LockKey(req.Name)
	defer func() {
		_ = nodeVolumeMutex.UnlockKey(req.Name)
	}()

	volumeID, size, err := cs.createVolumeInternal(ctx,
		p,
		req.Name,
		req.GetVolumeCapabilities(),
		req.GetCapacityRange(),
	)
	if err != nil {
		// This is already a status error.
		return nil, err
	}

	topology = append(topology, &csi.Topology{
		Segments: map[string]string{
			DriverTopologyKey: cs.nodeID,
		},
	})

	// Prepare the volume context. Including the name is useful for logging.
	p.Name = &req.Name
	volumeContext := p.ToContext()

	resp = &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:           volumeID,
			CapacityBytes:      size,
			AccessibleTopology: topology,
			VolumeContext:      volumeContext,
		},
	}

	return resp, nil
}

func (cs *nodeControllerServer) ControllerModifyVolume(ctx context.Context, req *csi.ControllerModifyVolumeRequest) (*csi.ControllerModifyVolumeResponse, error) {
	return nil, errors.New("not implemented")
}

func (cs *nodeControllerServer) createVolumeInternal(ctx context.Context,
	p parameters.Volume,
	volumeName string,
	volumeCapabilities []*csi.VolumeCapability,
	capacity *csi.CapacityRange,
) (volumeID string, actual int64, statusErr error) {
	logger := klog.FromContext(ctx).WithValues("volume-name", volumeName)
	ctx = klog.NewContext(ctx, logger)

	// Keep volume name as part of volume parameters for use in
	// getVolumeByName.
	p.Name = &volumeName

	asked := capacity.GetRequiredBytes()
	if vol := cs.getVolumeByName(volumeName); vol != nil {
		// Check if the size of existing volume can cover the new request
		logger.V(4).Info("Volume exists", "volume-id", vol.ID, "size", pmemlog.CapacityRef(vol.Size))
		if vol.Size < asked {
			statusErr = status.Error(codes.AlreadyExists, fmt.Sprintf("smaller volume with the same name %q already exists", volumeName))
			return
		}
		// Use existing volume, it's the one the caller asked
		// for earlier (idempotent call):
		volumeID = vol.ID
		actual = vol.Size
		return
	}

	volumeID = generateVolumeID(volumeName)
	logger = logger.WithValues("volume-id", volumeID)
	logger.V(4).Info("Creating new volume", "minimum-size", pmemlog.CapacityRef(asked), "maximum-size", pmemlog.CapacityRef(capacity.GetLimitBytes()))
	ctx = klog.NewContext(ctx, logger)

	// Check do we have entry with newly generated VolumeID already
	if vol := cs.getVolumeByID(volumeID); vol != nil {
		// if we have, that has to be VolumeID collision, because above we checked
		// that we don't have entry with such Name. VolumeID collision is very-very
		// unlikely so we should not get here in any near future, if otherwise state is good.
		statusErr = status.Error(codes.Internal, fmt.Sprintf("VolumeID hash collision between old name %s and new name %s",
			vol.Params[parameters.Name], volumeName))
		return
	}

	// Set which device manager was used to create the volume
	mode := cs.dm.GetMode()
	p.DeviceMode = &mode

	vol := &nodeVolume{
		ID:     volumeID,
		Size:   asked,
		Params: p.ToContext(),
	}
	if cs.sm != nil {
		// Persist new volume state *before* actually creating the volume.
		// Writing this state after creating the volume has the risk that
		// we leak the volume if we don't get around to storing the state.
		if err := cs.sm.Create(volumeID, vol); err != nil {
			statusErr = status.Error(codes.Internal, "store state: "+err.Error())
			return
		}
		defer func() {
			// In case of failure, remove volume from state again because it wasn't created.
			// This is allowed to fail because orphaned entries will be detected eventually.
			if statusErr != nil {
				if err := cs.sm.Delete(volumeID); err != nil {
					logger.Error(err, "Removing volume from persistent state failed")
				}
			}
		}()
	}
	actualSize, err := cs.dm.CreateDevice(ctx, volumeID, uint64(asked), p.GetUsage())
	if err != nil {
		code := codes.Internal
		if errors.Is(err, pmemerr.NotEnoughSpace) {
			code = codes.ResourceExhausted
		}
		statusErr = status.Errorf(code, "device creation failed: %v", err)
		return
	}
	actual = int64(actualSize)
	if vol.Size != actual {
		// Update volume size and store that persistently.
		vol.Size = actual
		if err := cs.sm.Create(volumeID, vol); err != nil {
			// We are in a difficult place now. We have
			// created the volume, but couldn't update the
			// metadata about it. The best we can do now
			// is probably to proceed, hoping that whatever
			// meta data was written is still valid.
			logger.Error(err, "Updating state with new volume size failed")
		}
	}

	cs.mutex.Lock()
	defer cs.mutex.Unlock()
	cs.pmemVolumes[volumeID] = vol
	logger.V(5).Info("Created new volume", "volume", *vol)

	return
}

func (cs *nodeControllerServer) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	logger := klog.FromContext(ctx).WithValues("volume-id", volumeID)
	ctx = klog.NewContext(ctx, logger)

	// Check arguments
	if volumeID == "" {
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
	}

	if err := cs.ValidateControllerServiceRequest(csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME); err != nil {
		return nil, err
	}

	// Serialize by VolumeId
	nodeVolumeMutex.LockKey(volumeID)
	defer nodeVolumeMutex.UnlockKey(volumeID) //nolint: errcheck

	logger.V(4).Info("Starting to delete volume")
	vol := cs.getVolumeByID(volumeID)
	if vol == nil {
		// Already deleted.
		return &csi.DeleteVolumeResponse{}, nil
	}
	p, err := parameters.Parse(parameters.NodeVolumeOrigin, vol.Params)
	if err != nil {
		// This should never happen because PMEM-CSI itself created these parameters.
		// But if it happens, better fail and force an admin to recover instead of
		// potentially destroying data.
		return nil, status.Errorf(codes.Internal, "previously stored volume parameters for volume with ID %q: %v", volumeID, err)
	}

	dm := cs.dm
	if dm.GetMode() != p.GetDeviceMode() {
		dm, err = pmdmanager.New(ctx, p.GetDeviceMode(), 0)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to initialize device manager for volume with ID %q and mode %s: %v", volumeID, p.GetDeviceMode(), err)
		}
	}

	if err := dm.DeleteDevice(ctx, req.VolumeId, p.GetEraseAfter()); err != nil {
		if errors.Is(err, pmemerr.DeviceInUse) {
			return nil, status.Errorf(codes.FailedPrecondition, err.Error())
		}
		return nil, status.Errorf(codes.Internal, "Failed to delete volume: %s", err.Error())
	}
	if cs.sm != nil {
		if err := cs.sm.Delete(req.VolumeId); err != nil {
			logger.Error(err, "Failed to remove volume from state")
		}
	}

	cs.mutex.Lock()
	defer cs.mutex.Unlock()
	delete(cs.pmemVolumes, req.VolumeId)

	logger.V(4).Info("Volume deleted")
	return &csi.DeleteVolumeResponse{}, nil
}

func (cs *nodeControllerServer) ValidateVolumeCapabilities(ctx context.Context, req *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {

	// Check arguments
	if len(req.GetVolumeId()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
	}
	if req.GetVolumeCapabilities() == nil {
		return nil, status.Error(codes.InvalidArgument, "Volume capabilities missing in request")
	}

	vol := cs.getVolumeByID(req.GetVolumeId())
	if vol == nil {
		return nil, status.Error(codes.NotFound, "Volume not created by this controller")
	}
	for _, cap := range req.VolumeCapabilities {
		if cap.GetAccessMode().GetMode() != csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER {
			return &csi.ValidateVolumeCapabilitiesResponse{
				Confirmed: nil,
				Message:   "Driver does not support '" + cap.AccessMode.Mode.String() + "' mode",
			}, nil
		}
	}
	return &csi.ValidateVolumeCapabilitiesResponse{
		Confirmed: &csi.ValidateVolumeCapabilitiesResponse_Confirmed{
			VolumeCapabilities: req.VolumeCapabilities,
			VolumeContext:      req.GetVolumeContext(),
		},
	}, nil
}

func (cs *nodeControllerServer) ListVolumes(ctx context.Context, req *csi.ListVolumesRequest) (*csi.ListVolumesResponse, error) {
	if err := cs.ValidateControllerServiceRequest(csi.ControllerServiceCapability_RPC_LIST_VOLUMES); err != nil {
		return nil, err
	}

	cs.mutex.Lock()
	defer cs.mutex.Unlock()

	// Copy from map into array for pagination.
	vols := make([]*nodeVolume, 0, len(cs.pmemVolumes))
	for _, vol := range cs.pmemVolumes {
		vols = append(vols, vol)
	}

	// Code originally copied from https://github.com/kubernetes-csi/csi-test/blob/f14e3d32125274e0c3a3a5df380e1f89ff7c132b/mock/service/controller.go#L309-L365

	var (
		ulenVols      = int32(len(vols))
		maxEntries    = req.MaxEntries
		startingToken int32
	)

	if v := req.StartingToken; v != "" {
		i, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return nil, status.Errorf(
				codes.Aborted,
				"startingToken=%d !< int32=%d",
				startingToken, math.MaxUint32)
		}
		startingToken = int32(i)
	}

	if startingToken > ulenVols {
		return nil, status.Errorf(
			codes.Aborted,
			"startingToken=%d > len(vols)=%d",
			startingToken, ulenVols)
	}

	// Discern the number of remaining entries.
	rem := ulenVols - startingToken

	// If maxEntries is 0 or greater than the number of remaining entries then
	// set maxEntries to the number of remaining entries.
	if maxEntries == 0 || maxEntries > rem {
		maxEntries = rem
	}

	var (
		i       int
		j       = startingToken
		entries = make(
			[]*csi.ListVolumesResponse_Entry,
			maxEntries)
	)

	for i = 0; i < len(entries); i++ {
		vol := vols[j]
		entries[i] = &csi.ListVolumesResponse_Entry{
			Volume: &csi.Volume{
				VolumeId:      vol.ID,
				CapacityBytes: vol.Size,
			},
		}
		j++
	}

	var nextToken string
	if n := startingToken + int32(i); n < ulenVols {
		nextToken = fmt.Sprintf("%d", n)
	}

	return &csi.ListVolumesResponse{
		Entries:   entries,
		NextToken: nextToken,
	}, nil
}

func (cs *nodeControllerServer) GetCapacity(ctx context.Context, req *csi.GetCapacityRequest) (*csi.GetCapacityResponse, error) {
	cap, err := cs.dm.GetCapacity(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, err.Error())
	}

	return &csi.GetCapacityResponse{
		AvailableCapacity: int64(cap.Available),
		// This is what Kubernetes >= 1.21 will use.
		MaximumVolumeSize: wrapperspb.Int64(int64(cap.MaxVolumeSize)),
	}, nil
}

func (cs *nodeControllerServer) getVolumeByID(volumeID string) *nodeVolume {
	cs.mutex.Lock()
	defer cs.mutex.Unlock()
	if pmemVol, ok := cs.pmemVolumes[volumeID]; ok {
		return pmemVol
	}
	return nil
}

func (cs *nodeControllerServer) getVolumeByName(volumeName string) *nodeVolume {
	cs.mutex.Lock()
	defer cs.mutex.Unlock()
	for _, pmemVol := range cs.pmemVolumes {
		if pmemVol.Params[parameters.Name] == volumeName {
			return pmemVol
		}
	}
	return nil
}

func (cs *nodeControllerServer) ControllerExpandVolume(context.Context, *csi.ControllerExpandVolumeRequest) (*csi.ControllerExpandVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (cs *nodeControllerServer) ControllerGetVolume(context.Context, *csi.ControllerGetVolumeRequest) (*csi.ControllerGetVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func generateVolumeID(name string) string {
	// VolumeID is hashed from Volume Name.
	// Hashing guarantees same ID for repeated requests.
	// Why do we generate new VolumeID via hashing?
	// We can not use Name directly as VolumeID because of at least 2 reasons:
	// 1. allowed max. Name length by CSI spec is 128 chars, which does not fit
	// into LVM volume name (for that we use VolumeID), where groupname+volumename
	// must fit into 126 chars.
	// Ndctl namespace name is even shorter, it can be 63 chars long.
	// 2. CSI spec. allows characters in Name that are not allowed in LVM names.
	hasher := sha256.New224()
	hasher.Write([]byte(name))
	hash := hex.EncodeToString(hasher.Sum(nil))
	// Use first characters of Name in VolumeID to help humans.
	// This also lowers collision probability even more, as an attacker
	// attempting to cause VolumeID collision, has to find another Name
	// producing same sha-224 hash, while also having common first N chars.
	use := 6
	if len(name) < 6 {
		use = len(name)
	}
	id := name[0:use] + "-" + hash
	return id
}
