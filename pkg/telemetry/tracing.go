// Package telemetry contiene instrumentacion tecnica, sin reglas de negocio.
package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Setup habilita OTLP cuando se configura el endpoint; sin endpoint no exporta.
// Solo se propaga TraceContext: nunca baggage con datos provistos por clientes.
func Setup(ctx context.Context, service string, log *slog.Logger) (func(context.Context) error, error) {
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return func(context.Context) error { return nil }, nil
	}
	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", service))),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithBatcher(exporter),
	)
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(error) {
		// El error crudo puede contener URLs o datos del exportador.
		log.Warn("no se pudieron exportar trazas", slog.String("component", "otlp"))
	}))
	return provider.Shutdown, nil
}

var (
	// identificador: UUID, hexadecimal largo o número. Se reemplaza por {id}.
	identificador = regexp.MustCompile(`^([0-9a-fA-F]{8}-[0-9a-fA-F-]{27}|[0-9a-fA-F]{16,}|[0-9]+)$`)
	// palabra: segmento fijo de una ruta propia (bookings, check-ins, vigencia).
	palabra = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
)

// Route evita URLs concretas, query strings e identificadores en los spans.
// Cubre la API propia (/api/v1), la interna (/internal/v1) y las cuentas de la
// API de partners; todo lo demás (health, métricas, rutas desconocidas) queda
// fuera. Un segmento que no es una palabra fija se reemplaza por {id}, así nunca
// llega a una traza un identificador, un código ni un dato del cliente.
func Route(path string) string {
	prefix := ""
	if strings.HasPrefix(path, "/partner-api/") {
		prefix = "/partner-api"
		path = strings.TrimPrefix(path, prefix)
	}
	parts := strings.Split(path, "/")
	if len(parts) == 5 && (parts[1] == "v1" || parts[1] == "v2") && parts[2] == "accounts" {
		switch parts[4] {
		case "credits", "balance", "movements":
			return prefix + "/" + parts[1] + "/accounts/{externalUserId}/" + parts[4]
		}
	}
	if prefix != "" || len(parts) < 4 || parts[0] != "" || (parts[1] != "api" && parts[1] != "internal") || parts[2] != "v1" {
		return "unmatched"
	}
	for i := 3; i < len(parts); i++ {
		if identificador.MatchString(parts[i]) || !palabra.MatchString(parts[i]) {
			parts[i] = "{id}"
		}
	}
	return strings.Join(parts, "/")
}

// plantilla convierte la ruta registrada en Gin (/bookings/:id/cancel) al
// formato de las trazas (/bookings/{id}/cancel).
func plantilla(fullPath string) string {
	parts := strings.Split(fullPath, "/")
	for i, p := range parts {
		if strings.HasPrefix(p, ":") {
			parts[i] = "{" + strings.TrimPrefix(p, ":") + "}"
		}
	}
	return strings.Join(parts, "/")
}

func method(value string) string {
	switch value {
	case "GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS":
		return value
	default:
		return "OTHER"
	}
}

// Middleware instrumenta solo el flujo minimo; los health checks no generan spans.
func Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		route := Route(c.Request.URL.Path)
		if route == "unmatched" {
			c.Next()
			return
		}
		// La plantilla registrada es más precisa que la normalización; el proxy del
		// gateway usa un comodín y conserva la ruta normalizada.
		if full := c.FullPath(); full != "" && !strings.Contains(full, "*") {
			route = plantilla(full)
		}
		parent := propagation.TraceContext{}.Extract(c.Request.Context(), propagation.HeaderCarrier(c.Request.Header))
		remote := trace.SpanContextFromContext(parent).WithTraceState(trace.TraceState{})
		parent = trace.ContextWithRemoteSpanContext(parent, remote)
		ctx, span := otel.Tracer("gym/http").Start(parent, method(c.Request.Method)+" "+route, trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(attribute.String("http.route", route), attribute.String("http.request.method", method(c.Request.Method))))
		defer span.End()
		c.Request = c.Request.WithContext(ctx)
		c.Next()
		span.SetAttributes(attribute.Int("http.response.status_code", c.Writer.Status()))
		if c.Writer.Status() >= 500 {
			span.SetStatus(codes.Error, "error HTTP")
		}
	}
}

// Transport crea el span cliente y propaga su contexto sin capturar headers/URL.
type Transport struct{ Base http.RoundTripper }

func (t Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	route := Route(r.URL.Path)
	if route == "unmatched" {
		return base.RoundTrip(r)
	}
	ctx, span := otel.Tracer("gym/http").Start(r.Context(), method(r.Method)+" "+route, trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String("http.route", route), attribute.String("http.request.method", method(r.Method))))
	defer span.End()
	out := r.Clone(ctx)
	// Evitar reenviar metadata arbitraria del cliente; solo nuestro traceparent.
	out.Header.Del("tracestate")
	out.Header.Del("baggage")
	out.Header.Del("traceparent")
	propagation.TraceContext{}.Inject(ctx, propagation.HeaderCarrier(out.Header))
	response, err := base.RoundTrip(out)
	if err != nil {
		span.SetStatus(codes.Error, "fallo de transporte")
	} else {
		span.SetAttributes(attribute.Int("http.response.status_code", response.StatusCode))
		if response.StatusCode >= 500 {
			span.SetStatus(codes.Error, "error HTTP")
		}
	}
	return response, err
}

// StartClient abre un span hacia una dependencia (postgresql, redis,
// opensearch) con el nombre de la operación. No registra consultas, claves,
// parámetros ni mensajes de error: solo el tipo de falla.
func StartClient(ctx context.Context, system, operation string) (context.Context, func(error)) {
	ctx, span := otel.Tracer("gym/"+system).Start(ctx, system+" "+operation,
		trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attribute.String("db.system.name", system)))
	return ctx, func(err error) {
		defer span.End()
		if err == nil {
			return
		}
		kind := "error"
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			kind = "deadline_exceeded"
		case errors.Is(err, context.Canceled):
			kind = "canceled"
		}
		span.SetAttributes(attribute.String("error.type", kind))
		span.SetStatus(codes.Error, kind)
	}
}

// Traceparent devuelve el traceparent W3C del span actual, o "" si no hay
// traza. Se guarda en el outbox para continuar la traza en el consumidor.
func Traceparent(ctx context.Context) string {
	if !trace.SpanContextFromContext(ctx).IsValid() {
		return ""
	}
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	return carrier["traceparent"]
}

// StartConsumer continúa, desde un mensaje, la traza que lo produjo. Un
// traceparent vacío o inválido inicia una traza nueva. Nunca usa tracestate ni
// baggage del mensaje.
func StartConsumer(ctx context.Context, traceparent, name string) (context.Context, func(error)) {
	parent := propagation.TraceContext{}.Extract(ctx, propagation.MapCarrier{"traceparent": traceparent})
	remote := trace.SpanContextFromContext(parent).WithTraceState(trace.TraceState{})
	if remote.IsValid() {
		parent = trace.ContextWithRemoteSpanContext(ctx, remote)
	} else {
		parent = ctx
	}
	ctx, span := otel.Tracer("gym/amqp").Start(parent, name+" process", trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(attribute.String("messaging.system", "rabbitmq"), attribute.String("messaging.destination.name", name)))
	return ctx, func(err error) {
		defer span.End()
		if err != nil {
			span.SetStatus(codes.Error, "mensaje no procesado")
		}
	}
}
