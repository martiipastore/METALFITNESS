# Bajas que cancelan reservas (RN-04, RN-34)

Fecha: 15/11/2026. Decisión: D-98 (precisión de ADR-005 v2). Diseño previo aprobado: D-20 y D-21 (07/10/2026).

## 1. Qué faltaba

El SPEC pide dos reglas que hasta ahora no se cumplían:

- **RN-04 / CA-05.1 / CL-12:** al cancelar una membresía, se cancelan las reservas futuras del alumno cuya fecha ya no cubre otra membresía vigente.
- **RN-34 / CA-01.3:** dar de baja un alumno cancela sus reservas futuras.
- **CL-19:** si una reserva compite con la cancelación, puede confirmarse un instante, pero no puede quedar CONFIRMADA después de que la cancelación se propaga.

Members ya escribía `membresia.cancelada` (por trigger) y `usuario.desactivado` en sus outbox, pero nadie los publicaba ni los consumía. Las reservas quedaban confirmadas y ocupaban cupo.

## 2. Cómo funciona

```mermaid
sequenceDiagram
    participant A as Administrador
    participant M as members-service
    participant R as RabbitMQ (gym.events)
    participant B as booking-service (×2)
    participant P as booking_db
    A->>M: cancelar membresía / dar de baja
    M->>M: cambio + evento en outbox (misma transacción)
    M->>R: relay publica membresia.cancelada / usuario.desactivado
    R->>B: cola booking.membresia-cancelada / booking.usuario-desactivado
    B->>P: bloqueo del alumno, deduplicación por event_id
    B->>M: ¿qué fechas siguen cubiertas? (sin caché; solo membresía cancelada)
    B->>P: cancela reservas sin cobertura, libera cupos, registra la revocación
    B->>R: ACK después del commit
```

- **Members:** el relay existente publica también `membresia.cancelada` (de `outbox_membresias`) y `usuario.desactivado` (de `outbox_usuarios`), y declara las colas de Booking para que los eventos esperen aunque Booking esté apagado. El trigger guarda nombre y email del alumno en todos los eventos de membresía; el relay los quita de todos menos `membresia.activada`, el único que los necesita para el correo.
- **Booking:** las dos instancias consumen las mismas colas, con los reintentos (10 s, 1 min, 10 min) y la DLQ de siempre. Todo el efecto va en una transacción (`Revocar` en `adapters/postgres/reservas.go`):
  1. Bloqueo transaccional del alumno, el mismo que ya toma cada reserva.
  2. Deduplicación por `event_id` en `eventos_consumidos` (regla 7). Una reentrega no hace nada.
  3. Reservas futuras CONFIRMADA del alumno, bloqueadas.
  4. **Membresía cancelada:** consulta a Members, sin caché, qué fechas siguen cubiertas, en lotes de hasta 31 fechas. Se cancelan las demás (CL-12). **Baja del alumno:** se cancelan todas.
  5. Cada reserva pasa a CANCELADA con motivo «membresía cancelada» o «alumno dado de baja» y su clase libera el cupo. El trigger de la clase genera `clase.actualizada` para el índice, y la caché de búsquedas se invalida.
  6. La revocación queda registrada en `revocaciones`, inmutable.

  Si Members no responde, la transacción se deshace y el evento vuelve a la cola de reintentos: nunca se decide con datos viejos. Una baja de un profesor u otro rol no tiene efecto en Booking.
- **Pantallas:** el alumno ve el motivo en «Mis reservas». El administrador recibe un aviso de que las reservas futuras se cancelan en unos segundos.

## 3. La carrera de CL-19

Booking valida la membresía con Members antes de abrir su transacción. Si la cancelación ocurre entre esa consulta y la inserción, Members ya respondió «vigente».

ADR-005 v2 lo resolvía con un bloqueo por `membresia_id`, guardando en cada reserva qué membresía la cubría. Al implementar se eligió **bloquear por alumno** (D-98):

- Booking ya bloquea por alumno en cada reserva (duplicados, turno de Musculación y superposición).
- La baja del alumno no tiene membresía asociada y necesita el mismo cierre.
- No cambia la API interna de vigencia ni la tabla de reservas.

El mecanismo:

1. `Reservar` anota el instante en que valida con Members, antes de consultarlo.
2. El consumidor registra la revocación con la hora de la base, dentro del bloqueo del alumno.
3. La transacción de la reserva toma el mismo bloqueo y, si encuentra una revocación del alumno registrada después de su validación (menos 2 s de margen por diferencia de reloj entre procesos), rechaza con `MEMBRESIA_NO_VIGENTE` o `USUARIO_INACTIVO`.

Con eso, una reserva o se confirmó antes, y el consumidor la encuentra y la cancela, o llega después y se rechaza. El rechazo es conservador: si el alumno tiene otra membresía vigente, reintenta y la nueva consulta a Members lo confirma.

## 4. Verificación

| Prueba | Qué cubre | Resultado |
|---|---|---|
| `domain/revocacion_test.go` | Qué reservas cancela cada baja (RN-04, RN-34, CL-12), fechas en zona argentina | Aprobado |
| `application/revocacion_test.go` | Consulta a Members por lotes, Members caído reintenta, baja sin consulta, reentrega, eventos inválidos | Aprobado |
| `adapters/amqp/revocaciones_test.go` | Traducción de los dos eventos, rol no alumno sin efecto, sobres inválidos a la DLQ | Aprobado |
| `adapters/postgres/revocaciones_integration_test.go` (PostgreSQL real) | Cancela y libera cupo una vez, deduplicación, rollback si falla la decisión, CL-19 secuencial y **concurrente (20 carreras, ninguna reserva confirmada al final; ocupación igual a las confirmadas)**, revocaciones inmutables | Aprobado |
| E2E `-attendance` (servicios reales, RabbitMQ, PostgreSQL y Mailpit aislados) | Cancelar la membresía cancela dos reservas futuras y libera los cupos; Members publicó el evento; reservar después da 422; la reentrega no repite nada; dar de baja un alumno cancela su reserva | **36 escenarios aprobados**, 4 nuevos (`bajas_*`, incluido CA-02.3). Evidencia: `docs/evidence/entrega-final/etapa6/attendance-e2e.json` |

También pasan todos los tests de Booking y Members, y en el stack local (`make demo`) cada cola nueva tiene dos consumidores, uno por instancia de Booking.

## 5. Profesores con clases futuras (CA-02.3, D-99, 15/11/2026)

RN-34 también dice que no se puede dar de baja a un profesor con clases futuras PROGRAMADA (CA-02.3: se rechaza hasta que se reasignen o cancelen).

- **Consulta a Booking:** antes de dar de baja a un PROFESOR, Members pregunta a Booking `GET /internal/v1/profesores/{id}/agenda` (clave interna, sin caché). Si tiene clases futuras PROGRAMADA, responde `409 PROFESOR_CON_CLASES_FUTURAS` y la persona queda ACTIVA. Si ninguna instancia de Booking responde, `503`: la baja falla cerrada. Es la primera dependencia síncrona de Members hacia Booking, limitada a esta operación del administrador. En Compose, `MEMBERS_BOOKING_URLS` lista las dos instancias; en Vercel no hay Booking y no se verifica.
- **Horarios recurrentes:** no se pueden borrar ni reasignar, así que no bloquean la baja; si lo hicieran, un profesor con horario no podría darse de baja nunca. En cambio, la generación de clases saltea los horarios de profesores que Members ya no informa como activos. Así, después de la baja no se le crean clases nuevas.
- **Riesgo residual:** si se generan clases justo entre la consulta y la baja, pueden quedar clases del profesor dado de baja. El administrador las ve en la gestión de clases y las cancela.

Verificación: tests de la agenda y de la generación en Booking (`application/agenda_test.go`); de la baja con agenda, Booking caído y otros roles en Members (`service/people_agenda_test.go`); del cliente con dos instancias (`repository/booking_test.go`); y el escenario E2E `bajas_profesor_con_clases_rechazado_y_sin_clases_nuevas_CA02_3`.

## 6. Pendientes

- `alumno.creado` y `membresia.vencida` siguen en el outbox sin publicar porque no tienen consumidor.
- Revisión del equipo de D-98 y D-99.
