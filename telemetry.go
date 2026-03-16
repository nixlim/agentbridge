package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	tracesdk "go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"
)

type CommandTelemetry struct {
	ID             string           `json:"id"`
	TraceID        string           `json:"trace_id,omitempty"`
	SpanID         string           `json:"span_id,omitempty"`
	Kind           string           `json:"kind"`
	Provider       string           `json:"provider,omitempty"`
	Command        string           `json:"command"`
	Args           []string         `json:"args,omitempty"`
	WorkDir        string           `json:"work_dir,omitempty"`
	TimeoutSeconds int              `json:"timeout_seconds"`
	Status         string           `json:"status"`
	PID            int              `json:"pid,omitempty"`
	StartedAt      time.Time        `json:"started_at"`
	LastEventAt    time.Time        `json:"last_event_at"`
	EndedAt        *time.Time       `json:"ended_at,omitempty"`
	DurationMs     int64            `json:"duration_ms,omitempty"`
	ExitCode       int              `json:"exit_code,omitempty"`
	StdoutBytes    int              `json:"stdout_bytes,omitempty"`
	StderrBytes    int              `json:"stderr_bytes,omitempty"`
	Error          string           `json:"error,omitempty"`
	Events         []TelemetryEvent `json:"events,omitempty"`
}

type TelemetryEvent struct {
	Name       string            `json:"name"`
	Timestamp  time.Time         `json:"timestamp"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

type spanRecord struct {
	Name       string            `json:"name"`
	TraceID    string            `json:"trace_id"`
	SpanID     string            `json:"span_id"`
	StartTime  time.Time         `json:"start_time"`
	EndTime    time.Time         `json:"end_time"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

type memorySpanExporter struct {
	mu    sync.Mutex
	spans []spanRecord
}

func (e *memorySpanExporter) ExportSpans(_ context.Context, spans []tracesdk.ReadOnlySpan) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	for _, span := range spans {
		record := spanRecord{
			Name:      span.Name(),
			TraceID:   span.SpanContext().TraceID().String(),
			SpanID:    span.SpanContext().SpanID().String(),
			StartTime: span.StartTime(),
			EndTime:   span.EndTime(),
		}
		if attrs := span.Attributes(); len(attrs) > 0 {
			record.Attributes = make(map[string]string, len(attrs))
			for _, attr := range attrs {
				record.Attributes[string(attr.Key)] = attr.Value.Emit()
			}
		}
		e.spans = append(e.spans, record)
	}
	if len(e.spans) > 128 {
		e.spans = append([]spanRecord(nil), e.spans[len(e.spans)-128:]...)
	}
	return nil
}

func (e *memorySpanExporter) Shutdown(context.Context) error { return nil }

func (e *memorySpanExporter) Recent() []spanRecord {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]spanRecord(nil), e.spans...)
}

type telemetryManager struct {
	tracer   oteltrace.Tracer
	exporter *memorySpanExporter
}

var globalTelemetry = newTelemetryManager()

func newTelemetryManager() *telemetryManager {
	exporter := &memorySpanExporter{}
	provider := tracesdk.NewTracerProvider(tracesdk.WithSyncer(exporter))
	otel.SetTracerProvider(provider)
	return &telemetryManager{
		tracer:   provider.Tracer("agentbridge"),
		exporter: exporter,
	}
}

type commandTelemetryObserver struct {
	mu       sync.Mutex
	update   func(CommandTelemetry)
	span     oteltrace.Span
	ctx      context.Context
	current  CommandTelemetry
	finished bool
}

func newCommandTelemetryObserver(ctx context.Context, kind, provider string, config AgentConfig, workDir string, args []string, update func(CommandTelemetry)) *commandTelemetryObserver {
	now := time.Now().UTC()
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, span := globalTelemetry.tracer.Start(ctx, fmt.Sprintf("%s.command", kind), oteltrace.WithAttributes(
		attribute.String("agentbridge.kind", kind),
		attribute.String("agentbridge.provider", provider),
		attribute.String("agentbridge.command", config.Command),
		attribute.Int("agentbridge.timeout_seconds", config.TimeoutSeconds),
		attribute.String("agentbridge.work_dir", workDir),
	))
	obs := &commandTelemetryObserver{
		update: update,
		span:   span,
		ctx:    ctx,
		current: CommandTelemetry{
			ID:             uuid.NewString(),
			TraceID:        span.SpanContext().TraceID().String(),
			SpanID:         span.SpanContext().SpanID().String(),
			Kind:           kind,
			Provider:       provider,
			Command:        config.Command,
			Args:           append([]string(nil), args...),
			WorkDir:        workDir,
			TimeoutSeconds: config.TimeoutSeconds,
			Status:         "queued",
			StartedAt:      now,
			LastEventAt:    now,
		},
	}
	obs.current.Events = append(obs.current.Events, TelemetryEvent{
		Name:      "queued",
		Timestamp: now,
		Attributes: map[string]string{
			"command": config.Command,
		},
	})
	obs.span.AddEvent("queued", oteltrace.WithAttributes(attribute.String("command", config.Command)))
	return obs
}

func (o *commandTelemetryObserver) Context() context.Context {
	if o == nil {
		return context.Background()
	}
	return withCommandTelemetryObserver(o.ctx, o)
}

func (o *commandTelemetryObserver) ProcessStarted(pid int) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.current.PID = pid
	o.current.Status = "running"
	o.span.SetAttributes(attribute.Int("agentbridge.pid", pid))
	o.recordLocked("process_started", map[string]string{"pid": fmt.Sprintf("%d", pid)})
}

func (o *commandTelemetryObserver) Output(stream string, n int) {
	if o == nil || n <= 0 {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	switch stream {
	case "stdout":
		o.current.StdoutBytes += n
	case "stderr":
		o.current.StderrBytes += n
	}
	o.current.Status = "running"
	o.recordLocked(stream+"_activity", map[string]string{"bytes": fmt.Sprintf("%d", n)})
}

func (o *commandTelemetryObserver) WaitStarted() {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.current.Status == "queued" {
		o.current.Status = "starting"
	} else {
		o.current.Status = "waiting_for_exit"
	}
	o.recordLocked("waiting_for_exit", nil)
}

func (o *commandTelemetryObserver) Finish(status string, exitCode int, err error, duration time.Duration) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.finished {
		return
	}
	o.finished = true
	now := time.Now().UTC()
	o.current.Status = status
	o.current.ExitCode = exitCode
	o.current.DurationMs = duration.Milliseconds()
	o.current.EndedAt = &now
	o.current.LastEventAt = now
	if err != nil {
		o.current.Error = err.Error()
		o.span.RecordError(err)
	}
	o.span.SetAttributes(
		attribute.String("agentbridge.final_status", status),
		attribute.Int("agentbridge.exit_code", exitCode),
		attribute.Int64("agentbridge.duration_ms", duration.Milliseconds()),
	)
	o.recordLocked("finished", map[string]string{
		"status":    status,
		"exit_code": fmt.Sprintf("%d", exitCode),
	})
	o.span.End()
}

func (o *commandTelemetryObserver) recordLocked(name string, attrs map[string]string) {
	now := time.Now().UTC()
	o.current.LastEventAt = now
	o.current.Events = append(o.current.Events, TelemetryEvent{
		Name:       name,
		Timestamp:  now,
		Attributes: cloneTelemetryStringMap(attrs),
	})
	if len(o.current.Events) > 24 {
		o.current.Events = append([]TelemetryEvent(nil), o.current.Events[len(o.current.Events)-24:]...)
	}
	if attrs == nil {
		o.span.AddEvent(name)
	} else {
		eventAttrs := make([]attribute.KeyValue, 0, len(attrs))
		for key, value := range attrs {
			eventAttrs = append(eventAttrs, attribute.String(key, value))
		}
		o.span.AddEvent(name, oteltrace.WithAttributes(eventAttrs...))
	}
	if o.update != nil {
		o.update(o.cloneLocked())
	}
}

func (o *commandTelemetryObserver) cloneLocked() CommandTelemetry {
	clone := o.current
	clone.Args = append([]string(nil), o.current.Args...)
	clone.Events = append([]TelemetryEvent(nil), o.current.Events...)
	return clone
}

func cloneTelemetryStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

type commandTelemetryContextKey struct{}

func withCommandTelemetryObserver(ctx context.Context, observer *commandTelemetryObserver) context.Context {
	return context.WithValue(ctx, commandTelemetryContextKey{}, observer)
}

func commandTelemetryObserverFromContext(ctx context.Context) *commandTelemetryObserver {
	if ctx == nil {
		return nil
	}
	observer, _ := ctx.Value(commandTelemetryContextKey{}).(*commandTelemetryObserver)
	return observer
}

type telemetryWriter struct {
	stream   string
	observer *commandTelemetryObserver
}

func (w telemetryWriter) Write(p []byte) (int, error) {
	if w.observer != nil {
		w.observer.Output(w.stream, len(p))
	}
	return len(p), nil
}

func encodeTelemetryJSON(value interface{}) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func telemetryMultiWriter(primary io.Writer, stream string, observer *commandTelemetryObserver) io.Writer {
	if observer == nil {
		return primary
	}
	return io.MultiWriter(primary, telemetryWriter{stream: stream, observer: observer})
}
