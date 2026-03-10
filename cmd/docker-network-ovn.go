package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/docker/go-plugins-helpers/network"
	"github.com/go-logr/logr"
	"github.com/ovn-org/libovsdb/client"
	"github.com/ovn-org/libovsdb/model"

	"docker-network-ovn/internal/config"
	"docker-network-ovn/internal/constants"
	"docker-network-ovn/internal/driver"
	"docker-network-ovn/internal/ovn"
	"docker-network-ovn/internal/ovs"
)

var Logger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

func init() {
	constants.SetLogger(Logger)
}

func fatal(msg string, args ...any) {
	Logger.Error(msg, args...)
	os.Exit(1)
}

// DockerPluginSocket is the path where the plugin will listen for Docker API calls.
const DockerPluginSocket = "/run/docker/plugins/ovn.sock"

func main() {
	bridge := config.EnvOrDefault(config.EnvOVNBridge, constants.DefaultOVNBridge)
	ovsSocket := config.EnvOrDefault(config.EnvOVSSocket, constants.DefaultOVSSocket)

	ctx := context.Background()

	ovsDBModel, err := model.NewClientDBModel(constants.DBOVS,
		map[string]model.Model{
			constants.TableBridge:      &ovs.Bridge{},
			constants.TablePort:        &ovs.Port{},
			constants.TableInterface:   &ovs.Interface{},
			constants.TableOpenvSwitch: &ovs.OpenvSwitch{},
		})
	if err != nil {
		fatal("Failed to create OVS DB model", "err", err)
	}

	discardLogger := logr.Discard()
	ovsClient, err := client.NewOVSDBClient(
		ovsDBModel,
		client.WithEndpoint(ovsSocket),
		client.WithLogger(&discardLogger),
	)
	if err != nil {
		fatal("Failed to create OVS client", "err", err)
	}

	if err := ovsClient.Connect(ctx); err != nil {
		fatal("Failed to connect to OVS database", "err", err)
	}

	if _, err := ovsClient.Monitor(ctx,
		ovsClient.NewMonitor(
			client.WithTable(&ovs.Bridge{}),
			client.WithTable(&ovs.Port{}),
			client.WithTable(&ovs.Interface{}),
			client.WithTable(&ovs.OpenvSwitch{}),
		),
	); err != nil {
		fatal("Failed to monitor OVS database", "err", err)
	}

	ovsAPI := ovs.NewOVSAPI(ovsClient, ctx)

	nbConn, err := ovsAPI.NBConnection()
	if err != nil {
		fatal("Failed to get OVN NB connection", "err", err)
	}

	Logger.Info("Using OVN NB connection", "conn", nbConn)

	nbModel, err := model.NewClientDBModel(constants.DBOVNNB,
		map[string]model.Model{
			constants.TableLogicalSwitch:     &ovn.LogicalSwitch{},
			constants.TableLogicalSwitchPort: &ovn.LogicalSwitchPort{},
		})
	if err != nil {
		fatal("Failed to create OVN NB DB model", "err", err)
	}

	nbClient, err := client.NewOVSDBClient(nbModel, client.WithEndpoint(nbConn))
	if err != nil {
		fatal("Failed to create OVN NB client", "err", err)
	}

	if err := nbClient.Connect(ctx); err != nil {
		fatal("Failed to connect to OVN NB database", "err", err)
	}

	if _, err := nbClient.Monitor(ctx,
		nbClient.NewMonitor(
			client.WithTable(&ovn.LogicalSwitch{}),
			client.WithTable(&ovn.LogicalSwitchPort{}),
		),
	); err != nil {
		fatal("Failed to monitor OVN NB database", "err", err)
	}

	Logger.Info("Successfully connected to OVS and OVN databases")

	defer ovsClient.Disconnect()
	defer nbClient.Disconnect()

	ovnAPI := ovn.NewOVNAPI(nbClient, ctx)

	driverInstance := driver.NewOVNDriver(bridge, ovsSocket, ovsAPI, ovnAPI)

	pluginDir := filepath.Dir(DockerPluginSocket)
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		fatal("Failed to create plugin directory", "err", err)
	}

	if err := os.Remove(DockerPluginSocket); err != nil && !os.IsNotExist(err) {
		fatal("Failed to remove existing plugin socket", "err", err)
	}
	defer func() {
		if err := os.Remove(DockerPluginSocket); err != nil && !os.IsNotExist(err) {
			Logger.Warn("Failed to remove plugin socket during cleanup", "err", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		Logger.Info("Received shutdown signal", "signal", sig)
		if err := os.Remove(DockerPluginSocket); err != nil && !os.IsNotExist(err) {
			Logger.Warn("Failed to remove plugin socket during shutdown", "err", err)
		}
		ovsClient.Disconnect()
		nbClient.Disconnect()
		os.Exit(0)
	}()

	handler := network.NewHandler(driverInstance)
	Logger.Info("Starting OVN plugin", "socket", DockerPluginSocket)
	if err := driverInstance.Reconcile(); err != nil {
		fatal("Startup reconciliation failed", "err", err)
	}
	if err := handler.ServeUnix(DockerPluginSocket, 0); err != nil {
		fatal("Failed to start plugin", "err", err)
	}
}
