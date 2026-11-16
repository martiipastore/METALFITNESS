# ADR-007 v2 — Caché de búsquedas de clases con copia vencida ante falla

| Campo | Valor |
|---|---|
| **Estado** | Propuesto (corrección pedida por la cátedra el 13/11/2026; falta revisión del equipo) |
| **Fecha** | 2026-11-14 |
| **Decisión del TP** | D7, D6 |
| **Amplía** | [ADR-007](ADR-007-cache-autorizacion-disponibilidad.md) (se mantiene vigente) y [ADR-014](ADR-014-busqueda-clases.md) en la búsqueda por texto y las marcas |
| **Relacionado con** | SPEC HU-13, CA-13.4, RN-13, RN-14; D-88; D-95 |

## Contexto

La revisión de la cátedra pidió tres correcciones sobre la búsqueda de clases:

1. Una búsqueda hecha una vez debe poder repetirse con PostgreSQL apagado, desde la caché.
2. Con OpenSearch habilitado, la búsqueda no debe depender de PostgreSQL. Hasta ahora el
   índice respondía, pero un alumno recibía `503` porque la marca «ya reservada» se lee en
   la base. El administrador no tenía ese problema.
3. Una barra de búsqueda que encuentre clases con una parte del nombre: «zum» → Zumba.

La caché de la etapa 9 (D-88) solo cubría el catálogo de actividades.

## Decisión

**Caché de páginas de búsqueda (cache-aside, teórico clase 3 §6.1).** `GET /classes` guarda
en Redis cada página por una clave con todos los parámetros que cambian el resultado:
fechas, actividad, franja, «solo con lugar», texto normalizado, página y tamaño (clase 3
§3.2). La clave no incluye al alumno: la copia se guarda sin `reservada_por_mi`, que se
completa aparte para cada alumno.

**Dos límites de antigüedad (clase 3 §7.2 y §8.3).**

| Parámetro | Valor propuesto | Variable |
|---|---|---|
| Vigencia fresca: se sirve sin consultar la fuente | 30 s | `BOOKING_CACHE_CLASES_FRESCO` |
| Retención: se sirve vencida solo si la fuente no responde | 30 min | `BOOKING_CACHE_CLASES_RETENCION` |

La respuesta informa `origen` (`fuente`, `cache` o `cache_vencida`) y `consultado_en`. La
pantalla avisa con la hora de la copia cuando muestra datos vencidos. Los valores son
propios del equipo, no del teórico: 30 s acota cuánto puede atrasarse la ocupación si
falla una invalidación; 30 min cubre una caída de la base en una demostración o un ensayo.

**Invalidación por generación (clase 3 §7.1, versionado de claves).** Las claves llevan una
generación aleatoria guardada en Redis. Cada escritura que cambia clases la reemplaza:
generar clases, cambiar capacidad, cancelar clase, reservar, cancelar reserva, cierre
automático y renombrar una actividad. El indexer también la reemplaza al aplicar una
versión, porque la caché copia resultados del índice y este se actualiza alrededor de 1 s
después del commit. Como OpenSearch hace visible un documento recién en su refresh periódico
(1 s), el indexer invalida dos veces: al aplicar y 1,5 s después; varias versiones seguidas
comparten una sola invalidación diferida. Sin la segunda, una búsqueda en ese hueco guardaba el
estado viejo durante 30 s. Forzar el refresh por documento se descartó porque elevaba el retraso
del índice a 10 o 25 s al generar muchas clases. Si Redis pierde la generación se crea otra:
nunca reaparecen copias viejas.

**Marcas degradadas (clase 7 §6).** Si PostgreSQL no responde, la página sale igual con
`marcas_disponibles: false` y sin afirmar reservas. La reserva sigue validándose contra
PostgreSQL y la base rechaza duplicados (RN-13), así que la marca faltante no habilita nada
indebido. La consulta de marcas tiene un límite propio de 800 ms: con la base detenida la
conexión no se rechaza enseguida, y sin ese límite el gateway cortaba a los 3 s con `504`.

**Búsqueda por texto (clase 5 §2.2 y §2.3).** El parámetro `q` admite hasta 60 caracteres y
5 palabras. Cada palabra, en minúsculas y sin tildes, debe ser el comienzo de alguna palabra
del nombre de la actividad o del profesor. OpenSearch indexa el campo `texto` con un
analizador propio (tokenizador estándar, `lowercase`, `asciifolding`) e `index_prefixes`, y
consulta con `prefix` por palabra. PostgreSQL aplica la misma regla, así que el resultado no
depende de qué fuente responde. Si el índice existente no tiene el campo, el indexer lo
reconstruye al arrancar con el reindexado por alias de D-87, sin pausa para las búsquedas.

## Hallazgos de la jornada de fallas (15/11/2026, D-100)

La jornada final (`tests/resilience/jornada.go`, `docs/POSTMORTEM.md`) encontró dos casos en que la búsqueda no respondía sin su fuente, aunque la regla de arriba lo prometía:

- **Copia inalcanzable tras una escritura.** Con OpenSearch caído y PostgreSQL arriba, cada reserva o cancelación cambia la generación, así que la copia vigente deja de leerse y la búsqueda respondía 503. Ahora cada copia se guarda también como «última conocida» de su clave, fuera de las generaciones y con la misma retención. Solo se sirve, como `cache_vencida`, cuando la fuente falla.
- **Instancias retiradas con PostgreSQL caído.** PostgreSQL era una dependencia crítica de Booking: su `/health/ready` daba `not_ready`, el balanceador (ADR-012 v3) retiraba las dos instancias y el gateway respondía 503 a todo. Cuando la caché de búsquedas u OpenSearch están activos, PostgreSQL pasa a ser **parcial** en Booking: la instancia informa `degraded`, sigue en el balanceo, la búsqueda responde desde la caché y reservar falla cerrado con 503 desde el propio Booking.

## Lo que no cambia

- La caché sigue sin decidir cupos, permisos ni reservas (ADR-007, RN-14).
- Sin copia en la caché y sin fuente, la búsqueda responde `503`, igual que antes. D-45 se
  mantiene: OpenSearch caído no se reemplaza con PostgreSQL.
- Membresías, asignaciones y API keys siguen sin cachearse.

## Consecuencias

- La ocupación mostrada puede atrasarse hasta 30 s si una invalidación falla, o más si la
  fuente está caída y se sirve una copia vencida; en ese caso se informa la hora.
- Con Redis caído, cada búsqueda paga el timeout de lectura (100 ms) y consulta la fuente
  sin guardar.
- Métricas nuevas en `/metrics` de Booking: `classes_cache_{hits,misses,errors,invalidations}_total`,
  `classes_search_responses_total{origen}` y `classes_search_unmarked_total`. Un error de
  Redis se cuenta como error, no como miss (clase 3 §2.1).
- Cambia el mapping del índice; el reindexado es automático.

## Evidencia

Tests unitarios en `internal/application/busqueda_cache_test.go` y `busqueda_test.go`, y de
dominio en `internal/domain/busqueda_texto_test.go`. Tests de integración contra Redis,
PostgreSQL y OpenSearch reales: `adapters/redis/clases_integration_test.go`,
`adapters/postgres/texto_integration_test.go` y `adapters/opensearch/texto_integration_test.go`.
