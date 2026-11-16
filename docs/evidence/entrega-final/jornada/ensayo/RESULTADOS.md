# Jornada de fallas controladas (entrega final)

Generado por `tests/resilience/jornada.go` el 2026-11-16T03:17:07Z. Datos completos en los JSON de esta carpeta.

Tráfico de fondo: un alumno busca cada 0,5 s, reserva y cancela cada 4 s e inicia sesión cada 15 s, a través del gateway. Cada falla detiene un contenedor con `docker stop` y lo vuelve a iniciar.

| Falla | Esperado | Búsqueda | Reserva | Login | Estado degradado | Recuperación |
|---|---|---|---|---|---|---|
| OpenSearch caído | Búsqueda servida desde la caché (fresca o vencida); reservas normales; clase.actualizada espera en RabbitMQ | 200 ×2, 503 DEPENDENCIA_NO_DISPONIBLE ×11 | 201 ×5 | 200 ×2 | 2 s | 129 s |

## OpenSearch caído (`gym-opensearch-1`)

- Detenido: 03:14:19; iniciado: 03:14:44 (hora UTC).
- Respuestas durante la caída: map[busqueda:map[200:2 503 DEPENDENCIA_NO_DISPONIBLE:11] cancelacion:map[200:5] login:map[200:2] reserva:map[201:5]].
- Origen de las búsquedas exitosas: map[cache:1 fuente:1]; sin marca «ya reservada»: 0.
- Alerta `ServicioCaido` pending (instance=api-gateway:8080,job=api-gateway,service=booking-indexer) a los 15 s.
- Alerta `ServicioCaido` firing (instance=api-gateway:8080,job=api-gateway,service=booking-indexer) a los 35 s.
- Alerta `ServicioCaido` pending (instance=api-gateway:8080,job=api-gateway,service=booking-service-2) a los 35 s.
- Outbox de Booking pendiente: máximo 2, al final 0.
- Líneas WARN/ERROR en logs: map[api-gateway:467 booking-indexer:1 booking-service:146 booking-service-2:129].
- Diseño: ADR-014, ADR-007 v2, D-95.
