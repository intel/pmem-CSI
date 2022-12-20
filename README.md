# Introduction to PMEM-CSI for Kubernetes\*

Intel PMEM-CSI is a [CSI](https://github.com/container-storage-interface/spec)
storage driver for container orchestrators like
Kubernetes. It makes local persistent memory
([PMEM](https://pmem.io/)) available as a filesystem volume to
container applications. Currently, PMEM-CSI can utilize non-volatile memory
devices that can be controlled via the [libndctl utility
library](https://github.com/pmem/ndctl). In this readme, we use
*persistent memory* to refer to a non-volatile dual in-line memory
module (NVDIMM).

The [v1.0 release](https://github.com/intel/pmem-csi/releases/latest)
is the latest feature release and is [regularly updated](docs/DEVELOPMENT.md#release-management) with newer base images
and bug fixes. 0.9 is still supported.

Documentation is part of the source code for each release and also
available in rendered form for easier reading:
- [latest 1.1.x release](https://intel.github.io/pmem-csi/1.1/)
- [latest 1.0.x release](https://intel.github.io/pmem-csi/1.0/)
- [latest 0.9.x release](https://intel.github.io/pmem-csi/0.9/)
- [latest 0.8.x release](https://intel.github.io/pmem-csi/0.8/)
- [latest 0.7.x release](https://intel.github.io/pmem-csi/0.7/)
- [latest documentation, in development](https://intel.github.io/pmem-csi/devel/)

## Supported Kubernetes versions

PMEM-CSI implements the CSI specification version 1.x, which is only
supported by Kubernetes versions >= v1.13. The following table
summarizes the status of support for PMEM-CSI on different Kubernetes
versions:

| Kubernetes version | Required alpha feature gates   | Support status
|--------------------|--------------------------------|----------------
| 1.13               | CSINodeInfo, CSIDriverRegistry, CSIBlockVolume | unsupported <sup>1</sup>
| 1.14               |                                | unsupported <sup>2</sup>
| 1.15               | CSIInlineVolume                | unsupported <sup>3</sup>
| 1.16               |                                | unsupported <sup>4</sup>
| 1.17               |                                | unsupported <sup>5</sup>
| 1.18               |                                | unsupported <sup>6</sup>
| 1.19               |                                | unsupported <sup>6</sup>
| 1.20               |                                | unsupported <sup>6</sup>
| 1.21               |                                | untested
| 1.22               |                                | supported
| 1.23               |                                | supported
| 1.24               |                                | supported
| 1.25               |                                | supported

<sup>1</sup> Several relevant features are only available in alpha
quality in Kubernetes 1.13 and the combination of skip attach and
block volumes is completely broken, with [the
fix](https://github.com/kubernetes/kubernetes/pull/79920) only being
available in later versions. The external-provisioner v1.0.1 for
Kubernetes 1.13 lacks the `--strict-topology` flag and therefore late
binding is unreliable. It's also a release that is not supported
officially by upstream anymore.

<sup>2</sup> Lacks support for ephemeral inline volumes.
Not supported officially by upstream anymore.

<sup>3</sup> Not supported officially by upstream anymore.

<sup>4</sup> No longer supported by current
[external-provisioner](https://github.com/kubernetes-csi/external-provisioner/)
2.0.0 because support for the v1beta CSI APIs was removed. Also not
supported officially by upstream anymore.

<sup>5</sup> Kubernetes 1.17 uses deprecated beta storage APIs.

<sup>6</sup> Storage capacity tracking is expected to be enabled. It became
[beta in Kubernetes
1.21](https://kubernetes.io/blog/2021/04/14/local-storage-features-go-beta/).

## Feature status

PMEM-CSI is under active development. New features are added
continuously and old features may be removed. To minimize the impact
of feature changes on production usage, the project uses the
following approach:
- New features are considered alpha while their API and usage are
  under discussion.
- Stable features are supported and tested across up- and downgrades
  between all supported PMEM-CSI releases. Whether a release is still
  supported is documented in the release notes.
- Alpha features may be removed at any time. Stable features will be
  marked as deprecated first and then may be removed after half a
  year. Deprecations are announced in the release notes of the release
  that deprecates the feature.

The following table lists the features that are stable:

Feature | Introduced in
--------|--------------
[LVM mode](docs/design.md#lvm-device-mode) | [v0.5.0](https://github.com/intel/pmem-csi/releases/tag/v0.5.0)
[Direct mode](docs/design.md#direct-device-mode) | [v0.5.0](https://github.com/intel/pmem-csi/releases/tag/v0.5.0)
[Persistent volumes](docs/design.md#volume-persistency) | [v0.5.0](https://github.com/intel/pmem-csi/releases/tag/v0.5.0)
[CSI Ephemeral volumes](docs/design.md#volume-persistency) | [v0.6.0](https://github.com/intel/pmem-csi/releases/tag/v0.6.0)
[Raw block volumes](docs/install.md#raw-block-volumes) | [v0.6.0](https://github.com/intel/pmem-csi/releases/tag/v0.6.0)
[Capacity-aware pod scheduling](docs/design.md#capacity-aware-pod-scheduling) | [v0.7.0](https://github.com/intel/pmem-csi/releases/tag/v0.7.0)
[Operator API](docs/install.md#pmem-csi-deployment-crd) | [v0.9.0](https://github.com/intel/pmem-csi/releases/tag/v0.9.0)

Release notes are prepared only for major new releases (such as v0.6.0)
but not for automatic updates (such as v0.6.1). For more information on
releases, see [release
management](docs/DEVELOPMENT.md#release-management).

## Demo

Click the image to watch the animated demo on asciinema.org:

[![asciicast](https://asciinema.org/a/Bz0KrHGgwHMSokekheZazDR2m.svg)](https://asciinema.org/a/Bz0KrHGgwHMSokekheZazDR2m)

## License

All of the source code required to build PMEM-CSI is available under
Open Source licenses. The source code files identify the external Go
modules that are used. Binaries are distributed as container images on
Docker\* Hub. Those images contain license texts under
`/usr/local/share/package-licenses` and source code under
`/usr/local/share/package-sources`.

## Content

- [Supported Kubernetes versions](#supported-kubernetes-versions)
- [Design and architecture](docs/design.md)
- [Installation and Usage](docs/install.md)
   - [Prerequisites](docs/install.md#prerequisites)
   - [Installation and setup](docs/install.md#installation-and-setup)
   - [Filing issues and contributing](docs/install.md#filing-issues-and-contributing)
- [Develop and contribute](docs/DEVELOPMENT.md)
- [Automated testing](docs/autotest.md)
- [Application examples](examples/readme.rst)
