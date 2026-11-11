//go:build !linux

package serviceregistry

import (
	"context"
	"os"
)

func (ApplicationHelperClient) Browse(context.Context, ApplicationBrowseRequest) (ApplicationBrowseReceipt, error) {
	return ApplicationBrowseReceipt{}, applicationError("helper.linux_required")
}
func (ApplicationHelperClient) ReadBrowse(context.Context, ApplicationBrowseRead) (ApplicationBrowseResult, error) {
	return ApplicationBrowseResult{}, applicationError("helper.linux_required")
}
func (ApplicationHelperClient) OpenBrowse(context.Context, ApplicationBrowseRead) (*os.File, ApplicationBrowseRoot, error) {
	return nil, ApplicationBrowseRoot{}, applicationError("helper.linux_required")
}
