package drivers

import (
	"fmt"
)

const busFunctionGroupNone = ""           // Add a non multi-function port.
const busFunctionGroupGeneric = "generic" // Add multi-function port to generic group (used for internal devices).
const busFunctionGroupConfig = "config"   // Add multi-function port to config group (used for config shares).
const busDevicePortPrefix = "qemu_pcie"   // Prefix used for name of PCIe ports.

// busAllocator is a function signature used for allocating PCI(e) slots in QEMU.
// There are different implementations for pre-start QEMU config file generation and for hotplugging of devices.
type busAllocator func(deviceName string, enableMultifunction bool) (busName string, busAddress string, multi bool, err error)

type qemuBusEntry struct {
	bridgeDev int // Device number on the root bridge.
	bridgeFn  int // Function number on the root bridge.

	dev string // Existing device name.
	fn  int    // Function number on the existing device.
}

type qemuBus struct {
	name string        // Bus type.
	cfg  *[]cfgSection // pointer to cfgSection slice.

	portNum int // Next available port/chassis on the bridge.
	devNum  int // Next available device number on the bridge.

	rootPort *qemuBusEntry // Current root port.

	entries map[string]*qemuBusEntry // Map of qemuBusEntry for a particular shared device.
}

func (a *qemuBus) allocateRoot() *qemuBusEntry {
	if a.rootPort == nil {
		a.rootPort = &qemuBusEntry{
			bridgeDev: a.devNum,
		}

		a.devNum++
	} else {
		if a.rootPort.bridgeFn == 7 {
			a.rootPort.bridgeFn = 0
			a.rootPort.bridgeDev = a.devNum
			a.devNum++
		} else {
			a.rootPort.bridgeFn++
		}
	}

	return a.rootPort
}

// allocate() does any needed port allocation and returns the bus name,
// address and whether the device needs to be configured as multi-function.
//
// The multiFunctionGroup parameter allows for grouping devices together as one or more multi-function devices.
// It automatically keeps track of the number of functions already used and will allocate new ports as needed.
func (a *qemuBus) allocate(multiFunctionGroup string) (busName string, busAddress string, multi bool) {
	return a.allocateInternal(multiFunctionGroup, true)
}

// allocateDirect() works like allocate() but will directly attach the device to the root PCI bridge.
// This prevents hotplug or hotremove of the device but is sometimes required for compatibility reasons.
func (a *qemuBus) allocateDirect() (busName string, busAddress string, multi bool) {
	return a.allocateInternal(busFunctionGroupNone, false)
}

func (a *qemuBus) allocateInternal(multiFunctionGroup string, hotplug bool) (busName string, busAddress string, multi bool) {
	if a.name == "ccw" {
		return "", "", false
	}

	// Find a device multi-function group if specified.
	var p *qemuBusEntry
	if multiFunctionGroup != "" {
		var ok bool
		p, ok = a.entries[multiFunctionGroup]
		if ok {
			// Check if existing multi-function group is full.
			if p.fn == 7 {
				p.fn = 0
				switch a.name {
				case "pci":
					p.bridgeDev = a.devNum
					a.devNum++
				case "pcie":
					r := a.allocateRoot()
					p.bridgeDev = r.bridgeDev
					p.bridgeFn = r.bridgeFn
				}
			} else {
				p.fn++
			}
		} else {
			// Create a new multi-function group.
			p = &qemuBusEntry{}

			switch a.name {
			case "pci":
				p.bridgeDev = a.devNum
				a.devNum++
			case "pcie":
				r := a.allocateRoot()
				p.bridgeDev = r.bridgeDev
				p.bridgeFn = r.bridgeFn
			}

			a.entries[multiFunctionGroup] = p
		}
	} else {
		// Create a temporary single function group.
		p = &qemuBusEntry{}

		if a.name == "pci" || !hotplug {
			p.bridgeDev = a.devNum
			a.devNum++
		} else if a.name == "pcie" {
			r := a.allocateRoot()
			p.bridgeDev = r.bridgeDev
			p.bridgeFn = r.bridgeFn
		}
	}

	// The first device added to a multi-function port needs to specify the multi-function feature.
	multi = p.fn == 0 && multiFunctionGroup != ""

	if a.name == "pci" || !hotplug {
		return a.name + ".0", fmt.Sprintf("%x.%d", p.bridgeDev, p.fn), multi
	}

	if a.name == "pcie" {
		if p.fn == 0 {
			portName := fmt.Sprintf("%s%d", busDevicePortPrefix, a.portNum)
			pcieOpts := qemuPCIeOpts{
				portName: portName,
				index:    a.portNum,
				devAddr:  fmt.Sprintf("%x.%d", p.bridgeDev, p.bridgeFn),
				// First root port added on a bridge bus address needs multi-function enabled.
				multifunction: p.bridgeFn == 0,
			}
			*a.cfg = append(*a.cfg, qemuPCIe(&pcieOpts)...)
			p.dev = portName
			a.portNum++
		}

		return p.dev, fmt.Sprintf("00.%d", p.fn), multi
	}

	return "", "", false
}

// qemuNewBus instantiates a new qemu bus allocator. Accepts the type name of the bus and the qemu config builder
// which it will use to write root port config entries too as ports are allocated.
func qemuNewBus(name string, cfg *[]cfgSection) *qemuBus {
	a := &qemuBus{
		name: name,
		cfg:  cfg,

		portNum: 0, // No PCIe ports are used in the default config.
		devNum:  1, // Address 0 is used by the DRAM controller.

		entries: map[string]*qemuBusEntry{},
	}

	return a
}
