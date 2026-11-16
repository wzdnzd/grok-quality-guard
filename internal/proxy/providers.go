package proxy

import (
	"context"
	"errors"
)

// NodeProvider renders a fixed configured endpoint.
type NodeProvider struct {
	Node Node
}

func (provider NodeProvider) Generate(ctx context.Context, _ GenerateRequest) (RenderedProxy, error) {
	if err := ctx.Err(); err != nil {
		return RenderedProxy{}, err
	}
	endpoint := provider.Node.Endpoint
	return RenderedProxy{
		Endpoint:       endpoint,
		URL:            endpoint.CanonicalURL(),
		MaskedURL:      endpoint.MaskedURL(),
		UsernameLength: len(endpoint.Username),
	}, nil
}

// TemplateProvider renders a compiled username template.
type TemplateProvider struct {
	Compiled *CompiledTemplate
}

func (provider TemplateProvider) Generate(ctx context.Context, request GenerateRequest) (RenderedProxy, error) {
	if err := ctx.Err(); err != nil {
		return RenderedProxy{}, err
	}
	if provider.Compiled == nil {
		return RenderedProxy{}, errors.New("template generator is unavailable")
	}
	if request.AvoidEndpointHash != "" {
		return provider.Compiled.RenderAvoiding(nil, request.AvoidEndpointHash)
	}
	return provider.Compiled.RenderWithReader(nil, nil)
}
