package main

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

const idleChatSurfacePresenceTTL = 30 * time.Second

var errChatSurfacePresent = errors.New("chat surface is visible")

type idleChatSurfaceRuntime interface {
	StartManualMode() error
	StopManualMode()
	IsManualMode() bool
	IsChatActive() bool
	IsDisabled() bool
}

type surfacePresenceKey struct {
	viewerClientID string
	surface        string
}

type surfacePresenceSnapshot struct {
	EffectiveMode         string
	IdleChatActive        bool
	ChatPresenceCount     int
	IdleChatPresenceCount int
	LeaseExpiresAt        *time.Time
}

// idleChatSurfacePresenceController owns PORTAL surface lease aggregation.
// State selection and the corresponding IdleChat transition run under one
// mutex so concurrent browser tabs cannot race independent start/stop calls.
type idleChatSurfacePresenceController struct {
	mu                 sync.Mutex
	runtime            idleChatSurfaceRuntime
	ttl                time.Duration
	resetTTS           func()
	leases             map[surfacePresenceKey]time.Time
	timer              *time.Timer
	timerGeneration    uint64
	closed             bool
	lastEffectiveMode  string
	portalOwnsIdleChat bool
}

func newIdleChatSurfacePresenceController(runtime idleChatSurfaceRuntime, ttl time.Duration, resetTTS func()) *idleChatSurfacePresenceController {
	if ttl <= 0 {
		ttl = idleChatSurfacePresenceTTL
	}
	return &idleChatSurfacePresenceController{
		runtime:           runtime,
		ttl:               ttl,
		resetTTS:          resetTTS,
		leases:            make(map[surfacePresenceKey]time.Time),
		lastEffectiveMode: "none",
	}
}

func (c *idleChatSurfacePresenceController) Update(viewerClientID, surface, action string) (surfacePresenceSnapshot, error) {
	if c == nil || c.runtime == nil {
		return surfacePresenceSnapshot{}, fmt.Errorf("idlechat surface presence is unavailable")
	}
	now := time.Now().UTC()
	key := surfacePresenceKey{viewerClientID: viewerClientID, surface: surface}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return surfacePresenceSnapshot{}, fmt.Errorf("idlechat surface presence is closed")
	}
	c.pruneExpiredLocked(now)
	var leaseExpiresAt *time.Time
	switch action {
	case "claim", "heartbeat":
		expiresAt := now.Add(c.ttl)
		c.leases[key] = expiresAt
		leaseExpiresAt = &expiresAt
	case "release":
		delete(c.leases, key)
	default:
		return surfacePresenceSnapshot{}, fmt.Errorf("unsupported surface action %q", action)
	}
	if err := c.reconcileLocked(); err != nil {
		c.scheduleExpiryLocked(now)
		return surfacePresenceSnapshot{}, err
	}
	c.scheduleExpiryLocked(now)
	return c.snapshotLocked(leaseExpiresAt), nil
}

func (c *idleChatSurfacePresenceController) StartExplicit() error {
	if c == nil || c.runtime == nil {
		return fmt.Errorf("idlechat surface presence is unavailable")
	}
	now := time.Now().UTC()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneExpiredLocked(now)
	chatCount, _ := c.countsLocked()
	if chatCount > 0 {
		c.scheduleExpiryLocked(now)
		return errChatSurfacePresent
	}
	if !c.runtime.IsChatActive() {
		c.resetIdleChatTTSLocked()
	}
	if err := c.runtime.StartManualMode(); err != nil {
		return err
	}
	// An explicit authorized start transfers ownership away from PORTAL.
	c.portalOwnsIdleChat = false
	c.scheduleExpiryLocked(now)
	return nil
}

func (c *idleChatSurfacePresenceController) StopExplicit() {
	if c == nil || c.runtime == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.runtime.StopManualMode()
	c.resetIdleChatTTSLocked()
	c.portalOwnsIdleChat = false
}

func (c *idleChatSurfacePresenceController) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	c.timerGeneration++
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	if c.portalOwnsIdleChat && c.runtime != nil {
		c.runtime.StopManualMode()
		c.resetIdleChatTTSLocked()
		c.portalOwnsIdleChat = false
	}
}

func (c *idleChatSurfacePresenceController) reconcileLocked() error {
	chatCount, idleChatCount := c.countsLocked()
	effectiveMode := "none"
	if chatCount > 0 {
		effectiveMode = "chat"
	} else if idleChatCount > 0 {
		effectiveMode = "idlechat"
	}

	switch effectiveMode {
	case "chat":
		if c.lastEffectiveMode != "chat" || c.runtime.IsManualMode() || c.runtime.IsChatActive() || !c.runtime.IsDisabled() {
			c.runtime.StopManualMode()
			c.resetIdleChatTTSLocked()
		}
		c.portalOwnsIdleChat = false
	case "idlechat":
		if !c.runtime.IsManualMode() && !c.runtime.IsChatActive() {
			if err := c.runtime.StartManualMode(); err != nil {
				return fmt.Errorf("start idlechat for visible surface: %w", err)
			}
			c.portalOwnsIdleChat = true
		}
	case "none":
		if c.portalOwnsIdleChat {
			c.runtime.StopManualMode()
			c.resetIdleChatTTSLocked()
			c.portalOwnsIdleChat = false
		}
	}
	c.lastEffectiveMode = effectiveMode
	return nil
}

func (c *idleChatSurfacePresenceController) snapshotLocked(leaseExpiresAt *time.Time) surfacePresenceSnapshot {
	chatCount, idleChatCount := c.countsLocked()
	effectiveMode := "none"
	if chatCount > 0 {
		effectiveMode = "chat"
	} else if idleChatCount > 0 {
		effectiveMode = "idlechat"
	}
	return surfacePresenceSnapshot{
		EffectiveMode:         effectiveMode,
		IdleChatActive:        c.runtime.IsManualMode() || c.runtime.IsChatActive(),
		ChatPresenceCount:     chatCount,
		IdleChatPresenceCount: idleChatCount,
		LeaseExpiresAt:        leaseExpiresAt,
	}
}

func (c *idleChatSurfacePresenceController) countsLocked() (int, int) {
	var chatCount, idleChatCount int
	for key := range c.leases {
		switch key.surface {
		case "chat":
			chatCount++
		case "idlechat":
			idleChatCount++
		}
	}
	return chatCount, idleChatCount
}

func (c *idleChatSurfacePresenceController) pruneExpiredLocked(now time.Time) {
	for key, expiresAt := range c.leases {
		if !expiresAt.After(now) {
			delete(c.leases, key)
		}
	}
}

func (c *idleChatSurfacePresenceController) scheduleExpiryLocked(now time.Time) {
	c.timerGeneration++
	generation := c.timerGeneration
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	if c.closed || len(c.leases) == 0 {
		return
	}
	var earliest time.Time
	for _, expiresAt := range c.leases {
		if earliest.IsZero() || expiresAt.Before(earliest) {
			earliest = expiresAt
		}
	}
	delay := earliest.Sub(now)
	if delay < time.Millisecond {
		delay = time.Millisecond
	}
	c.timer = time.AfterFunc(delay, func() {
		c.expireLeases(generation)
	})
}

func (c *idleChatSurfacePresenceController) expireLeases(generation uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || generation != c.timerGeneration {
		return
	}
	now := time.Now().UTC()
	c.pruneExpiredLocked(now)
	if err := c.reconcileLocked(); err != nil {
		log.Printf("[IdleChat] surface lease reconciliation failed: %v", err)
	}
	c.scheduleExpiryLocked(now)
}

func (c *idleChatSurfacePresenceController) resetIdleChatTTSLocked() {
	if c.resetTTS != nil {
		c.resetTTS()
	}
}

func validSurfacePresenceViewerClientID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 256 {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}
