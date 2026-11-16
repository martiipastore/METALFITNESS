# Postmortem — Jornada de fallas controladas (entrega final)

| Campo | Valor |
|---|---|
| Fecha | 15/11/2026, de 21:14 a 22:00 (hora de Argentina) |
| Entorno | Stack local completo (`make demo`): gateway, dos instancias de Booking, Members, Benefits, Training, indexer, notification-worker, PostgreSQL, MongoDB, Redis, RabbitMQ, OpenSearch, Prometheus y Grafana |
| Tipo | Ensayo planificado, sin usuarios reales. No es un incidente productivo ni una medición de SLA |
| Herramienta | `go run tests/resilience/jornada.go` (reproducible; restaura los contenedores aunque falle) |
| Relación | Enunciado §2.9 y §3; D10 (resiliencia), D11 (observabilidad), D12 (balanceo); ADR-005 v2, ADR-007 v2, ADR-010 v2, ADR-012 v3; D-100 |
| Evidencia | [`docs/evidence/entrega-final/jornada/`](evidence/entrega-final/jornada/RESULTADOS.md): `ensayo/`, `primera/` y la corrida de verificación |
| Pendiente | Caída del proveedor externo (MELT): se suma cuando el grupo proveedor entregue su versión corregida |

## 1. Resumen

Se detuvo una por vez cada dependencia propia del sistema mientras un alumno usaba la aplicación. El objetivo era comprobar que cada falla quedara contenida donde el diseño lo dice.

La jornada encontró **cinco problemas** que los tests no mostraban. Se corrigieron y una corrida de verificación confirmó el comportamiento esperado. El más grave: con PostgreSQL caído, el balanceador retiraba las dos instancias de Booking y la búsqueda de clases, que la cátedra pidió que funcionara sin base, respondía 503.

En la verificación, ninguna falla afectó funciones que no dependen de la pieza caída. Las que sí dependen fallaron cerradas con 503, sin datos incorrectos, y el sistema volvió solo a `ready` entre 2 y 44 segundos después de reiniciar el contenedor.

## 2. Cómo se hizo

**Tráfico de fondo**, a través del gateway y con un alumno sintético con membresía:

- búsqueda de clases cada 0,5 s, con texto parcial («zum», «fun», «pil» o sin texto);
- reserva de una clase futura y su cancelación cada 4 s;
- inicio de sesión cada 15 s.

**Observación continua:**

- `/api/v1/status` cada 2 s;
- alertas de Prometheus cada 5 s;
- eventos pendientes en el outbox de Booking cada 5 s;
- conteo de líneas WARN/ERROR en los logs JSON de cada servicio.

**Fallas:** `docker stop` del contenedor durante 60 s y `docker start`. Entre una falla y la siguiente, se espera a que el estado vuelva a `ready` y se observan 30 s más.

Se hicieron tres corridas:

| Corrida | Hora | Alcance | Resultado |
|---|---|---|---|
| Ensayo | 21:14 | Solo OpenSearch, ventanas de 20 s | Encontró los problemas 1 y 2 |
| Primera jornada | 21:26 | Las cinco fallas | Confirmó 1 y 2; encontró 3, 4 y 5 |
| Verificación | 21:51 | Las cinco fallas, con todas las correcciones | Comportamiento esperado en las cinco |

## 3. Impacto (corrida de verificación)

Cantidad de respuestas durante los 60 s de cada caída, por operación.

| Falla | Búsqueda | Reserva | Login | Qué ve el alumno |
|---|---|---|---|---|
| **OpenSearch** | 38 × 200 (35 con copia vencida) | 16 × 201 | 5 × 200 | Clases con el aviso «datos de las HH:MM»; reserva normal |
| **PostgreSQL** | 65 × 200 (49 sin la marca «ya reservada») | 15 × 503 | 2 × 503, 2 × 504 | Clases sin la marca; «no se pudo reservar, reintentá»; no puede iniciar sesión |
| **RabbitMQ** | 129 × 200 | 16 × 201 | 4 × 200 | Nada. Puntos, correos e índice llegan con demora |
| **Redis** | 124 × 200 (todas desde la fuente) | 16 × 201 | 4 × 503 | No puede iniciar sesión; el resto normal |
| **Members** | 39 × 200 | 12 × 503 | 3 × 502, 1 × 504 | «No se pudo reservar»; no puede iniciar sesión; búsqueda normal |

Ninguna reserva se confirmó sin validar la membresía. Ningún evento se perdió: el outbox de Booking llegó a 42 pendientes con RabbitMQ caído y volvió a 0 al recuperarse. Sin RabbitMQ siguen pendientes, en sus colas durables, el índice de búsqueda, los puntos y las notificaciones.

## 4. Línea temporal (corrida de verificación, hora de Argentina)

| Hora | Hecho | Detección |
|---|---|---|
| 21:50:48 | Inicio del tráfico de fondo | — |
| 21:51:18 | **OpenSearch detenido** | Estado `not_ready` (indexer) a los 2 s; `ServicioCaido` (indexer) y `ServicioDegradado` (Booking ×2) a los 30 s |
| 21:52:22 | OpenSearch iniciado | Estado `ready` a las 21:53:06 (44 s: arranque de OpenSearch) |
| 21:53:36 | **PostgreSQL detenido** | Estado degradado a los 0,1 s; `ServicioCaido` (Members, Benefits) y `ServicioDegradado` (Booking ×2, indexer) a los 32 s |
| 21:54:37 | PostgreSQL iniciado | `ready` a las 21:54:39 (2 s) |
| 21:55:10 | **RabbitMQ detenido** | Degradado a los 0,6 s; `ServicioCaido` (notification-worker) y `ServicioDegradado` (cinco servicios) a los 29 s; `MetricasNoDisponibles` (RabbitMQ) a los 79 s |
| 21:56:14 | RabbitMQ iniciado | `ready` a las 21:56:37 (23 s); outbox vaciado |
| 21:57:08 | **Redis detenido** | Degradado a los 0,8 s; `ServicioDegradado` (Booking ×2) a los 31 s |
| 21:58:09 | Redis iniciado | `ready` a las 21:58:12 (3 s) |
| 21:58:42 | **Members detenido** | Degradado a los 0,2 s; `ServicioCaido` (Members) a los 26 s |
| 21:59:43 | Members iniciado | `ready` a las 21:59:46 (3 s) |

Las alertas tienen un `for` de 20 s, para no avisar por un parpadeo; por eso saltan entre 26 y 32 s después de la caída. El estado agregado lo refleja en menos de 2 s.

## 5. Detección y diagnóstico

Cada falla se identificó sin entrar a los contenedores:

- **`/api/v1/status`** del gateway muestra qué servicio dejó de estar `ready` y si está `degraded` o `not_ready`.
- **Grafana** («Metal Fitness — recorrido y salud») muestra el estado de cada servicio, las respuestas 5xx por clase, el reparto del balanceo y los mensajes por cola.
- **Las alertas de Prometheus** avisan del servicio afectado: `ServicioCaido`, `ServicioDegradado` (nueva), `MetricasNoDisponibles` y `MensajesEnDLQ`.
- **Los logs JSON** llevan `correlation_id`. Por ejemplo, el log `instancia retirada del balanceo` del gateway explicó el problema 3, y los `servicio sin instancias disponibles` permitieron contar las solicitudes afectadas.

Durante la jornada todavía no había trazas de Members ni de Booking: una reserva se reconstruía con logs por `correlation_id` y métricas. Se agregaron después, como acción de este postmortem (D-101, [TRAZAS.md](entrega-final/TRAZAS.md)).

## 6. Problemas encontrados, causa y respuesta

| # | Corrida | Síntoma | Causa | Corrección (D-100) |
|---|---|---|---|---|
| 1 | Ensayo | Con OpenSearch caído, 11 de 13 búsquedas respondieron 503 | La copia vencida de ADR-007 v2 se buscaba solo en la generación vigente de la caché. Con PostgreSQL arriba siguen las reservas y cada una cambia la generación, así que la copia quedaba inalcanzable | Booking guarda además la **última copia** de cada búsqueda, fuera de las generaciones, y la sirve como `cache_vencida` solo si la fuente falla. Test unitario y de integración con Redis real |
| 2 | Ensayo | Con OpenSearch caído, las dos instancias de Booking aparecían `unreachable` de a ratos | Cada servicio esperaba hasta 2 s por dependencia en `/health/ready`, y el gateway y el balanceador esperaban también 2 s en total. La respuesta llegaba en el límite. Tres seguidas habrían retirado la instancia por una dependencia **parcial** | `HEALTH_CHECK_TIMEOUT` de 2 s a **1 s** en todos los servicios: quien llama espera más que el llamado (presupuesto de tiempos, teórico clase 7) |
| 3 | Primera | Con PostgreSQL caído, 73 de 91 búsquedas respondieron 503: **la corrección de la cátedra dejaba de funcionar** | PostgreSQL era dependencia crítica de Booking: `/health/ready` daba `not_ready`, el balanceador retiró las dos instancias a los 16 s y el gateway respondió «sin instancias» a todo. La corrección se había verificado el 14/11, antes de que existiera el balanceo (15/11) | Con la caché de búsquedas u OpenSearch activos, PostgreSQL pasa a ser **parcial** en Booking: la instancia informa `degraded` y sigue en el balanceo; reservar sigue fallando cerrado. Se descartó un «modo pánico» del balanceador porque cambiaba ADR-012 v3 |
| 4 | Primera | Con PostgreSQL caído, el login respondió **500 ERROR_INTERNO** | El manejo de errores de identidad en Members trataba todo error no clasificado como interno | Un error no clasificado responde **503** con `Retry-After`, como el resto de Members, sin exponer el detalle. Test |
| 5 | Primera | Con Redis caído **no saltó ninguna alerta**; solo se veía en el estado | Las alertas solo cubrían servicios caídos, no degradados | Alerta nueva **`ServicioDegradado`** (`gateway_upstream_degraded == 1` durante 20 s) |

Los cinco problemas comparten una causa de fondo. Cada pieza estaba probada por separado (caché, chequeos de salud, balanceo), pero no su interacción bajo una falla real. El problema 3 es una regresión: lo introdujo el balanceo del 15/11 y no lo detectó ningún test, porque ninguno combinaba balanceo, caché y base caída. Para eso sirve la jornada.

## 7. Lo que funcionó como estaba diseñado

- **Falla cerrada:** sin Members o sin PostgreSQL no se confirmó ninguna reserva; sin Redis no se aceptó ningún login (ADR-005 v2, D-91). La validación de membresía y el límite de login no se cachean (ADR-007).
- **Outbox:** con RabbitMQ caído, las reservas siguieron y los eventos esperaron en la base; al volver el broker se publicaron todos (regla 6).
- **Dependencias parciales:** con Redis caído, la búsqueda fue a la fuente y el tráfico de usuarios siguió sin control de tráfico, como decide D-91.
- **Recuperación automática:** ningún servicio necesitó reiniciarse a mano; todos volvieron a `ready` solos.

## 8. Acciones

| Acción | Estado |
|---|---|
| Problemas 1 a 5 (D-100) | Corregidos, con tests y verificados en la tercera corrida |
| Jornada reproducible (`tests/resilience/jornada.go`) para repetir antes de la defensa y después de cada cambio de infraestructura | Hecho |
| Trazas de Members y Booking y propagación por el outbox, para reconstruir una reserva durante una falla con una sola traza (L-26) | Hecho el 15/11/2026 (D-101): un ingreso por DNI y sus puntos se ven en una traza de gateway, Booking, Members, RabbitMQ y Benefits |
| Caída del proveedor externo (MELT) en la jornada | Pendiente de la corrección del grupo proveedor |
| Login con Members caído responde 502 o 504 según el momento | Aceptado: los dos indican que el servicio no respondió. Se documenta en lugar de unificarlo en el gateway |
| Healthcheck de MongoDB con `mongosh` (consume CPU, CARGA_D13 §5.4) | Hecho el 16/11/2026: chequeo TCP, de 61 % a 5 % de CPU en reposo; arranque desde cero verificado (ARRANQUE_DOCKER.md §2.2) |

## 9. Límites del ejercicio

- Una sola instancia de cada dependencia y fallas de a una; no se probaron fallas simultáneas ni de red parcial (latencia, pérdida de paquetes).
- El tráfico es de un solo alumno; la capacidad bajo carga está en [CARGA_D13.md](entrega-final/CARGA_D13.md), que incluye la caída de una instancia de Booking con 25 usuarios.
- Training y Benefits se observaron, pero el tráfico de fondo no los usa; su comportamiento ante fallas está en sus E2E y en el [postmortem inicial](entrega-2/POSTMORTEM_INICIAL.md).

## Antecedente

El primer ejercicio de resiliencia (Entrega 2, 16/10/2026) detuvo Benefits y PostgreSQL y simuló una respuesta perdida después del commit. Comprobó que la misma `Idempotency-Key` recupera el resultado sin duplicar puntos. Ver [POSTMORTEM_INICIAL.md](entrega-2/POSTMORTEM_INICIAL.md).
