package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"

	nixosds "github.com/bartei/terraform-provider-nixos/internal/datasource"
	nixosrs "github.com/bartei/terraform-provider-nixos/internal/resource"
)

var _ provider.Provider = &NixOSProvider{}

type NixOSProvider struct {
	version string
}

func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &NixOSProvider{version: version}
	}
}

func (p *NixOSProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "nixos"
	resp.Version = p.version
}

func (p *NixOSProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manage NixOS configurations on remote hosts via SSH.",
	}
}

func (p *NixOSProvider) Configure(_ context.Context, _ provider.ConfigureRequest, _ *provider.ConfigureResponse) {
}

func (p *NixOSProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		nixosrs.NewConfigurationResource,
	}
}

func (p *NixOSProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		nixosds.NewSystemInfoDataSource,
	}
}
