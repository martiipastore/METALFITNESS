# Jornada de fallas controladas (entrega final)

Generado por `tests/resilience/jornada.go` el 2026-11-16T04:00:17Z. Datos completos en los JSON de esta carpeta.

Tráfico de fondo: un alumno busca cada 0,5 s, reserva y cancela cada 4 s e inicia sesión cada 15 s, a través del gateway. Cada falla detiene un contenedor con `docker stop` y lo vuelve a iniciar.

| Falla | Esperado | Búsqueda | Reserva | Login | Estado degradado | Recuperación |
|---|---|---|---|---|---|---|
| OpenSearch caído | Búsqueda servida desde la caché (fresca o vencida); reservas normales; clase.actualizada espera en RabbitMQ | 200 ×38 | 201 ×16 | 200 ×5 | 2 s | 44 s |
| PostgreSQL caído | Búsqueda con la copia de Redis y marcas_disponibles=false; reservar y login fallan cerrados con 503 | 200 ×65 | 503 DEPENDENCIA_NO_DISPONIBLE ×15 | 503 DEPENDENCIA_NO_DISPONIBLE ×2, 504 TIMEOUT ×2 | 0 s | 2 s |
| RabbitMQ caído | Reservas y búsqueda normales; el outbox acumula y se vacía al volver el broker | 200 ×129 | 201 ×16 | 200 ×4 | 1 s | 23 s |
| Redis caído | Login falla cerrado (503); el resto sigue sin límite de tráfico; la búsqueda va a la fuente | 200 ×124 | 201 ×16 | 503 DEPENDENCIA_NO_DISPONIBLE ×4 | 1 s | 3 s |
| Members caído | Reservar falla cerrado (503); búsqueda normal; login no disponible | 200 ×39 | 503 DEPENDENCIA_NO_DISPONIBLE ×12 | 502 DEPENDENCIA_NO_DISPONIBLE ×3, 504 TIMEOUT ×1 | 0 s | 3 s |

## OpenSearch caído (`gym-opensearch-1`)

- Detenido: 03:51:18; iniciado: 03:52:22 (hora UTC).
- Respuestas durante la caída: map[busqueda:map[200:38] cancelacion:map[200:16] login:map[200:5] reserva:map[201:16]].
- Origen de las búsquedas exitosas: map[cache:2 cache_vencida:35 fuente:1]; sin marca «ya reservada»: 0.
- Alerta `ServicioCaido` pending (instance=api-gateway:8080,job=api-gateway,service=booking-indexer) a los 10 s.
- Alerta `ServicioDegradado` pending (instance=api-gateway:8080,job=api-gateway,service=booking-service-1) a los 10 s.
- Alerta `ServicioDegradado` pending (instance=api-gateway:8080,job=api-gateway,service=booking-service-2) a los 10 s.
- Alerta `ServicioCaido` firing (instance=api-gateway:8080,job=api-gateway,service=booking-indexer) a los 30 s.
- Alerta `ServicioDegradado` firing (instance=api-gateway:8080,job=api-gateway,service=booking-service-1) a los 30 s.
- Alerta `ServicioDegradado` firing (instance=api-gateway:8080,job=api-gateway,service=booking-service-2) a los 30 s.
- Outbox de Booking pendiente: máximo 2, al final 0.
- Líneas WARN/ERROR en logs: map[api-gateway:93 booking-indexer:2].
- Diseño: ADR-014, ADR-007 v2, D-95.

## PostgreSQL caído (`gym-postgres-1`)

- Detenido: 03:53:36; iniciado: 03:54:37 (hora UTC).
- Respuestas durante la caída: map[busqueda:map[200:65] login:map[503 DEPENDENCIA_NO_DISPONIBLE:2 504 TIMEOUT:2] reserva:map[503 DEPENDENCIA_NO_DISPONIBLE:15]].
- Origen de las búsquedas exitosas: map[cache:59 fuente:6]; sin marca «ya reservada»: 49.
- Alerta `ServicioCaido` pending (instance=api-gateway:8080,job=api-gateway,service=members-service) a los 12 s.
- Alerta `ServicioCaido` pending (instance=api-gateway:8080,job=api-gateway,service=benefits-service) a los 12 s.
- Alerta `ServicioDegradado` pending (instance=api-gateway:8080,job=api-gateway,service=booking-service-1) a los 12 s.
- Alerta `ServicioDegradado` pending (instance=api-gateway:8080,job=api-gateway,service=booking-service-2) a los 12 s.
- Alerta `ServicioDegradado` pending (instance=api-gateway:8080,job=api-gateway,service=booking-indexer) a los 12 s.
- Alerta `ServicioCaido` firing (instance=api-gateway:8080,job=api-gateway,service=members-service) a los 32 s.
- Alerta `ServicioCaido` firing (instance=api-gateway:8080,job=api-gateway,service=benefits-service) a los 32 s.
- Alerta `ServicioDegradado` firing (instance=api-gateway:8080,job=api-gateway,service=booking-service-1) a los 32 s.
- Alerta `ServicioDegradado` firing (instance=api-gateway:8080,job=api-gateway,service=booking-service-2) a los 32 s.
- Alerta `ServicioDegradado` firing (instance=api-gateway:8080,job=api-gateway,service=booking-indexer) a los 32 s.
- Outbox de Booking pendiente: máximo 1, al final 1.
- Líneas WARN/ERROR en logs: map[api-gateway:55 booking-service:22 booking-service-2:28 members-service:32].
- Diseño: ADR-007 v2, D-95, ADR-005 v2.

## RabbitMQ caído (`gym-rabbitmq-1`)

- Detenido: 03:55:10; iniciado: 03:56:14 (hora UTC).
- Respuestas durante la caída: map[busqueda:map[200:129] cancelacion:map[200:16] login:map[200:4] reserva:map[201:16]].
- Origen de las búsquedas exitosas: map[cache:68 fuente:61]; sin marca «ya reservada»: 0.
- Alerta `ServicioCaido` pending (instance=api-gateway:8080,job=api-gateway,service=notification-worker) a los 9 s.
- Alerta `ServicioDegradado` pending (instance=api-gateway:8080,job=api-gateway,service=booking-indexer) a los 9 s.
- Alerta `ServicioDegradado` pending (instance=api-gateway:8080,job=api-gateway,service=benefits-service) a los 9 s.
- Alerta `ServicioDegradado` pending (instance=api-gateway:8080,job=api-gateway,service=members-service) a los 9 s.
- Alerta `ServicioDegradado` pending (instance=api-gateway:8080,job=api-gateway,service=booking-service-1) a los 9 s.
- Alerta `ServicioDegradado` pending (instance=api-gateway:8080,job=api-gateway,service=booking-service-2) a los 9 s.
- Alerta `MetricasNoDisponibles` pending (instance=rabbitmq:15692,job=rabbitmq) a los 19 s.
- Alerta `ServicioCaido` firing (instance=api-gateway:8080,job=api-gateway,service=notification-worker) a los 29 s.
- Alerta `ServicioDegradado` firing (instance=api-gateway:8080,job=api-gateway,service=booking-service-1) a los 29 s.
- Alerta `ServicioDegradado` firing (instance=api-gateway:8080,job=api-gateway,service=booking-service-2) a los 29 s.
- Alerta `ServicioDegradado` firing (instance=api-gateway:8080,job=api-gateway,service=booking-indexer) a los 29 s.
- Alerta `ServicioDegradado` firing (instance=api-gateway:8080,job=api-gateway,service=benefits-service) a los 29 s.
- Alerta `ServicioDegradado` firing (instance=api-gateway:8080,job=api-gateway,service=members-service) a los 29 s.
- Alerta `MetricasNoDisponibles` firing (instance=rabbitmq:15692,job=rabbitmq) a los 79 s.
- Outbox de Booking pendiente: máximo 42, al final 0.
- Líneas WARN/ERROR en logs: map[api-gateway:64 benefits-service:70 booking-indexer:33 booking-service:140 booking-service-2:140 members-service:60].
- Diseño: ADR-005 v2, regla 6.

## Redis caído (`gym-redis-1`)

- Detenido: 03:57:08; iniciado: 03:58:09 (hora UTC).
- Respuestas durante la caída: map[busqueda:map[200:124] cancelacion:map[200:16] login:map[503 DEPENDENCIA_NO_DISPONIBLE:4] reserva:map[201:16]].
- Origen de las búsquedas exitosas: map[fuente:124]; sin marca «ya reservada»: 0.
- Alerta `ServicioDegradado` pending (instance=api-gateway:8080,job=api-gateway,service=booking-service-1) a los 11 s.
- Alerta `ServicioDegradado` pending (instance=api-gateway:8080,job=api-gateway,service=booking-service-2) a los 11 s.
- Alerta `ServicioDegradado` firing (instance=api-gateway:8080,job=api-gateway,service=booking-service-2) a los 31 s.
- Alerta `ServicioDegradado` firing (instance=api-gateway:8080,job=api-gateway,service=booking-service-1) a los 31 s.
- Outbox de Booking pendiente: máximo 2, al final 0.
- Líneas WARN/ERROR en logs: map[api-gateway:166].
- Diseño: D-91, ADR-007.

## Members caído (`gym-members-service-1`)

- Detenido: 03:58:42; iniciado: 03:59:43 (hora UTC).
- Respuestas durante la caída: map[busqueda:map[200:39] login:map[502 DEPENDENCIA_NO_DISPONIBLE:3 504 TIMEOUT:1] reserva:map[503 DEPENDENCIA_NO_DISPONIBLE:12]].
- Origen de las búsquedas exitosas: map[cache:31 fuente:8]; sin marca «ya reservada»: 0.
- Alerta `ServicioCaido` pending (instance=api-gateway:8080,job=api-gateway,service=members-service) a los 6 s.
- Alerta `ServicioCaido` firing (instance=api-gateway:8080,job=api-gateway,service=members-service) a los 26 s.
- Outbox de Booking pendiente: máximo 2, al final 0.
- Líneas WARN/ERROR en logs: map[api-gateway:53 booking-service:7 booking-service-2:5].
- Diseño: ADR-005 v2, ADR-007.
