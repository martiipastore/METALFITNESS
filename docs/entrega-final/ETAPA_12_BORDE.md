# Etapa 12 — Entrada única, control de tráfico y balanceo

**Estado al 11/11/2026:**
- **Parte A, control de tráfico:** implementada y validada localmente.
- **Parte B, balanceo de Booking:** implementada y verificada el 15/11/2026 en el gateway (ADR-012 v3, D-96)
  grupal.

El enunciado pide el «punto de entrada único con autenticación, direccionamiento y
**control de tráfico**» para la presentación grupal. El balanceo con evidencia de
distribución se evalúa en la defensa individual (§2.3 y «Defensa individual»).

Fuentes: [ARCHITECTURE §5.1 a §5.3](../ARCHITECTURE.md), D-79, D-91,
[ADR-007](../adr/ADR-007-cache-autorizacion-disponibilidad.md). Para la parte B: D-46,
D-51 y [ADR-012 v2](../adr/ADR-012-v2-estabilidad-health-checks.md).

## Parte A — Control de tráfico en el gateway

### Qué se agregó

Paquete `gateway/internal/ratelimit`: **token bucket atómico en Redis**. Un script Lua
recarga las fichas según el tiempo transcurrido y consume una en una sola operación.
Usa el reloj del servidor Redis, así que varias instancias del gateway comparten el
mismo contador sin carreras. Claves `gw:rl:<política>:<id>` con vencimiento automático.

| Política | Rutas | Clave | Límite | Si Redis no responde |
|---|---|---|---|---|
| `login` | `POST /api/v1/auth/login`, `activate` y `resend-activation` | IP de la conexión | 5 por minuto (ráfaga 5) | **Falla cerrado:** `503 DEPENDENCIA_NO_DISPONIBLE` con `Retry-After` |
| `user` | Resto de `/api/v1/**` autenticado | `sub` del JWT verificado | 10/s, ráfaga 20 | Falla abierto |
| `partner` | `/partner-api/v1` y `/v2` | SHA-256 de la API key (IP si falta) | 20/s, ráfaga 40 | Falla abierto |

Al excederse, la respuesta es `429` con `Retry-After` en segundos y problema RFC 7807:
`LIMITE_EXCEDIDO` en la API propia y `RATE_LIMITED` en el formato del contrato de
partners v2.

**Orden en el gateway (§5.1):** limpieza de identidad → autenticación → **límite** →
routing.
- El límite de usuario usa el `sub` verificado, nunca un `X-User-Id` enviado por el
  cliente.
- Un `401` se responde antes y no consume fichas.
- `/api/v1/status`, `/health/*` y `/metrics` no se limitan.
- La IP se toma de la conexión, no de `X-Forwarded-For`, que el cliente podría falsear
  para evadir el límite.

**Observabilidad.**
- `GET /metrics` del gateway expone `gateway_ratelimit_allowed_total{policy}`,
  `gateway_ratelimit_rejected_total{policy}` y `ratelimit_redis_errors_total`.
- Redis es una dependencia **parcial** de `/health/ready` (ADR-007): si cae, el gateway
  queda `degraded` sin salir de servicio.

**Configuración.**
- Prendido por defecto en Docker Compose (`GATEWAY_RATE_LIMIT_ENABLED=true`): Redis
  está siempre en el stack.
- Apagado si la variable no existe, como en Vercel, que no tiene Redis (D-79).
- Límites ajustables con `GATEWAY_RATE_LIMIT_*`.

### Pruebas

| Prueba | Resultado |
|---|---|
| Unitarias del gateway (`internal/server/ratelimit_test.go`, store en memoria) | Aprobadas: login 5 por IP con el sexto en 429 y `Retry-After`; usuario identificado por el JWT y no por `X-User-Id` falsificado; baldes independientes por usuario y por política; partners por hash de key con `RATE_LIMITED` y sin la key en claro; con Redis caído, login 503 y el resto pasa; status, health y 401 no consumen fichas; métricas |
| Integración con Redis 7 real (`internal/ratelimit`, testcontainers) | Aprobadas: ráfaga, rechazo con espera, recarga en el tiempo, 5/min = una ficha cada 12 s, claves independientes, vencimiento y **atomicidad con 100 solicitudes concurrentes** (exactamente 20 permitidas con ráfaga 20) |
| `go vet` y tests de config, proxy y server | Aprobados |

Los tests de `proxy` y `server` se ejecutaron en un contenedor `golang:1.22`: el control
de aplicaciones de Windows bloqueó los ejecutables de test en la carpeta temporal. El
test con Redis real corrió en Windows.

### Validación contra el stack real (11/11/2026)

| Escenario | Resultado |
|---|---|
| 6 logins seguidos desde la misma IP | 5 llegan a Members (401 por credenciales) y el sexto recibe **429 `LIMITE_EXCEDIDO`, `Retry-After: 11`** |
| Login después de esperar | 200 |
| 30 solicitudes simultáneas de un administrador | 23 aceptadas (ráfaga 20 más recarga) y **7 con 429** |
| 120 solicitudes de partner con la misma key en 1,6 s | 70 llegan a Benefits y **50 con 429** |
| Redis detenido | Login **503 `DEPENDENCIA_NO_DISPONIBLE`, `Retry-After: 1`**; un usuario autenticado recibe 200; `/health/ready` = `degraded`; `ratelimit_redis_errors_total` = 2 |
| Redis recuperado | Login 200 y `/health/ready` = `ready` sin reiniciar el gateway |

### Limitaciones

- **Todos los navegadores del Docker local comparten la IP de la red de Docker.** Las 5
  entradas por minuto del login se comparten entre todos los que prueban en la misma
  máquina. En una demostración con varios roles conviene no ingresar más de 5 veces por
  minuto, o subir `GATEWAY_RATE_LIMIT_LOGIN_PER_MINUTE` en `.env`.
- En la nube el límite general queda desactivado (D-79). El login sigue protegido por la
  cuota en PostgreSQL de Members y los partners por la cuota por integración de Benefits.

## Parte B — Balanceo de Booking (implementada el 15/11/2026)

**Qué pide el enunciado (§2.3):** dos o más instancias de un servicio; reconocer una instancia no disponible; comprobar la distribución y el estado de las instancias con la observabilidad.

**Cambio de herramienta.** La documentación de Traefik v3.7 confirmó que su chequeo activo no tiene umbrales de fallos ni de éxitos consecutivos, así que no puede cumplir D-51 (3/2). El equipo eligió balancear en el gateway con la política aprobada sin cambios. Ver [ADR-012 v3](../adr/ADR-012-v3-balanceo-en-gateway.md) y D-96.

**Qué se hizo:**

- `booking-service` y `booking-service-2` en Compose, con la misma configuración (diagnóstico en `127.0.0.1:8082` y `127.0.0.1:8087`).
- `gateway/internal/balance`: round-robin con igual peso y sin afinidad; chequeo de `/health/ready` cada 5 s con timeout de 2 s; sale tras 3 fallos seguidos y vuelve tras 2 éxitos seguidos.
- Sin instancias elegibles, `503` con `Retry-After: 5` (`502 UNAVAILABLE` para partners).
- Observabilidad: métricas `gateway_balancer_*` por instancia, sección «Balanceo de Booking» en Grafana, `booking-service-1` y `booking-service-2` en `/api/v1/status` y en la alerta `ServicioCaido`, y logs de retiro y reincorporación. Prometheus lee las métricas de las dos instancias.

**Evidencia (15/11/2026, stack local):**

| Prueba | Resultado |
|---|---|
| 100 solicitudes con las dos instancias sanas | 100 × 200; 50 a cada instancia; Prometheus registra lo mismo |
| `docker kill` de `booking-service-2` a las 20:26:03,7 UTC, con una solicitud cada 200 ms | Retirada a las 20:26:15,1 (11,5 s). 296 de 300 solicitudes con 200; 3 × 502 y 1 × 504, todas antes del retiro |
| Se reinicia a las 20:27:28,6 | Reincorporada a las 20:27:39,5 (10,9 s), sin errores |

Las fallas durante la detección son el costo aceptado de no retirar por un fallo aislado (ADR-012 v2, consecuencias).

**Cómo demostrarlo:**

```bash
docker kill gym-booking-service-2-1                    # caída abrupta
# Grafana, «Balanceo de Booking»: la instancia 2 pasa a «Fuera» en 10 a 15 s
# y la instancia 1 absorbe todo el tráfico
docker compose --env-file .env -f deploy/docker-compose.yml start booking-service-2
# vuelve a «En servicio» tras dos chequeos exitosos
```

Tests: `gateway/internal/balance/balance_test.go` y `gateway/internal/proxy/balance_test.go`.
