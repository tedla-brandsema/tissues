package server

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// Config is the typed Server listener schema. Source resolution belongs to
// lib/core/config; Server receives only a fully resolved value.
type Config struct {
	Host              string        `cfg:"string,restart=true"`
	Port              int           `cfg:"int,default=8080,restart=true,env=PORT" val:"rangeint,min=1,max=65535"`
	ReadTimeout       time.Duration `cfg:"duration,default=10s,restart=true" val:"posduration"`
	ReadHeaderTimeout time.Duration `cfg:"duration,default=5s,restart=true" val:"posduration"`
	WriteTimeout      time.Duration `cfg:"duration,default=20s,restart=true" val:"posduration"`
	IdleTimeout       time.Duration `cfg:"duration,default=60s,restart=true" val:"posduration"`
	MaxHeaderBytes    int           `cfg:"int,default=65536,restart=true" val:"rangeint,min=1024,max=1048576"`
}

// Address returns the listener address represented by the typed fields.
func (cfg Config) Address() string {
	if strings.TrimSpace(cfg.Host) == "" {
		return ":" + strconv.Itoa(cfg.Port)
	}
	return net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
}

// ValidateConfig checks the cross-field representation after source loading.
func (cfg Config) ValidateConfig() error {
	if _, err := net.ResolveTCPAddr("tcp", cfg.Address()); err != nil {
		return fmt.Errorf("resolve server address: %w", err)
	}
	return nil
}
