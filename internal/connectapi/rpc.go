package connectapi

import (
	"context"

	"connectrpc.com/connect"

	talonv1 "github.com/guygrigsby/talon/internal/api/v1/talon/v1"
	"github.com/guygrigsby/talon/internal/server"
)

// RpcService is the catch-all generic surface — every other
// service in this package is the typed alternative to going
// through Dispatch. Used by the talon CLI for registry methods
// that don't have a typed Connect service yet (cron.*,
// secrets.reload, diagnostics.*, usage.*).
type RpcService struct {
	Reg *server.Registry
}

func (s *RpcService) Dispatch(ctx context.Context, req *connect.Request[talonv1.DispatchRequest]) (*connect.Response[talonv1.DispatchResponse], error) {
	method := req.Msg.GetMethod()
	if method == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			emptyMethodErr)
	}
	var params any
	if err := jsonUnmarshalAny(req.Msg.GetParamsJson(), &params); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	raw, err := dispatchJSON(ctx, s.Reg, method, params)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&talonv1.DispatchResponse{ResultJson: string(raw)}), nil
}

// emptyMethodErr is constructed once so the InvalidArgument path
// doesn't allocate per call. Tested implicitly via the CLI; the
// FE never hits this RPC.
var emptyMethodErr = newStaticError("dispatch: method is required")

func newStaticError(msg string) error {
	return &staticError{msg: msg}
}

type staticError struct{ msg string }

func (e *staticError) Error() string { return e.msg }
