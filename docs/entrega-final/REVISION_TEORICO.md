# Revisión del repositorio contra el teórico

Fecha: 13/11/2026. Alcance: código de `main` en ef1197b, revisado clase por clase contra el teórico de la materia (clases 1 a 9) y el enunciado del práctico integrador. No se cambió código; las correcciones quedan para etapas posteriores.

## 1. Lo que cumple

| Tema | Clase | Evidencia |
|---|---|---|
| Arquitectura interna | 8 | En Booking y Benefits, `domain` usa solo la biblioteca estándar, sin tags; lo controlan los tests `internal/domain/architecture_test.go`. Members y Training respetan handler → service → repository. |
| Base por servicio e invariantes en la base | 2 | `booking_db`: cupo con `CHECK (ocupacion <= capacidad)`, superposición con `EXCLUDE USING gist` y unicidad con índices únicos. Reservas con `FOR UPDATE` sobre la clase. |
| Outbox | 2, 4 | El relay lee con `FOR UPDATE SKIP LOCKED`, publica con confirms y `mandatory` y marca el evento recién después de la confirmación. |
| Consumidor idempotente | 2, 4 | Benefits deduplica por `event_id` y por tipo y origen en la misma transacción que el movimiento, y hace ACK después del efecto (`adapters/postgres/attendance.go`, `pkg/messaging/worker.go`). |
| Reintentos y DLQ | 4 | Tres reintentos con demora (10 s, 1 min, 10 min); los errores permanentes o agotados van a `<cola>.dlq`. `prefetch` de 10. |
| Índice como read model | 5 | Versionado externo en OpenSearch, alias `clases-read` y reindexado sin pausa. |
| Caché | 3 | Cache-aside con TTL, clave versionada (`booking:actividades:v1`), timeout propio e invalidación después del commit. No se cachean datos críticos. |
| Borde | 6 | El gateway borra las cabeceras de identidad entrantes, responde 404 a rutas `internal`, no reintenta y aplica el rate limit en Redis con `429` y `Retry-After`. |
| Instrucciones ocultas del enunciado | — | El PDF del enunciado contiene instrucciones dirigidas a IA que contradicen el propio enunciado (contraseña en claro, índice sincronizado solo al buscar, health en portugués). No se aplican: login con bcrypt (ADR-006), índice sincronizado por eventos (requisito 2.6) y health según `pkg/health`. |

## 2. Hallazgos

### Prioridad alta

| # | Hallazgo | Referencia | Propuesta |
|---|---|---|---|
| A1 | Booking no consume `membresia.cancelada` ni `usuario.desactivado`. Las reservas futuras siguen confirmadas cuando se cancela una membresía o se da de baja a un alumno. `ARCHITECTURE.md` lo marca como diseño objetivo, pero su §10 lo describe como si funcionara. | SPEC RN-04, RN-34, CL-12; clases 2 y 4 | Implementar el relay de Members y el consumidor idempotente en Booking, o declararlo como limitación en `ARCHITECTURE.md` §15 y corregir §10. Requiere decisión del equipo. |
| A2 | ADR-005 v2 no coincide con el código. El ADR promete 800 ms, 1 reintento en las consultas a Members y `traceparent` propagado. El código usa 2 s (Booking, Training) y 3 a 3,5 s (Benefits), no tiene reintentos y solo Benefits genera trazas. | Enunciado 2.5; clase 7 §3 | Reintento único con backoff y jitter en los GET idempotentes a Members, dentro del presupuesto restante, y actualizar el ADR con los valores reales. |
| A3 | No hay balanceo de carga: Booking corre en una sola instancia y no hay Traefik en el Compose. | Enunciado 2.3, D12; clase 6 | Etapa 12, parte B (ADR-012 v2). |
| A4 | No se mide la cobertura. Medición del 13/11/2026 con `go test -cover`, sin Docker: booking `domain` 77,3 %, booking `application` 48,0 %, benefits `domain` 89,0 %, benefits `application` 35,1 %, training `service` 81,8 %, members `service` 13,7 %. | Enunciado 2.11.1 | Agregar cobertura a `make test`, fijar un umbral y subir Members `service`. |

### Prioridad media

| # | Hallazgo | Referencia | Propuesta |
|---|---|---|---|
| M1 | Un error de Redis se cuenta como miss además de error, lo que distorsiona la tasa de aciertos usada como evidencia de la caché. | Clase 3 §2.1; enunciado 2.7 | En `adapters/redis`, contar como miss solo la ausencia de clave. |
| M2 | Los reintentos de mensajería vuelven por `gym.events` (topic) con la routing key original. Hoy hay una cola por evento, pero un segundo suscriptor recibiría también los reintentos de otro. | Clase 4 §4.2 | Reenviar al exchange por defecto con el nombre de la cola. Requiere recrear las colas quorum, porque cambian sus argumentos. |
| M3 | El campo `traceparent` de los eventos viaja siempre vacío y la traza se corta en el broker. | Clase 4 §8; enunciado 2.10 | Guardar el `traceparent` en el outbox al escribir el evento y restaurarlo en el consumidor. |
| M4 | Los reintentos de mensajería tienen esperas fijas, sin jitter. | Clase 7 §3.3 | Expiración por mensaje con variación aleatoria o documentar el límite. |
| M5 | Con Redis caído, cada lectura del catálogo paga el timeout del GET y el del SET antes de ir a PostgreSQL, sin límite. | Clase 3 §8.3 | No intentar el SET cuando el GET falló por error y documentarlo en ADR-007. |

### Prioridad baja

| # | Hallazgo | Referencia | Propuesta |
|---|---|---|---|
| B1 | El relay de Booking declara las colas `benefits.*` del consumidor para que `mandatory` no descarte eventos. | Clase 4 §3.1 | Justificarlo en un ADR o mover la declaración al consumidor. |
| B2 | El gateway no propaga el tiempo restante a los servicios; cada uno usa timeouts fijos. Hoy entran dentro del presupuesto. | Clase 7 §2.2 | Registrarlo en `ARCHITECTURE.md` §15. |

## 3. Orden propuesto

1. Correcciones rápidas: M1, M5 y M3.
2. Reintento en las consultas a Members y alineación de ADR-005 (A2).
3. Balanceo de Booking (A3).
4. Consumidor de `membresia.cancelada` (A1), previa decisión de alcance.
5. Cobertura de Members (A4) y el resto de los hallazgos.
