// SPDX-License-Identifier: MIT

package shale

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// startSpan starts a span of the background work, or returns a span that does
// nothing if the DB has no tracer.
func (db *DB) startSpan(name string) trace.Span {
	if db.tr == nil {
		return trace.SpanFromContext(context.Background())
	}
	_, span := db.tr.Start(context.Background(), name, trace.WithSpanKind(trace.SpanKindInternal))
	return span
}

// endSpan ends span with the attributes. A failure sets the status of the span to
// error with no text, because the error can name a file.
func endSpan(span trace.Span, err error, attrs ...attribute.KeyValue) {
	span.SetAttributes(attrs...)
	if err != nil {
		span.SetStatus(codes.Error, "failed")
	}
	span.End()
}
