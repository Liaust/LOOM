package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"loom.local/loom/internal/serviceregistry"
)

func main() {
	allowlistPath := flag.String("allowlist", "", "reviewed service allowlist path")
	applicationRestore := flag.Bool("application-restore", false, "restore current committed installations from fixed root policy at boot")
	applicationPublish := flag.Bool("application-publish", false, "publish one reviewed application descriptor from stdin through the fixed root helper")
	applicationSocket := flag.Bool("application-socket", false, "serve one fixed socket-activated application request")
	applicationFixture := flag.String("application-fixture-config", "", "explicit disposable root-owned fixture startup configuration")
	flag.Parse()
	if *applicationPublish {
		if *allowlistPath != "" || *applicationRestore || *applicationSocket || *applicationFixture != "" || flag.NArg() != 0 || os.Geteuid() != 0 {
			fail("publisher mode requires the operator")
		}
		raw, err := io.ReadAll(io.LimitReader(os.Stdin, 256*1024+1))
		if err != nil || len(raw) > 256*1024 {
			fail("publication exceeds limit")
		}
		var publication serviceregistry.ApplicationPublication
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&publication) != nil {
			fail("invalid publication")
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			fail("invalid publication")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		if err := (serviceregistry.ApplicationHelperClient{SocketPath: serviceregistry.ApplicationSocketPath}).Publish(ctx, publication); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println(`{"published":true}`)
		return
	}
	if *applicationSocket || *applicationRestore {
		if *allowlistPath != "" || flag.NArg() != 0 || (*applicationSocket && *applicationRestore) {
			fail("incompatible mode")
		}
		path := "/etc/loom/project-applications-helper.json"
		fixture := *applicationFixture != ""
		if fixture {
			path = *applicationFixture
		}
		run := serviceregistry.ServeApplicationHelper
		if *applicationRestore {
			run = serviceregistry.RestoreApplicationHelper
		}
		if err := run(path, fixture); err != nil {
			fail("application request refused")
		}
		return
	}
	if *applicationFixture != "" {
		fail("fixture needs application mode")
	}
	if os.Geteuid() == 0 {
		fail("legacy allowlist mode is unprivileged only")
	}
	if *allowlistPath == "" {
		fail("allowlist is required")
	}
	allowlist, err := serviceregistry.LoadAllowlist(*allowlistPath)
	if err != nil {
		fail(err.Error())
	}
	var request serviceregistry.ManagerRequest
	decoder := json.NewDecoder(os.Stdin)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		fail("invalid request")
	}
	service := serviceregistry.ManagerService{Allowlist: allowlist, Systemd: serviceregistry.SystemdCommandRunner{}, Timeout: 20 * time.Second}
	result, err := service.Execute(context.Background(), request)
	if err != nil {
		fail(err.Error())
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fail("encode response")
	}
}
func fail(message string) {
	_, _ = fmt.Fprintln(os.Stderr, "service manager request failed")
	os.Exit(1)
}
