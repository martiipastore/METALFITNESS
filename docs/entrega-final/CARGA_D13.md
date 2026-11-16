# Carga del flujo principal (D13)

Fecha: 15/11/2026. Decisiones: D-97 (parámetros) y [ADR-013 v3](../adr/ADR-013-v3-capacidad-flujo-principal.md) (límites y alternativa de escalado, propuesto). Evidencia: [RESULTADOS.md](../evidence/entrega-final/carga/RESULTADOS.md), `resultados.json` y `environment.json` en la misma carpeta.

## 1. Qué se pidió

El enunciado pide medir el sistema bajo carga, encontrar el límite principal y proponer cómo escalar, con costos. ADR-013 v2 (etapa 7 de la Entrega 2) midió la API de partners de Benefits. Esta medición cubre el flujo principal del alumno: buscar clases y reservar, con la caché de búsqueda (ADR-007 v2) y las dos instancias de Booking (ADR-012 v3).

## 2. Cómo se mide

```bash
make demo                               # stack completo
go run scripts/cargar-datos-demo.go     # clases de las próximas dos semanas
go run tests/load/run-flujo.go          # unos 20 minutos; -corta para una prueba breve
go run tests/load/run-flujo.go -informe # regenera RESULTADOS.md sin volver a medir
```

El runner hace todo sin pasos manuales:

1. **Sube los umbrales del limitador del gateway** mientras mide (recrea `api-gateway` con valores altos y lo restaura al final, también si falla). Con los valores normales, 10 solicitudes por segundo por usuario, el test mediría el limitador y no el sistema.
2. **Crea los datos sintéticos por la API:** 100 alumnos `carga.NNN@gym.local` con membresía mensual, un profesor y la actividad «Carga Pico». Las contraseñas se derivan de `GYM_INTERNAL_SERVICE_KEY` y no se imprimen ni se guardan.
3. **Abre una sesión por alumno una sola vez** y pasa los tokens a k6. Members limita el login a 120 por minuto en todo el sistema (protección contra fuerza bruta); esa protección no se toca. Los tokens se renuevan cuando tienen más de 8 minutos.
4. **Libera las reservas de corridas anteriores** cancelándolas por la API como cada alumno, antes de medir y al terminar. Sin esto, la primera corrida llenaba todas las clases de la demo.
5. **Corre k6** (`grafana/k6:1.3.0`) dentro de la red de Docker, contra `api-gateway:8080`.
6. **Muestrea CPU y memoria** de cada contenedor con `docker stats` durante la ventana de medición, y lee del gateway el reparto por instancia y, de Booking, el origen de cada búsqueda.
7. **Comprueba la integridad por SQL** después de cada pico: capacidad, ocupación, reservas confirmadas, clases sobre cupo y reservas duplicadas.

Durante la corrida, Windows suspendía la notebook por inactividad y la medición quedaba inválida (dos intentos). La corrida válida se lanzó con `SetThreadExecutionState`, que mantiene el equipo despierto mientras vive el proceso, sin cambiar la configuración de energía. Conviene hacer lo mismo o dejar el equipo enchufado y activo.

## 3. Parámetros (D-97)

| Parámetro | Valor | Motivo |
|---|---|---|
| Escalera | 10, 25, 50 y 100 usuarios simultáneos | Llegar al límite sin saltar de golpe |
| Duración | 30 s de calentamiento y 60 s de medición por escalón | El calentamiento llena la caché y las conexiones; no se cuenta |
| Pausa | Ninguna | Medir capacidad, no un ritmo humano |
| Corte | p95 > 500 ms o errores > 1 % | No seguir subiendo un escenario que ya se degradó |
| Mezcla | 90 % búsquedas y 10 % reservas | Un alumno busca más de lo que reserva |
| Pico | 100 alumnos por una clase de 30 cupos, tres veces | La apertura de una clase muy pedida |
| Caída | `docker kill` de `booking-service-2` a los 20 s y arranque a los 55 s, con 25 usuarios | Verificar ADR-012 v3 bajo carga |

Un 409 de reserva (`SIN_CUPO`, `RESERVA_DUPLICADA`, `SUPERPOSICION_RESERVA`) es una respuesta de negocio correcta y se cuenta aparte. Son errores los 5xx, 429, 401, 403, otros 4xx y los timeouts.

## 4. Resultados

Equipo: notebook con 8 CPU; Docker 29.9.3 con 3,7 GiB de memoria; k6 en el mismo equipo.

| Escenario | Usuarios | Ops/s | p50 ms | p95 ms | p99 ms | Errores | Reparto booking-1 / booking-2 |
|---|---|---|---|---|---|---|---|
| Búsqueda | 10 | 794 | 9 | 28 | 49 | 0 % | 38.192 / 38.191 |
| Búsqueda | 25 | 572 | 33 | 105 | 184 | 0 % | 21.256 / 21.256 |
| Búsqueda | 50 | 659 | 60 | 172 | 254 | 0 % | 33.247 / 33.248 |
| Búsqueda | 100 | 1.215 | 68 | 167 | 331 | 0 % | 51.115 / 51.115 |
| Mezcla | 10 | 540 | 11 | 47 | 91 | 0 % | 20.253 / 20.253 |
| Mezcla | 25 | 942 | 20 | 62 | 107 | 0 % | 36.337 / 36.336 |
| Mezcla | 50 | 1.269 | 33 | 85 | 133 | 0 % | 53.497 / 53.497 |
| Mezcla | 100 | 535 | 146 | 438 | 840 | 0,07 % | 32.664 / 32.664 |

Ningún escalón superó el corte, así que la escalera llegó a 100 usuarios en los dos escenarios.

**Caché:** en el escenario de búsqueda, más del 99 % de las respuestas salió de Redis (101.662 de 102.230 con 100 usuarios). En la mezcla fue del 81 % con 10 usuarios al 99,8 % con 50. Cada reserva confirmada invalida la caché: con 10 usuarios se confirmaron 220 reservas y la búsqueda fue 7.073 veces a OpenSearch. Desde 50 usuarios las clases del conjunto ya estaban llenas, casi todas las reservas fueron `SIN_CUPO` y la caché casi no se invalidó.

**Picos:** en las tres corridas hubo exactamente 30 reservas confirmadas y 70 `SIN_CUPO`, sin errores. El p95 fue de 1.766, 1.508 y 1.015 ms. El SQL confirmó ocupación 30, ninguna clase sobre cupo y ninguna reserva duplicada.

**Caída:** 570 errores de 60.642 operaciones (0,94 %): 530 respuestas 502 y 40 respuestas 504, todas entre el `kill` y el retiro de la instancia. Después siguió todo por `booking-service-1`, y la instancia volvió al reparto al arrancar. Coincide con la verificación de ADR-012 v3 (retiro a los 11,5 s).

## 5. Análisis

### 5.1 Límite principal: CPU del equipo, con el gateway al frente

En la mezcla, el rendimiento llega a 1.269 ops/s con 50 usuarios y cae con 100. En la búsqueda llega a 1.215 ops/s con 100 usuarios; los escalones intermedios están afectados por ruido (§5.4). En los dos, desde ahí la latencia crece con los usuarios. En un test cerrado, sin pausa, latencia media ≈ usuarios / rendimiento: con 100 usuarios y 1.215 ops/s da 82 ms, coherente con el p50 de 68 ms. Es saturación.

CPU promedio y máxima por contenedor (100 % = un núcleo):

| Escalón | Gateway | Booking 1 | Booking 2 | PostgreSQL | Redis | Otros |
|---|---|---|---|---|---|---|
| Búsqueda, 100 | 171 / 202 % | 101 / 117 % | 104 / 117 % | 66 / 74 % | 42 / 47 % | — |
| Mezcla, 50 | 136 / 176 % | 71 / 91 % | 76 / 99 % | 48 / 60 % | 38 / 73 % | MongoDB 66 / 102 % |
| Mezcla, 100 | 115 / 174 % | 62 / 101 % | 73 / 99 % | 63 / 98 % | — | OpenSearch 81 / 296 % |

El gateway es el contenedor que más CPU usa en casi todos los escalones. Por cada solicitud verifica la firma RS256 del JWT, ejecuta el token bucket en Redis y hace de proxy. Booking reparte su trabajo en dos instancias y cada una usa alrededor de un núcleo. Sumando k6 y el resto de los contenedores, el equipo queda cerca de sus 8 núcleos: el límite es el equipo compartido. Para saber cuál sería el límite de cada componente por separado habría que medir con k6 en otra máquina (ADR-013 v3, alternativa A).

### 5.2 Efecto cascada con 100 usuarios en la mezcla

Las 23 respuestas 503 fueron búsquedas que tardaron unos 2,1 s. Las métricas de Booking registraron 593 lecturas de Redis que superaron su timeout de 100 ms (`classes_cache_errors_total`). Sin poder leer la copia, la búsqueda va a OpenSearch, que en ese escalón llegó a 296 % de CPU. Si no responde en 2 s (`BOOKING_SEARCH_TIMEOUT`), Booking responde 503 `DEPENDENCIA_NO_DISPONIBLE`.

Es el comportamiento diseñado (fallar rápido en lugar de encolar), y no hubo datos incorrectos. La copia vencida de ADR-007 v2 no ayuda en este caso, porque lo que falla es leerla. Con 0,07 % de errores, el escalón quedó dentro del corte.

### 5.3 Pico de reservas: serialización por el cupo

Durante los picos, Members respondió las consultas de vigencia con p50 de 16 ms y p95 de 105 ms, según sus logs. Booking tardó 964 a 1.004 ms de mediana y 1,6 s de p95 en cada reserva. La consulta a Members ocurre antes de abrir la transacción (`application/booking.go`, `Reservar`), así que no retiene el bloqueo.

El tiempo restante es la cola sobre la fila de la clase: la transacción la bloquea con `FOR UPDATE` para comprobar el cupo y la superposición, inserta la reserva y el evento en el outbox, y confirma. 100 reservas en 1,6 s son unos 16 ms por reserva en serie.

No hay réplica que lo acorte, porque todas esperan la misma fila. Es el costo de garantizar el cupo en la base (regla 8 de CLAUDE.md, RN-12). El primer pico fue el más lento (1.766 ms) y el tercero el más rápido (1.015 ms), por conexiones y cachés que se calientan.

Sin trazas de Booking (L-26) no se puede separar cuánto de esos 16 ms es espera de conexión del pool y cuánto es la transacción. pgxpool usa por defecto el máximo entre 4 y la cantidad de CPU: 8 conexiones por instancia.

### 5.4 Ruido de la medición

Los escalones de búsqueda con 25 y 50 usuarios rindieron menos que el de 100 (572 y 659 contra 1.215 ops/s). En esos dos, MongoDB promedió 82 a 87 % de CPU sin recibir carga del test. Su healthcheck ejecuta `mongosh` cada 5 s, un proceso de Node.js con un arranque costoso. Es el mismo patrón que se corrigió en RabbitMQ el 15/11/2026 (ARRANQUE_DOCKER.md §2.1). No se corrigió todavía: el chequeo actual es parte del arranque verificado desde cero (pregunta a la IP del contenedor para no dar sano durante el init) y cambiarlo exige repetir esa verificación.

Además, es una sola corrida en una notebook con k6 en el mismo equipo. Los números sirven para encontrar el límite y su causa, no como capacidad garantizada.

## 6. Conclusión

- El flujo principal soporta del orden de 1.000 operaciones por segundo en una notebook, con p95 por debajo de 200 ms hasta 50 a 100 usuarios simultáneos. Un gimnasio de 1.000 alumnos necesitaría alrededor de 3 ops/s en su hora pico.
- El límite es la CPU compartida y el componente que más consume es el gateway. Al saturarse, la búsqueda falla rápido (503) en lugar de colgarse.
- El pico sobre una clase está acotado por la serialización que protege el cupo: 30 de 30 confirmadas, siempre íntegro, con hasta 1,8 s de espera.
- La alternativa de escalado y sus costos están en [ADR-013 v3](../adr/ADR-013-v3-capacidad-flujo-principal.md), propuesta para el equipo.

## 7. Pendientes

- Revisión del equipo de ADR-013 v3 y de los costos, que son del 16/10/2026.
- Chequeo liviano de MongoDB y nueva verificación del arranque desde cero.
- Repetir la medición con k6 en otro equipo, para separar el límite del gateway del límite de la notebook.
