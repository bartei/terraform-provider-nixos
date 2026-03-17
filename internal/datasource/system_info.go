package datasource

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/bartei/terraform-provider-nixos/internal/sshclient"
)

var _ datasource.DataSource = &SystemInfoDataSource{}

type SystemInfoDataSource struct{}

type SystemInfoModel struct {
	ID                types.String `tfsdk:"id"`
	SSHHost           types.String `tfsdk:"ssh_host"`
	SSHUser           types.String `tfsdk:"ssh_user"`
	SSHPrivateKey     types.String `tfsdk:"ssh_private_key"`
	NixOSVersion      types.String `tfsdk:"nixos_version"`
	KernelVersion     types.String `tfsdk:"kernel_version"`
	SystemHash        types.String `tfsdk:"system_hash"`
	Hostname          types.String `tfsdk:"hostname"`
	Architecture      types.String `tfsdk:"architecture"`
	CurrentSystemPath types.String `tfsdk:"current_system_path"`
	Uptime            types.String `tfsdk:"uptime"`
}

func NewSystemInfoDataSource() datasource.DataSource {
	return &SystemInfoDataSource{}
}

func (d *SystemInfoDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_system_info"
}

func (d *SystemInfoDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads runtime information from a NixOS system via SSH.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
			},
			"ssh_host": schema.StringAttribute{
				Required:    true,
				Description: "IP or hostname of the NixOS machine.",
			},
			"ssh_user": schema.StringAttribute{
				Required:    true,
				Description: "SSH user.",
			},
			"ssh_private_key": schema.StringAttribute{
				Required:    true,
				Sensitive:   true,
				Description: "SSH private key for authentication.",
			},
			"nixos_version": schema.StringAttribute{
				Computed:    true,
				Description: "NixOS version string (e.g. \"24.11.20241201.abc1234\").",
			},
			"kernel_version": schema.StringAttribute{
				Computed:    true,
				Description: "Linux kernel version.",
			},
			"system_hash": schema.StringAttribute{
				Computed:    true,
				Description: "Nix store hash of /run/current-system.",
			},
			"hostname": schema.StringAttribute{
				Computed:    true,
				Description: "System hostname.",
			},
			"architecture": schema.StringAttribute{
				Computed:    true,
				Description: "CPU architecture (e.g. x86_64).",
			},
			"current_system_path": schema.StringAttribute{
				Computed:    true,
				Description: "Full nix store path of the current system.",
			},
			"uptime": schema.StringAttribute{
				Computed:    true,
				Description: "Human-readable system uptime.",
			},
		},
	}
}

func (d *SystemInfoDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config SystemInfoModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	host := config.SSHHost.ValueString()
	tflog.Info(ctx, "Reading NixOS system info", map[string]interface{}{"host": host})

	client, err := sshclient.New(host, config.SSHUser.ValueString(), config.SSHPrivateKey.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("SSH Connection Failed",
			fmt.Sprintf("Could not connect to %s: %s", host, err))
		return
	}
	defer client.Close()

	run := func(cmd string) string {
		out, _, err := client.Run(cmd)
		if err != nil {
			tflog.Warn(ctx, "Command failed", map[string]interface{}{"cmd": cmd, "error": err.Error()})
			return ""
		}
		return strings.TrimSpace(out)
	}

	config.ID = types.StringValue(host)
	config.NixOSVersion = types.StringValue(run("nixos-version"))
	config.KernelVersion = types.StringValue(run("uname -r"))
	config.SystemHash = types.StringValue(run("nix-store --query --hash /run/current-system"))
	config.Hostname = types.StringValue(run("hostname"))
	config.Architecture = types.StringValue(run("uname -m"))
	config.CurrentSystemPath = types.StringValue(run("readlink -f /run/current-system"))
	config.Uptime = types.StringValue(run("uptime -p"))

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}
