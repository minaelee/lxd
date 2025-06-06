ceph_setup() {
  local LXD_DIR="${1}"

  echo "==> Setting up CEPH backend in ${LXD_DIR}"
}

ceph_configure() {
  local LXD_DIR="${1}"

  echo "==> Configuring CEPH backend in ${LXD_DIR}"

  lxc storage create "lxdtest-$(basename "${LXD_DIR}")" ceph volume.size=25MiB ceph.osd.pg_num=8
  lxc profile device add default root disk path="/" pool="lxdtest-$(basename "${LXD_DIR}")"
}

ceph_teardown() {
  local LXD_DIR="${1}"

  echo "==> Tearing down CEPH backend in ${LXD_DIR}"
}
