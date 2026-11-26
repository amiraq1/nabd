package main

import (
	"fmt"
	"net"
	"time"
)

type interfaceSnapshot struct {
	Name  string
	Flags net.Flags
	IPs   []net.IP
}

func snapshotSystemInterfaces() ([]interfaceSnapshot, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := make([]interfaceSnapshot, 0, len(ifaces))
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			return nil, fmt.Errorf("read addresses for interface %s: %w", iface.Name, err)
		}
		var ips []net.IP
		for _, addr := range addrs {
			switch v := addr.(type) {
			case *net.IPNet:
				ips = append(ips, v.IP)
			case *net.IPAddr:
				ips = append(ips, v.IP)
			}
		}
		out = append(out, interfaceSnapshot{Name: iface.Name, Flags: iface.Flags, IPs: ips})
	}
	return out, nil
}

func verifyNetNSID(hostNetNS, childNetNS string) error {
	if hostNetNS == "" {
		return fmt.Errorf("missing host network namespace identifier")
	}
	if childNetNS == "" {
		return fmt.Errorf("empty child network namespace identifier")
	}
	if childNetNS == hostNetNS {
		return fmt.Errorf("child shares host network namespace %q", childNetNS)
	}
	return nil
}

func verifyIsolatedInterfaces(ifaces []interfaceSnapshot) error {
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback == 0 {
			return fmt.Errorf("unexpected non-loopback network interface %q in isolated netns", iface.Name)
		}
		for _, ip := range iface.IPs {
			if ip == nil {
				continue
			}
			if !ip.IsLoopback() {
				return fmt.Errorf("unexpected non-loopback address %s on %q in isolated netns", ip.String(), iface.Name)
			}
		}
	}
	return nil
}

func verifyNetIsolation(
	dialAddr string,
	hostNetNS string,
	readNetNS func() (string, error),
	listIfaces func() ([]interfaceSnapshot, error),
) error {
	if dialAddr == "" {
		return fmt.Errorf("missing EXPECT_BLOCKED_ADDR")
	}
	conn, err := net.DialTimeout("tcp", dialAddr, 500*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		return fmt.Errorf("network namespace allowed connection to host loopback listener %s", dialAddr)
	}

	childNetNS, err := readNetNS()
	if err != nil {
		return fmt.Errorf("read child network namespace: %w", err)
	}
	if err := verifyNetNSID(hostNetNS, childNetNS); err != nil {
		return err
	}

	ifaces, err := listIfaces()
	if err != nil {
		return fmt.Errorf("inspect sandbox network interfaces: %w", err)
	}
	return verifyIsolatedInterfaces(ifaces)
}
