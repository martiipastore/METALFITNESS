# Correcciones de la búsqueda de clases

Fecha: 14/11/2026. Origen: revisión de la cátedra del 13/11/2026. Decisión: D-95 (propuesta) y [ADR-007 v2](../adr/ADR-007-v2-cache-busqueda-clases.md). Historias: HU-13, CA-13.4.

## 1. Qué se pidió y qué se hizo

| Pedido | Antes | Ahora |
|---|---|---|
| Buscar una vez con la base, guardar en caché y volver a encontrar las clases con la base apagada | La caché solo guardaba el catálogo de actividades | `GET /classes` guarda cada página en Redis. Con PostgreSQL apagado, la misma búsqueda responde desde la caché |
| Que OpenSearch responda sin consultar la base | El administrador podía buscar; el alumno recibía `503` porque «ya reservada» sale de PostgreSQL | El índice responde para todos los roles. Sin base, la página sale con `marcas_disponibles: false` y la pantalla lo avisa |
| Barra de búsqueda con texto parcial («zum» → Zumba) | No existía | Campo «Buscar» que filtra mientras se escribe, por actividad o profesor, sin distinguir mayúsculas ni tildes |

## 2. Cómo funciona

```text
GET /classes?q=zum
  1. ¿Hay copia de esta búsqueda de menos de 30 s?  sí → responde (origen=cache)
  2. Consulta la fuente: OpenSearch si está habilitado; si no, PostgreSQL
       responde → guarda la copia y responde      (origen=fuente)
       falla y hay copia (hasta 30 min) → copia   (origen=cache_vencida, con su hora)
       falla y no hay copia → 503
  3. Si el que busca es alumno, completa «ya reservada» desde PostgreSQL (800 ms como máximo)
       la base no responde → marcas_disponibles=false
```

- **Invalidación:** cada escritura que cambia clases (generar, capacidad, cancelar clase, reservar, cancelar reserva, cierre automático, renombrar actividad) y cada versión que aplica el indexer cambian la generación de la caché.
- **Texto:** cada palabra debe ser el comienzo de una palabra de la actividad o del profesor. «zum» encuentra Zumba; «umba» no; «str gom» encuentra Strong Nation con Lucía Gómez. Misma regla en OpenSearch y en PostgreSQL.
- **Índice:** el campo nuevo `texto` cambia el mapping. Al arrancar, el indexer detecta un índice viejo y lo reconstruye sin pausa (alias, D-87). `make demo` también reindexa.

## 3. Dos ajustes que salieron al probar con el sistema real

- **Visibilidad antes de invalidar.** OpenSearch deja buscable un documento recién en su refresh periódico (alrededor de 1 s; teórico, clase 5). El indexer invalidaba la caché antes de eso, y una búsqueda en ese hueco guardaba el resultado viejo durante 30 s: se vio como «zum → 0 clases» justo después de generar. Ahora el indexer **invalida dos veces**: al aplicar el cambio y otra vez 1,5 s después, cuando el documento ya es visible. Si llegan varios cambios seguidos, la invalidación diferida se hace una sola vez, después del último.
  Se probaron y descartaron dos alternativas que fuerzan el refresh por documento: `refresh=wait_for` (23 clases nuevas tardaron 25,8 s en indexarse) y `refresh=true` (generar dos meses de clases llevó el retraso a 9,8 s). Con la doble invalidación, 39 clases generadas juntas tuvieron un retraso máximo de 0,64 s y una actividad nueva apareció para la alumna antes de 1 s.
- **Presupuesto para la marca «ya reservada».** Con PostgreSQL detenido, la consulta de marcas no fallaba enseguida: esperaba hasta vencer la conexión, y el gateway cortaba a los 3 s con `504` antes de que la búsqueda pudiera degradarse. Ahora tiene un límite propio de 800 ms (`application.TiempoMaximoMarcas`). Con OpenSearch, que tiene 2 s como máximo, suma 2,8 s, dentro de los 3 s del gateway (teórico, clase 7 §2).

## 4. Demostración

Requisitos: `make demo` y `go run scripts/cargar-datos-demo.go`. El script crea los profesores Lucía Gómez y Juan Ruiz, la alumna `alumna@gym.local` con membresía mensual (imprime su contraseña una sola vez), horarios en Zumba, Funcional, Strong Nation, GAP y Musculación, y las clases de las próximas dos semanas.

**Corrección 3 — texto parcial.** Entrar como alumna o administrador a Clases y escribir `zum` en «Buscar». Aparecen solo las clases de Zumba, sin apretar «Buscar».

**Corrección 2 — OpenSearch sin base.**

```powershell
docker compose --env-file .env -f deploy/docker-compose.yml stop postgres
```

Buscar como administrador y como alumna: las clases siguen apareciendo. La alumna ve el aviso «No pudimos verificar tus reservas». Reservar falla con un mensaje claro, porque la reserva siempre se valida en la base.

**Corrección 1 — caché con la base apagada.** Con OpenSearch habilitado, la base apagada no alcanza para probar la caché, porque responde el índice. Hay que apagar también OpenSearch después de buscar:

```powershell
docker compose --env-file .env -f deploy/docker-compose.yml start postgres
# Buscar "stro" en la pantalla: queda guardado en la caché.
docker compose --env-file .env -f deploy/docker-compose.yml stop postgres opensearch
# Repetir la búsqueda: responde desde la caché.
docker compose --env-file .env -f deploy/docker-compose.yml start postgres opensearch
```

Dentro de los 30 s la respuesta tiene `X-Cache: HIT`. Después, `X-Cache: STALE` y la pantalla avisa «Mostramos la última búsqueda guardada, de las HH:MM». Una búsqueda que nunca se hizo responde `503`. Al volver a prender todo, el sistema vuelve a `ready` en unos 30 s.

**Evidencia en métricas.** En `http://localhost:8082/metrics`: `classes_cache_hits_total`, `classes_search_responses_total{origen="cache"}` y `{origen="cache_vencida"}`. Retraso del índice en `http://localhost:8086/metrics`: `clase_index_lag_seconds_max`.

## 5. Verificación

**Tests.** Unitarios: `internal/application/busqueda_cache_test.go` (incluida la marca con la base colgada), `busqueda_test.go` e `internal/domain/busqueda_texto_test.go`. Integración con dependencias reales: `adapters/redis/clases_integration_test.go`, `adapters/postgres/texto_integration_test.go` y `adapters/opensearch/texto_integration_test.go`. `go vet` y `go test ./...` de booking-service aprobados el 14/11/2026, con Docker. Build del frontend aprobado.

**Sistema real.** El 14/11/2026, desde Docker vacío (`make clean`, `make demo` y el script de datos), a través del gateway:

| Prueba | Resultado |
|---|---|
| `q=zum`, `q=ZUM` | 200, 4 clases, solo Zumba |
| `q=musculacion` (sin tilde) | 200, 10 clases de Musculación |
| `q=gom` (profesora Gómez) | 200, 16 clases de Zumba, Strong Nation y Musculación |
| `q=umba` | 200, 0 clases (solo comienzos de palabra) |
| La alumna reserva y vuelve a buscar | 201; la búsqueda siguiente sale de la fuente con `reservada_por_mi` en la clase reservada |
| PostgreSQL detenido: administrador y alumna buscan | 200; la alumna con `marcas_disponibles: false` |
| PostgreSQL detenido: la alumna reserva | 503 |
| PostgreSQL y OpenSearch detenidos: búsqueda hecha antes | 200 `X-Cache: HIT` |
| Lo mismo, una búsqueda nunca hecha | 503 |
| La misma búsqueda 31 s después | 200 `X-Cache: STALE`, `origen: cache_vencida` |

## 6. Límites conocidos

- La ocupación puede atrasarse hasta 30 s si una invalidación falla, y más mientras se sirve una copia vencida; en ese caso se informa la hora.
- Solo encuentra comienzos de palabra: «umba» no encuentra Zumba.
- Sin copia y sin fuente, la búsqueda sigue respondiendo `503` (D-45).
- Entre que el índice aplica un cambio y la invalidación diferida (1,5 s), una búsqueda puede mostrar el estado anterior; nunca más de eso, salvo que Redis falle (entonces, hasta 30 s).
- La primera consulta a un OpenSearch recién levantado puede superar el timeout de 2 s y responder `503` una vez.
- El Word de decisiones no está sincronizado con D-95.
