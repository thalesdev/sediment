// SPDX-License-Identifier: MIT

package server

import (
	"context"
	"math/rand/v2"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/thalesdev/sediment/resp"
	"github.com/thalesdev/sediment/server/command"
)

// sampled reports whether the next command gets a span.
func (s *Server) sampled() bool {
	rate := s.opts.TraceSampleRate
	return s.opts.Tracer != nil && rate > 0 && (rate >= 1 || rand.Float64() < rate)
}

// startSpan starts the span of a command, if the sample picks it. It returns a nil
// span if not. The span has the name of the command and the number of its keys, and
// never a key or an argument.
func (s *Server) startSpan(spec *command.Spec, args [][]byte) trace.Span {
	if !s.sampled() {
		return nil
	}
	return s.newSpan(spec, len(command.KeysOf(spec, args)))
}

// startBatchSpan starts the span of a group of n SET commands that run as one
// write, if the sample picks it. The span has the number of keys of the group.
func (s *Server) startBatchSpan(spec *command.Spec, n int) trace.Span {
	if !s.sampled() {
		return nil
	}
	return s.newSpan(spec, n)
}

// newSpan starts a span for the command with the number of its keys.
func (s *Server) newSpan(spec *command.Spec, keys int) trace.Span {
	_, span := s.opts.Tracer.Start(context.Background(), spec.Name,
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("db.system.name", "sediment"),
			attribute.String("db.operation.name", spec.Name),
			attribute.Int("sediment.keys", keys),
		))
	return span
}

// endSpan ends the span of a command with the result and the size of the reply.
func endSpan(span trace.Span, reply resp.Value) {
	result := "ok"
	if reply.Kind == resp.KindError {
		result = "error"
	}
	span.SetAttributes(attribute.String("sediment.result", result), attribute.Int("sediment.reply.bytes", command.ReplySize(reply)))
	span.End()
}

// traceID returns the trace id of the span as text, or "" if there is no span, so
// that a line of the log can name the trace.
func traceID(span trace.Span) string {
	if span == nil {
		return ""
	}
	if id := span.SpanContext().TraceID(); id.IsValid() {
		return id.String()
	}
	return ""
}
