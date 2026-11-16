# Trazas completas (D11)

Fecha: 15/11/2026. Decisión: D-101, que extiende [ADR-011 v2](../adr/ADR-011-v2-observabilidad-local.md). Origen: acción del [postmortem](../POSTMORTEM.md) de la jornada de fallas.

## 1. Qué faltaba

Hasta ahora solo el gateway y Benefits exportaban trazas, y solo para la API de partners. Una búsqueda, una reserva o un ingreso no generaban spans. Durante la jornada de fallas, una operación de Booking se reconstruía cruzando logs por `correlation_id`. Tampoco había traza a través de RabbitMQ, aunque ARCHITECTURE §11 lo pedía.

## 2. Qué se hizo

| Pieza | Cambio |
|---|---|
| `pkg/telemetry` | `Route` reconoce `/api/v1`, `/internal/v1` y las cuentas de partners. Cada segmento que no es una palabra fija se reemplaza por `{id}`: UUID, números, códigos como `MENSUAL` o un email. El middleware usa la plantilla registrada en Gin (`/api/v1/bookings/{id}/cancel`). Agrega `StartClient` (span hacia una dependencia), `Traceparent` y `StartConsumer` (continuar una traza desde un mensaje). |
| Booking y Members | Inician el exportador OTLP como el gateway y Benefits. Compose les pasa `OTEL_EXPORTER_OTLP_ENDPOINT`. |
| Llamadas entre servicios | Booking → Members (vigencia, DNI, nombres), Members → Booking (agenda del profesor) y Members → Benefits (registro externo) propagan `traceparent`. |
| Spans de dependencias en Booking | PostgreSQL: cada transacción idempotente (`booking.reservar`, `booking.cancelar_reserva`, `booking.ingreso`…), la búsqueda, las marcas y las revocaciones. Redis: lectura (con `cache.hit`), última copia y escritura. OpenSearch: búsqueda. |
| Outbox | Columna `traceparent` (migración 004 de Booking, con CHECK de formato W3C). `asistencia.registrada` guarda el del ingreso y el relay lo envía en el sobre. |
| Benefits | El consumidor continúa la traza (`asistencia.registrada process`) y mide el ledger (`postgres.aplicar_hecho_booking`). |

Se mantienen las reglas de ADR-011 v2: instrumentación manual, sin `otelgin` ni `otelhttp`, y atributos restringidos. No se registran consultas SQL, claves de Redis, parámetros, query strings, headers ni cuerpos. Solo se propaga `traceparent`; `tracestate` y `baggage` del cliente se descartan. Un rechazo de negocio (`SIN_CUPO`, `RESERVA_DUPLICADA`) es una respuesta correcta y no se marca como error. Sí se marcan las fallas de conexión, los timeouts y los errores de PostgreSQL sin traducir.

## 3. Evidencia

Trazas reales del stack local (`make demo`), leídas de la API de Jaeger (`127.0.0.1:16686`).

**Ingreso por DNI y acreditación de puntos**, traza `67bc5542d0fb2ca085d7feaf7d86c428`:

| Inicio | Duración | Servicio | Span |
|---|---|---|---|
| +0 ms | 64 ms | api-gateway | `POST /api/v1/check-ins` (servidor y cliente) |
| +1 ms | 63 ms | booking-service | `POST /api/v1/check-ins` |
| +4 ms | 3 ms | booking → members | `POST /internal/v1/alumnos/buscar-dni` |
| +8 ms | 1 ms | booking → members | `GET /internal/v1/alumnos/{id}/vigencia` |
| +9 ms | 54 ms | booking-service | `postgresql booking.ingreso` (asistencia y outbox) |
| +238 ms | 50 ms | benefits-service | `asistencia.registrada process`, a través de RabbitMQ |
| +238 ms | 50 ms | benefits-service | `postgres.aplicar_hecho_booking` (+500 puntos) |

**Búsqueda de clases**, dos veces la misma consulta:

- Primera: `redis clases.leer` con `cache.hit=false` → `opensearch clases.buscar` → `redis clases.guardar` → `postgresql booking.marcas`.
- Segunda: solo `redis clases.leer` con `cache.hit=true`.

**Reserva:** gateway → `POST /api/v1/bookings` → Members (`vigencia`) → `postgresql booking.reservar`.

## 4. Verificación

- `pkg/telemetry`: normalización de rutas, incluidos los casos que no deben filtrar datos, y el cruce del broker: el consumidor es hijo del productor y un `traceparent` inválido inicia una traza nueva. Se conserva el test previo de propagación y logs sin secretos.
- Tests de Booking, Members, Benefits y gateway, incluidas sus integraciones con contenedores.
- Recorrido manual sobre el stack local con las trazas de arriba.

## 5. Límites

- Training y notification-worker todavía no exportan trazas.
- `clase.actualizada` (trigger), `inasistencia.registrada` (proceso programado) y los eventos de Members no llevan `traceparent`; se siguen por `correlation_id`.
- Jaeger guarda en memoria: las trazas se pierden al reiniciarlo (ADR-011 v2). Loki sigue pendiente.
