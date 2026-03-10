package config

import "os"

const (
	// Environment variable names
	EnvOVNBridge = "OVN_BRIDGE"
	EnvOVSSocket = "OVS_SOCKET"
)

func EnvOrDefault(key string, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
