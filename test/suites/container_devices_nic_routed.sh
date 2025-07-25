test_container_devices_nic_routed() {
  ensure_import_testimage
  ensure_has_localhost_remote "${LXD_ADDR}"

  ctName="nt$$"
  ipRand=$(shuf -i 0-9 -n 1)

  # These special values are needed to be enabled in kernel.
  # No need to enable IPv4 forwarding, as LXD will do this on the veth host_name interface automatically.
  sysctl net.ipv6.conf.all.forwarding=1
  sysctl net.ipv6.conf.all.proxy_ndp=1

  # Standard bridge.
  lxc network create "${ctName}" \
        ipv4.address=192.0.2.1/24 \
        ipv6.address=2001:db8::1/64
  sysctl net.ipv6.conf."${ctName}".proxy_ndp=1
  sysctl net.ipv6.conf."${ctName}".forwarding=1
  sysctl net.ipv4.conf."${ctName}".forwarding=1

  # Create container connected to bridge (which will be used for neighbor probe testing).
  lxc init testimage "${ctName}neigh"
  lxc config device add "${ctName}neigh" eth0 nic network="${ctName}"
  lxc start "${ctName}neigh"
  lxc exec "${ctName}neigh" -- ip -6 addr add 2001:db8::FFFF/64 dev eth0
  lxc exec "${ctName}neigh" -- ip -6 route replace default via 2001:db8::1 dev eth0
  lxc exec "${ctName}neigh" -- ip -4 addr add 192.0.2.254/24 dev eth0
  lxc exec "${ctName}neigh" -- ip -4 route replace default via 192.0.2.1 dev eth0

  ping -nc2 -i0.1 -W1 192.0.2.254
  wait_for_dad "${ctName}" # waiting for DAD on the host device
  wait_for_dad "${ctName}neigh" eth0
  ping -6 -nc2 -i0.1 -W1 "2001:db8::FFFF"

  # Create dummy vlan parent.
  # Use slash notation when setting sysctls on vlan interface (that has period in interface name).
  ip link add link "${ctName}" name "${ctName}.1234" type vlan id 1234
  sysctl net/ipv6/conf/"${ctName}.1234"/proxy_ndp=1
  sysctl net/ipv6/conf/"${ctName}.1234"/forwarding=1
  sysctl net/ipv4/conf/"${ctName}.1234"/forwarding=1

  # Add IP addresses to parent vlan interface (this is needed for automatic gateway detection in container).
  ip link set "${ctName}.1234" up
  ip addr add 192.0.3.254/32 dev "${ctName}.1234"
  ip addr add 2001:db8:2::1/128 dev "${ctName}.1234"

  # Record how many nics we started with.
  startNicCount=$(find /sys/class/net | wc -l)

  lxc init testimage "${ctName}"

  # Check vlan option not allowed without parent option.
  ! lxc config device add "${ctName}" eth0 nic name=eth0 nictype=routed vlan=1234 || false

  # Check VLAN parent interface creation and teardown.
  lxc config device add "${ctName}" eth0 nic name=eth0 nictype=routed parent="${ctName}" vlan=1235
  lxc start "${ctName}"
  stat "/sys/class/net/${ctName}.1235"
  lxc stop -f "${ctName}"
  ! stat "/sys/class/net/${ctName}.1235" || false
  lxc config device remove "${ctName}" eth0

  # Add routed NIC to instance.
  lxc config device add "${ctName}" eth0 nic name=eth0 nictype=routed parent="${ctName}"

  # Check starting routed NIC with IPs in use on parent network is prevented.
  lxc config device set "${ctName}" eth0 ipv4.address="192.0.2.254"
  ! lxc start "${ctName}" || false
  lxc config device set "${ctName}" eth0 ipv4.neighbor_probe=false
  lxc start "${ctName}"
  lxc stop "${ctName}" -f

  lxc config device set "${ctName}" eth0 ipv4.address="" ipv6.address="2001:db8::FFFF"

  ! lxc start "${ctName}" || false
  lxc config device set "${ctName}" eth0 ipv6.neighbor_probe=false
  lxc start "${ctName}"
  lxc stop "${ctName}" -f
  lxc config device unset "${ctName}" eth0 ipv4.neighbor_probe
  lxc config device unset "${ctName}" eth0 ipv6.neighbor_probe

  # Check starting routed NIC with unused IPs.
  lxc config device set "${ctName}" eth0 ipv4.address="192.0.2.1${ipRand}" ipv6.address="2001:db8::1${ipRand}" ipv4.routes="192.0.3.0/24" ipv6.routes="2001:db7::/64" mtu=1600
  lxc start "${ctName}"

  ctHost=$(lxc config get "${ctName}" volatile.eth0.host_name)
  # Check profile routes are applied
  if ! ip -4 r list dev "${ctHost}"| grep -F "192.0.3.0/24" ; then
    echo "ipv4.routes invalid"
    false
  fi
  if ! ip -6 r list dev "${ctHost}" | grep -F "2001:db7::/64" ; then
    echo "ipv6.routes invalid"
    false
  fi

  # Check IP is assigned and doesn't have a broadcast address set.
  lxc exec "${ctName}" -- ip -4 a | grep -F "inet 192.0.2.1${ipRand}/32 scope global eth0"

  # Check neighbour proxy entries added to parent interface.
  ip neigh show proxy dev "${ctName}" | grep -F "192.0.2.1${ipRand}"
  ip neigh show proxy dev "${ctName}" | grep -F "2001:db8::1${ipRand}"

  # Check custom MTU is applied.
  if ! lxc exec "${ctName}" -- ip link show eth0 | grep -F "mtu 1600" ; then
    echo "mtu invalid"
    false
  fi

  # Check MAC address is applied.
  ctMAC=$(lxc config get "${ctName}" volatile.eth0.hwaddr)
  if [ "$(lxc exec "${ctName}" -- cat /sys/class/net/eth0/address)" != "${ctMAC}" ]; then
    echo "mac invalid"
    false
  fi

  lxc stop "${ctName}" --force

  # Check neighbour proxy entries removed from parent interface.
  ! ip neigh show proxy dev "${ctName}" | grep -F "192.0.2.1${ipRand}" || false
  ! ip neigh show proxy dev "${ctName}" | grep -F "2001:db8::1${ipRand}" || false

  # Check that MTU is inherited from parent device when not specified on device.
  ip link set "${ctName}" mtu 1605
  lxc config device unset "${ctName}" eth0 mtu
  lxc start "${ctName}"

  if [ "$(lxc exec "${ctName}" -- cat /sys/class/net/eth0/mtu)" != "1605" ]; then
    echo "mtu not inherited from parent"
    false
  fi

  #Spin up another container with multiple IPv4 addresses (no IPv6 to check single family operation).
  lxc init testimage "${ctName}2"
  lxc config device add "${ctName}2" eth0 nic \
    name=eth0 \
    nictype=routed \
    parent="${ctName}" \
    ipv4.address="192.0.2.2${ipRand}, 192.0.2.3${ipRand}"
  lxc start "${ctName}2"
  lxc exec "${ctName}2" -- ip -4 r | grep -F "169.254.0.1"
  ! lxc exec "${ctName}2" -- ip -6 r | grep -F "fe80::1" || false
  lxc stop -f "${ctName}2"

  # Check single IPv6 family auto default gateway works.
  lxc config device unset "${ctName}2" eth0 ipv4.address
  lxc config device set "${ctName}2" eth0 ipv6.address="2001:db8::2${ipRand}, 2001:db8::3${ipRand}"
  lxc start "${ctName}2"
  ! lxc exec "${ctName}2" -- ip r | grep -F "169.254.0.1" || false
  lxc exec "${ctName}2" -- ip -6 r | grep -F "fe80::1"
  lxc stop -f "${ctName}2"

  # Enable both IP families.
  lxc config device set "${ctName}2" eth0 ipv4.address="192.0.2.2${ipRand}, 192.0.2.3${ipRand}"
  lxc start "${ctName}2"

  # Wait for IPv6 DAD to complete.
  wait_for_dad "${ctName}" eth0
  wait_for_dad "${ctName}2" eth0

  # Check comms between containers.
  lxc exec "${ctName}" -- ping -nc2 -i0.1 -W1 "192.0.2.1"
  lxc exec "${ctName}" -- ping -6 -nc2 -i0.1 -W1 "2001:db8::1"

  lxc exec "${ctName}2" -- ping -nc2 -i0.1 -W1 "192.0.2.1"
  lxc exec "${ctName}2" -- ping -6 -nc2 -i0.1 -W1 "2001:db8::1"

  lxc exec "${ctName}" -- ping -nc2 -i0.1 -W1 "192.0.2.2${ipRand}"
  lxc exec "${ctName}" -- ping -nc2 -i0.1 -W1 "192.0.2.3${ipRand}"

  lxc exec "${ctName}" -- ping -6 -nc2 -i0.1 -w1 "2001:db8::3${ipRand}"
  lxc exec "${ctName}" -- ping -6 -nc2 -i0.1 -W1 "2001:db8::2${ipRand}"

  lxc exec "${ctName}2" -- ping -nc2 -i0.1 -W1 "192.0.2.1${ipRand}"
  lxc exec "${ctName}2" -- ping -6 -nc2 -i0.1 -W1 "2001:db8::1${ipRand}"

  lxc stop -f "${ctName}2"
  lxc stop -f "${ctName}"

  # Check routed ontop of VLAN parent with custom routing tables.
  lxc config device set "${ctName}" eth0 vlan 1234
  lxc config device set "${ctName}" eth0 ipv4.host_table=100
  lxc config device set "${ctName}" eth0 ipv6.host_table=101
  lxc start "${ctName}"

  # Check VLAN interface created
  if [ "$(cat "/sys/class/net/${ctName}.1234/carrier")" != "1" ]; then
    echo "vlan interface not created"
    false
  fi

  # Check static routes added to custom routing table
  ip -4 route show table 100 | grep -F "192.0.2.1${ipRand}"
  ip -6 route show table 101 | grep -F "2001:db8::1${ipRand}"

  # Check volatile cleanup on stop.
  lxc stop -f "${ctName}"
  if [ "$(lxc config show "${ctName}" | grep -F volatile.eth0 | grep -vF volatile.eth0.hwaddr | grep -vF volatile.eth0.name)" != "" ]; then
    echo "unexpected volatile key remains"
    false
  fi

  # Check parent device is still up.
  if [ "$(cat "/sys/class/net/${ctName}/carrier")" != "1" ]; then
    echo "parent is down"
    false
  fi

  # Check we haven't left any NICS lying around.
  endNicCount=$(find /sys/class/net | wc -l)
  if [ "$startNicCount" != "$endNicCount" ]; then
    echo "leftover NICS detected"
    false
  fi

  # Cleanup routed checks
  lxc delete "${ctName}" -f
  lxc delete "${ctName}2" -f
  lxc delete "${ctName}neigh" -f
  ip link delete "${ctName}.1234"
  lxc network show "${ctName}"
  lxc network delete "${ctName}"
}
