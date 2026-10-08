package queryfrontend

import (
	"context"
	"strings"

	"connectrpc.com/connect"
)

// AttributeIndexHeader opts a request into experimental AttributeIndexV1
// dataset lookup and index-only metadata queries. Requests without this header
// retain TSDB execution.
const AttributeIndexHeader = "X-Pyroscope-Use-Attribute-Index"

type attributeIndexContextKey struct{}

// WithAttributeIndex records whether a query should prefer AttributeIndexV1
// dataset lookup and index-only metadata queries. It is exported for callers that do not
// enter through the public Connect handler.
func WithAttributeIndex(ctx context.Context, enabled bool) context.Context {
	return context.WithValue(ctx, attributeIndexContextKey{}, enabled)
}

func useAttributeIndex(ctx context.Context) bool {
	enabled, _ := ctx.Value(attributeIndexContextKey{}).(bool)
	return enabled
}

// AttributeIndexInterceptor translates the public, experimental opt-in header
// into request context. QueryFrontend copies it into InvokeOptions before the
// request crosses process boundaries.
var AttributeIndexInterceptor connect.Interceptor = attributeIndexInterceptor{}

type attributeIndexInterceptor struct{}

func (attributeIndexInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if !req.Spec().IsClient {
			ctx = WithAttributeIndex(ctx, headerRequestsAttributeIndex(req.Header()))
		}
		return next(ctx, req)
	}
}

func (attributeIndexInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (attributeIndexInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		ctx = WithAttributeIndex(ctx, headerRequestsAttributeIndex(conn.RequestHeader()))
		return next(ctx, conn)
	}
}

func headerRequestsAttributeIndex(headers map[string][]string) bool {
	values := headers[AttributeIndexHeader]
	if len(values) == 0 {
		values = headers[strings.ToLower(AttributeIndexHeader)]
	}
	for _, value := range values {
		if value == "1" || strings.EqualFold(value, "true") {
			return true
		}
	}
	return false
}
