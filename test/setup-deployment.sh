#!/bin/bash

set -o errexit
set -o pipefail

# This reads a file and encodes it for use in a secret.
read_key () {
    base64 -w 0 "$1"
}

TEST_DIRECTORY=${TEST_DIRECTORY:-$(dirname $(readlink -f $0))}
source ${TEST_CONFIG:-${TEST_DIRECTORY}/test-config.sh}

CLUSTER=${CLUSTER:-pmem-govm}
REPO_DIRECTORY="${REPO_DIRECTORY:-$(dirname $(dirname $(readlink -f $0)))}"
CLUSTER_DIRECTORY="${CLUSTER_DIRECTORY:-${REPO_DIRECTORY}/_work/${CLUSTER}}"
SSH="${CLUSTER_DIRECTORY}/ssh.0"
KUBECTL="${SSH} kubectl" # Always use the kubectl installed in the cluster.
KUBERNETES_VERSION="$(cat "$CLUSTER_DIRECTORY/kubernetes.version")"
DEPLOYMENT_DIRECTORY="${REPO_DIRECTORY}/deploy/kubernetes-$KUBERNETES_VERSION${TEST_KUBERNETES_FLAVOR}"
case ${TEST_DEPLOYMENTMODE} in
    testing)
        deployment_suffix="/testing";;
    production)
        deployment_suffix="";;
    *)
        echo >&2 "invalid TEST_DEPLOYMENTMODE: ${TEST_DEPLOYMENTMODE}"
        exit 1
esac
DEPLOY=(
    ${TEST_DEVICEMODE}${deployment_suffix}
    pmem-storageclass-ext4.yaml
    pmem-storageclass-ext4-kata.yaml
    pmem-storageclass-xfs.yaml
    pmem-storageclass-xfs-kata.yaml
    pmem-storageclass-late-binding.yaml
)
echo "INFO: deploying from ${DEPLOYMENT_DIRECTORY}/${TEST_DEVICEMODE}${deployment_suffix}"

# Set up the TEST_DRIVER_NAMESPACE.
if ! ${KUBECTL} get "ns/${TEST_DRIVER_NAMESPACE}" 2>/dev/null >/dev/null; then
    ${KUBECTL} create ns "${TEST_DRIVER_NAMESPACE}"
fi

for deploy in ${DEPLOY[@]}; do
    # Deployment files can come from:
    # 1. deploy/kubernetes-*
    # 2. deploy/common
    # 3. deploy/kustomize directly
    path="${DEPLOYMENT_DIRECTORY}/${deploy}"
    paths="$path"
    if ! [ -e "$path" ]; then
        path="${REPO_DIRECTORY}/deploy/common/${deploy}"
        paths+=" $path"
    fi
    if ! [ -e "$path" ]; then
        path="${REPO_DIRECTORY}/deploy/kustomize/${deploy}"
        paths+=" $path"
    fi
    if [ -f "$path" ]; then
        case "$path" in
            *storageclass*)
                # Patch the node selector label into the storage class instead of the default storage=pmem.
                sed -e "s;: storage\$;: \"$(echo $TEST_PMEM_NODE_LABEL | cut -d= -f1)\";" \
                    -e "s;- pmem\$;- \"$(echo $TEST_PMEM_NODE_LABEL | cut -d= -f2)\";" \
                    "$path" | ${KUBECTL} apply -f -
                ;;
            *)
                ${KUBECTL} apply -f - <"$path"
                ;;
            esac
    elif [ -d "$path" ]; then
        # A kustomize base. We need to copy all files over into the cluster, otherwise
        # `kubectl kustomize` won't work.
        tmpdir=$(${SSH} mktemp -d)
        case "$path" in /*) tar -C / -chf - "$(echo "$path" | sed -e 's;^/;;')";;
                         *) tar -chf - "$path";;
        esac | ${SSH} tar -xf - -C "$tmpdir"
        if [ -f "$path/pmem-csi.yaml" ]; then
            # Replace registry. This is easier with sed than kustomize...
            ${SSH} sed -i -e "s^intel/pmem^${TEST_PMEM_REGISTRY}/pmem^g" "$tmpdir/$path/pmem-csi.yaml"
            # Replace Namespace object name
            ${SSH} "sed -ie 's;\(name: \)pmem-csi$;\1${TEST_DRIVER_NAMESPACE};g' $tmpdir/$path/pmem-csi.yaml"
            # Same for image pull policy.
            ${SSH} <<EOF
sed -i -e "s^imagePullPolicy:.IfNotPresent^imagePullPolicy: ${TEST_IMAGE_PULL_POLICY}^g" "$tmpdir/$path/pmem-csi.yaml"
EOF
        fi
        ${SSH} mkdir "$tmpdir/my-deployment"
        trap '${SSH} "rm -rf $tmpdir"' SIGTERM SIGINT EXIT
        ${SSH} "cat >'$tmpdir/my-deployment/kustomization.yaml'" <<EOF
bases:
  - ../$path
EOF
        case $deploy in
            ${TEST_DEVICEMODE}${deployment_suffix})
                ${SSH} "cat >>'$tmpdir/my-deployment/kustomization.yaml'" <<EOF
patchesJson6902:
EOF

                if [ "${TEST_DEVICEMODE}" = "lvm" ]; then
                    # Test these options and kustomization by injecting some non-default values.
                    # This could be made optional to test both default and non-default values,
                    # but for now we just change this in all deployments.
                    ${SSH} "cat >>'$tmpdir/my-deployment/kustomization.yaml'" <<EOF
  - target:
      group: apps
      version: v1
      kind: DaemonSet
      name: pmem-csi-intel-com-node
    path: lvm-parameters-patch.yaml
EOF
                    ${SSH} "cat >'$tmpdir/my-deployment/lvm-parameters-patch.yaml'" <<EOF
- op: add
  path: /spec/template/spec/containers/0/command/-
  value: "--pmemPercentage=50"
EOF
                fi

                ${SSH} "cat >>'$tmpdir/my-deployment/kustomization.yaml'" <<EOF
  - target:
      group: apps
      version: v1
      kind: DaemonSet
      name: pmem-csi-intel-com-node-setup
    path: node-selector-patch.yaml
EOF
                    ${SSH} "cat >'$tmpdir/my-deployment/node-selector-patch.yaml'" <<EOF
- op: add
  path: /spec/template/spec/containers/0/command/-
  value: -nodeSelector={$(echo ${TEST_PMEM_NODE_LABEL} | sed -e 's/\([^=]*\)=\(.*\)/"\1":"\2"/')}
EOF

                # Always use the configured label for selecting nodes.
                ${SSH} "cat >>'$tmpdir/my-deployment/kustomization.yaml'" <<EOF
  - target:
      group: apps
      version: v1
      kind: DaemonSet
      name: pmem-csi-intel-com-node
    path: node-label-patch.yaml
EOF
                case $deploy in
                    *-testing)
                        ${SSH} "cat >>'$tmpdir/my-deployment/kustomization.yaml'" <<EOF
  - target:
      group: apps
      version: v1
      kind: DaemonSet
      name: pmem-csi-intel-com-node-testing
    path: node-label-patch.yaml
EOF
                        ;;
                esac
                ${SSH} "cat >>'$tmpdir/my-deployment/node-label-patch.yaml'" <<EOF
- op: add
  path: /spec/template/spec/nodeSelector
  value:
     {$(echo "${TEST_PMEM_NODE_LABEL}" | sed -e 's/\(.*\)=\(.*\)/\1: "\2"/')}
EOF
                ;;
        esac

        ${SSH} "cat >>'$tmpdir/my-deployment/kustomization.yaml'" <<EOF
namespace: ${TEST_DRIVER_NAMESPACE}
EOF
        # When quickly taking down one installation of PMEM-CSI and recreating it, sometimes we get:
        #   nodePort: Invalid value: 32000: provided port is already allocated
        #
        # A fix is going into 1.19: https://github.com/kubernetes/kubernetes/pull/89937/commits
        # Not sure whether that is applicable here because we don't use a HA setup and
        # besides, we also need to support older Kubernetes releases. Therefore we retry...
        start=$SECONDS
        while ! output="$(${KUBECTL} apply --kustomize "$tmpdir/my-deployment" 2>&1)"; do
            if echo "$output" | grep -q "nodePort: Invalid value: ${TEST_SCHEDULER_EXTENDER_NODE_PORT}: provided port is already allocated" &&
                    [ $(($SECONDS - $start)) -lt 60 ]; then
                # Retry later...
                echo "Warning: kubectl failed with potentially temporary error, will try again: $output"
                sleep 1
            else
                echo "$output"
                exit 1
            fi
        done
        echo "$output"
        ${SSH} rm -rf "$tmpdir"
    else
        case "$path" in
            */scheduler|*/webhook)
                # optional, continue
                :
                ;;
            *)
                # Should be there, fail.
                echo >&2 "$paths are all missing."
                exit 1
                ;;
        esac
    fi
done

${KUBECTL} label --overwrite ns kube-system pmem-csi.intel.com/webhook=ignore

if [ "${TEST_DEPLOYMENT_QUIET}" = "" ]; then
    cat <<EOF

To try out the PMEM-CSI driver with persistent volumes that use late binding:
   cat deploy/common/pmem-pvc-late-binding.yaml | ${KUBECTL} create -f -
   cat deploy/common/pmem-app-late-binding.yaml | ${KUBECTL} create -f -

To try out the PMEM-CSI driver with ephemeral volumes:
   cat deploy/common/pmem-app-ephemeral.yaml | ${KUBECTL} create -f -
EOF
fi
