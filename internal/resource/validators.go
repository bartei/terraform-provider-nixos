package resource

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"

	"github.com/bartei/terraform-provider-nixos/internal/sshclient"
)

// remoteDirValidator rejects remote_directory values that would be dangerous
// to `rm -rf` on the remote host. It delegates to sshclient.ValidateRemoteDir
// so the plan-time check and the runtime guard share one definition.
type remoteDirValidator struct{}

var _ validator.String = remoteDirValidator{}

func (remoteDirValidator) Description(context.Context) string {
	return "must be a clean absolute path with at least two components (e.g. /root/nix), " +
		"using only letters, digits, '.', '_', '-' and '/', and not under a system directory"
}

func (v remoteDirValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (remoteDirValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if err := sshclient.ValidateRemoteDir(req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid remote_directory", err.Error())
	}
}

// int64AtLeast rejects values below min.
type int64AtLeast struct{ min int64 }

var _ validator.Int64 = int64AtLeast{}

func (v int64AtLeast) Description(context.Context) string {
	return fmt.Sprintf("must be at least %d", v.min)
}

func (v int64AtLeast) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v int64AtLeast) ValidateInt64(_ context.Context, req validator.Int64Request, resp *validator.Int64Response) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if got := req.ConfigValue.ValueInt64(); got < v.min {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid value",
			fmt.Sprintf("Value must be at least %d, got %d.", v.min, got))
	}
}
