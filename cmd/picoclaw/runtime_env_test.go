package main

import "testing"

func TestEnvBool(t *testing.T) {
	t.Setenv("PICOCLAW_TEST_BOOL", "true")
	if !envBool("PICOCLAW_TEST_BOOL") {
		t.Fatal("true should be enabled")
	}
	t.Setenv("PICOCLAW_TEST_BOOL", "1")
	if !envBool("PICOCLAW_TEST_BOOL") {
		t.Fatal("1 should be enabled")
	}
	t.Setenv("PICOCLAW_TEST_BOOL", "off")
	if envBool("PICOCLAW_TEST_BOOL") {
		t.Fatal("off should be disabled")
	}
}

func TestIsLoopbackRemoteAddr(t *testing.T) {
	if !isLoopbackRemoteAddr("127.0.0.1:1234") {
		t.Fatal("127.0.0.1 should be loopback")
	}
	if !isLoopbackRemoteAddr("[::1]:1234") {
		t.Fatal("::1 should be loopback")
	}
	if isLoopbackRemoteAddr("192.168.1.10:1234") {
		t.Fatal("LAN address should not be loopback")
	}
}
