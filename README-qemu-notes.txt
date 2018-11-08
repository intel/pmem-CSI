Notes about VM config for Pmem-CSI development environment

**NOTE about hugepages:** 
Emulated nvdimm appears not fully working if the VM is configured to use
Hugepages. With Hugepages configured, no data survives guest reboots,
nothing is ever written into backing store file in host. Configured backing 
store file is not even part of command line options to qemu. Instead of that,
there is some dev/hugepages/libvirt/... path which in reality remains
empty in host.

VM config was originally created by libvirt/GUI (also doable using
virt-install CLI), with configuration changes made directly in VM-config
xml file to emulate pair of NVDIMMs backed by host file.

# maxMemory

  <maxMemory slots='16' unit='KiB'>67108864</maxMemory>

# NUMA config, 2 nodes with 16 GB mem in both

  <cpu ...>
    <...>
    <numa>
      <cell id='0' cpus='0-3' memory='16777216' unit='KiB'/>
      <cell id='1' cpus='4-7' memory='16777216' unit='KiB'/>
    </numa>
  </cpu>

# Emulated 2x 8G NVDIMM with labels support

    <memory model='nvdimm' access='shared'>
      <source>
        <path>/var/lib/libvirt/images/nvdimm0</path>
      </source>
      <target>
        <size unit='KiB'>8388608</size>
        <node>0</node>
        <label>
          <size unit='KiB'>2048</size>
        </label>
      </target>
      <address type='dimm' slot='0'/>
    </memory>
    <memory model='nvdimm' access='shared'>
      <source>
        <path>/var/lib/libvirt/images/nvdimm1</path>
      </source>
      <target>
        <size unit='KiB'>8388608</size>
        <node>1</node>
        <label>
          <size unit='KiB'>2048</size>
        </label>
      </target>
      <address type='dimm' slot='1'/>
    </memory>

# 2x 8 GB NVDIMM backing file creation example on Host

dd if=/dev/zero of=/var/lib/libvirt/images/nvdimm0 bs=4K count=2097152
dd if=/dev/zero of=/var/lib/libvirt/images/nvdimm1 bs=4K count=2097152

# Labels initialization is needed once per emulated NVDIMM

The first OS startup in currently used dev.system with emulated NVDIMM triggers
creation of one device-size pmem region and ndctl would show zero remaining
available space. To make emulated NVDIMMs usable by ndctl, we use labels
initialization steps which have to be run once after first bootup with
new device(s). These steps need to be repeated if device backing file(s)
start from scratch.

For example, these commands for set of 2 NVDIMMs:
(can be re-written as loop for more devices)

ndctl disable-region region0
ndctl init-labels nmem0
ndctl enable-region region0

ndctl disable-region region1
ndctl init-labels nmem1
ndctl enable-region region1
