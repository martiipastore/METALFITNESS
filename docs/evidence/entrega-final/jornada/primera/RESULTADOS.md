# Jornada de fallas controladas (entrega final)

Generado por `tests/resilience/jornada.go` el 2026-11-16T03:36:07Z. Datos completos en los JSON de esta carpeta.

Tráfico de fondo: un alumno busca cada 0,5 s, reserva y cancela cada 4 s e inicia sesión cada 15 s, a través del gateway. Cada falla detiene un contenedor con `docker stop` y lo vuelve a iniciar.

| Falla | Esperado | Búsqueda | Reserva | Login | Estado degradado | Recuperación |
|---|---|---|---|---|---|---|
| OpenSearch caído | Búsqueda servida desde la caché (fresca o vencida); reservas normales; clase.actualizada espera en RabbitMQ | 200 ×36 | 201 ×16 | 200 ×4 | 2 s | 54 s |
| PostgreSQL caído | Búsqueda con la copia de Redis y marcas_disponibles=false; reservar y login fallan cerrados con 503 | 200 ×18, 503 DEPENDENCIA_NO_DISPONIBLE ×73 | 503 DEPENDENCIA_NO_DISPONIBLE ×16 | 500 ERROR_INTERNO ×2, 504 TIMEOUT ×3 | 2 s | 2 s |
| RabbitMQ caído | Reservas y búsqueda normales; el outbox acumula y se vacía al volver el broker | 200 ×127 | 201 ×16 | 200 ×4 | 1 s | 27 s |
| Redis caído | Login falla cerrado (503); el resto sigue sin límite de tráfico; la búsqueda va a la fuente | 200 ×123 | 201 ×16 | 503 DEPENDENCIA_NO_DISPONIBLE ×4 | 2 s | 2 s |
| Members caído | Reservar falla cerrado (503); búsqueda normal; login no disponible | 200 ×37 | 503 DEPENDENCIA_NO_DISPONIBLE ×12 | 502 DEPENDENCIA_NO_DISPONIBLE ×2, 504 TIMEOUT ×2 | 1 s | 4 s |

## OpenSearch caído (`gym-opensearch-1`)

- Detenido: 03:26:51; iniciado: 03:27:54 (hora UTC).
- Respuestas durante la caída: map[busqueda:map[200:36] cancelacion:map[200:16] login:map[200:4] reserva:map[201:16]].
- Origen de las búsquedas exitosas: map[cache:1 cache_vencida:35]; sin marca «ya reservada»: 0.
- Alerta `ServicioCaido` pending (instance=api-gateway:8080,job=api-gateway,service=booking-indexer) a los 15 s.
- Alerta `ServicioCaido` firing (instance=api-gateway:8080,job=api-gateway,service=booking-indexer) a los 35 s.
- Outbox de Booking pendiente: máximo 2, al final 0.
- Líneas WARN/ERROR en logs: map[api-gateway:108 booking-indexer:3].
- Diseño: ADR-014, ADR-007 v2, D-95.

## PostgreSQL caído (`gym-postgres-1`)

- Detenido: 03:29:19; iniciado: 03:30:23 (hora UTC).
- Respuestas durante la caída: map[busqueda:map[200:18 503 DEPENDENCIA_NO_DISPONIBLE:73] login:map[500 ERROR_INTERNO:2 504 TIMEOUT:3] reserva:map[503 DEPENDENCIA_NO_DISPONIBLE:16]].
- Origen de las búsquedas exitosas: map[cache:15 fuente:3]; sin marca «ya reservada»: 12.
- Alerta `ServicioCaido` pending (instance=api-gateway:8080,job=api-gateway,service=booking-service-1) a los 17 s.
- Alerta `ServicioCaido` pending (instance=api-gateway:8080,job=api-gateway,service=booking-service-2) a los 17 s.
- Alerta `ServicioCaido` pending (instance=api-gateway:8080,job=api-gateway,service=benefits-service) a los 17 s.
- Alerta `ServicioCaido` pending (instance=api-gateway:8080,job=api-gateway,service=members-service) a los 17 s.
- Alerta `ServicioCaido` firing (instance=api-gateway:8080,job=api-gateway,service=booking-service-1) a los 37 s.
- Alerta `ServicioCaido` firing (instance=api-gateway:8080,job=api-gateway,service=booking-service-2) a los 37 s.
- Alerta `ServicioCaido` firing (instance=api-gateway:8080,job=api-gateway,service=benefits-service) a los 37 s.
- Alerta `ServicioCaido` firing (instance=api-gateway:8080,job=api-gateway,service=members-service) a los 37 s.
- Outbox de Booking pendiente: máximo 2, al final 0.
- Líneas WARN/ERROR en logs: map[api-gateway:265 booking-service:23 booking-service-2:23 members-service:36].
- Diseño: ADR-007 v2, D-95, ADR-005 v2.

## RabbitMQ caído (`gym-rabbitmq-1`)

- Detenido: 03:30:56; iniciado: 03:31:59 (hora UTC).
- Respuestas durante la caída: map[busqueda:map[200:127] cancelacion:map[200:16] login:map[200:4] reserva:map[201:16]].
- Origen de las búsquedas exitosas: map[cache:71 fuente:56]; sin marca «ya reservada»: 0.
- Alerta `ServicioCaido` pending (instance=api-gateway:8080,job=api-gateway,service=notification-worker) a los 20 s.
- Alerta `MetricasNoDisponibles` pending (instance=rabbitmq:15692,job=rabbitmq) a los 20 s.
- Alerta `ServicioCaido` firing (instance=api-gateway:8080,job=api-gateway,service=notification-worker) a los 40 s.
- Alerta `MetricasNoDisponibles` firing (instance=rabbitmq:15692,job=rabbitmq) a los 80 s.
- Outbox de Booking pendiente: máximo 46, al final 0.
- Líneas WARN/ERROR en logs: map[api-gateway:71 benefits-service:80 booking-indexer:41 booking-service:155 booking-service-2:155 members-service:69].
- Diseño: ADR-005 v2, regla 6.

## Redis caído (`gym-redis-1`)

- Detenido: 03:32:57; iniciado: 03:33:58 (hora UTC).
- Respuestas durante la caída: map[busqueda:map[200:123] cancelacion:map[200:16] login:map[503 DEPENDENCIA_NO_DISPONIBLE:4] reserva:map[201:16]].
- Origen de las búsquedas exitosas: map[cache:1 fuente:122]; sin marca «ya reservada»: 0.
- Outbox de Booking pendiente: máximo 2, al final 0.
- Líneas WARN/ERROR en logs: map[api-gateway:162].
- Diseño: D-91, ADR-007.

## Members caído (`gym-members-service-1`)

- Detenido: 03:34:30; iniciado: 03:35:31 (hora UTC).
- Respuestas durante la caída: map[busqueda:map[200:37] login:map[502 DEPENDENCIA_NO_DISPONIBLE:2 504 TIMEOUT:2] reserva:map[503 DEPENDENCIA_NO_DISPONIBLE:12]].
- Origen de las búsquedas exitosas: map[cache:27 fuente:10]; sin marca «ya reservada»: 0.
- Alerta `ServicioCaido` pending (instance=api-gateway:8080,job=api-gateway,service=members-service) a los 16 s.
- Alerta `ServicioCaido` firing (instance=api-gateway:8080,job=api-gateway,service=members-service) a los 36 s.
- Outbox de Booking pendiente: máximo 2, al final 1.
- Líneas WARN/ERROR en logs: map[api-gateway:54 booking-service:8 booking-service-2:4].
- Diseño: ADR-005 v2, ADR-007.
