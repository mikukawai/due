package due_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestApplicationLinkerConfiguration(t *testing.T) {
	for _, tc := range []struct {
		application string
		timeout     string
	}{
		{application: "gate", timeout: "1s"},
		{application: "node", timeout: "3s"},
	} {
		t.Run(tc.application, func(t *testing.T) {
			var config struct {
				Cluster map[string]struct {
					Linker struct {
						CallTimeout string `toml:"callTimeout"`
					} `toml:"linker"`
				} `toml:"cluster"`
			}
			metadata, err := toml.DecodeFile(filepath.Join(tc.application, "etc", "etc.toml"), &config)
			if err != nil {
				t.Fatal(err)
			}
			if got := config.Cluster[tc.application].Linker.CallTimeout; got != tc.timeout {
				t.Fatalf("linker.callTimeout = %q; want %q in the namespace read by v2.6.6", got, tc.timeout)
			}
			if metadata.IsDefined("cluster", tc.application, "timeout") {
				t.Fatal("obsolete cluster timeout key must be removed")
			}
		})
	}
}

func TestApplicationWireContract(t *testing.T) {
	for _, application := range []string{"gate", "node", "client", "mcp-client"} {
		t.Run(application, func(t *testing.T) {
			var config struct {
				Packet struct {
					ByteOrder     string `toml:"byteOrder"`
					RouteBytes    int    `toml:"routeBytes"`
					SeqBytes      int    `toml:"seqBytes"`
					BufferBytes   int    `toml:"bufferBytes"`
					HeartbeatTime bool   `toml:"heartbeatTime"`
				} `toml:"packet"`
			}
			if _, err := toml.DecodeFile(filepath.Join(application, "etc", "etc.toml"), &config); err != nil {
				t.Fatal(err)
			}
			packet := config.Packet
			if packet.ByteOrder != "big" || packet.RouteBytes != 4 || packet.SeqBytes != 0 || packet.BufferBytes != 100000 || packet.HeartbeatTime {
				t.Fatalf("client wire contract changed: %+v", packet)
			}
		})
	}
}

func TestUpgradePreservesDeploymentVolumes(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("docker", "docker-compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"../../data/mongo:/data/db",
		"../../data/redis:/data",
		"image: mongo:8.0",
		"image: redis:8.0",
		"image: hashicorp/consul:1.20",
	} {
		if !strings.Contains(string(data), required) {
			t.Errorf("deployment customization not preserved: %s", required)
		}
	}
}
