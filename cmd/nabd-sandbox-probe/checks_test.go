package main

import (
	"errors"
	"net"
	"strings"
	"testing"
)

func TestVerifyNetNSID(t *testing.T) {
	if err := verifyNetNSID("", "net:[2]"); err == nil {
		t.Fatal("expected empty hostNetNS to fail closed")
	}
	if err := verifyNetNSID("net:[1]", ""); err == nil {
		t.Fatal("expected empty childNetNS to fail closed")
	}
	if err := verifyNetNSID("net:[1234]", "net:[1234]"); err == nil || !strings.Contains(err.Error(), "shares host") {
		t.Fatalf("expected matching netns to fail closed, got %v", err)
	}
	if err := verifyNetNSID("net:[1234]", "net:[5678]"); err != nil {
		t.Fatalf("expected distinct netns to succeed, got %v", err)
	}
}

func TestVerifyIsolatedInterfaces(t *testing.T) {
	ok := []interfaceSnapshot{
		{Name: "lo", Flags: net.FlagLoopback | net.FlagUp, IPs: []net.IP{net.ParseIP("127.0.0.1")}},
	}
	if err := verifyIsolatedInterfaces(ok); err != nil {
		t.Fatalf("expected loopback-only interface to pass, got %v", err)
	}

	if err := verifyIsolatedInterfaces(nil); err != nil {
		t.Fatalf("expected empty interface slice to pass, got %v", err)
	}

	nonLoop := []interfaceSnapshot{
		{Name: "eth0", Flags: net.FlagUp, IPs: []net.IP{net.ParseIP("10.0.0.2")}},
	}
	if err := verifyIsolatedInterfaces(nonLoop); err == nil || !strings.Contains(err.Error(), "non-loopback") {
		t.Fatalf("expected non-loopback interface to fail, got %v", err)
	}

	rogueIP := []interfaceSnapshot{
		{Name: "lo", Flags: net.FlagLoopback, IPs: []net.IP{net.ParseIP("192.168.1.50")}},
	}
	if err := verifyIsolatedInterfaces(rogueIP); err == nil || !strings.Contains(err.Error(), "non-loopback address") {
		t.Fatalf("expected non-loopback IP on loopback to fail, got %v", err)
	}
}

func TestVerifyNetIsolationFailsClosedOnReachableHostListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, aErr := ln.Accept()
			if aErr != nil {
				return
			}
			_ = c.Close()
		}
	}()

	err = verifyNetIsolation(
		ln.Addr().String(),
		"net:[100]",
		func() (string, error) { return "net:[200]", nil },
		func() ([]interfaceSnapshot, error) { return nil, nil },
	)
	if err == nil || !strings.Contains(err.Error(), "allowed connection to host loopback listener") {
		t.Fatalf("expected failure when host loopback listener is reachable, got: %v", err)
	}
}

func TestVerifyNetIsolationPassesWhenListenerClosedAndNetNSIsolated(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	// Missing address must fail closed
	if err := verifyNetIsolation(
		"",
		"net:[100]",
		func() (string, error) { return "net:[200]", nil },
		func() ([]interfaceSnapshot, error) { return nil, nil },
	); err == nil {
		t.Fatal("expected empty EXPECT_BLOCKED_ADDR to fail closed")
	}

	// Unreadable netns must fail closed
	if err := verifyNetIsolation(
		addr,
		"net:[100]",
		func() (string, error) { return "", errors.New("proc unavailable") },
		func() ([]interfaceSnapshot, error) { return nil, nil },
	); err == nil {
		t.Fatal("expected netns read error to fail closed")
	}

	// Distinct netns + unreachable addr + loopback only succeeds
	if err := verifyNetIsolation(
		addr,
		"net:[100]",
		func() (string, error) { return "net:[200]", nil },
		func() ([]interfaceSnapshot, error) {
			return []interfaceSnapshot{{Name: "lo", Flags: net.FlagLoopback}}, nil
		},
	); err != nil {
		t.Fatalf("expected isolated probe check to pass, got %v", err)
	}
}
