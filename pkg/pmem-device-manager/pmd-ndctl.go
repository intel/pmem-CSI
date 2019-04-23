package pmdmanager

import (
	"fmt"

	"github.com/intel/pmem-csi/pkg/ndctl"
	"k8s.io/klog/glog"
	"k8s.io/kubernetes/pkg/util/mount"
)

type pmemNdctl struct {
	ctx *ndctl.Context
}

var _ PmemDeviceManager = &pmemNdctl{}

//NewPmemDeviceManagerNdctl Instantiates a new ndctl based pmem device manager
func NewPmemDeviceManagerNdctl() (PmemDeviceManager, error) {
	ctx, err := ndctl.NewContext()
	if err != nil {
		return nil, fmt.Errorf("Failed to initialize pmem context: %s", err.Error())
	}
	// Check is /sys writable. If not then there is no point starting
	mounter := mount.New("")
	mounts, err := mounter.List()
	for i := range mounts {
		glog.Infof("NewPmemDeviceManagerNdctl: Check mounts: device=%s path=%s opts=%s",
			mounts[i].Device, mounts[i].Path, mounts[i].Opts)
		if mounts[i].Device == "sysfs" && mounts[i].Path == "/sys" {
			for _, opt := range mounts[i].Opts {
				if opt == "rw" {
					glog.Infof("NewPmemDeviceManagerNdctl: /sys mounted read-write, good")
				} else if opt == "ro" {
					return nil, fmt.Errorf("FATAL: /sys mounted read-only, can not operate\n")
				}
			}
			break
		}
	}

	return &pmemNdctl{
		ctx: ctx,
	}, nil
}

func (pmem *pmemNdctl) GetCapacity() (map[string]uint64, error) {
	Capacity := map[string]uint64{}
	nsmodes := []ndctl.NamespaceMode{ndctl.FsdaxMode, ndctl.SectorMode}
	var capacity uint64
	for _, bus := range pmem.ctx.GetBuses() {
		for _, r := range bus.ActiveRegions() {
			available := r.MaxAvailableExtent()
			if available > capacity {
				capacity = available
			}
		}
	}
	// we set same capacity for all namespace modes
	// TODO: we should maintain all modes capacity when adding or subtracting
	// from upper layer, not done right now!!
	for _, nsmod := range nsmodes {
		Capacity[string(nsmod)] = capacity
	}
	return Capacity, nil
}

func (pmem *pmemNdctl) CreateDevice(name string, size uint64, nsmode string) error {
	devicemutex.Lock()
	defer devicemutex.Unlock()
	// Check that such name does not exist. In certain error states, for example when
	// namespace creation works but device zeroing fails (missing /dev/pmemX.Y in container),
	// this function is asked to create new devices repeatedly, forcing running out of space.
	// Avoid device filling with garbage entries by returning error.
	// Overall, no point having more than one namespace with same name.
	_, err := pmem.GetDevice(name)
	if err == nil {
		glog.Infof("Device with name: %s already exists, refuse to create another", name)
		return fmt.Errorf("CreateDevice: Failed: namespace with that name exists")
	}
	// align up by 1 GB, also compensate for libndctl giving us 1 GB less than we ask
	var align uint64 = 1024 * 1024 * 1024
	size /= align
	size += 2
	size *= align
	ns, err := pmem.ctx.CreateNamespace(ndctl.CreateNamespaceOpts{
		Name:  name,
		Size:  size,
		Align: align,
		Mode:  ndctl.NamespaceMode(nsmode),
	})
	if err != nil {
		return err
	}
	data, _ := ns.MarshalJSON() //nolint: gosec
	glog.Infof("Namespace created: %s", data)
	// clear start of device to avoid old data being recognized as file system
	device, err := pmem.GetDevice(name)
	if err != nil {
		return err
	}
	err = ClearDevice(device, false)
	if err != nil {
		return err
	}

	return nil
}

func (pmem *pmemNdctl) DeleteDevice(name string, flush bool) error {
	devicemutex.Lock()
	defer devicemutex.Unlock()
	device, err := pmem.GetDevice(name)
	if err != nil {
		return err
	}
	err = ClearDevice(device, flush)
	if err != nil {
		return err
	}
	return pmem.ctx.DestroyNamespaceByName(name)
}

func (pmem *pmemNdctl) FlushDeviceData(name string) error {
	devicemutex.Lock()
	defer devicemutex.Unlock()
	device, err := pmem.GetDevice(name)
	if err != nil {
		return err
	}
	return ClearDevice(device, true)
}

func (pmem *pmemNdctl) GetDevice(name string) (PmemDeviceInfo, error) {
	ns, err := pmem.ctx.GetNamespaceByName(name)
	if err != nil {
		return PmemDeviceInfo{}, err
	}

	return namespaceToPmemInfo(ns), nil
}

func (pmem *pmemNdctl) ListDevices() ([]PmemDeviceInfo, error) {
	devices := []PmemDeviceInfo{}
	for _, ns := range pmem.ctx.GetActiveNamespaces() {
		devices = append(devices, namespaceToPmemInfo(ns))
	}
	return devices, nil
}

func namespaceToPmemInfo(ns *ndctl.Namespace) PmemDeviceInfo {
	return PmemDeviceInfo{
		Name: ns.Name(),
		Path: "/dev/" + ns.BlockDeviceName(),
		Size: ns.Size(),
	}
}
