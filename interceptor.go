package grpc

import (
	"encoding/base64"
	"fmt"
	"strings"

	grpc_ctxtags "github.com/grpc-ecosystem/go-grpc-middleware/tags"
	"github.com/grpc-ecosystem/go-grpc-middleware/util/metautils"
	"github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/ext"
	"github.com/opentracing/opentracing-go/log"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/grpclog"
	"google.golang.org/grpc/metadata"
)

const (
	TagTraceId           = "trace.traceid"
	TagSpanId            = "trace.spanid"
	TagSampled           = "trace.sampled"
	jaegerNotSampledFlag = "0"
	binHdrSuffix         = "-bin"
)

var (
	grpcTag = opentracing.Tag{Key: string(ext.Component), Value: "gRPC"}
)

type clientSpanTagKey struct{}
type metadataTextMap metadata.MD

func (m metadataTextMap) Set(key, val string) {
	// gRPC allows for complex binary values to be written.
	encodedKey, encodedVal := encodeKeyValue(key, val)
	// The metadata object is a multimap, and previous values may exist, but for opentracing headers, we do not append
	// we just override.
	m[encodedKey] = []string{encodedVal}
}

func (m metadataTextMap) ForeachKey(callback func(key, val string) error) error {
	for k, vv := range m {
		for _, v := range vv {
			if decodedKey, decodedVal, err := metadata.DecodeKeyValue(k, v); err == nil {
				if err = callback(decodedKey, decodedVal); err != nil {
					return err
				}
			} else {
				return fmt.Errorf("failed decoding opentracing from gRPC metadata: %v", err)
			}
		}
	}
	return nil
}

func encodeKeyValue(k, v string) (string, string) {
	k = strings.ToLower(k)
	if strings.HasSuffix(k, binHdrSuffix) {
		val := base64.StdEncoding.EncodeToString([]byte(v))
		v = string(val)
	}
	return k, v
}

type tagsCarrier struct {
	grpc_ctxtags.Tags
}

func (t *tagsCarrier) Set(key, val string) {
	key = strings.ToLower(key)
	if strings.Contains(key, "traceid") {
		t.Tags.Set(TagTraceId, val) // this will most likely be base-16 (hex) encoded
	}

	if strings.Contains(key, "spanid") && !strings.Contains(strings.ToLower(key), "parent") {
		t.Tags.Set(TagSpanId, val) // this will most likely be base-16 (hex) encoded
	}

	if strings.Contains(key, "sampled") {
		switch val {
		case "true", "false":
			t.Tags.Set(TagSampled, val)
		}
	}

	if key == "uber-trace-id" {
		parts := strings.Split(val, ":")
		if len(parts) == 4 {
			t.Tags.Set(TagTraceId, parts[0])
			t.Tags.Set(TagSpanId, parts[1])

			if parts[3] != jaegerNotSampledFlag {
				t.Tags.Set(TagSampled, "true")
			} else {
				t.Tags.Set(TagSampled, "false")
			}
		}
	}
}

func injectOpentracingIdsToTags(span opentracing.Span, tags grpc_ctxtags.Tags) {
	if err := span.Tracer().Inject(span.Context(), opentracing.HTTPHeaders, &tagsCarrier{tags}); err != nil {
		grpclog.Infof("grpc_opentracing: failed extracting trace info into ctx %v", err)
	}
}

// UnaryServerInterceptor returns a new unary server interceptor for OpenTracing.
func UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		newCtx, serverSpan := newServerSpanFromInbound(ctx, opentracing.GlobalTracer(), info.FullMethod)
		resp, err := handler(newCtx, req)
		finishServerSpan(ctx, serverSpan, err)
		return resp, err
	}
}

func newServerSpanFromInbound(ctx context.Context, tracer opentracing.Tracer, fullMethodName string) (context.Context, opentracing.Span) {
	md := metautils.ExtractIncoming(ctx)
	parentSpanCtx, err := tracer.Extract(opentracing.HTTPHeaders, metadataTextMap(md))
	if err != nil && err != opentracing.ErrSpanContextNotFound {
		grpclog.Printf("grpc_opentracing: failed parsing trace information: %v", err)
	}

	opts := []opentracing.StartSpanOption{
		opentracing.ChildOf(parentSpanCtx),
		ext.SpanKindRPCServer,
		grpcTag,
	}
	if tagx := ctx.Value(clientSpanTagKey{}); tagx != nil {
		if opt, ok := tagx.(opentracing.StartSpanOption); ok {
			opts = append(opts, opt)
		}
	}
	serverSpan := tracer.StartSpan(fullMethodName, opts...)

	md = md.Clone()
	if err := tracer.Inject(serverSpan.Context(), opentracing.HTTPHeaders, metadataTextMap(md)); err != nil {
		grpclog.Printf("grpc_opentracing: failed serializing trace information: %v", err)
	}

	injectOpentracingIdsToTags(serverSpan, grpc_ctxtags.Extract(ctx))
	ctxWithMetadata := md.ToIncoming(ctx)
	return opentracing.ContextWithSpan(ctxWithMetadata, serverSpan), serverSpan
}

func finishServerSpan(ctx context.Context, serverSpan opentracing.Span, err error) {
	// Log context information
	tags := grpc_ctxtags.Extract(ctx)
	for k, v := range tags.Values() {
		// Don't tag errors, log them instead.
		if vErr, ok := v.(error); ok {
			serverSpan.LogKV(k, vErr.Error())

		} else {
			serverSpan.SetTag(k, v)
		}
	}
	if err != nil {
		ext.Error.Set(serverSpan, true)
		serverSpan.LogFields(log.String("event", "error"), log.String("message", err.Error()))
	}
	serverSpan.Finish()
}
