package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	pb "github.com/guygrigsby/talon/internal/plugin/pb"
	"github.com/guygrigsby/talon/internal/provider"
)

// PluginImageProvider adapts a pb.PluginClient (one whose manifest offers
// an image provider) to the provider.ImageProvider interface. Symmetric to
// PluginProvider for text completion: the server handler routing doesn't
// need plugin-specific branches.
type PluginImageProvider struct {
	name   string
	client pb.PluginClient
}

// NewPluginImageProvider wires a plugin client behind the named image
// provider. name must match the provider key in the manifest's
// OffersImageProviders list.
func NewPluginImageProvider(name string, client pb.PluginClient) *PluginImageProvider {
	return &PluginImageProvider{name: name, client: client}
}

func (p *PluginImageProvider) Name() string { return p.name }

// StreamImageGeneration translates req to a pb.StreamImageGenerationRequest,
// opens the gRPC server-stream, and pumps each pb.ImageDelta to the returned
// channel. Channel closes when:
//   - the stream EOFs (ImageDeltaResult delivered)
//   - the stream carries an error event (ImageDeltaError delivered, then close)
//   - ctx is cancelled (channel closes; receiver should check ctx.Err)
func (p *PluginImageProvider) StreamImageGeneration(ctx context.Context, req provider.ImageRequest) (<-chan provider.ImageDelta, error) {
	pbReq, err := imageRequestToProto(req)
	if err != nil {
		return nil, fmt.Errorf("plugin image %s: marshal request: %w", p.name, err)
	}
	stream, err := p.client.StreamImageGeneration(ctx, pbReq)
	if err != nil {
		return nil, fmt.Errorf("plugin image %s: open stream: %w", p.name, err)
	}

	ch := make(chan provider.ImageDelta)
	go func() {
		defer close(ch)
		for {
			pbDelta, err := stream.Recv()
			if errors.Is(err, io.EOF) || err == io.EOF {
				return
			}
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				select {
				case <-ctx.Done():
				case ch <- provider.ImageDelta{
					Kind: provider.ImageDeltaError,
					Err:  fmt.Errorf("plugin image %s: %w", p.name, err),
				}:
				}
				return
			}
			d, terminal := translateImageDelta(pbDelta)
			select {
			case <-ctx.Done():
				return
			case ch <- d:
			}
			if terminal {
				return
			}
		}
	}()
	return ch, nil
}

// imageRequestToProto converts a provider.ImageRequest to the proto wire type.
func imageRequestToProto(req provider.ImageRequest) (*pb.StreamImageGenerationRequest, error) {
	pbReq := &pb.StreamImageGenerationRequest{
		Prompt:         req.Prompt,
		NegativePrompt: req.NegativePrompt,
		Model:          req.Model.Model(),
		WorkflowId:     req.WorkflowID,
		Workflow:       req.Workflow,
		InputImage:     req.InputImage,
		Width:          int32(req.Width),
		Height:         int32(req.Height),
		Steps:          int32(req.Steps),
	}
	if req.Seed != nil {
		s := *req.Seed
		pbReq.Seed = &s
	}
	if len(req.NodeOverrides) > 0 {
		b, err := json.Marshal(req.NodeOverrides)
		if err != nil {
			return nil, err
		}
		pbReq.NodeOverrides = b
	}
	return pbReq, nil
}

// translateImageDelta converts an inbound pb.ImageDelta to provider.ImageDelta.
// Returns (delta, terminal) where terminal=true means the stream should close
// after delivering this delta (result or error).
func translateImageDelta(d *pb.ImageDelta) (provider.ImageDelta, bool) {
	if d == nil {
		return provider.ImageDelta{Kind: provider.ImageDeltaProgress}, false
	}
	switch v := d.GetKind().(type) {
	case *pb.ImageDelta_Progress:
		pr := v.Progress
		return provider.ImageDelta{
			Kind: provider.ImageDeltaProgress,
			Progress: &provider.ImageProgress{
				Step:  int(pr.GetStep()),
				Total: int(pr.GetTotal()),
				Node:  pr.GetNode(),
			},
		}, false
	case *pb.ImageDelta_Result:
		r := v.Result
		return provider.ImageDelta{
			Kind: provider.ImageDeltaResult,
			Result: &provider.ImageResult{
				Ref:      r.GetRef(),
				Data:     r.GetData(),
				MimeType: r.GetMimeType(),
			},
		}, true
	case *pb.ImageDelta_Error:
		return provider.ImageDelta{
			Kind: provider.ImageDeltaError,
			Err:  errors.New(v.Error),
		}, true
	}
	return provider.ImageDelta{Kind: provider.ImageDeltaProgress}, false
}
