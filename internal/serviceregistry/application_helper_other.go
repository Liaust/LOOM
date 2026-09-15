//go:build !linux

package serviceregistry

import (
	"context"
	"golang.org/x/sys/unix"
)

func (ApplicationHelperClient) Execute(context.Context, ApplicationRuntimeRequest) (ApplicationRuntimeReceipt, error) {
	return ApplicationRuntimeReceipt{}, applicationError("helper.linux_required")
}
func (ApplicationHelperClient) Publish(context.Context, ApplicationPublication) error {
	return applicationError("helper.linux_required")
}
func (ApplicationHelperClient) PublishPrepared(context.Context, ApplicationPreparedPublication) error {
	return applicationError("helper.linux_required")
}
func (ApplicationHelperClient) PlanProvision(context.Context, ApplicationProvisionRequest) (ApplicationProvisionPlan, error) {
	return ApplicationProvisionPlan{}, applicationError("helper.linux_required")
}
func (ApplicationHelperClient) Provision(context.Context, ApplicationProvisionPlan) (ApplicationProvisionPlan, error) {
	return ApplicationProvisionPlan{}, applicationError("helper.linux_required")
}
func ServeApplicationHelper(string, bool) error { return applicationError("helper.linux_required") }

func (ApplicationHelperClient) Archive(context.Context, ApplicationArchiveControl) (ManagerResult, error) {
	return ManagerResult{}, applicationError("helper.linux_required")
}

func applicationMountID(string) (uint64, error) {
	return 0, applicationError("data.linux_mount_identity_required")
}

func applicationFixtureNoNewPrivileges() error { return applicationError("helper.linux_required") }

func applicationRenameDirectoryNoReplace(from, to string) error {
	return unix.RenamexNp(from, to, unix.RENAME_EXCL)
}

func RestoreApplicationHelper(string, bool) error { return applicationError("helper.linux_required") }

func (ApplicationHelperClient) QueryPrerequisites(context.Context, ApplicationPrerequisiteQuery) (ApplicationPrerequisiteSnapshot, error) {
	return ApplicationPrerequisiteSnapshot{}, applicationError("helper.linux_required")
}
