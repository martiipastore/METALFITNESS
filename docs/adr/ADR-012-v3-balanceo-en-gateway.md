# ADR-012 v3 — Balanceo de Booking en el gateway con la política 3/2

| Campo | Valor |
|---|---|
| **Estado** | Aceptado por el equipo el 15/11/2026; implementado y verificado |
| **Fecha** | 2026-11-15 |
| **Decisión del TP** | D12 |
| **Reemplaza** | [ADR-012 v2](ADR-012-v2-estabilidad-health-checks.md) solo en la herramienta (Traefik); conserva su política |
| **Relacionado con** | D-46, D-51, D-96; enunciado §2.3; teórico, clase 6 §2 (pp. 85–87) |

## Contexto

ADR-012 v2 aprobó dos instancias de Booking con igual peso, sin afinidad, chequeo de `/health/ready` cada 5 s con timeout de 2 s, retiro tras **3 fallos consecutivos** y reincorporación tras **2 éxitos consecutivos** (D-51, para evitar flapping, teórico p. 87). También exigió verificar antes de implementar que Traefik pudiera expresar esa política y, si no podía, presentar al equipo una alternativa basada en el mecanismo de clase.

La documentación de Traefik v3.7 (configuración de servicios HTTP, `healthCheck`) no ofrece umbrales de fallos ni de éxitos consecutivos para el chequeo activo: retira una instancia al primer chequeo fallido y la reincorpora al primero exitoso. El chequeo pasivo `maxFailedAttempts` cuenta errores del tráfico real, no del chequeo de salud, y no tiene condición de éxitos para volver.

## Alternativas consideradas

| Alternativa | Ventaja | Desventaja |
|---|---|---|
| Traefik retirando al primer fallo | Herramienta ya elegida | Reemplaza D-51 y acepta el flapping que se quería evitar |
| Traefik con chequeo activo y pasivo (`maxFailedAttempts=3`) | Se acerca a «3 fallos» | No cumple «2 éxitos para volver»; dos mecanismos difíciles de explicar |
| **Balanceo en el gateway (elegida)** | Cumple D-46 y D-51 tal cual; no agrega componentes; el gateway ya chequea servicios y expone métricas | El gateway concentra una responsabilidad más; el balanceo es código propio |

El teórico (clase 6 §2.5) contempla un balanceador integrado al punto de entrada o a una librería cliente, no solo un componente de red separado. La clase distingue routing semántico de balanceo (§3.1): acá el gateway hace las dos cosas, pero en piezas separadas (`internal/proxy` y `internal/balance`).

## Decisión

`gateway/internal/balance` reparte las solicitudes de un servicio entre sus instancias:

- **Round-robin** entre las instancias elegibles, con igual peso y sin afinidad (D-46).
- **Chequeo activo** `GET /health/ready` cada 5 s con timeout de 2 s. Cuenta como éxito un 200, sea `ready` o `degraded` (ADR-007).
- **Política 3/2** (D-51): sale tras 3 fallos consecutivos y vuelve tras 2 éxitos consecutivos; un resultado contrario reinicia la cuenta.
- **Sin instancias elegibles**, el gateway responde enseguida `503 DEPENDENCIA_NO_DISPONIBLE` con `Retry-After: 5`, en lugar de esperar el timeout contra un destino caído. La API de partners recibe `502 UNAVAILABLE`, el error de su contrato (D-44).
- **Sin reintentos** de solicitudes en otra instancia, igual que antes: el gateway no reintenta escrituras (ADR-005 v2, ADR-010 v2).
- **Configuración:** varias URLs separadas por coma en `GATEWAY_<SERVICIO>_URL` son instancias del mismo servicio. Compose define `booking-service` y `booking-service-2`.
- **Observabilidad:** `/metrics` del gateway expone `gateway_balancer_instance_up`, `gateway_balancer_requests_total`, `gateway_balancer_removals_total` y `gateway_balancer_restorations_total` por instancia; el tablero de Grafana tiene la sección «Balanceo de Booking». `/api/v1/status` lista `booking-service-1` y `booking-service-2`, y la alerta `ServicioCaido` avisa si una no responde. Los logs registran cada retiro y reincorporación.

Las dos instancias comparten base, Redis y OpenSearch. El relay del outbox y el cierre automático corren en ambas sin duplicar trabajo (`FOR UPDATE SKIP LOCKED`) y las migraciones se serializan con un lock de PostgreSQL.

## Consecuencias

- Positiva: la política aprobada se cumple sin componentes nuevos, y cada parámetro se prueba con tests.
- Negativa: mientras se detecta una caída (unos 10 a 15 s), parte de las solicitudes a la instancia caída fallan con 502/504. Es el costo aceptado de no retirar por un fallo aislado.
- Negativa: el gateway es una instancia única (L-06); balancear el propio gateway queda fuera del alcance.
- Traefik deja de estar en el diseño del balanceo. Las menciones de Traefik como borde HTTPS en una VM (D-34, ADR-006) ya estaban superadas por el despliegue en Vercel (D-75, ADR-021).

## Verificación (15/11/2026)

- **Tests:** `gateway/internal/balance/balance_test.go` cubre todos los casos que pedía ADR-012 v2: uno y dos fallos no retiran y el tercero sí; un éxito intermedio reinicia la cuenta; un solo éxito no reincorpora y dos sí; un fallo intermedio reinicia la cuenta de éxitos; reparto parejo; respuesta lenta como fallo. `gateway/internal/proxy/balance_test.go` prueba el reparto y el rechazo sin instancias contra servidores reales.
- **Stack local:** 100 solicitudes con las dos instancias sanas se repartieron 50/50, y Prometheus registró lo mismo.
- **Caída provocada:** con una solicitud cada 200 ms, `docker kill` de `booking-service-2` a las 20:26:03,7 UTC; el gateway la retiró a las 20:26:15,1 (11,5 s) y fallaron 4 de 300 solicitudes, todas en esa ventana. La instancia se reinició a las 20:27:28,6 y volvió al balanceo a las 20:27:39,5 (10,9 s). Ver `docs/entrega-final/ETAPA_12_BORDE.md`, parte B.
