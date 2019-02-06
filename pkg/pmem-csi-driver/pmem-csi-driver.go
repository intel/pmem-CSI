/*
Copyright 2017 The Kubernetes Authors.
Copyright 2018 Intel Corporation.

SPDX-License-Identifier: Apache-2.0
*/

package pmemcsidriver

import (
	"context"
	"crypto/tls"
	"fmt"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi/v0"
	pmdmanager "github.com/intel/pmem-csi/pkg/pmem-device-manager"
	pmemgrpc "github.com/intel/pmem-csi/pkg/pmem-grpc"
	registry "github.com/intel/pmem-csi/pkg/pmem-registry"
	"google.golang.org/grpc"
	"k8s.io/klog/glog"
)

const (
	vendorVersion     string        = "0.0.1"
	connectionTimeout time.Duration = 10 * time.Second
	retryTimeout      time.Duration = 10 * time.Second
	requestTimeout    time.Duration = 10 * time.Second
)

type DriverMode string

const (
	//Controller defintion for controller driver mode
	Controller DriverMode = "controller"
	//Node defintion for noder driver mode
	Node DriverMode = "node"
	//Unified defintion for unified driver mode
	Unified DriverMode = "unified"
)

//Config type for driver configuration
type Config struct {
	//DriverName name of the csi driver
	DriverName string
	//NodeID node id on which this csi driver is running
	NodeID string
	//Endpoint exported csi driver endpoint
	Endpoint string
	//Mode mode fo the driver
	Mode DriverMode
	//RegistryEndpoint exported registry server endpoint
	RegistryEndpoint string
	//CAFile Root certificate authority certificate file
	CAFile string
	//CertFile certificate for server authentication
	CertFile string
	//KeyFile server private key file
	KeyFile string
	//ClientCertFile certificate for client side authentication
	ClientCertFile string
	//ClientKeyFile client private key
	ClientKeyFile string
	//ControllerEndpoint exported node controller endpoint
	ControllerEndpoint string
	//DeviceManager device manager to use
	DeviceManager string
}

type pmemDriver struct {
	driver          *CSIDriver
	cfg             Config
	ids             *identityServer
	ns              *nodeServer
	rs              *registryServer
	serverTLSConfig *tls.Config
	clientTLSConfig *tls.Config
}

func GetPMEMDriver(cfg Config) (*pmemDriver, error) {
	validModes := map[DriverMode]struct{}{
		Controller: struct{}{},
		Node:       struct{}{},
		Unified:    struct{}{},
	}
	var serverConfig *tls.Config
	var clientConfig *tls.Config
	var err error

	if _, ok := validModes[cfg.Mode]; !ok {
		return nil, fmt.Errorf("Invalid driver mode: %s", string(cfg.Mode))
	}
	if cfg.DriverName == "" || cfg.NodeID == "" || cfg.Endpoint == "" {
		return nil, fmt.Errorf("One of mandatory(Drivername Node id or Endpoint) configuration option missing")
	}
	if cfg.RegistryEndpoint == "" {
		cfg.RegistryEndpoint = cfg.Endpoint
	}
	if cfg.ControllerEndpoint == "" {
		cfg.ControllerEndpoint = cfg.Endpoint
	}

	peerName := "pmem-registry"
	if cfg.Mode == Controller {
		//When driver running in Controller mode, we connect to node controllers
		//so use appropriate peer name
		peerName = "pmem-node-controller"
	}

	if cfg.CertFile != "" && cfg.KeyFile != "" {
		serverConfig, err = pmemgrpc.LoadServerTLS(cfg.CAFile, cfg.CertFile, cfg.KeyFile, peerName)
		if err != nil {
			return nil, err
		}
	}

	/* if no client certificate details provided use same server certificate to connect to peer server */
	if cfg.ClientCertFile == "" {
		cfg.ClientCertFile = cfg.CertFile
		cfg.ClientKeyFile = cfg.KeyFile
	}

	if cfg.ClientCertFile != "" && cfg.ClientKeyFile != "" {
		clientConfig, err = pmemgrpc.LoadClientTLS(cfg.CAFile, cfg.ClientCertFile, cfg.ClientKeyFile, peerName)
		if err != nil {
			return nil, err
		}
	}

	driver, err := NewCSIDriver(cfg.DriverName, vendorVersion, cfg.NodeID)
	if err != nil {
		return nil, err
	}
	driver.AddVolumeCapabilityAccessModes([]csi.VolumeCapability_AccessMode_Mode{
		csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
	})

	return &pmemDriver{
		cfg:             cfg,
		driver:          driver,
		serverTLSConfig: serverConfig,
		clientTLSConfig: clientConfig,
	}, nil
}

func (pmemd *pmemDriver) Run() error {
	// Create GRPC servers
	pmemd.ids = NewIdentityServer(pmemd)
	s := NewNonBlockingGRPCServer()

	if pmemd.cfg.Mode == Controller {
		pmemd.rs = NewRegistryServer(pmemd.clientTLSConfig)
		cs := NewMasterControllerServer(pmemd.driver, pmemd.rs)

		if pmemd.cfg.Endpoint != pmemd.cfg.RegistryEndpoint {
			if err := s.Start(pmemd.cfg.Endpoint, nil, pmemd.ids, cs); err != nil {
				return err
			}
			if err := s.Start(pmemd.cfg.RegistryEndpoint, pmemd.serverTLSConfig, pmemd.rs); err != nil {
				return err
			}
		} else {
			if err := s.Start(pmemd.cfg.Endpoint, pmemd.serverTLSConfig, pmemd.ids, cs, pmemd.rs); err != nil {
				return err
			}
		}
	} else {
		dm, err := newDeviceManager(pmemd.cfg.DeviceManager)
		if err != nil {
			return err
		}
		pmemd.ns = NewNodeServer(pmemd.driver, dm)
		cs := NewNodeControllerServer(pmemd.driver, dm)

		if pmemd.cfg.Mode == Node {
			if pmemd.cfg.Endpoint != pmemd.cfg.ControllerEndpoint {
				if err := s.Start(pmemd.cfg.Endpoint, nil, pmemd.ids, pmemd.ns); err != nil {
					return err
				}
				if err := s.Start(pmemd.cfg.ControllerEndpoint, pmemd.serverTLSConfig, cs); err != nil {
					return err
				}
			} else {
				if err := s.Start(pmemd.cfg.Endpoint, nil, pmemd.ids, cs, pmemd.ns); err != nil {
					return err
				}
			}
			if err := pmemd.registerNodeController(); err != nil {
				return err
			}
		} else /* if pmemd.cfg.Mode == Unified */ {
			if err := s.Start(pmemd.cfg.Endpoint, pmemd.serverTLSConfig, pmemd.ids, cs, pmemd.ns); err != nil {
				return err
			}
		}
	}

	defer s.Stop()
	s.Wait()

	return nil
}

func (pmemd *pmemDriver) registerNodeController() error {
	fmt.Printf("Connecting to Registry at : %s\n", pmemd.cfg.RegistryEndpoint)
	var err error
	var conn *grpc.ClientConn

	for {
		conn, err = pmemgrpc.Connect(pmemd.cfg.RegistryEndpoint, pmemd.clientTLSConfig, connectionTimeout)
		if err == nil {
			glog.Info("Connected to RegistryServer!!!")
			break
		}
		/* TODO: Retry loop */
		glog.Infof("Failed to connect RegistryServer : %s, retrying after 10 seconds...", err.Error())
		time.Sleep(10 * time.Second)
	}
	client := registry.NewRegistryClient(conn)
	req := registry.RegisterControllerRequest{
		NodeId:   pmemd.driver.nodeID,
		Endpoint: pmemd.cfg.ControllerEndpoint,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if _, err = client.RegisterController(ctx, &req); err != nil {
		return fmt.Errorf("Fail to register with server at '%s' : %s", pmemd.cfg.RegistryEndpoint, err.Error())
	}

	return nil
}

func newDeviceManager(dmType string) (pmdmanager.PmemDeviceManager, error) {
	switch dmType {
	case "lvm":
		return pmdmanager.NewPmemDeviceManagerLVM()
	case "ndctl":
		return pmdmanager.NewPmemDeviceManagerNdctl()
	}
	return nil, fmt.Errorf("Unsupported device manager type '%s", dmType)
}
