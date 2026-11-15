# ADR-013 v3 — Capacidad del flujo principal (búsqueda y reservas) y alternativa de escalado

| Campo | Valor |
|---|---|
| **Estado** | Propuesto el 15/11/2026: mediciones hechas; la alternativa de escalado y los costos esperan la revisión del equipo |
| **Fecha** | 2026-11-15 |
| **Decisión del TP** | D13 |
| **Amplía** | [ADR-013 v2](ADR-013-v2-capacidad-costos-medidos.md), que midió solo la API de partners de Benefits; sus resultados siguen vigentes |
| **Relacionado con** | D-97 (parámetros), D-96 y ADR-012 v3 (balanceo), ADR-007 v2 (caché de búsqueda), RN-12/RN-13 (cupo y duplicados); teórico, clases 3, 6 y 7 |

## Contexto

El enunciado pide medir el sistema bajo carga, identificar el límite principal y proponer una alternativa de escalado con una estimación de costos. ADR-013 v2 lo hizo para la API de partners. Desde entonces el flujo principal cambió: la búsqueda pasa por OpenSearch y por la caché de Redis (ADR-007 v2) y Booking corre en dos instancias balanceadas por el gateway (ADR-012 v3). Faltaba medir ese flujo.

## Qué se midió

Protocolo D-97, ejecutado el 15/11/2026 con `tests/load/run-flujo.go` (evidencia en [docs/evidence/entrega-final/carga](../evidence/entrega-final/carga/RESULTADOS.md), análisis en [CARGA_D13.md](../entrega-final/CARGA_D13.md)):

- **Búsqueda:** `GET /classes` con texto parcial y filtros variados, como alumno, a través del gateway.
- **Mezcla:** 90 % búsquedas y 10 % reservas sobre 22 clases reales.
- **Pico:** 100 alumnos reservan a la vez la misma clase de 30 cupos, tres veces, con verificación por SQL.
- **Caída:** mezcla con 25 usuarios, `docker kill` de una instancia de Booking a los 20 s y arranque a los 55 s.

Escalera de 10, 25, 50 y 100 usuarios simultáneos sin pausa, 30 s de calentamiento y 60 s de medición; corte con p95 > 500 ms o errores > 1 %.

## Resultados

| Escenario | Usuarios | Ops/s | p95 ms | Errores |
|---|---|---|---|---|
| Búsqueda | 10 / 25 / 50 / 100 | 794 / 572 / 659 / 1.215 | 28 / 105 / 172 / 167 | 0 % en todos |
| Mezcla | 10 / 25 / 50 / 100 | 540 / 942 / 1.269 / 535 | 47 / 62 / 85 / 438 | 0 / 0 / 0 / 0,07 % |
| Pico (3 corridas) | 100 | — | 1.766 / 1.508 / 1.015 | 0 %; 30 confirmadas y 70 `SIN_CUPO` en cada una |
| Caída de una instancia | 25 | 674 | 69 | 0,94 % (530 × 502 y 40 × 504) |

Ningún escalón de la escalera superó el corte. La integridad se mantuvo siempre: ninguna clase sobre su cupo y ninguna reserva duplicada.

## Límites observados

1. **CPU del equipo compartido, con el gateway como mayor consumidor.** Desde 25 a 50 usuarios el rendimiento deja de crecer (entre 1.000 y 1.270 ops/s) y la latencia sube en proporción a los usuarios. Es la señal de saturación de un sistema cerrado (latencia ≈ usuarios / rendimiento). El gateway es el contenedor con más CPU en casi todos los escalones (1,1 a 2 núcleos de 8): valida el JWT RS256, consulta el token bucket en Redis y hace de proxy en cada solicitud. Las dos instancias de Booking usan menos de un núcleo cada una. Comparten el equipo con k6 y unos 20 contenedores, así que el límite es el equipo, no un servicio aislado.
2. **Efecto cascada al saturarse.** Con 100 usuarios en la mezcla, Booking registró 593 lecturas de Redis que superaron su timeout de 100 ms. Sin copia, esas búsquedas fueron a OpenSearch, que llegó a 296 % de CPU. 23 superaron los 2 s de `BOOKING_SEARCH_TIMEOUT` y respondieron 503. La copia vencida no ayuda en ese caso, porque la falla es leerla, no la fuente.
3. **Pico sobre una clase: serialización por diseño.** 100 reservas simultáneas a una clase tardan hasta 1,0 a 1,8 s (p95). Members respondió la vigencia con p95 de 105 ms; el resto se pasa dentro de Booking, en la transacción que bloquea la fila de la clase (`FOR UPDATE`) para garantizar el cupo (RN-12). Son unos 16 ms por reserva en serie. Ninguna réplica lo acorta: todas compiten por la misma fila. Es el costo de la invariante, no una falla.
4. **Ruido del equipo.** Los escalones de 25 y 50 usuarios de búsqueda rindieron menos que el de 100. En ellos, MongoDB promedió 80 a 87 % de CPU sin recibir carga del test: su healthcheck ejecuta `mongosh` (un proceso de Node.js) cada 5 s. Es el mismo problema que ya se corrigió en RabbitMQ (ver ARRANQUE_DOCKER.md §2.1). Además es una sola corrida en una notebook, sin repeticiones.

La caída de una instancia confirma ADR-012 v3 bajo carga: 0,94 % de errores, todos en la ventana de detección, y el reparto continuó en la instancia sana.

## Alternativas de escalado

La demanda razonable de un gimnasio está muy por debajo de lo medido. Con 1.000 alumnos activos que buscan 10 veces y reservan una vez por día, son unas 11.000 operaciones diarias. Incluso concentradas en una hora pico, dan alrededor de 3 ops/s, frente a las 1.000 medidas. El escalado no se justifica por volumen, sino por aislamiento y por el pico de una clase muy pedida.

| Alternativa | Qué resuelve | Qué no resuelve | Costo estimado |
|---|---|---|---|
| **A. Mantener el diseño; separar la carga de prueba del sistema medido** | Mide el sistema y no la notebook; quita el ruido de healthcheck de Mongo con un chequeo liviano | — | Sin costo de infraestructura |
| **B. VM dedicada para todo el stack, 4 OCPU / 24 GB** | Duplica CPU y da memoria para OpenSearch, que hoy entra justo en los 3,7 GiB del Docker local | La serialización del pico | USD 65,04/mes contra USD 36,77/mes de 2 OCPU / 12 GB (Oracle São Paulo, con 200 GB de disco, antes de franquicias; [cotización del 16/10/2026](../entrega-2/COSTOS_ENTREGA_2.md), a reverificar) |
| **C. Segunda instancia del gateway detrás de un balanceador de red** | El gateway es el mayor consumidor y una instancia única (L-06, L-16) | La serialización del pico; agrega un componente | Un balanceador gestionado más una instancia; no cotizado |
| **D. Cola de espera para la apertura de clases muy pedidas** | El pico: ordena a los alumnos y responde enseguida «en espera» | Agrega estado y una regla de negocio nueva que el SPEC no tiene | Desarrollo; sin infraestructura nueva |

**Propuesta:** A ahora, porque es gratis y vuelve más confiable la medición. B si el stack completo se despliega fuera de Vercel; B se dimensiona con estos datos: el gateway y OpenSearch necesitan núcleos y memoria propios. C solo si una medición en un equipo dedicado muestra al gateway como límite. D no se recomienda: un p95 de 1,8 s en el peor caso de 100 alumnos por una clase de 30 es aceptable y el SPEC no define una cola. Ninguna alternativa cambia el bloqueo de la clase, que protege el cupo.

## Consecuencias

- Hay un procedimiento reproducible del flujo principal (`go run tests/load/run-flujo.go`) que deja la demo como estaba: cancela por la API las reservas de los alumnos sintéticos y restaura el limitador.
- Los límites quedan explicados con datos: saturación del equipo, cascada al saturarse y serialización del pico.
- Los costos son los del 16/10/2026 y deben reverificarse antes de citarlos en la defensa. El despliegue vigente (Vercel, ADR-021) no incluye Booking, así que B solo aplica si se despliega el stack completo.

## Fundamento académico

Capacidad, latencia y saturación (teórico pp. 29–30, 86, 101–102 y 143); caché y su comportamiento ante fallas (clase 3); balanceo y health checks (clase 6); resiliencia y degradación (clase 7). k6 y los percentiles son herramientas adicionales aprobadas en D-50. La escalera, la duración, el corte y la demanda supuesta son elecciones propias (D-97), no del teórico.
