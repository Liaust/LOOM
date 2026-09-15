package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"loom.local/loom/internal/restoreauthority"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "loom restore authority:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 || os.Args[1] != "serve" {
		return fmt.Errorf("usage: loom-restore-authority serve [options]")
	}
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	socketPath := flags.String("socket-path", restoreauthority.DefaultSocketPath, "fixed local Unix socket path")
	listenFD := flags.Int("listen-fd", 3, "systemd-provided Unix listener file descriptor (must be 3)")
	peerUser := flags.String("peer-user", "", "only Unix user allowed to connect")
	socketOwner := flags.String("socket-owner", "", "required Unix socket owner")
	socketGroup := flags.String("socket-group", "", "required Unix socket group")
	operationalDatabase := flags.String("operational-database", "", "active operational database to refuse")
	operationalOwner := flags.String("operational-owner", "", "fixed restored operational database owner")
	provenanceDatabase := flags.String("provenance-database", "", "active Provenance database to refuse")
	provenanceOwner := flags.String("provenance-owner", "", "fixed restored Provenance database owner")
	createdbPath := flags.String("createdb-path", "", "absolute reviewed createdb binary")
	pgRestorePath := flags.String("pg-restore-path", "", "absolute reviewed pg_restore binary")
	dropdbPath := flags.String("dropdb-path", "", "absolute reviewed dropdb binary")
	postgresSocketDirectory := flags.String("postgres-socket-directory", restoreauthority.DefaultPostgresSocketDir, "fixed local PostgreSQL Unix socket directory")
	postgresPort := flags.Uint("postgres-port", uint(restoreauthority.DefaultPostgresPort), "fixed local PostgreSQL port")
	maxDumpBytes := flags.Int64("max-dump-bytes", restoreauthority.DefaultMaxDumpBytes, "maximum authenticated dump bytes")
	if err := flags.Parse(os.Args[2:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional argument")
	}
	if *postgresPort == 0 || *postgresPort > 65535 {
		return fmt.Errorf("PostgreSQL port must be between 1 and 65535")
	}
	clientUID, err := restoreauthority.ResolveUserID(*peerUser)
	if err != nil {
		return err
	}
	socketUID, err := restoreauthority.ResolveUserID(*socketOwner)
	if err != nil {
		return err
	}
	socketGID, err := restoreauthority.ResolveGroupID(*socketGroup)
	if err != nil {
		return err
	}
	server, err := restoreauthority.NewServer(restoreauthority.ServerConfig{
		SocketPath: *socketPath, SocketUID: socketUID, SocketGID: socketGID, ClientUID: clientUID,
		Operational:  restoreauthority.DatabasePolicy{ActiveDatabase: *operationalDatabase, Owner: *operationalOwner},
		Provenance:   restoreauthority.DatabasePolicy{ActiveDatabase: *provenanceDatabase, Owner: *provenanceOwner},
		CreatedbPath: *createdbPath, PGRestorePath: *pgRestorePath, DropdbPath: *dropdbPath,
		PostgresSocketDirectory: *postgresSocketDirectory, PostgresPort: uint16(*postgresPort), MaxDumpBytes: *maxDumpBytes,
	})
	if err != nil {
		return err
	}
	listener, err := openListener(*listenFD)
	if err != nil {
		return err
	}
	defer listener.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	return server.Serve(ctx, listener)
}

func openListener(listenFD int) (*net.UnixListener, error) {
	if listenFD != 3 {
		return nil, fmt.Errorf("systemd listener must be file descriptor 3")
	}
	file := os.NewFile(uintptr(listenFD), "restore-authority-listener")
	if file == nil {
		return nil, fmt.Errorf("open systemd listener descriptor")
	}
	listener, err := net.FileListener(file)
	_ = file.Close()
	if err != nil {
		return nil, fmt.Errorf("open systemd listener: %w", err)
	}
	unixListener, ok := listener.(*net.UnixListener)
	if !ok {
		_ = listener.Close()
		return nil, fmt.Errorf("systemd listener is not a Unix socket")
	}
	return unixListener, nil
}
